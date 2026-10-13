package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) jobGet(r *http.Request, a Actor, sc Scope) (any, int, error) {
	j, e := loadJob(r.Context(), s.DB, sc, r.PathValue("job"))
	if e != nil {
		return nil, 0, e
	}
	t, _ := findType(j.Type)
	if e = permission(a, t, false); e != nil {
		return nil, 0, e
	}
	if s.Config.OperationsEnabled {
		meta, e := s.jobWithCapabilities(r.Context(), a, sc, j.ID)
		if e != nil {
			return nil, 0, e
		}
		if r.PathValue("report") != "report" {
			return meta, 200, nil
		}
		if len(j.Report) == 0 {
			return nil, 0, api(409, "report_not_ready")
		}
		p, e := loadExecutionPlan(r.Context(), s.DB, sc, j.ID, false)
		if e != nil {
			return nil, 0, e
		}
		method, path, body, e := operationReceiptCommand(p)
		if e != nil {
			return nil, 0, e
		}
		raw, e := s.call(r.Context(), method, s.operationOwnerBase(p.Command.Owner), path, r.Header.Get("Authorization"), p.Command.NativeKey, "application/json", body)
		if e != nil {
			return nil, 0, e
		}
		if _, e = confirmedOperationResult(p, raw); e != nil {
			return nil, 0, e
		}
		return map[string]any{"job_id": j.ID, "scope": sc, "type": j.Type, "owner": j.Owner, "report": j.Report}, 200, nil
	}
	if len(j.Report) > 0 {
		ss, e := loadSession(r.Context(), s.DB, sc, j.SessionID, false)
		if e != nil {
			return nil, 0, e
		}
		if _, e = s.ownerGet(r.Context(), ss, r.Header.Get("Authorization")); e != nil {
			return nil, 0, e
		}
	}
	if r.PathValue("report") == "report" {
		if len(j.Report) == 0 {
			return nil, 0, api(409, "report_not_ready")
		}
		return map[string]any{"job_id": j.ID, "scope": sc, "type": j.Type, "owner": j.Owner, "report": j.Report}, 200, nil
	}
	return j, 200, nil
}
func (s *Server) listJobs(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !a.Can("upload", false) {
		return nil, 0, api(403, "access_denied")
	}
	n, e := limit(r)
	if e != nil {
		return nil, 0, e
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT id,type FROM uploads_jobs_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? ORDER BY created_at DESC LIMIT ?", sc.OrganizationID, sc.DivisionID, sc.ActorID, n)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id, kind string
		if e = rows.Scan(&id, &kind); e != nil {
			return nil, 0, e
		}
		t, _ := findType(kind)
		if permission(a, t, false) == nil {
			ids = append(ids, id)
		}
	}
	if e = rows.Err(); e != nil {
		return nil, 0, e
	}
	out := []Job{}
	for _, id := range ids {
		j, e := loadJob(r.Context(), s.DB, sc, id)
		if e != nil {
			return nil, 0, e
		}
		j.Report = nil // list is metadata; sensitive reports require a live owner check
		if s.Config.OperationsEnabled {
			j, e = s.jobWithCapabilities(r.Context(), a, sc, id)
			if e != nil {
				return nil, 0, e
			}
		}
		out = append(out, j)
	}
	return out, 200, nil
}
func (s *Server) retry(r *http.Request, a Actor, sc Scope) (any, int, error) {
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	var in struct{}
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	j, e := loadJob(r.Context(), s.DB, sc, r.PathValue("job"))
	if e != nil {
		return nil, 0, e
	}
	t, e := findType(j.Type)
	if e != nil {
		return nil, 0, e
	}
	if e = permission(a, t, true); e != nil {
		return nil, 0, e
	}
	if s.Config.OperationsEnabled {
		var state string
		e = s.DB.QueryRowContext(r.Context(), "SELECT state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&state)
		if e != nil && e != sql.ErrNoRows {
			return nil, 0, e
		}
		if e == nil && state == "completed" {
			// reserve below still verifies this key belongs to the exact action.
		} else {
			p, er := loadExecutionPlan(r.Context(), s.DB, sc, j.ID, false)
			if er != nil {
				return nil, 0, api(409, "background_authorization_required")
			}
			caps := operationCapabilities(j, &p, true, time.Now())
			if !caps.RetryDelivery {
				return nil, 0, api(409, "receipt_recovery_required")
			}
		}
	}
	ss, e := loadSession(r.Context(), s.DB, sc, j.SessionID, false)
	if e != nil {
		return nil, 0, e
	}
	if j.Status != "completed" && !ss.Expires.After(time.Now()) {
		return nil, 0, api(410, "session_expired")
	}
	_, _, e = s.reserve(r.Context(), sc, key, "job.retry:"+j.ID, digest([]byte(`{}`)), j.ID)
	if e != nil {
		return nil, 0, e
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		current, e := deliveryJobLocked(r.Context(), tx, j.ID)
		if e != nil {
			return e
		}
		if current.Scope != sc {
			return api(404, "job_not_found")
		}
		var state string
		if e = tx.QueryRowContext(r.Context(), "SELECT state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=? FOR UPDATE", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&state); e != nil {
			return e
		}
		// A repeated HTTP command cannot enqueue another generation, including
		// when the first response was lost and Rabbit already consumed its job.
		if state == "completed" {
			return nil
		}
		if current.Status == "completed" {
			return receipt(r.Context(), tx, sc, key, "completed")
		}
		if s.Config.OperationsEnabled {
			p, er := loadExecutionPlan(r.Context(), tx, sc, j.ID, true)
			if er != nil {
				return er
			}
			if p.ApplyStartedAt.Valid || p.GrantMode != "apply" || !p.GrantID.Valid || !p.GrantExpiresAt.Valid || !p.GrantExpiresAt.Time.After(time.Now()) {
				return api(409, "receipt_recovery_required")
			}
		}
		if current.Status != "awaiting_authorization" && current.Status != "failed" && current.Status != "retry" {
			return api(409, "job_not_retryable")
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE uploads_jobs_v1 SET status='queued',error_code='',attempt=0,lease_token=NULL,lease_until=NULL,available_at=UTC_TIMESTAMP(6) WHERE id=?", j.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state='queued' WHERE id=?", current.SessionID); e != nil {
			return e
		}
		if e = EnqueueImport(r.Context(), tx, j.ID, sc); e != nil {
			return e
		}
		if e = receipt(r.Context(), tx, sc, key, "completed"); e != nil {
			return e
		}
		return event(r.Context(), tx, sc, t, j.ID, "job.retry_queued", current.PreviewRevision)
	})
	if e != nil {
		return nil, 0, e
	}
	return s.jobGet(r, a, sc)
}

