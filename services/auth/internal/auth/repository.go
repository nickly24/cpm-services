package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Repository struct{ DB *sql.DB }

// accountSnapshot reads live account, profile and tenant permissions in one SQL
// statement. There is no permission cache. LEFT joins retain organisations with
// no divisions, while role-specific profile joins prevent cross-role ID matches.
const accountSnapshot = `
 FROM auth_users a
 JOIN auth_account_state ast ON ast.user_id=a.id AND ast.is_active=1
 LEFT JOIN students ps ON a.role='student' AND ps.id=a.ref_id
 LEFT JOIN proctors pp ON a.role='proctor' AND pp.id=a.ref_id
 LEFT JOIN examinators pe ON a.role='examinator' AND pe.id=a.ref_id
 LEFT JOIN admins pa ON a.role='admin' AND pa.id=a.ref_id
 LEFT JOIN supervisors pv ON a.role='supervisor' AND pv.id=a.ref_id
 LEFT JOIN admin_role_users pf ON a.role='staff_admin' AND pf.id=a.ref_id
 LEFT JOIN (auth_organization_access oa JOIN organizations o ON o.id=oa.organization_id AND o.is_active=1)
   ON oa.user_id=a.id
 LEFT JOIN auth_division_access dx ON a.role<>'admin' AND dx.user_id=a.id AND dx.organization_id=o.id
 LEFT JOIN divisions md ON md.id=dx.division_id AND md.organization_id=o.id AND md.is_active=1
 LEFT JOIN divisions ad ON a.role='admin' AND ad.organization_id=o.id AND ad.is_active=1
 LEFT JOIN auth_scoped_roles sr ON sr.id=dx.role_id AND sr.organization_id=dx.organization_id
 LEFT JOIN auth_scoped_role_permissions rp ON a.role='staff_admin' AND md.id IS NOT NULL AND rp.role_id=dx.role_id
`
const accountColumns = `a.id,a.ref_id,a.username,a.role,ast.auth_version,
 CASE a.role WHEN 'student' THEN ps.id WHEN 'proctor' THEN pp.id WHEN 'examinator' THEN pe.id
   WHEN 'admin' THEN pa.id WHEN 'supervisor' THEN pv.id WHEN 'staff_admin' THEN pf.id END,
 CASE a.role WHEN 'student' THEN ps.full_name WHEN 'proctor' THEN pp.full_name WHEN 'examinator' THEN pe.full_name
   WHEN 'admin' THEN pa.full_name WHEN 'supervisor' THEN pv.full_name WHEN 'staff_admin' THEN pf.full_name END,
 CASE WHEN a.role='staff_admin' THEN pf.is_active ELSE 1 END,
 CASE WHEN a.role='staff_admin' THEN pf.session_version ELSE 0 END,
 o.id,o.name,COALESCE(ad.id,md.id),COALESCE(ad.name,md.name),dx.group_id,dx.role_id,COALESCE(sr.name,''),
 rp.section,rp.can_view,rp.can_edit`

func (r *Repository) FindAccount(ctx context.Context, login string) (*Account, error) {
	a, _, err := r.account(ctx, "a.username=?", []any{login}, false, true)
	return a, err
}

func (r *Repository) LoadAccount(ctx context.Context, id int64) (*Account, error) {
	a, _, err := r.account(ctx, "a.id=?", []any{id}, false, false)
	return a, err
}

// SessionAccount avoids separately re-reading the same account after checking
// its session. Versions and the selected cabinet are still validated by Auth.
func (r *Repository) SessionAccount(ctx context.Context, sid string) (*Account, Session, error) {
	return r.account(ctx, "sess.id=?", []any{sid}, true, false)
}

func (r *Repository) account(ctx context.Context, where string, args []any, withSession, password bool) (*Account, Session, error) {
	columns, joins := accountColumns, accountSnapshot
	if password {
		columns += ",a.password"
	}
	if withSession {
		columns += ",sess.id,sess.user_id,sess.organization_id,sess.division_id,sess.expires_at,sess.revoked_at,sess.auth_version,sess.session_version,sess.context_version"
		joins += " JOIN auth_sessions sess ON sess.user_id=a.id "
	}
	rows, err := r.DB.QueryContext(ctx, "SELECT "+columns+joins+" WHERE "+where+" ORDER BY o.id,COALESCE(ad.id,md.id),rp.section", args...)
	if err != nil {
		return nil, Session{}, err
	}
	defer rows.Close()
	a := &Account{Organizations: []Organization{}}
	var session Session
	found := false
	for rows.Next() {
		var profileID, oid, did sql.NullInt64
		var name, orgName, divName, section sql.NullString
		var active, view, edit sql.NullBool
		var legacy sql.NullInt64
		var revoked sql.NullTime
		var hash sql.NullString
		d := Division{Permissions: map[string]Permission{}}
		dest := []any{&a.UserID, &a.EntityID, &a.Username, &a.Role, &a.AuthVersion, &profileID, &name, &active, &legacy,
			&oid, &orgName, &did, &divName, &d.GroupID, &d.RoleID, &d.RoleName, &section, &view, &edit}
		if password {
			dest = append(dest, &hash)
		}
		if withSession {
			dest = append(dest, &session.ID, &session.UserID, &session.Context.OrganizationID, &session.Context.DivisionID, &session.ExpiresAt, &revoked, &session.AuthVersion, &session.LegacySessionVersion, &session.ContextVersion)
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, Session{}, err
		}
		if _, ok := profileTables[a.Role]; !ok || !profileID.Valid || !active.Valid || !active.Bool {
			return nil, Session{}, ErrCredentials
		}
		if withSession && (revoked.Valid || !session.ExpiresAt.After(time.Now())) {
			return nil, Session{}, ErrToken
		}
		found = true
		a.FullName = name.String
		a.LegacySessionVersion = legacy.Int64
		a.Password = hash.String
		if !oid.Valid {
			continue
		}
		if len(a.Organizations) == 0 || a.Organizations[len(a.Organizations)-1].ID != oid.Int64 {
			a.Organizations = append(a.Organizations, Organization{ID: oid.Int64, Name: orgName.String, Divisions: []Division{}})
		}
		o := &a.Organizations[len(a.Organizations)-1]
		if !did.Valid || a.Role == "staff_admin" && d.RoleID == nil {
			continue
		}
		if len(o.Divisions) == 0 || o.Divisions[len(o.Divisions)-1].ID != did.Int64 {
			d.ID = did.Int64
			d.Name = divName.String
			if a.Role == "admin" {
				for _, s := range Sections {
					d.Permissions[s] = Permission{true, true}
				}
			}
			o.Divisions = append(o.Divisions, d)
		}
		if a.Role == "staff_admin" && section.Valid {
			for _, s := range Sections {
				if section.String == s {
					o.Divisions[len(o.Divisions)-1].Permissions[s] = Permission{view.Bool, edit.Bool}
					break
				}
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, Session{}, err
	}
	if !found {
		if withSession {
			return nil, Session{}, ErrToken
		}
		return nil, Session{}, ErrCredentials
	}
	if a.Role == "admin" && len(a.Organizations) > 1 {
		return nil, Session{}, ErrForbidden
	}
	return a, session, nil
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
