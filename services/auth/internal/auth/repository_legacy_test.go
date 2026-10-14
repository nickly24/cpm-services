package auth

import (
	"context"
	"database/sql"
	"errors"
)

// Previous loader retained only for SQL equivalence regression tests.
func (r *Repository) legacySnapshotAccount(ctx context.Context, id int64) (*Account, error) {
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
