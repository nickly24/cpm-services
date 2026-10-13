package core

import (
	"database/sql"
	"net/http"
	"strings"
)

var profileTables = map[string]string{"student": "students", "proctor": "proctors", "examinator": "examinators", "supervisor": "supervisors", "admin": "admins", "staff_admin": "admin_role_users"}

const profiles = `(SELECT id,'student' role,full_name,school_id,` + "`class`" + `,tg_name FROM students UNION ALL SELECT id,'proctor',full_name,NULL,NULL,NULL FROM proctors UNION ALL SELECT id,'examinator',full_name,NULL,NULL,NULL FROM examinators UNION ALL SELECT id,'supervisor',full_name,NULL,NULL,NULL FROM supervisors UNION ALL SELECT id,'admin',full_name,NULL,NULL,NULL FROM admins UNION ALL SELECT id,'staff_admin',full_name,NULL,NULL,NULL FROM admin_role_users)`
const userSelect = `SELECT a.id account_id,a.ref_id profile_id,a.role,a.username login,p.full_name,p.school_id,p.` + "`class`" + `,COALESCE(e.tg_name,p.tg_name,'') tg_name,x.group_id,x.role_id,s.is_active FROM auth_users a JOIN ` + profiles + ` p ON p.id=a.ref_id AND p.role=a.role JOIN auth_account_state s ON s.user_id=a.id LEFT JOIN core_profile_extra e ON e.user_id=a.id JOIN auth_division_access x ON x.user_id=a.id`

