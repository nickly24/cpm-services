package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

func operationCapabilities(j Job, p *privateExecutionPlan, canEdit bool, now time.Time) JobCapabilities {
	var out JobCapabilities
	if !canEdit || p == nil || j.Scope != p.Scope || j.ID != p.JobID {
		return out
	}
	active := p.GrantID.Valid && p.GrantExpiresAt.Valid && p.GrantExpiresAt.Time.After(now)
	if !p.ApplyStartedAt.Valid && !p.GrantID.Valid && p.UploadExpiresAt.After(now) && (j.Status == "authorizing" || j.Status == "authorization_unknown") {
		out.RetryConfirmation = true
	}
	out.RetryDelivery = active && !p.ApplyStartedAt.Valid && p.GrantMode == "apply" && (j.Status == "retry" || j.Status == "failed")
	out.RevokeAuthorization = p.OriginalGrantID.Valid && j.Status != "completed" && j.Status != "revoked" && j.Error != "operation_revoked"
	out.RecoverReceipt = p.OriginalGrantID.Valid && (j.Status == "outcome_unknown" || j.Status == "access_required" || j.Status == "authorization_expired" || j.Status == "revoked")
	return out
}

func (s *Server) jobWithCapabilities(ctx context.Context, a Actor, sc Scope, id string) (Job, error) {
	j, err := loadJob(ctx, s.DB, sc, id)
	if err != nil {
		return j, err
	}
	j.Report = nil
	if !s.Config.OperationsEnabled {
		return j, nil
	}
	t, err := findType(j.Type)
	if err != nil {
		return j, err
	}
	p, err := loadExecutionPlan(ctx, s.DB, sc, id, false)
	if errors.Is(err, sql.ErrNoRows) {
		j.Capabilities = &JobCapabilities{}
		j.Authorization = &JobAuthorizationMetadata{Bound: false, Mode: "apply"}
		return j, nil
	}
	if err != nil {
		return j, err
	}
	ss, err := loadSession(ctx, s.DB, sc, j.SessionID, false)
	if err != nil {
		return j, err
	}
	p.UploadExpiresAt = ss.Expires
	caps := operationCapabilities(j, &p, permission(a, t, true) == nil, time.Now())
	j.Capabilities = &caps
	j.Authorization = &JobAuthorizationMetadata{Bound: p.OriginalGrantID.Valid, Mode: p.GrantMode}
	if p.GrantExpiresAt.Valid {
		expiry := p.GrantExpiresAt.Time
		j.Authorization.ExpiresAt = &expiry
	}
	return j, nil
}

type ConfirmJobActionDTO struct {
	Confirmed bool `json:"confirmed"`
}

func (s *Server) recoverJob(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !s.Config.OperationsEnabled {
		return nil, 0, api(503, "background_authorization_pending")
	}
	key, err := command(r)
	if err != nil {
		return nil, 0, err
	}
	var in ConfirmJobActionDTO
	if err = decodeOperationRequest(r, &in); err != nil {
		return nil, 0, err
	}
	if !in.Confirmed {
		return nil, 0, api(400, "import_confirmation_required")
	}
	j, err := s.jobWithCapabilities(r.Context(), a, sc, r.PathValue("job"))
	if err != nil {
		return nil, 0, err
	}
	t, _ := findType(j.Type)
	if err = permission(a, t, true); err != nil {
		return nil, 0, err
	}
	body, _ := json.Marshal(in)
	_, _, err = s.reserve(r.Context(), sc, key, "job.recover:"+j.ID, digest(body), j.ID)
	if err != nil {
		return nil, 0, err
	}
	var state string
	err = s.DB.QueryRowContext(r.Context(), "SELECT state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&state)
	if err != nil {
		return nil, 0, err
	}
	if state == "completed" {
		return j, 202, nil
	}
	if j.Capabilities == nil || !j.Capabilities.RecoverReceipt {
		return nil, 0, api(409, "receipt_recovery_unavailable")
	}
	p, err := loadExecutionPlan(r.Context(), s.DB, sc, j.ID, false)
	if err != nil {
		return nil, 0, err
	}
	var binding operationBinding
	if json.Unmarshal(p.BindingBody, &binding) != nil || !p.OriginalGrantID.Valid {
		return nil, 0, api(503, "execution_plan_integrity_failed")
	}
	env, err := s.requestOperationGrant(r.Context(), r.Header.Get("Authorization"), ownerCommand("receipt-grant", j.ID, key), "/internal/auth/operation-grants/"+p.OriginalGrantID.String+"/recover", []byte("{}"))
	if err != nil {
		return nil, 0, err
	}
	if err = verifyIssuedOperation(env, binding, sc, "receipt", time.Now()); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err = s.bindGrantAndEnqueue(ctx, sc, j.ID, env, key); err != nil {
		return nil, 0, err
	}
	j, err = s.jobWithCapabilities(ctx, a, sc, j.ID)
	return j, 202, err
}

func (s *Server) revokeOperationGrant(ctx context.Context, bearer, grantID, key string) error {
	if !lowerHexReference(grantID, 32) || !canonicalUUID(key) || bearer == "" {
		return api(400, "invalid_operation_revoke")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", s.Config.AuthURL+"/internal/auth/operation-grants/"+grantID+"/revoke", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Service-Key", s.Config.OperationsIssuerKey)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return api(503, "operation_revoke_unknown")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return api(503, "operation_revoke_unknown")
	}
	var out struct {
		Status  bool   `json:"status"`
		GrantID string `json:"grant_id,omitempty"`
	}
	if res.StatusCode != 200 || decodeOperationJSON(raw, &out) != nil || !out.Status || out.GrantID != "" && out.GrantID != grantID {
		return api(503, "operation_revoke_unknown")
	}
	return nil
}

