package core

import (
	"database/sql"
	"net/http"
)

func attendanceRead(a Actor) bool {
	return a.Can("attendance", false) || a.Role == "student" || a.Role == "proctor" || a.Role == "supervisor"
}
func (s *Server) attendanceTypes(r *http.Request, a Actor, d int64) (any, int, error) {
	rows, e := list(r.Context(), s.DB, "SELECT id,code,name_ru,sort_order FROM attendance_types ORDER BY sort_order,id")
	return rows, 200, e
}
func (s *Server) days(r *http.Request, a Actor, d int64) (any, int, error) {
	if !attendanceRead(a) {
		return nil, 0, denied
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	if e := period(from, to); e != nil {
		return nil, 0, e
	}
	rows, e := list(r.Context(), s.DB, "SELECT id,date,comment FROM core_class_days WHERE organization_id=? AND division_id=? AND date BETWEEN ? AND ? ORDER BY date LIMIT 366", a.OrganizationID, d, from, to)
	return rows, 200, e
}
func (s *Server) daySave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("attendance", true) {
		return nil, 0, denied
	}
	var in ClassDayDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validDate(in.Date) || !validText(in.Comment, 500, false) {
		return nil, 0, bad("invalid_class_day")
	}
	var n int64
	var e error
	if r.Method == "PUT" {
		n, e = id(r, "day")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if n == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO core_class_days(organization_id,division_id,date,comment) VALUES(?,?,?,?)", a.OrganizationID, d, in.Date, in.Comment)
			if e != nil {
				return e
			}
			n, e = res.LastInsertId()
			if e != nil {
				return e
			}
		} else {
			var date string
			if e := tx.QueryRowContext(r.Context(), "SELECT DATE_FORMAT(date,'%Y-%m-%d') FROM core_class_days WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d).Scan(&date); e != nil {
				return e
			}
			if date != in.Date {
				var count int
				if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_attendance WHERE class_day_id=?", n).Scan(&count); e != nil {
					return e
				}
				if count > 0 {
					return &APIError{409, "class_day_has_attendance"}
				}
			}
			if _, e := tx.ExecContext(r.Context(), "UPDATE core_class_days SET date=?,comment=? WHERE id=?", in.Date, in.Comment, n); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "class_day.save", n)
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": n}, code, e
}
func (s *Server) dayGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if !attendanceRead(a) {
		return nil, 0, denied
	}
	n, e := id(r, "day")
	if e != nil {
		return nil, 0, e
	}
	row, e := one(r.Context(), s.DB, "SELECT id,date,comment FROM core_class_days WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d)
	return row, 200, e
}
func (s *Server) dayDelete(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("attendance", true) {
		return nil, 0, denied
	}
	n, e := id(r, "day")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := one(r.Context(), tx, "SELECT id FROM core_class_days WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_attendance WHERE class_day_id=?", n).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return &APIError{409, "class_day_has_attendance"}
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM core_class_days WHERE id=?", n); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "class_day.delete", n)
	})
	return nil, 200, e
}
func (s *Server) attendanceQuery(r *http.Request, a Actor, d, day int64) ([]map[string]any, error) {
	q := `SELECT e.id,e.user_id account_id,p.full_name,m.group_id,e.class_day_id,cd.date,e.attendance_type_id,t.code type_code,t.name_ru type_name,e.zap_id FROM core_attendance e JOIN core_class_days cd ON cd.id=e.class_day_id JOIN attendance_types t ON t.id=e.attendance_type_id JOIN auth_users u ON u.id=e.user_id JOIN students p ON p.id=u.ref_id JOIN auth_division_access m ON m.user_id=e.user_id AND m.division_id=e.division_id WHERE cd.organization_id=? AND e.division_id=?`
	args := []any{a.OrganizationID, d}
	if day > 0 {
		q += " AND cd.id=?"
		args = append(args, day)
	} else {
		from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
		if e := period(from, to); e != nil {
			return nil, e
		}
		q += " AND cd.date BETWEEN ? AND ?"
		args = append(args, from, to)
	}
	if a.Role == "student" {
		q += " AND e.user_id=?"
		args = append(args, a.AccountID)
	} else if a.Role == "proctor" && !a.Can("attendance", false) {
		if a.GroupID == nil {
			return []map[string]any{}, nil
		}
		q += " AND m.group_id=?"
		args = append(args, *a.GroupID)
	}
	if raw := r.URL.Query().Get("account_id"); raw != "" {
		var u int64
		var e error
		u, e = parsePositive(raw)
		if e != nil {
			return nil, e
		}
		row, e := s.member(r, s.DB, a, d, u)
		if e != nil {
			return nil, e
		}
		if row["role"] != "student" {
			return nil, bad("student_required")
		}
		if a.Role == "student" && u != a.AccountID {
			return nil, denied
		}
		if a.Role == "proctor" && !a.OwnGroup(pointer(row["group_id"])) {
			return nil, denied
		}
		q += " AND e.user_id=?"
		args = append(args, u)
	}
	return list(r.Context(), s.DB, q+" ORDER BY cd.date,e.user_id LIMIT 10000", args...)
}
func (s *Server) dayAttendance(r *http.Request, a Actor, d int64) (any, int, error) {
	if !attendanceRead(a) {
		return nil, 0, denied
	}
	n, e := id(r, "day")
	if e != nil {
		return nil, 0, e
	}
	if _, e = one(r.Context(), s.DB, "SELECT id FROM core_class_days WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d); e != nil {
		return nil, 0, e
	}
	rows, e := s.attendanceQuery(r, a, d, n)
	return rows, 200, e
}
func (s *Server) attendanceReport(r *http.Request, a Actor, d int64) (any, int, error) {
	if !attendanceRead(a) {
		return nil, 0, denied
	}
	entries, e := s.attendanceQuery(r, a, d, 0)
	if e != nil {
		return nil, 0, e
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	days, e := list(r.Context(), s.DB, "SELECT id,date,comment FROM core_class_days WHERE organization_id=? AND division_id=? AND date BETWEEN ? AND ? ORDER BY date", a.OrganizationID, d, from, to)
	if e != nil {
		return nil, 0, e
	}
	q := userSelect + " WHERE x.organization_id=? AND x.division_id=? AND a.role='student'"
	args := []any{a.OrganizationID, d}
	if a.Role == "student" {
		q += " AND a.id=?"
		args = append(args, a.AccountID)
	} else if a.Role == "proctor" {
		if a.GroupID == nil {
			q += " AND 1=0"
		} else {
			q += " AND x.group_id=?"
			args = append(args, *a.GroupID)
		}
	}
	students, e := list(r.Context(), s.DB, q+" ORDER BY a.id LIMIT 1000", args...)
	return map[string]any{"days": days, "students": students, "entries": entries}, 200, e
}
func (s *Server) attendanceTarget(r *http.Request, db DBTX, a Actor, d, u int64) error {
	row, e := s.member(r, db, a, d, u)
	if e != nil {
		return e
	}
	if row["role"] != "student" {
		return bad("student_required")
	}
	if !a.Can("attendance", true) && !a.OwnGroup(pointer(row["group_id"])) {
		return denied
	}
	return nil
}
func (s *Server) attendanceSet(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("attendance", true) && a.Role != "proctor" {
		return nil, 0, denied
	}
	n, e := id(r, "day")
	if e != nil {
		return nil, 0, e
	}
	var in AttendanceDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.AccountID < 1 || in.TypeID < 1 {
		return nil, 0, bad("invalid_attendance")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		if _, e := one(r.Context(), tx, "SELECT id FROM core_class_days WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d); e != nil {
			return e
		}
		if e := s.attendanceTarget(r, tx, a, d, in.AccountID); e != nil {
			return e
		}
		var zap sql.NullInt64
		err := tx.QueryRowContext(r.Context(), "SELECT zap_id FROM core_attendance WHERE class_day_id=? AND user_id=?", n, in.AccountID).Scan(&zap)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if zap.Valid {
			return &APIError{409, "attendance_linked_to_zap"}
		}
		if _, e := tx.ExecContext(r.Context(), "INSERT INTO core_attendance(division_id,class_day_id,user_id,attendance_type_id) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE attendance_type_id=VALUES(attendance_type_id)", d, n, in.AccountID, in.TypeID); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "attendance.set", in.AccountID)
	})
	return nil, 200, e
}
func (s *Server) attendanceDelete(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("attendance", true) && a.Role != "proctor" {
		return nil, 0, denied
	}
	n, e := id(r, "day")
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
		if e := s.attendanceTarget(r, tx, a, d, u); e != nil {
			return e
		}
		var zap sql.NullInt64
		if e := tx.QueryRowContext(r.Context(), "SELECT zap_id FROM core_attendance WHERE division_id=? AND class_day_id=? AND user_id=?", d, n, u).Scan(&zap); e != nil {
			return e
		}
		if zap.Valid {
			return &APIError{409, "attendance_linked_to_zap"}
		}
		if _, e := tx.ExecContext(r.Context(), "DELETE FROM core_attendance WHERE division_id=? AND class_day_id=? AND user_id=?", d, n, u); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "attendance.delete", u)
	})
	return nil, 200, e
}
