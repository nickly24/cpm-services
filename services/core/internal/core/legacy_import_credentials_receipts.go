package core

import (
	"database/sql"
	"errors"
	"net/http"
)

// Credential import remains a distinct contract. Receipts contain no parsed
// credentials, hashes or full request; they only point to the private session.
func credentialImportReceipt(r *http.Request, db DBTX, a Actor, d int64, key, digest string) (int64, bool, error) {
	var stored string
	var session int64
	e := db.QueryRowContext(r.Context(), "SELECT command_hash,session_id FROM core_import_receipts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", a.OrganizationID, d, a.AccountID, key).Scan(&stored, &session)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, false, nil
	}
	if e != nil {
		return 0, false, e
	}
	if stored != digest {
		return 0, false, &APIError{409, "idempotency_key_reused"}
	}
	return session, true, nil
}
func (s *Server) importReceiptGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if e := legacyImportPermission(a, d, false); e != nil {
		return nil, 0, e
	}
	copy := r.Clone(r.Context())
	copy.Header.Set("Idempotency-Key", r.PathValue("receipt"))
	key, e := legacyCommandKey(copy, true)
	if e != nil {
		return nil, 0, e
	}
	row, e := one(r.Context(), s.DB, "SELECT i.id session_id,i.status,i.created_at FROM core_import_receipts c JOIN core_imports i ON i.id=c.session_id WHERE c.organization_id=? AND c.division_id=? AND c.actor_id=? AND c.command_key=? AND i.organization_id=c.organization_id AND i.division_id=c.division_id AND i.actor_id=c.actor_id", a.OrganizationID, d, a.AccountID, key)
	if e != nil {
		return nil, 0, e
	}
	return row, 200, nil
}