func (s *Server) revokeJob(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !s.Config.OperationsEnabled {
		return nil, 0, api(503, "background_authorization_pending")
	}
	key, err := command(r)
	if err != nil {
		return nil, 0, err
	}
	var in ConfirmJobActionDTO
	if err = decodeOperationRequest(r, &in); err != nil {
		return nil, 0, err
	}
	if !in.Confirmed {
		return nil, 0, api(400, "import_confirmation_required")
	}
	j, err := s.jobWithCapabilities(r.Context(), a, sc, r.PathValue("job"))
	if err != nil {
		return nil, 0, err
	}
	t, _ := findType(j.Type)
	if err = permission(a, t, true); err != nil {
		return nil, 0, err
	}
	body, _ := json.Marshal(in)
	_, _, err = s.reserve(r.Context(), sc, key, "job.revoke:"+j.ID, digest(body), j.ID)
	if err != nil {
		return nil, 0, err
	}
	var state string
	err = s.DB.QueryRowContext(r.Context(), "SELECT state FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&state)
	if err != nil {
		return nil, 0, err
	}
	if state == "completed" {
		return j, 200, nil
	}
	p, err := loadExecutionPlan(r.Context(), s.DB, sc, j.ID, false)
	if err != nil {
		return nil, 0, err
	}
	if !p.OriginalGrantID.Valid {
		return nil, 0, api(409, "authorization_not_bound")
	}
	if err = s.revokeOperationGrant(r.Context(), r.Header.Get("Authorization"), p.OriginalGrantID.String, ownerCommand("revoke", j.ID, key)); err != nil {
		return nil, 0, err
	}
	if p.GrantID.Valid && p.GrantID.String != p.OriginalGrantID.String {
		if err = s.revokeOperationGrant(r.Context(), r.Header.Get("Authorization"), p.GrantID.String, ownerCommand("revoke-receipt", j.ID, key)); err != nil {
			return nil, 0, err
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	err = s.tx(ctx, func(tx *sql.Tx) error {
		current, e := deliveryJobLocked(ctx, tx, j.ID)
		if e != nil {
			return e
		}
		if current.Scope != sc {
			return api(404, "job_not_found")
		}
		plan, e := loadExecutionPlan(ctx, tx, sc, j.ID, true)
		if e != nil {
			return e
		}
		if plan.GrantID != p.GrantID || plan.OriginalGrantID != p.OriginalGrantID {
			// A concurrent explicit recovery may have issued another readonly
			// grant. Keep this action pending so replay revokes that grant too.
			return api(409, "authorization_changed_retry_revoke")
		}
		if current.Status != "completed" {
			status := "revoked"
			if plan.ApplyStartedAt.Valid {
				status = "outcome_unknown"
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status=?,error_code='operation_revoked',lease_token=NULL,lease_until=NULL WHERE id=?", status, j.ID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state=? WHERE id=?", status, j.SessionID); e != nil {
				return e
			}
		}
		if e = receipt(ctx, tx, sc, key, "completed"); e != nil {
			return e
		}
		return event(ctx, tx, sc, t, j.ID, "job.authorization_revoked", current.PreviewRevision)
	})
	if err != nil {
		return nil, 0, err
	}
	j, err = s.jobWithCapabilities(ctx, a, sc, j.ID)
	return j, 200, err
}
