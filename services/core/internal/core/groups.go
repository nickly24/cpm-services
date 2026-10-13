package core

import (
	"database/sql"
	"net/http"
	"strings"
)

// Group access grants a roster, not access to account credentials or contacts.
// Keep account and profile identities separate for subject-service references.
const groupMemberSelect = `SELECT a.id account_id,a.ref_id profile_id,a.role,p.full_name,school_scope.school_id school_id,p.` + "`class`" + `,x.group_id,s.is_active FROM auth_users a JOIN ` + profiles + ` p ON p.id=a.ref_id AND p.role=a.role JOIN auth_account_state s ON s.user_id=a.id JOIN auth_division_access x ON x.user_id=a.id LEFT JOIN core_school_scope school_scope ON school_scope.school_id=p.school_id AND school_scope.organization_id=x.organization_id`

func (s *Server) ownGroupMember(r *http.Request, a Actor, d, g int64) error {
	if (a.Role != "proctor" && a.Role != "student") || a.GroupID == nil || *a.GroupID != g {
		return denied
	}
	q := `SELECT x.group_id FROM auth_division_access x JOIN auth_users u ON u.id=x.user_id
 JOIN auth_account_state ast ON ast.user_id=u.id AND ast.is_active=1
 WHERE x.user_id=? AND x.organization_id=? AND x.division_id=? AND x.group_id=? AND u.role=?`
	if a.Role == "proctor" {
		q += ` AND EXISTS(SELECT 1 FROM core_proctor_groups p WHERE p.user_id=x.user_id AND p.division_id=x.division_id AND p.group_id=x.group_id)`
	}
	var found int64
	e := s.DB.QueryRowContext(r.Context(), q, a.AccountID, a.OrganizationID, d, g, a.Role).Scan(&found)
	if e == sql.ErrNoRows {
		return denied
	}
	return e
}

