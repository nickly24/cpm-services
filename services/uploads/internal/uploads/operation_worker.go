package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Server) operationOwnerBase(owner string) string {
	switch owner {
	case "core":
		return s.Config.CoreURL
	case "cards":
		return s.Config.CardsURL
	case "tests":
		return s.Config.TestsURL
	case "exams":
		return s.Config.ExamsURL
	}
	return ""
}

// Derive receipt routes from the registered native command, never a caller's
// supplied endpoint. A receipt call cannot fall through to an apply route.
func operationReceiptCommand(p privateExecutionPlan) (string, string, []byte, error) {
	if !canonicalUUID(p.Command.NativeKey) || !strings.HasSuffix(p.Command.Path, "/commit") || p.Command.Method != "POST" {
		return "", "", nil, api(503, "execution_plan_integrity_failed")
	}
	if err := verifyOperationRoute(p); err != nil {
		return "", "", nil, err
	}
	path := strings.TrimSuffix(p.Command.Path, "/commit")
	switch p.Command.Type {
	case "users.credentials", "users.legacy", "exams.questions", "exams.assignments", "exams.outside":
		return "GET", path + "/receipts/" + p.Command.NativeKey, nil, nil
	case "cards.xlsx":
		return "GET", "/v1/cards/receipts/" + p.Command.NativeKey, nil, nil
	case "tests.json", "tests.external-results":
		return "POST", path + "/receipt", p.Command.Body, nil
	}
	return "", "", nil, api(409, "background_type_unavailable")
}

