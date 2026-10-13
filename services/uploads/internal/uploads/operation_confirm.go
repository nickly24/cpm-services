package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type ConfirmImportDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Confirmed        bool  `json:"confirmed"`
}

var errConfirmationReplay = errors.New("confirmation replay requires unlocked lookup")

func (s *Server) confirmedCommit(r *http.Request, a Actor, sc Scope) (any, int, error) {
	key, err := command(r)
	if err != nil {
		return nil, 0, err
	}
	var in ConfirmImportDTO
	if err = decodeOperationRequest(r, &in); err != nil {
		return nil, 0, err
	}
	if !in.Confirmed || in.ExpectedRevision < 1 {
		return nil, 0, api(400, "import_confirmation_required")
	}
	ss, err := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if err != nil {
		return nil, 0, err
	}
	t, err := findType(ss.Type)
	if err != nil {
		return nil, 0, err
	}
	if err = permission(a, t, true); err != nil {
		return nil, 0, err
	}
	if !t.Commit || ss.Type == "cards.transform" {
		return nil, 0, api(409, "background_type_unavailable")
	}
	if ss.Revision != in.ExpectedRevision {
		return nil, 0, api(409, "preview_modified")
	}
	body, _ := json.Marshal(in)
	jobID, _, err := s.reserve(r.Context(), sc, key, "session.commit:"+ss.ID, digest(body), uuid())
	if err != nil {
		return nil, 0, err
	}
	var p privateExecutionPlan
	err = s.tx(r.Context(), func(tx *sql.Tx) error {
		current, e := loadSession(r.Context(), tx, sc, ss.ID, true)
		if e != nil {
			return e
		}
		if current.Revision != in.ExpectedRevision {
			return api(409, "preview_modified")
		}
		if current.State != "preview" {
			// The worker locks job -> plan -> session. Release this session lock
			// before reading an existing job/plan, avoiding the reverse order.
			return errConfirmationReplay
		}
		if !current.Expires.After(time.Now()) {
			return api(410, "session_expired")
		}
		var staged *stagedPreview
		if stagedType(ss.Type) {
			cached, e := loadStagedPreview(r.Context(), tx, current, true)
			if e != nil {
				return e
			}
			staged = &cached
		}
		cmd, e := immutableOwnerCommand(current, jobID, staged)
		if e != nil {
			return e
		}
		p = privateExecutionPlan{JobID: jobID, SessionID: current.ID, Scope: sc, CoordinatorKey: key, IssuanceKey: ownerCommand("grant", jobID, "apply"), Command: cmd, GrantMode: "apply"}
		if staged != nil {
			p.PreviewBody = append([]byte(nil), staged.Input...)
			p.PreviewBodySHA256 = sql.NullString{String: digest(p.PreviewBody), Valid: true}
			p.ConfirmedPreview = masked(staged.Preview)
		}
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO uploads_jobs_v1(id,organization_id,division_id,actor_account_id,session_id,type,owner,status) VALUES(?,?,?,?,?,?,?,'authorizing')", jobID, sc.OrganizationID, sc.DivisionID, sc.ActorID, current.ID, t.ID, t.Owner); e != nil {
			return e
		}
		if e = saveExecutionPlan(r.Context(), tx, p); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state='authorizing' WHERE id=?", current.ID); e != nil {
			return e
		}
		return event(r.Context(), tx, sc, t, jobID, "job.confirmed", current.Revision)
	})
	if errors.Is(err, errConfirmationReplay) {
		var existingID string
		err = s.DB.QueryRowContext(r.Context(), "SELECT id FROM uploads_jobs_v1 WHERE session_id=? AND organization_id=? AND division_id=? AND actor_account_id=?", ss.ID, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&existingID)
		if err != nil {
			return nil, 0, api(409, "preview_not_confirmable")
		}
		// Historical jobs and other confirmation intents cannot be upgraded.
		p, err = loadExecutionPlan(r.Context(), s.DB, sc, existingID, false)
		if err != nil || p.CoordinatorKey != key || existingID != jobID {
			return nil, 0, api(409, "original_confirmation_required")
		}
	}
	if err != nil {
		return nil, 0, err
	}
	j, err := loadJob(r.Context(), s.DB, sc, jobID)
	if err != nil {
		return nil, 0, err
	}
	if j.Status != "authorizing" && j.Status != "authorization_unknown" {
		j, err = s.jobWithCapabilities(r.Context(), a, sc, j.ID)
		return j, 202, err
	}
	if len(p.BindingBody) == 0 {
		var raw json.RawMessage
		var quote *testsOperationQuote
		if stagedType(ss.Type) {
			cached, e := loadStagedPreview(r.Context(), s.DB, ss, false)
			if e != nil {
				return nil, 0, e
			}
			if e = s.stagedConfirmableSnapshot(r.Context(), ss, r.Header.Get("Authorization"), cached); e != nil {
				return s.confirmationFailure(r, a, sc, j.ID, e)
			}
			raw = cached.Preview
			path := "/internal/tests/imports/json/prepare"
			if ss.Type == "tests.external-results" {
				path = "/internal/tests/external-results/prepare"
			}
			prepared, e := s.callHeaders(r.Context(), "POST", s.Config.TestsURL, path, r.Header.Get("Authorization"), p.Command.NativeKey, "application/json", p.Command.Body, map[string]string{"X-CPM-Import-Prepare-Key": s.Config.OperationDeliveryKeys["tests"]})
			if e != nil {
				return s.confirmationFailure(r, a, sc, j.ID, e)
			}
			quote = &testsOperationQuote{}
			if decodeOperationJSON(prepared, quote) != nil {
				return s.confirmationFailure(r, a, sc, j.ID, api(503, "owner_invalid_operation_quote"))
			}
		} else {
			raw, err = s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
			if err != nil {
				return s.confirmationFailure(r, a, sc, j.ID, err)
			}
		}
		binding, e := bindOwnerOperation(ss, p, raw, quote)
		if e != nil {
			return s.confirmationFailure(r, a, sc, j.ID, e)
		}
		p.BindingBody, e = operationCanonical(binding)
		if e != nil {
			return nil, 0, e
		}
		p.ConfirmedPreview = masked(raw)
		err = s.tx(r.Context(), func(tx *sql.Tx) error {
			saved, e := loadExecutionPlan(r.Context(), tx, sc, j.ID, true)
			if e != nil {
				return e
			}
			if len(saved.BindingBody) > 0 && !bytes.Equal(saved.BindingBody, p.BindingBody) {
				return api(409, "owner_preview_modified")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE uploads_execution_plans_v1 SET binding_body=?,confirmed_preview=? WHERE job_id=? AND binding_body IS NULL", p.BindingBody, p.ConfirmedPreview, j.ID)
			return e
		})
		if err != nil {
			return nil, 0, err
		}
	}
	var binding operationBinding
	if json.Unmarshal(p.BindingBody, &binding) != nil {
		return nil, 0, api(503, "execution_plan_integrity_failed")
	}
	env, err := s.requestOperationGrant(r.Context(), r.Header.Get("Authorization"), p.IssuanceKey, "/internal/auth/operation-grants", p.BindingBody)
	if err != nil {
		return s.confirmationFailure(r, a, sc, j.ID, err)
	}
	if err = verifyIssuedOperation(env, binding, sc, "apply", time.Now().UTC()); err != nil {
		return s.confirmationFailure(r, a, sc, j.ID, err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err = s.bindGrantAndEnqueue(ctx, sc, j.ID, env, key); err != nil {
		return nil, 0, err
	}
	j, err = s.jobWithCapabilities(ctx, a, sc, j.ID)
	return j, 202, err
}

func (s *Server) stagedConfirmableSnapshot(ctx context.Context, ss Session, token string, p stagedPreview) error {
	if err := s.freshStagedPreview(ctx, ss, token, p); err != nil {
		return err
	}
	var state struct {
		Valid     *bool `json:"valid"`
		CanCommit *bool `json:"can_commit"`
	}
	if json.Unmarshal(p.Preview, &state) != nil || ss.Type == "tests.json" && (state.Valid == nil || !*state.Valid) || ss.Type == "tests.external-results" && (state.CanCommit == nil || !*state.CanCommit) {
		return api(409, "preview_has_errors")
	}
	return nil
}

func (s *Server) confirmationFailure(r *http.Request, a Actor, sc Scope, jobID string, cause error) (any, int, error) {
	code := "operation_authorization_pending"
	var ae *APIError
	if errors.As(cause, &ae) {
		code = ae.Message
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_, err := s.DB.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='authorization_unknown',error_code=? WHERE id=? AND organization_id=? AND division_id=? AND actor_account_id=? AND status='authorizing'", code, jobID, sc.OrganizationID, sc.DivisionID, sc.ActorID)
	if err != nil {
		return nil, 0, err
	}
	j, err := s.jobWithCapabilities(ctx, a, sc, jobID)
	return j, 202, err
}

func (s *Server) bindGrantAndEnqueue(ctx context.Context, sc Scope, jobID string, env operationEnvelope, key string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, jobID)
		if e != nil {
			return e
		}
		if j.Scope != sc {
			return api(404, "job_not_found")
		}
		p, e := loadExecutionPlan(ctx, tx, sc, jobID, true)
		if e != nil {
			return e
		}
		if j.Status == "completed" {
			return receipt(ctx, tx, sc, key, "completed")
		}
		if p.GrantID.Valid && p.GrantID.String == env.GrantID && (j.Status == "queued" || terminalDeliveryStatus(j.Status) || j.Status == "running") {
			return nil
		}
		if j.Status != "authorizing" && j.Status != "authorization_unknown" && env.Mode != "receipt" {
			return api(409, "job_not_authorizable")
		}
		if p.ApplyStartedAt.Valid && env.Mode != "receipt" {
			return api(409, "receipt_recovery_required")
		}
		var binding operationBinding
		if json.Unmarshal(p.BindingBody, &binding) != nil || !sameOperationBinding(binding, env.Binding) {
			return api(409, "operation_request_mismatch")
		}
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_execution_plans_v1 SET original_grant_id=COALESCE(original_grant_id,?),grant_id=?,grant_mode=?,grant_expires_at=? WHERE job_id=?", env.GrantID, env.GrantID, env.Mode, env.ExpiresAt.UTC().Truncate(time.Microsecond), jobID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='queued',error_code='',lease_token=NULL,lease_until=NULL,available_at=UTC_TIMESTAMP(6) WHERE id=?", jobID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state='queued' WHERE id=?", j.SessionID); e != nil {
			return e
		}
		if e = receipt(ctx, tx, sc, key, "completed"); e != nil {
			return e
		}
		if e = EnqueueImport(ctx, tx, jobID, sc); e != nil {
			return e
		}
		t, _ := findType(j.Type)
		return event(ctx, tx, sc, t, jobID, "job.authorized", j.PreviewRevision)
	})
}
