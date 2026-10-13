package core

import (
	"database/sql"
	"net/http"
	"strings"
)

func (s *Server) school(r *http.Request, db DBTX, a Actor, id int64, active bool) (map[string]any, error) {
	q := "SELECT s.id,s.name,s.short_name,s.notes,s.is_active FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE s.id=? AND x.organization_id=?"
	if active {
		q += " AND s.is_active=1"
	}
	return one(r.Context(), db, q, id, a.OrganizationID)
}
func (s *Server) schools(r *http.Request, a Actor, d int64) (any, int, error) {
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := "SELECT s.id,s.name,s.short_name,s.notes,s.is_active FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE x.organization_id=?"
	if !a.OrganizationAdmin {
		q += " AND s.is_active=1"
	}
	args := []any{a.OrganizationID}
	if term := r.URL.Query().Get("q"); term != "" {
		if !validText(term, 100, false) {
			return nil, 0, bad("invalid_search")
		}
		q += " AND s.name LIKE ?"
		args = append(args, "%"+term+"%")
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q+" ORDER BY s.name,s.id LIMIT ? OFFSET ?", args...)
	if !a.Can("schools", false) {
		for _, row := range rows {
			delete(row, "notes")
		}
	}
	return rows, 200, e
}
func (s *Server) schoolGet(r *http.Request, a Actor, d int64) (any, int, error) {
	id, e := id(r, "school")
	if e != nil {
		return nil, 0, e
	}
	row, e := s.school(r, s.DB, a, id, !a.OrganizationAdmin)
	if !a.Can("schools", false) {
		delete(row, "notes")
	}
	return row, 200, e
}
func (s *Server) schoolSave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in SchoolDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 255, true) || !validText(in.ShortName, 64, false) || !validText(in.Notes, 5000, false) {
		return nil, 0, bad("invalid_school")
	}
	var n int64
	var e error
	if r.Method == "PUT" {
		n, e = id(r, "school")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		var organization int64
		if e := tx.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 FOR UPDATE", a.OrganizationID).Scan(&organization); e != nil {
			return e
		}
		if n != 0 {
			if _, e := s.school(r, tx, a, n, false); e != nil {
				return e
			}
		}
		var duplicate int
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE x.organization_id=? AND s.name=? AND s.id<>?", a.OrganizationID, strings.TrimSpace(in.Name), n).Scan(&duplicate); e != nil {
			return e
		}
		if duplicate != 0 {
			return &APIError{409, "school_name_exists"}
		}
		if n == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO schools(organization_id,name,short_name,notes,is_active) VALUES(?,?,?,?,?)", a.OrganizationID, strings.TrimSpace(in.Name), in.ShortName, in.Notes, in.Active)
			if e != nil {
				return e
			}
			n, e = res.LastInsertId()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_school_scope(school_id,organization_id) VALUES(?,?)", n, a.OrganizationID); e != nil {
				return e
			}
		} else {
			if _, e := tx.ExecContext(r.Context(), "UPDATE schools SET name=?,short_name=?,notes=?,is_active=? WHERE id=? AND organization_id=?", strings.TrimSpace(in.Name), in.ShortName, in.Notes, in.Active, n, a.OrganizationID); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, nil, "school.save", n)
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": n}, code, e
}
func (s *Server) directions(r *http.Request, a Actor, d int64) (any, int, error) {
	rows, e := list(r.Context(), s.DB, "SELECT p.id,p.name FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id WHERE x.organization_id=? AND x.is_active=1 ORDER BY p.id", a.OrganizationID)
	return rows, 200, e
}
func (s *Server) directionSave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var in NameDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validText(in.Name, 200, true) {
		return nil, 0, bad("invalid_name")
	}
	var n int64
	var e error
	if r.Method == "PUT" {
		n, e = id(r, "direction")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		var organization int64
		if e := tx.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 FOR UPDATE", a.OrganizationID).Scan(&organization); e != nil {
			return e
		}
		if n == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO directions(name) VALUES(?)", strings.TrimSpace(in.Name))
			if e != nil {
				return e
			}
			n, e = res.LastInsertId()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_direction_scope(direction_id,organization_id) VALUES(?,?)", n, a.OrganizationID); e != nil {
				return e
			}
		} else {
			var found int64
			if e := tx.QueryRowContext(r.Context(), "SELECT direction_id FROM core_direction_scope WHERE direction_id=? AND organization_id=? AND is_active=1", n, a.OrganizationID).Scan(&found); e != nil {
				return e
			}
			if _, e := tx.ExecContext(r.Context(), "UPDATE directions SET name=? WHERE id=?", strings.TrimSpace(in.Name), n); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, nil, "direction.save", n)
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": n}, code, e
}
func (s *Server) directionArchive(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	n, e := id(r, "direction")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		var found int64
		if e := tx.QueryRowContext(r.Context(), "SELECT direction_id FROM core_direction_scope WHERE direction_id=? AND organization_id=? AND is_active=1 FOR UPDATE", n, a.OrganizationID).Scan(&found); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE core_direction_scope SET is_active=0 WHERE direction_id=?", n); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, nil, "direction.archive", n)
	})
	return nil, 200, e
}
