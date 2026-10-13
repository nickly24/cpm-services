package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type privateExecutionPlan struct {
	JobID, SessionID, CoordinatorKey, IssuanceKey string
	Scope                                         Scope
	Command                                       ownerCommandPlan
	PreviewBody, ConfirmedPreview, BindingBody    []byte
	PreviewBodySHA256                             sql.NullString
	GrantID                                       sql.NullString
	OriginalGrantID                               sql.NullString
	GrantMode                                     string
	GrantExpiresAt, ApplyStartedAt                sql.NullTime
	OwnerResult                                   []byte
	UploadExpiresAt                               time.Time
}

// Caller holds the scoped job/session locks; this insert belongs to the same
// transaction as the authorizing job reservation. No network call runs here.
func saveExecutionPlan(ctx context.Context, tx *sql.Tx, p privateExecutionPlan) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO uploads_execution_plans_v1
	(job_id,organization_id,division_id,actor_account_id,session_id,type,owner,
	coordinator_command_key,native_command_key,issuance_key,apply_method,apply_path,
	apply_body,apply_body_sha256,preview_body,preview_body_sha256,confirmed_preview)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.JobID, p.Scope.OrganizationID, p.Scope.DivisionID, p.Scope.ActorID, p.SessionID,
		p.Command.Type, p.Command.Owner, p.CoordinatorKey, p.Command.NativeKey, p.IssuanceKey, p.Command.Method, p.Command.Path,
		p.Command.Body, p.Command.BodySHA256, p.PreviewBody, p.PreviewBodySHA256, p.ConfirmedPreview)
	return err
}

func loadExecutionPlan(ctx context.Context, q query, sc Scope, jobID string, lock bool) (privateExecutionPlan, error) {
	var p privateExecutionPlan
	stmt := `SELECT job_id,organization_id,division_id,actor_account_id,session_id,type,owner,
	coordinator_command_key,native_command_key,issuance_key,apply_method,apply_path,apply_body,
	apply_body_sha256,preview_body,preview_body_sha256,confirmed_preview,binding_body,
	original_grant_id,grant_id,grant_mode,grant_expires_at,apply_started_at,owner_result
	FROM uploads_execution_plans_v1 WHERE job_id=? AND organization_id=? AND division_id=? AND actor_account_id=?`
	if lock {
		stmt += " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, stmt, jobID, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(
		&p.JobID, &p.Scope.OrganizationID, &p.Scope.DivisionID, &p.Scope.ActorID, &p.SessionID, &p.Command.Type, &p.Command.Owner,
		&p.CoordinatorKey, &p.Command.NativeKey, &p.IssuanceKey, &p.Command.Method, &p.Command.Path, &p.Command.Body,
		&p.Command.BodySHA256, &p.PreviewBody, &p.PreviewBodySHA256, &p.ConfirmedPreview, &p.BindingBody,
		&p.OriginalGrantID, &p.GrantID, &p.GrantMode, &p.GrantExpiresAt, &p.ApplyStartedAt, &p.OwnerResult)
	if err != nil {
		return p, err
	}
	if p.JobID != jobID || p.Scope != sc || p.Command.NativeKey != ownerCommand("commit", jobID, "") ||
		!canonicalUUID(p.IssuanceKey) || !keyPattern.MatchString(p.CoordinatorKey) ||
		len(p.Command.Body) > stagedInputLimit || digest(p.Command.Body) != p.Command.BodySHA256 ||
		p.PreviewBodySHA256.Valid && digest(p.PreviewBody) != p.PreviewBodySHA256.String {
		return p, api(503, "execution_plan_integrity_failed")
	}
	if len(p.BindingBody) > 0 {
		var binding operationBinding
		if json.Unmarshal(p.BindingBody, &binding) != nil || binding.JobID != p.JobID || binding.UploadSessionID != p.SessionID ||
			binding.Owner != p.Command.Owner || binding.Type != p.Command.Type || binding.NativeCommandKey != p.Command.NativeKey ||
			binding.CoordinatorCommandKey != p.CoordinatorKey || binding.ApplyWireBodySHA256 != p.Command.BodySHA256 {
			return p, api(503, "execution_plan_integrity_failed")
		}
		p.Command.OwnerRequestSHA256 = binding.OwnerRequestSHA256
	}
	return p, nil
}
