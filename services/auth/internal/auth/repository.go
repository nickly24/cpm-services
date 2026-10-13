package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Repository struct{ DB *sql.DB }

func (r *Repository) FindAccount(ctx context.Context, login string) (*Account, error) {
	var id int64
	var password sql.NullString
	err := r.DB.QueryRowContext(ctx, "SELECT id,password FROM auth_users WHERE username=?", login).Scan(&id, &password)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := r.LoadAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Password = password.String
	return a, nil
}

func (r *Repository) LoadAccount(ctx context.Context, id int64) (*Account, error) {
	a := &Account{Organizations: []Organization{}}
	err := r.DB.QueryRowContext(ctx, `SELECT a.id,a.ref_id,a.username,a.role,s.auth_version FROM auth_users a
 JOIN auth_account_state s ON s.user_id=a.id WHERE a.id=? AND s.is_active=1`, id).
		Scan(&a.UserID, &a.EntityID, &a.Username, &a.Role, &a.AuthVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentials
	}
	if err != nil {
		return nil, err
	}
	table, ok := profileTables[a.Role]
	if !ok {
		return nil, ErrCredentials
	}
	if a.Role == "staff_admin" {
		var active bool
		err = r.DB.QueryRowContext(ctx, "SELECT full_name,is_active,session_version FROM admin_role_users WHERE id=?", a.EntityID).Scan(&a.FullName, &active, &a.LegacySessionVersion)
		if err == nil && !active {
			return nil, ErrCredentials
		}
	} else {
		err = r.DB.QueryRowContext(ctx, "SELECT full_name FROM `"+table+"` WHERE id=?", a.EntityID).Scan(&a.FullName)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCredentials
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT o.id,o.name FROM auth_organization_access x JOIN organizations o ON o.id=x.organization_id
 WHERE x.user_id=? AND o.is_active=1 ORDER BY o.id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		o := Organization{Divisions: []Division{}}
		if err = rows.Scan(&o.ID, &o.Name); err != nil {
			rows.Close()
			return nil, err
		}
		a.Organizations = append(a.Organizations, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if a.Role == "admin" && len(a.Organizations) > 1 {
		return nil, ErrForbidden
	}
	for i := range a.Organizations {
		o := &a.Organizations[i]
		query := `SELECT d.id,d.name,x.group_id,x.role_id,COALESCE(sr.name,'') FROM auth_division_access x
   JOIN divisions d ON d.id=x.division_id AND d.organization_id=x.organization_id
   LEFT JOIN auth_scoped_roles sr ON sr.id=x.role_id AND sr.organization_id=x.organization_id
   WHERE x.user_id=? AND x.organization_id=? AND d.is_active=1 ORDER BY d.id`
		args := []any{id, o.ID}
		if a.Role == "admin" {
			query = "SELECT id,name,NULL,NULL,'' FROM divisions WHERE organization_id=? AND is_active=1 ORDER BY id"
			args = []any{o.ID}
		}
		rows, err = r.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := Division{Permissions: map[string]Permission{}}
			if err = rows.Scan(&d.ID, &d.Name, &d.GroupID, &d.RoleID, &d.RoleName); err != nil {
				rows.Close()
				return nil, err
			}
			if a.Role == "staff_admin" && d.RoleID == nil {
				continue
			}
			o.Divisions = append(o.Divisions, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for j := range o.Divisions {
			d := &o.Divisions[j]
			if a.Role == "admin" {
				for _, s := range Sections {
					d.Permissions[s] = Permission{true, true}
				}
				continue
			}
			// Delegated roles only. Ordinary role permissions are enforced by each domain.
			if a.Role != "staff_admin" || d.RoleID == nil {
				continue
			}
			rows, err = r.DB.QueryContext(ctx, "SELECT section,can_view,can_edit FROM auth_scoped_role_permissions WHERE role_id=?", *d.RoleID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var section string
				var p Permission
				if err = rows.Scan(&section, &p.View, &p.Edit); err != nil {
					rows.Close()
					return nil, err
				}
				for _, s := range Sections {
					if section == s {
						d.Permissions[section] = p
						break
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return a, nil
}

func (r *Repository) NewSession(ctx context.Context, s Session, refreshHash string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO auth_sessions (id,user_id,organization_id,division_id,expires_at,auth_version,session_version,context_version) VALUES (?,?,?,?,?,?,?,?)", s.ID, s.UserID, s.Context.OrganizationID, s.Context.DivisionID, s.ExpiresAt, s.AuthVersion, s.LegacySessionVersion, s.ContextVersion)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO auth_refresh_tokens (token_hash,session_id) VALUES (?,?)", refreshHash, s.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func scanSession(row *sql.Row) (Session, error) {
	var s Session
	var revoked sql.NullTime
	err := row.Scan(&s.ID, &s.UserID, &s.Context.OrganizationID, &s.Context.DivisionID, &s.ExpiresAt, &revoked, &s.AuthVersion, &s.LegacySessionVersion, &s.ContextVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrToken
	}
	if err != nil {
		return s, err
	}
	if revoked.Valid || !s.ExpiresAt.After(time.Now()) {
		return s, ErrToken
	}
	return s, nil
}

func (r *Repository) Session(ctx context.Context, id string) (Session, error) {
	return scanSession(r.DB.QueryRowContext(ctx, "SELECT id,user_id,organization_id,division_id,expires_at,revoked_at,auth_version,session_version,context_version FROM auth_sessions WHERE id=?", id))
}

func (r *Repository) Switch(ctx context.Context, session Session, target ContextDTO) error {
	res, err := r.DB.ExecContext(ctx, "UPDATE auth_sessions SET organization_id=?,division_id=?,context_version=context_version+1 WHERE id=? AND context_version=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6)", target.OrganizationID, target.DivisionID, session.ID, session.ContextVersion)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrToken
	}
	return nil
}

func (r *Repository) Revoke(ctx context.Context, id string) error {
	_, err := r.DB.ExecContext(ctx, "UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,UTC_TIMESTAMP(6)) WHERE id=?", id)
	return err
}

func (r *Repository) RefreshSession(ctx context.Context, hash string) (Session, error) {
	var sid string
	err := r.DB.QueryRowContext(ctx, "SELECT session_id FROM auth_refresh_tokens WHERE token_hash=?", hash).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrToken
	}
	if err != nil {
		return Session{}, err
	}
	return r.Session(ctx, sid)
}

func (r *Repository) RotateRefresh(ctx context.Context, sid, oldHash, newHash string, issue func(Session) error) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	session, err := scanSession(tx.QueryRowContext(ctx, "SELECT id,user_id,organization_id,division_id,expires_at,revoked_at,auth_version,session_version,context_version FROM auth_sessions WHERE id=? FOR UPDATE", sid))
	if err != nil {
		return err
	}
	var used sql.NullTime
	err = tx.QueryRowContext(ctx, "SELECT used_at FROM auth_refresh_tokens WHERE token_hash=? AND session_id=? FOR UPDATE", oldHash, sid).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrToken
	}
	if err != nil {
		return err
	}
	if used.Valid {
		if _, err = tx.ExecContext(ctx, "UPDATE auth_sessions SET revoked_at=UTC_TIMESTAMP(6) WHERE id=?", sid); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrToken
	}
	// Issue from the locked current context, never from a snapshot read before a switch.
	// Failure leaves the refresh token unconsumed. No response is sent before commit.
	if err = issue(session); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_refresh_tokens SET used_at=UTC_TIMESTAMP(6) WHERE token_hash=?", oldHash); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO auth_refresh_tokens (token_hash,session_id) VALUES (?,?)", newHash, sid); err != nil {
		return err
	}
	return tx.Commit()
}