func (s *Server) group(r *http.Request, db DBTX, a Actor, d, g int64) (map[string]any, error) {
	return one(r.Context(), db, "SELECT g.id,g.name FROM `groups` g JOIN core_group_scope x ON x.group_id=g.id WHERE g.id=? AND x.organization_id=? AND x.division_id=? AND x.is_active=1", g, a.OrganizationID, d)
}
func (s *Server) groups(r *http.Request, a Actor, d int64) (any, int, error) {
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := `SELECT g.id,g.name,(SELECT COUNT(*) FROM auth_division_access m JOIN auth_users u ON u.id=m.user_id WHERE m.group_id=g.id AND m.organization_id=x.organization_id AND m.division_id=x.division_id AND u.role='student') student_count,p.user_id proctor_account_id FROM ` + "`groups`" + ` g JOIN core_group_scope x ON x.group_id=g.id LEFT JOIN core_proctor_groups p ON p.group_id=g.id AND p.division_id=x.division_id WHERE x.organization_id=? AND x.division_id=? AND x.is_active=1`
	args := []any{a.OrganizationID, d}
	if !a.Can("groups", false) && !a.Can("users", false) {
		if (a.Role == "student" || a.Role == "proctor") && a.GroupID != nil {
			q += " AND g.id=?"
			args = append(args, *a.GroupID)
		} else {
			return nil, 0, denied
		}
	}
	if term := r.URL.Query().Get("q"); term != "" {
		if !validText(term, 100, false) {
			return nil, 0, bad("invalid_search")
		}
		q += " AND g.name LIKE ?"
		args = append(args, "%"+term+"%")
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q+" ORDER BY g.id LIMIT ? OFFSET ?", args...)
	return rows, 200, e
}
func (s *Server) groupMembers(r *http.Request, a Actor, d int64) (any, int, error) {
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	g, e := id(r, "group")
	if e != nil {
		return nil, 0, e
	}
	if _, e = s.group(r, s.DB, a, d, g); e != nil {
		return nil, 0, e
	}
	if !a.Can("groups", false) && !a.Can("users", false) {
		if e := s.ownGroupMember(r, a, d, g); e != nil {
			return nil, 0, e
		}
	}
	q := groupMemberSelect
	if a.Can("users", false) {
		q = userSelect
	}
	q += " WHERE x.organization_id=? AND x.division_id=? AND x.group_id=?"
	args := []any{a.OrganizationID, d, g}
	// A student's own-group access identifies their group, not its other
	// members. Keep this fence before search and pagination so neither can
	// enumerate peers. Assigned proctors and authorized staff retain a roster.
	if a.Role == "student" {
		q += " AND a.id=?"
		args = append(args, a.AccountID)
	}
	if term := r.URL.Query().Get("q"); term != "" {
		if !validText(term, 100, false) {
			return nil, 0, bad("invalid_search")
		}
		q += " AND p.full_name LIKE ?"
		args = append(args, "%"+term+"%")
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q+" ORDER BY a.id LIMIT ? OFFSET ?", args...)
	return rows, 200, e
}
func (s *Server) groupSave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("groups", true) {
		return nil, 0, denied
	}
	var in NameDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 50, true) {
		return nil, 0, bad("invalid_name")
	}
	var g int64
	var e error
	if r.Method == "PUT" {
		g, e = id(r, "group")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if g == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO `groups`(name) VALUES(?)", strings.TrimSpace(in.Name))
			if e != nil {
				return e
			}
			g, e = res.LastInsertId()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_group_scope(group_id,organization_id,division_id) VALUES(?,?,?)", g, a.OrganizationID, d); e != nil {
				return e
			}
		} else {
			if _, e := s.group(r, tx, a, d, g); e != nil {
				return e
			}
			if _, e := tx.ExecContext(r.Context(), "UPDATE `groups` SET name=? WHERE id=?", strings.TrimSpace(in.Name), g); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "group.save", g)
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": g}, code, e
}
func (s *Server) groupArchive(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("groups", true) {
		return nil, 0, denied
	}
	g, e := id(r, "group")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := s.group(r, tx, a, d, g); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM core_proctor_groups WHERE group_id=?", g); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE auth_division_access SET group_id=NULL WHERE group_id=? AND division_id=?", g, d); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE core_group_scope SET is_active=0 WHERE group_id=?", g); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "group.archive", g)
	})
	return nil, 200, e
}
func (s *Server) groupAssign(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("groups", true) {
		return nil, 0, denied
	}
	g, e := id(r, "group")
	if e != nil {
		return nil, 0, e
	}
	var in GroupMemberDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.AccountID < 1 {
		return nil, 0, bad("invalid_user")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := s.group(r, tx, a, d, g); e != nil {
			return e
		}
		row, e := s.member(r, tx, a, d, in.AccountID)
		if e != nil {
			return e
		}
		role := row["role"]
		if role != "student" && role != "proctor" {
			return bad("group_for_student_or_proctor_only")
		}
		if num(row["is_active"]) != 1 {
			return bad("inactive_user")
		}
		if role == "proctor" {
			if _, e = tx.ExecContext(r.Context(), "DELETE FROM core_proctor_groups WHERE user_id=? AND division_id=?", in.AccountID, d); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_proctor_groups VALUES(?,?,?)", in.AccountID, d, g); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE auth_division_access SET group_id=? WHERE user_id=? AND division_id=?", g, in.AccountID, d); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "group.assign", in.AccountID)
	})
	return nil, 200, e
}
func (s *Server) groupUnassign(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("groups", true) {
		return nil, 0, denied
	}
	g, e := id(r, "group")
	if e != nil {
		return nil, 0, e
	}
	u, e := id(r, "user")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := s.group(r, tx, a, d, g); e != nil {
			return e
		}
		row, e := s.member(r, tx, a, d, u)
		if e != nil {
			return e
		}
		if num(row["group_id"]) != g {
			return missing
		}
		if _, e = tx.ExecContext(r.Context(), "DELETE FROM core_proctor_groups WHERE user_id=? AND division_id=? AND group_id=?", u, d, g); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE auth_division_access SET group_id=NULL WHERE user_id=? AND division_id=?", u, d); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "group.unassign", u)
	})
	return nil, 200, e
}
