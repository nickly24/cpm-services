package core

import "context"

// Every future credential reset, revoke-all or role/profile identity transition
// must call this helper in the same transaction as the security mutation.
// Auth's background-operation epoch intentionally reuses this account revision.
// Single-session logout and cabinet changes never call it.
func bumpAccountAuthVersion(ctx context.Context, db DBTX, accountID int64) error {
	if accountID < 1 {
		return bad("invalid_account_id")
	}
	res, e := db.ExecContext(ctx, "UPDATE auth_account_state SET auth_version=auth_version+1 WHERE user_id=?", accountID)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return missing
	}
	return nil
}
func setAccountActiveAndBumpAuthVersion(ctx context.Context, db DBTX, accountID int64, active bool) error {
	if accountID < 1 {
		return bad("invalid_account_id")
	}
	res, e := db.ExecContext(ctx, "UPDATE auth_account_state SET is_active=?,auth_version=auth_version+1 WHERE user_id=?", active, accountID)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return missing
	}
	return nil
}