func (s *Server) member(r *http.Request, db DBTX, a Actor, d, user int64) (map[string]any, error) {
	return one(r.Context(), db, userSelect+` WHERE a.id=? AND x.organization_id=? AND x.division_id=?`, user, a.OrganizationID, d)
}
func (s *Server) visibleUser(a Actor, row map[string]any) bool {
	return a.Can("users", false) || num(row["account_id"]) == a.AccountID || a.OwnGroup(pointer(row["group_id"])) && row["role"] == "student"
}
func (s *Server) editableProfile(r *http.Request, db DBTX, a Actor, d, user int64) error {
	row, e := s.member(r, db, a, d, user)
	if e != nil {
		return e
	}
	if !a.Can("users", true) {
		return denied
	}
	if row["role"] == "admin" && user != a.AccountID {
		return denied
	}
	if !a.OrganizationAdmin && row["role"] == "staff_admin" {
		return denied
	}
	var owner int64
	e = db.QueryRowContext(r.Context(), "SELECT organization_id FROM core_user_owners WHERE user_id=?", user).Scan(&owner)
	if e != nil {
		return e
	}
	if owner != a.OrganizationID {
		return denied
	}
	if !a.OrganizationAdmin {
		var count int
		e = db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_division_access WHERE user_id=? AND division_id<>?", user, d).Scan(&count)
		if e != nil {
			return e
		}
		if count != 0 {
			return &APIError{403, "global_profile_requires_organization_admin"}
		}
	}
	return nil
}
func (s *Server) users(r *http.Request, a Actor, d int64) (any, int, error) {
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := userSelect + ` WHERE x.organization_id=? AND x.division_id=?`
	args := []any{a.OrganizationID, d}
	if !a.Can("users", false) {
		if a.Role == "proctor" && a.GroupID != nil {
			q += ` AND ((a.role='student' AND x.group_id=?) OR a.id=?)`
			args = append(args, *a.GroupID, a.AccountID)
		} else {
			q += ` AND a.id=?`
			args = append(args, a.AccountID)
		}
	}
	if role := r.URL.Query().Get("role"); role != "" {
		if _, found := profileTables[role]; !found {
			return nil, 0, bad("invalid_role")
		}
		q += " AND a.role=?"
		args = append(args, role)
	}
	if term := r.URL.Query().Get("q"); term != "" {
		if !validText(term, 100, false) {
			return nil, 0, bad("invalid_search")
		}
		q += " AND p.full_name LIKE ?"
		args = append(args, "%"+term+"%")
	}
	if raw := r.URL.Query().Get("unassigned"); raw == "true" {
		q += " AND x.group_id IS NULL"
	}
	q += " ORDER BY p.full_name,a.id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q, args...)
	return rows, 200, e
}
func (s *Server) userGet(r *http.Request, a Actor, d int64) (any, int, error) {
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	row, e := s.member(r, s.DB, a, d, u)
	if e != nil {
		return nil, 0, e
	}
	if !s.visibleUser(a, row) {
		return nil, 0, denied
	}
	return ok(row)
}
func (s *Server) validUser(r *http.Request, db DBTX, a Actor, d int64, in UserDTO) error {
	if in.Role == "admin" || profileTables[in.Role] == "" {
		return bad("invalid_role")
	}
	if !a.OrganizationAdmin && in.Role != "student" {
		return denied
	}
	if !validText(in.FullName, 50, true) || !validText(in.Login, 50, true) || strings.ContainsAny(in.Login, " \t\r\n") || len(in.Password) < 8 || len(in.Password) > 1024 || !positive(in.RoleID) || !positive(in.SchoolID) || !positive(in.GroupID) || !validText(in.TGName, 30, false) {
		return bad("invalid_user")
	}
	if in.Role == "student" {
		if in.Class < 1 || in.Class > 99 || !validText(in.TGName, 30, false) || in.RoleID != nil {
			return bad("invalid_student")
		}
	} else if in.SchoolID != nil || in.Class != 0 {
		return bad("student_fields_for_staff")
	}
	if in.Role == "staff_admin" {
		if in.RoleID == nil {
			return bad("role_required")
		}
		var n int64
		if e := db.QueryRowContext(r.Context(), "SELECT id FROM auth_scoped_roles WHERE id=? AND organization_id=?", *in.RoleID, a.OrganizationID).Scan(&n); e != nil {
			return missing
		}
	} else if in.RoleID != nil {
		return bad("role_for_staff_admin_only")
	}
	if in.GroupID != nil {
		if !a.Can("groups", true) {
			return denied
		}
		if in.Role != "student" && in.Role != "proctor" {
			return bad("group_for_student_or_proctor_only")
		}
		if _, e := s.group(r, db, a, d, *in.GroupID); e != nil {
			return e
		}
	}
	if in.SchoolID != nil {
		if _, e := s.school(r, db, a, *in.SchoolID, true); e != nil {
			return e
		}
	}
	var count int
	if e := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM auth_users WHERE username=?", in.Login).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return &APIError{409, "login_exists"}
	}
	return nil
}
func (s *Server) insertUser(r *http.Request, tx *sql.Tx, a Actor, d int64, in UserDTO, hash string) (int64, error) {
	if e := s.validUser(r, tx, a, d, in); e != nil {
		return 0, e
	}
	var result sql.Result
	var e error
	table := profileTables[in.Role]
	switch in.Role {
	case "student":
		result, e = tx.ExecContext(r.Context(), "INSERT INTO students(full_name,`class`,tg_name,school_id) VALUES(?,?,?,?)", strings.TrimSpace(in.FullName), in.Class, in.TGName, in.SchoolID)
	case "staff_admin":
		var legacy int64
		e = tx.QueryRowContext(r.Context(), "SELECT id FROM admin_roles ORDER BY id LIMIT 1").Scan(&legacy)
		if e == sql.ErrNoRows {
			res, err := tx.ExecContext(r.Context(), "INSERT INTO admin_roles(name) VALUES('Core compatibility role')")
			if err != nil {
				return 0, err
			}
			legacy, _ = res.LastInsertId()
			e = nil
		}
		if e == nil {
			result, e = tx.ExecContext(r.Context(), "INSERT INTO admin_role_users(full_name,role_id) VALUES(?,?)", strings.TrimSpace(in.FullName), legacy)
		}
	default:
		result, e = tx.ExecContext(r.Context(), "INSERT INTO `"+table+"`(full_name) VALUES(?)", strings.TrimSpace(in.FullName))
	}
	if e != nil {
		return 0, e
	}
	profileID, e := result.LastInsertId()
	if e != nil {
		return 0, e
	}
	result, e = tx.ExecContext(r.Context(), "INSERT INTO auth_users(username,password,role,ref_id) VALUES(?,?,?,?)", in.Login, hash, in.Role, profileID)
	if e != nil {
		return 0, e
	}
	u, e := result.LastInsertId()
	if e != nil {
		return 0, e
	}
	for _, step := range []struct {
		q    string
		args []any
	}{
		{"INSERT INTO auth_account_state(user_id) VALUES(?)", []any{u}},
		{"INSERT INTO core_user_owners VALUES(?,?)", []any{u, a.OrganizationID}},
		{"INSERT INTO auth_organization_access VALUES(?,?)", []any{u, a.OrganizationID}},
		{"INSERT INTO auth_division_access(user_id,organization_id,division_id,role_id,group_id) VALUES(?,?,?,?,?)", []any{u, a.OrganizationID, d, in.RoleID, in.GroupID}},
	} {
		if _, e = tx.ExecContext(r.Context(), step.q, step.args...); e != nil {
			return 0, e
		}
	}
	if in.Role == "proctor" && in.GroupID != nil {
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_proctor_groups VALUES(?,?,?)", u, d, *in.GroupID); e != nil {
			return 0, e
		}
	}
	if in.Role != "student" {
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_profile_extra(user_id,tg_name) VALUES(?,?)", u, in.TGName); e != nil {
			return 0, e
		}
	}
	return u, audit(r.Context(), tx, a, &d, "user.create", u)
}
func (s *Server) userCreate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("users", true) {
		return nil, 0, denied
	}
	var in UserDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if e := s.validUser(r, s.DB, a, d, in); e != nil {
		return nil, 0, e
	}
	hash, e := s.Auth.Hash(r.Context(), in.Password)
	if e != nil {
		return nil, 0, e
	}
	var uid int64
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var err error
		uid, err = s.insertUser(r, tx, a, d, in, hash)
		return err
	})
	return map[string]any{"account_id": uid}, 201, e
}
func (s *Server) userUpdate(r *http.Request, a Actor, d int64) (any, int, error) {
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	var in ProfileDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.FullName, 50, true) || !validText(in.TGName, 30, false) || !positive(in.SchoolID) {
		return nil, 0, bad("invalid_profile")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := s.editableProfile(r, tx, a, d, u); e != nil {
			return e
		}
		row, e := s.member(r, tx, a, d, u)
		if e != nil {
			return e
		}
		role := row["role"].(string)
		table := profileTables[role]
		if role == "student" {
			if in.Class < 1 || in.Class > 99 {
				return bad("invalid_class")
			}
			if in.SchoolID != nil {
				if _, e = s.school(r, tx, a, *in.SchoolID, true); e != nil {
					return e
				}
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE students SET full_name=?,`class`=?,tg_name=?,school_id=? WHERE id=?", strings.TrimSpace(in.FullName), in.Class, in.TGName, in.SchoolID, row["profile_id"])
		} else {
			if in.Class != 0 || in.SchoolID != nil {
				return bad("student_fields_for_staff")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE `"+table+"` SET full_name=? WHERE id=?", strings.TrimSpace(in.FullName), row["profile_id"])
		}
		if e != nil {
			return e
		}
		if role != "student" {
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_profile_extra(user_id,tg_name) VALUES(?,?) ON DUPLICATE KEY UPDATE tg_name=VALUES(tg_name)", u, in.TGName); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "user.update", u)
	})
	return nil, 200, e
}
func (s *Server) userCredentials(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	if e = s.editableProfile(r, s.DB, a, d, u); e != nil {
		return nil, 0, e
	}
	var in CredentialsDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Login, 50, true) || strings.ContainsAny(in.Login, " \t\r\n") || len(in.Password) < 8 || len(in.Password) > 1024 {
		return nil, 0, bad("invalid_credentials")
	}
	hash, e := s.Auth.Hash(r.Context(), in.Password)
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := s.editableProfile(r, tx, a, d, u); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE auth_users SET username=?,password=? WHERE id=?", in.Login, hash, u); e != nil {
			return e
		}
		if e := bumpAccountAuthVersion(r.Context(), tx, u); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "user.credentials", u)
	})
	return nil, 200, e
}
func (s *Server) userActive(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	if u == a.AccountID {
		return nil, 0, bad("cannot_disable_self")
	}
	var in ActiveDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Active == nil {
		return nil, 0, bad("is_active_required")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := s.editableProfile(r, tx, a, d, u); e != nil {
			return e
		}
		if e := setAccountActiveAndBumpAuthVersion(r.Context(), tx, u, *in.Active); e != nil {
			return e
		}
		row, e := s.member(r, tx, a, d, u)
		if e != nil {
			return e
		}
		if row["role"] == "staff_admin" {
			if _, e = tx.ExecContext(r.Context(), "UPDATE admin_role_users SET is_active=?,session_version=session_version+1 WHERE id=?", *in.Active, row["profile_id"]); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "user.active", u)
	})
	return nil, 200, e
}
func (s *Server) myProfile(r *http.Request, a Actor, d int64) (any, int, error) {
	row, e := one(r.Context(), s.DB, `SELECT a.id account_id,a.ref_id profile_id,a.role,a.username login,p.full_name,p.school_id,p.`+"`class`"+`,COALESCE(e.tg_name,p.tg_name,'') tg_name FROM auth_users a LEFT JOIN core_profile_extra e ON e.user_id=a.id JOIN `+profiles+` p ON p.id=a.ref_id AND p.role=a.role WHERE a.id=?`, a.AccountID)
	return row, 200, e
}