func (s *Server) RunBroker(ctx context.Context) { s.Broker.Run(ctx) }

// Maintenance handles stale editing outcomes and bounded file retention only.
// It never selects an import for execution; Rabbit owns import delivery.
func (s *Server) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = s.DB.ExecContext(c, "UPDATE uploads_sessions_v1 ss SET state='edit_unknown' WHERE state='editing' AND NOT EXISTS (SELECT 1 FROM uploads_receipts_v1 r WHERE r.resource_id=ss.id AND r.action LIKE 'session.edit:%' AND r.created_at>DATE_SUB(UTC_TIMESTAMP(6), INTERVAL 180 SECOND))")
		_ = s.cleanup(c)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Background authorization is a concrete pending dependency. The original
// browser bearer is neither cached in memory nor persisted in SQL/Rabbit. Until
// an approved Auth delegation contract is available this worker cannot apply
// domain changes, and records a visible waiting outcome before acknowledging.
func (s *Server) process(ctx context.Context, id string, sc Scope) error {
	if s.Config.OperationsEnabled {
		return s.processAuthorized(ctx, id, sc)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, id)
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
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='awaiting_authorization',error_code='background_authorization_pending',attempt=attempt+1,lease_token=NULL,lease_until=NULL WHERE id=?", id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state='awaiting_authorization' WHERE id=?", j.SessionID); e != nil {
			return e
		}
		t, e := findType(j.Type)
		if e != nil {
			return e
		}
		return event(ctx, tx, sc, t, id, "job.awaiting_authorization", j.PreviewRevision)
	})
}

func makeReport(ss Session, preview, result json.RawMessage) json.RawMessage {
	var p map[string]any
	if decodeJSONNumbers(preview, &p) != nil {
		p = nil
	}
	rows := []any{}
	if nested, ok := p["preview"].(map[string]any); ok {
		p = nested
	}
	sources, _ := p["cards"].([]any)
	if sources == nil {
		sources, _ = p["users"].([]any)
	}
	if sources == nil {
		sources, _ = p["rows"].([]any)
	}
	if sources == nil {
		sources, _ = p["selected_cards"].([]any)
	}
	for i, x := range sources {
		r, _ := x.(map[string]any)
		row := r["row"]
		if row == nil {
			row = r["source_row"]
		}
		if row == nil {
			row = i + 2
		}
		state := "owner_confirmed"
		if r["action"] == "skip" || r["excluded"] == true {
			state = "skipped"
		}
		rows = append(rows, map[string]any{"source_row": row, "row_id": r["row_id"], "state": state, "warnings": r["warnings"]})
	}
	// This records owner-confirmed actions, never speculative incremental commits.
	b, _ := json.Marshal(map[string]any{"session_id": ss.ID, "owner_session_id": ss.OwnerSessionID, "owner_revision": ss.OwnerRevision, "source_hash": ss.SourceHash, "rows": rows, "owner_result": json.RawMessage(masked(result)), "atomicity": "owner_transaction_only"})
	return b
}
func reportCount(raw json.RawMessage) (int, int) {
	var v struct {
		Rows []any `json:"rows"`
	}
	json.Unmarshal(raw, &v)
	n := len(v.Rows)
	if n == 0 {
		n = 1
	}
	return n, n
}