func (s *Server) callOperationOwner(ctx context.Context, p privateExecutionPlan, stage, method, path string, body []byte) (json.RawMessage, error) {
	base := s.operationOwnerBase(p.Command.Owner)
	key := s.Config.OperationDeliveryKeys[p.Command.Owner]
	if base == "" || len(key) < 32 || !p.GrantID.Valid || !lowerHexReference(p.GrantID.String, 32) || !lowerHexReference(p.JobID, 32) || !canonicalUUID(p.Command.NativeKey) ||
		(stage != "apply" && stage != "receipt") {
		return nil, api(503, "execution_plan_integrity_failed")
	}
	if stage == "apply" {
		if verifyOperationRoute(p) != nil || method != p.Command.Method || path != p.Command.Path || !bytes.Equal(body, p.Command.Body) {
			return nil, api(503, "execution_plan_integrity_failed")
		}
	} else {
		m, route, original, e := operationReceiptCommand(p)
		if e != nil || m != method || route != path || !bytes.Equal(original, body) {
			return nil, api(503, "execution_plan_integrity_failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, api(503, "owner_outcome_unknown")
	}
	req.Header.Set("X-CPM-Operation-Grant", p.GrantID.String)
	req.Header.Set("X-CPM-Operation-Stage", stage)
	req.Header.Set("X-CPM-Operation-Job", p.JobID)
	req.Header.Set("X-CPM-Operation-Service-Key", key)
	req.Header.Set("Idempotency-Key", p.Command.NativeKey)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, api(503, "owner_outcome_unknown")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return nil, api(503, "owner_outcome_unknown")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		switch res.StatusCode {
		case 401, 403:
			return nil, api(403, "operation_access_denied")
		case 404:
			return nil, api(404, "owner_receipt_unavailable")
		case 409:
			return nil, api(409, "operation_request_mismatch")
		case 410:
			return nil, api(410, "operation_expired")
		default:
			return nil, api(503, "owner_outcome_unknown")
		}
	}
	var envelope struct {
		Status bool            `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if decodeOperationJSON(raw, &envelope) != nil || !envelope.Status || len(envelope.Data) == 0 || string(envelope.Data) == "null" || (stage == "receipt" && res.StatusCode != 200) {
		return nil, api(503, "owner_outcome_unknown")
	}
	return envelope.Data, nil
}

func (s *Server) processAuthorized(ctx context.Context, jobID string, sc Scope) error {
	var p privateExecutionPlan
	var ss Session
	var run bool
	var deliveryGeneration int64
	workerLease := uuid()
	stage := "apply"
	err := s.tx(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, jobID)
		if e != nil {
			return e
		}
		if j.Scope != sc {
			return api(403, "original_scope_required")
		}
		if terminalDeliveryStatus(j.Status) {
			return nil
		}
		if j.Status != "queued" && j.Status != "retry" {
			return errOutcomeNotSaved
		}
		p, e = loadExecutionPlan(ctx, tx, sc, jobID, true)
		if errors.Is(e, sql.ErrNoRows) {
			// Old technical deliveries have no grant and remain non-executable.
			_, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='awaiting_authorization',error_code='background_authorization_pending',lease_token=NULL,lease_until=NULL WHERE id=?", jobID)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state='awaiting_authorization' WHERE id=?", j.SessionID)
			return e
		}
		if e != nil {
			return e
		}
		if !p.GrantID.Valid || len(p.BindingBody) == 0 {
			return errOutcomeNotSaved
		}
		ss, e = loadSession(ctx, tx, sc, j.SessionID, true)
		if e != nil {
			return e
		}
		if p.SessionID != ss.ID || p.Command.Type != ss.Type || p.Command.Owner != ss.Owner {
			return api(503, "execution_plan_integrity_failed")
		}
		if p.ApplyStartedAt.Valid || p.GrantMode == "receipt" {
			stage = "receipt"
		}
		if !p.GrantExpiresAt.Valid || !p.GrantExpiresAt.Time.After(time.Now()) || stage == "apply" && !ss.Expires.After(time.Now()) {
			status := "authorization_expired"
			if p.ApplyStartedAt.Valid || p.GrantMode == "receipt" {
				status = "outcome_unknown"
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status=?,error_code='operation_expired',lease_token=NULL,lease_until=NULL WHERE id=?", status, jobID); e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state=? WHERE id=?", status, j.SessionID)
			return e
		}
		if stage == "apply" {
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_execution_plans_v1 SET apply_started_at=UTC_TIMESTAMP(6) WHERE job_id=? AND apply_started_at IS NULL", jobID); e != nil {
				return e
			}
		}
		deliveryGeneration = j.Generation
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='running',attempt=attempt+1,lease_token=?,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 120 SECOND) WHERE id=?", workerLease, jobID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state='running' WHERE id=?", j.SessionID); e != nil {
			return e
		}
		run = true
		return nil
	})
	if err != nil || !run {
		return err
	}
	// Completion always requires the readonly matching native receipt, including
	// after a successful apply response. A malformed/lost reply is not no-change.
	if stage == "apply" {
		_, _ = s.callOperationOwner(ctx, p, "apply", p.Command.Method, p.Command.Path, p.Command.Body)
	}
	method, path, body, err := operationReceiptCommand(p)
	var result json.RawMessage
	if err == nil {
		result, err = s.callOperationOwner(ctx, p, "receipt", method, path, body)
	}
	if err == nil {
		result, err = confirmedOperationResult(p, result)
	}
	status, errorCode := "completed", ""
	if err != nil {
		status, errorCode = "outcome_unknown", "owner_outcome_unknown"
		var ae *APIError
		if errors.As(err, &ae) {
			errorCode = ae.Message
			if ae.Code == 403 {
				status = "access_required"
			}
		}
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.tx(persist, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(persist, tx, jobID)
		if e != nil {
			return e
		}
		if j.Scope != sc {
			return api(403, "original_scope_required")
		}
		if j.Status == "completed" {
			return nil
		}
		if status != "completed" {
			// A newer explicit recovery or worker owns its own grant, generation
			// and lease. An old negative receipt cannot overwrite that work.
			if j.Generation != deliveryGeneration || j.Status != "running" {
				return nil
			}
			var lease sql.NullString
			if e = tx.QueryRowContext(persist, "SELECT lease_token FROM uploads_jobs_v1 WHERE id=?", jobID).Scan(&lease); e != nil {
				return e
			}
			current, e := loadExecutionPlan(persist, tx, sc, jobID, true)
			if e != nil {
				return e
			}
			if !lease.Valid || lease.String != workerLease || current.GrantID != p.GrantID {
				return nil
			}
		}
		var report json.RawMessage
		processed := 0
		if status == "completed" {
			report = makeReport(ss, p.ConfirmedPreview, result)
			processed, _ = reportCount(report)
			if _, e = tx.ExecContext(persist, "UPDATE uploads_execution_plans_v1 SET owner_result=? WHERE job_id=?", result, jobID); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(persist, "UPDATE uploads_jobs_v1 SET status=?,error_code=?,processed=?,total=IF(? > 0,?,total),report=?,lease_token=NULL,lease_until=NULL WHERE id=?", status, errorCode, processed, processed, processed, report, jobID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(persist, "UPDATE uploads_sessions_v1 SET state=? WHERE id=?", status, ss.ID); e != nil {
			return e
		}
		t, _ := findType(ss.Type)
		return event(persist, tx, sc, t, jobID, "job."+status, ss.Revision)
	})
}
