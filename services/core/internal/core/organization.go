package core

import (
	"database/sql"
	"net/http"
	"strings"
)

var Sections = []string{"dashboard", "organizations", "divisions", "groups", "directions", "users", "schools", "upload", "schedule", "assignments", "review-queue", "homework-archive", "monitoring", "tests", "test-results", "exams", "attendance", "scan", "zaps", "train", "ratings"}

func (s *Server) organization(r *http.Request, a Actor, d int64) (any, int, error) {
	returnRow, e := one(r.Context(), s.DB, "SELECT id,name,is_active FROM organizations WHERE id=?", a.OrganizationID)
	return returnRow, 200, e
}
func (s *Server) organizationUpdate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in NameDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 150, true) {
		return nil, 0, bad("invalid_name")
	}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(r.Context(), "UPDATE organizations SET name=? WHERE id=?", strings.TrimSpace(in.Name), a.OrganizationID); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, nil, "organization.update", a.OrganizationID)
	})
	return nil, 200, e
}
func (s *Server) divisions(r *http.Request, a Actor, d int64) (any, int, error) {
	q := "SELECT d.id,d.name,d.is_active FROM divisions d WHERE d.organization_id=? AND d.is_active=1"
	args := []any{a.OrganizationID}
	if !a.OrganizationAdmin {
		q += " AND EXISTS(SELECT 1 FROM auth_division_access x WHERE x.user_id=? AND x.division_id=d.id)"
		args = append(args, a.AccountID)
	}
	rows, e := list(r.Context(), s.DB, q+" ORDER BY d.id", args...)
	return rows, 200, e
}
func (s *Server) divisionCreate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in NameDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 150, true) {
		return nil, 0, bad("invalid_name")
	}
	var n int64
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		res, e := tx.ExecContext(r.Context(), "INSERT INTO core_id_allocator VALUES(NULL)")
		if e != nil {
			return e
		}
		n, e = res.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO divisions(id,organization_id,name) VALUES(?,?,?)", n, a.OrganizationID, strings.TrimSpace(in.Name)); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &n, "division.create", n)
	})
	return map[string]any{"id": n}, 201, e
}
func (s *Server) divisionUpdate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in NameDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 150, true) {
		return nil, 0, bad("invalid_name")
	}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE divisions SET name=? WHERE id=? AND organization_id=?", strings.TrimSpace(in.Name), d, a.OrganizationID); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "division.update", d)
	})
	return nil, 200, e
}
func (s *Server) divisionArchive(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_division_access WHERE division_id=?", d).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return &APIError{409, "division_has_members"}
		}
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_attendance_history_reservations WHERE organization_id=? AND division_id=?", a.OrganizationID, d).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return &APIError{409, "division_has_history"}
		}
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_legacy_rating_scope WHERE organization_id=? AND division_id=?", a.OrganizationID, d).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return &APIError{409, "division_has_history"}
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE divisions SET is_active=0 WHERE id=?", d); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "division.archive", d)
	})
	return nil, 200, e
}
func (s *Server) grant(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("users", true) {
		return nil, 0, denied
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	var in GrantDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !positive(in.RoleID) {
		return nil, 0, bad("invalid_role")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var role string
		var org int64
		if e := tx.QueryRowContext(r.Context(), "SELECT a.role,o.organization_id FROM auth_users a JOIN core_user_owners o ON o.user_id=a.id WHERE a.id=? FOR UPDATE", u).Scan(&role, &org); e != nil {
			return e
		}
		if org != a.OrganizationID {
			return denied
		}
		if role == "admin" {
			return bad("admin_has_organization_access")
		}
		if role == "staff_admin" {
			if !a.OrganizationAdmin {
				return denied
			}
			if in.RoleID == nil {
				return bad("role_required")
			}
			var id int64
			if e := tx.QueryRowContext(r.Context(), "SELECT id FROM auth_scoped_roles WHERE id=? AND organization_id=?", *in.RoleID, a.OrganizationID).Scan(&id); e != nil {
				return e
			}
		} else if in.RoleID != nil {
			return bad("role_for_staff_admin_only")
		}
		if _, e := tx.ExecContext(r.Context(), "INSERT IGNORE INTO auth_organization_access VALUES(?,?)", u, a.OrganizationID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "INSERT INTO auth_division_access(user_id,organization_id,division_id,role_id) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE role_id=VALUES(role_id)", u, a.OrganizationID, d, in.RoleID); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "membership.grant", u)
	})
	return nil, 200, e
}
func (s *Server) revokeGrant(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("users", true) {
		return nil, 0, denied
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var role string
		if e := tx.QueryRowContext(r.Context(), "SELECT role FROM auth_users WHERE id=? FOR UPDATE", u).Scan(&role); e != nil {
			return e
		}
		if role == "staff_admin" && !a.OrganizationAdmin {
			return denied
		}
		if _, e := s.member(r, tx, a, d, u); e != nil {
			return e
		}
		if u == a.AccountID {
			return bad("cannot_remove_self")
		}
		var count int
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_division_access WHERE user_id=?", u).Scan(&count); e != nil {
			return e
		}
		if count <= 1 {
			return &APIError{409, "last_membership"}
		}
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_attendance_history_reservations WHERE organization_id=? AND division_id=? AND user_id=?", a.OrganizationID, d, u).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return &APIError{409, "membership_has_history"}
		}
		if role == "student" {
			if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_rating_locks WHERE user_id=? AND division_id=?", u, d).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return &APIError{409, "rating_exists"}
			}
			// Legacy Allratings has no division. Its reviewed migration scope is
			// retained separately; it must not block an unrelated new membership.
			if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_legacy_rating_scope WHERE organization_id=? AND division_id=? AND user_id=?", a.OrganizationID, d, u).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return &APIError{409, "rating_exists"}
			}
			// Existing learning history is retained, even before ratings are implemented.
			if e := tx.QueryRowContext(r.Context(), "SELECT (SELECT COUNT(*) FROM core_attendance WHERE user_id=? AND division_id=?)+(SELECT COUNT(*) FROM core_zaps WHERE user_id=? AND division_id=?)", u, d, u, d).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return &APIError{409, "membership_has_history"}
			}
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM core_proctor_groups WHERE user_id=? AND division_id=?", u, d); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM auth_division_access WHERE user_id=? AND division_id=?", u, d); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM auth_organization_access WHERE user_id=? AND organization_id=? AND NOT EXISTS(SELECT 1 FROM auth_division_access WHERE user_id=? AND organization_id=?)", u, a.OrganizationID, u, a.OrganizationID); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "membership.revoke", u)
	})
	return nil, 200, e
}

