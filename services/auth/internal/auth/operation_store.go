package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type SQLOperationStore struct{ DB *sql.DB }

const operationColumns = `id,issuance_key,input_hash,mode,parent_id,account_id,organization_id,division_id,profile_id,auth_version,role,binding,issued_at,expires_at,revoked`

type operationRow interface{ Scan(...any) error }

func scanOperationGrant(row operationRow) (OperationGrant, error) {
	var g OperationGrant
	var parent sql.NullString
	var binding []byte
	e := row.Scan(&g.ID, &g.IssuanceKey, &g.InputHash, &g.Mode, &parent, &g.AccountID, &g.OrganizationID, &g.DivisionID, &g.ProfileID, &g.AuthVersion, &g.Role, &binding, &g.IssuedAt, &g.ExpiresAt, &g.Revoked)
	if errors.Is(e, sql.ErrNoRows) {
		return g, opError(404, "operation_not_found")
	}
	if e != nil {
		return g, opError(503, "authorization_unavailable")
	}
	if parent.Valid {
		g.ParentID = &parent.String
	}
	if decodeOperationJSON(binding, &g.Binding) != nil || g.Binding.validate(g.DivisionID) != nil || !operationHex32.MatchString(g.ID) || !operationSHA.MatchString(g.InputHash) || g.AccountID < 1 || g.OrganizationID < 1 || g.DivisionID < 1 || g.ProfileID < 1 || g.AuthVersion < 1 || (g.Mode != "apply" && g.Mode != "receipt") {
		return g, opError(503, "authorization_unavailable")
	}
	return g, nil
}

func (s SQLOperationStore) Get(ctx context.Context, id string) (OperationGrant, error) {
	return scanOperationGrant(s.DB.QueryRowContext(ctx, "SELECT "+operationColumns+" FROM auth_operation_grants WHERE id=?", id))
}

func (s SQLOperationStore) Issue(ctx context.Context, g OperationGrant) (OperationGrant, error) {
	// An expired source cannot create a record, but a matching lost-response
	// issuance must still return its historical ID/expiry for receipt recovery.
	if !g.ExpiresAt.After(g.IssuedAt) {
		old, e := scanOperationGrant(s.DB.QueryRowContext(ctx, "SELECT "+operationColumns+" FROM auth_operation_grants WHERE account_id=? AND issuance_key=?", g.AccountID, g.IssuanceKey))
		if e != nil {
			var oe *operationError
			if errors.As(e, &oe) && oe.status == 404 {
				return g, opError(410, "operation_expired")
			}
			return g, e
		}
		if old.InputHash != g.InputHash {
			return g, opError(409, "operation_issuance_conflict")
		}
		return old, nil
	}
	encoded, e := json.Marshal(g.Binding)
	if e != nil {
		return g, opError(503, "authorization_unavailable")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return g, opError(503, "authorization_unavailable")
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `INSERT INTO auth_operation_grants
 (id,issuance_key,input_hash,mode,parent_id,account_id,organization_id,division_id,profile_id,auth_version,role,binding,issued_at,expires_at,revoked)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,FALSE) ON DUPLICATE KEY UPDATE id=id`, g.ID, g.IssuanceKey, g.InputHash, g.Mode, g.ParentID, g.AccountID, g.OrganizationID, g.DivisionID, g.ProfileID, g.AuthVersion, g.Role, encoded, g.IssuedAt, g.ExpiresAt)
	if e != nil {
		return g, opError(503, "authorization_unavailable")
	}
	old, e := scanOperationGrant(tx.QueryRowContext(ctx, "SELECT "+operationColumns+" FROM auth_operation_grants WHERE account_id=? AND issuance_key=? FOR UPDATE", g.AccountID, g.IssuanceKey))
	if e != nil {
		return g, e
	}
	if old.InputHash != g.InputHash {
		return g, opError(409, "operation_issuance_conflict")
	}
	if tx.Commit() != nil {
		return g, opError(503, "authorization_unavailable")
	}
	return old, nil
}

func (s SQLOperationStore) Revoke(ctx context.Context, id string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE auth_operation_grants SET revoked=TRUE WHERE id=?", id)
	if e != nil {
		return opError(503, "authorization_unavailable")
	}
	return nil
}