// Organization administrators can inspect all participations in their own
// organization, including a foreign-owned shared account. Ownership controls
// assignment; foreign organizations and their memberships are never returned.
func (s *Server) organizationUserMemberships(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	tx, e := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	row, e := one(r.Context(), tx, `SELECT a.id account_id,a.role,o.organization_id owner_organization_id
 FROM auth_users a LEFT JOIN core_user_owners o ON o.user_id=a.id
 WHERE a.id=? AND (o.organization_id=? OR EXISTS(SELECT 1 FROM auth_organization_access x WHERE x.user_id=a.id AND x.organization_id=?))`, u, a.OrganizationID, a.OrganizationID)
	if e != nil {
		return nil, 0, e
	}
	memberships, e := list(r.Context(), tx, `SELECT m.division_id,d.name division_name,m.role_id,role.name role_name,g.group_id
 FROM auth_division_access m JOIN divisions d ON d.id=m.division_id AND d.organization_id=m.organization_id
 LEFT JOIN auth_scoped_roles role ON role.id=m.role_id AND role.organization_id=m.organization_id
 LEFT JOIN core_group_scope g ON g.group_id=m.group_id AND g.organization_id=m.organization_id AND g.division_id=m.division_id AND g.is_active=1
 WHERE m.organization_id=? AND m.user_id=? ORDER BY m.division_id`, a.OrganizationID, u)
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	canRevoke := u != a.AccountID && row["role"] != "admin"
	for _, membership := range memberships {
		membership["can_revoke"] = canRevoke
	}
	return ok(map[string]any{"account_id": u, "owner_organization_id": row["owner_organization_id"],
		"can_assign": num(row["owner_organization_id"]) == a.OrganizationID && row["role"] != "admin",
		"can_revoke": canRevoke, "memberships": memberships})
}
func (s *Server) roles(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	rows, e := list(r.Context(), s.DB, "SELECT id,name FROM auth_scoped_roles WHERE organization_id=? ORDER BY id", a.OrganizationID)
	if e != nil {
		return nil, 0, e
	}
	for _, row := range rows {
		perms, err := list(r.Context(), s.DB, "SELECT section,can_view,can_edit FROM auth_scoped_role_permissions WHERE role_id=? ORDER BY section", row["id"])
		if err != nil {
			return nil, 0, err
		}
		row["permissions"] = perms
	}
	return ok(map[string]any{"roles": rows, "sections": Sections})
}
func validRole(in RoleDTO) bool {
	if !validText(in.Name, 100, true) || in.Permissions == nil {
		return false
	}
	for section, p := range in.Permissions {
		found := false
		for _, k := range Sections {
			if k == section {
				found = true
			}
		}
		if !found || p.Edit && !p.View {
			return false
		}
	}
	return true
}
func (s *Server) roleSave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in RoleDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validRole(in) {
		return nil, 0, bad("invalid_role")
	}
	var role int64
	var e error
	if r.Method == "PUT" {
		role, e = id(r, "role")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if role == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO core_id_allocator VALUES(NULL)")
			if e != nil {
				return e
			}
			role, e = res.LastInsertId()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO auth_scoped_roles(id,organization_id,name) VALUES(?,?,?)", role, a.OrganizationID, strings.TrimSpace(in.Name)); e != nil {
				return e
			}
		} else {
			var n int64
			if e := tx.QueryRowContext(r.Context(), "SELECT id FROM auth_scoped_roles WHERE id=? AND organization_id=? FOR UPDATE", role, a.OrganizationID).Scan(&n); e != nil {
				return e
			}
			if _, e := tx.ExecContext(r.Context(), "UPDATE auth_scoped_roles SET name=? WHERE id=?", strings.TrimSpace(in.Name), role); e != nil {
				return e
			}
			if _, e := tx.ExecContext(r.Context(), "DELETE FROM auth_scoped_role_permissions WHERE role_id=?", role); e != nil {
				return e
			}
		}
		for section, p := range in.Permissions {
			if _, e := tx.ExecContext(r.Context(), "INSERT INTO auth_scoped_role_permissions VALUES(?,?,?,?)", role, section, p.View, p.Edit); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, nil, "role.save", role)
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": role}, code, e
}
func (s *Server) roleDelete(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	role, e := id(r, "role")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		var n int64
		if e := tx.QueryRowContext(r.Context(), "SELECT id FROM auth_scoped_roles WHERE id=? AND organization_id=? FOR UPDATE", role, a.OrganizationID).Scan(&n); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_division_access WHERE role_id=?", role).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return &APIError{409, "role_in_use"}
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM auth_scoped_role_permissions WHERE role_id=?", role); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM auth_scoped_roles WHERE id=?", role); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, nil, "role.delete", role)
	})
	return nil, 200, e
}
