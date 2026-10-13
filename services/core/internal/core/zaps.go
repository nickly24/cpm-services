package core

import (
	"database/sql"
	"encoding/base64"
	"net/http"
)

func (s *Server) zap(r *http.Request, db DBTX, a Actor, d, z int64) (map[string]any, error) {
	return one(r.Context(), db, "SELECT id,user_id account_id,text,status,answer FROM core_zaps WHERE id=? AND organization_id=? AND division_id=?", z, a.OrganizationID, d)
}
func zapVisible(a Actor, row map[string]any) bool {
	return a.Can("zaps", false) || a.Role == "student" && num(row["account_id"]) == a.AccountID
}
func (s *Server) zaps(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("zaps", false) && a.Role != "student" {
		return nil, 0, denied
	}
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := "SELECT id,user_id account_id,text,status,answer FROM core_zaps WHERE organization_id=? AND division_id=?"
	args := []any{a.OrganizationID, d}
	if a.Role == "student" {
		q += " AND user_id=?"
		args = append(args, a.AccountID)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		if st != "set" && st != "apr" && st != "dec" {
			return nil, 0, bad("invalid_status")
		}
		q += " AND status=?"
		args = append(args, st)
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	return rows, 200, e
}
func (s *Server) zapGet(r *http.Request, a Actor, d int64) (any, int, error) {
	z, e := id(r, "zap")
	if e != nil {
		return nil, 0, e
	}
	row, e := s.zap(r, s.DB, a, d, z)
	if e != nil {
		return nil, 0, e
	}
	if !zapVisible(a, row) {
		return nil, 0, denied
	}
	dates, e := list(r.Context(), s.DB, "SELECT id,date,status FROM core_zap_dates WHERE zap_id=? ORDER BY date", z)
	if e != nil {
		return nil, 0, e
	}
	images, e := list(r.Context(), s.DB, "SELECT id,mime FROM core_zap_images WHERE zap_id=? ORDER BY id", z)
	row["dates"] = dates
	row["images"] = images
	return row, 200, e
}
func (s *Server) zapImage(r *http.Request, a Actor, d int64) (any, int, error) {
	z, e := id(r, "zap")
	if e != nil {
		return nil, 0, e
	}
	row, e := s.zap(r, s.DB, a, d, z)
	if e != nil {
		return nil, 0, e
	}
	if !zapVisible(a, row) {
		return nil, 0, denied
	}
	image, e := id(r, "image")
	if e != nil {
		return nil, 0, e
	}
	var mime string
	var data []byte
	e = s.DB.QueryRowContext(r.Context(), "SELECT mime,image FROM core_zap_images WHERE id=? AND zap_id=?", image, z).Scan(&mime, &data)
	return ImageDTO{MIME: mime, Data: base64.StdEncoding.EncodeToString(data)}, 200, e
}
func (s *Server) zapCreate(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("zaps", true) && a.Role != "student" {
		return nil, 0, denied
	}
	var in ZapDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if a.Role == "student" && in.AccountID != a.AccountID {
		return nil, 0, denied
	}
	if in.AccountID < 1 || !validText(in.Text, 5000, true) || len(in.Dates) < 1 || len(in.Dates) > 60 || len(in.Images) > 3 {
		return nil, 0, bad("invalid_zap")
	}
	dates := map[string]bool{}
	for _, date := range in.Dates {
		if !validDate(date) {
			return nil, 0, bad("invalid_date")
		}
		dates[date] = true
	}
	blobs := [][]byte{}
	for _, img := range in.Images {
		b, e := base64.StdEncoding.DecodeString(img.Data)
		if e != nil || len(b) == 0 || len(b) > 1<<20 || img.MIME != "image/jpeg" && img.MIME != "image/png" || http.DetectContentType(b) != img.MIME {
			return nil, 0, bad("invalid_image")
		}
		blobs = append(blobs, b)
	}
	var z int64
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		row, e := s.member(r, tx, a, d, in.AccountID)
		if e != nil {
			return e
		}
		if row["role"] != "student" {
			return bad("student_required")
		}
		res, e := tx.ExecContext(r.Context(), "INSERT INTO core_zaps(organization_id,division_id,user_id,text,answer) VALUES(?,?,?,?,'')", a.OrganizationID, d, in.AccountID, in.Text)
		if e != nil {
			return e
		}
		z, e = res.LastInsertId()
		if e != nil {
			return e
		}
		for date := range dates {
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_zap_dates(zap_id,date) VALUES(?,?)", z, date); e != nil {
				return e
			}
		}
		for i, img := range in.Images {
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_zap_images(zap_id,mime,image) VALUES(?,?,?)", z, img.MIME, blobs[i]); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "zap.create", z)
	})
	return map[string]any{"id": z}, 201, e
}
func (s *Server) linkZapDate(r *http.Request, tx *sql.Tx, d, z, u, dateID int64) error {
	var date string
	if e := tx.QueryRowContext(r.Context(), "SELECT DATE_FORMAT(date,'%Y-%m-%d') FROM core_zap_dates WHERE id=? AND zap_id=?", dateID, z).Scan(&date); e != nil {
		return e
	}
	var day int64
	e := tx.QueryRowContext(r.Context(), "SELECT id FROM core_class_days WHERE division_id=? AND date=?", d, date).Scan(&day)
	if e == sql.ErrNoRows {
		_, e = tx.ExecContext(r.Context(), "UPDATE core_zap_dates SET status='no_class_day' WHERE id=?", dateID)
		return e
	}
	if e != nil {
		return e
	}
	var existing sql.NullInt64
	e = tx.QueryRowContext(r.Context(), "SELECT zap_id FROM core_attendance WHERE class_day_id=? AND user_id=?", day, u).Scan(&existing)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if existing.Valid && existing.Int64 != z {
		return &APIError{409, "another_zap_linked"}
	}
	if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_attendance(division_id,class_day_id,user_id,attendance_type_id,zap_id) VALUES(?,?,?,2,?) ON DUPLICATE KEY UPDATE attendance_type_id=2,zap_id=VALUES(zap_id)", d, day, u, z); e != nil {
		return e
	}
	_, e = tx.ExecContext(r.Context(), "UPDATE core_zap_dates SET status='linked' WHERE id=?", dateID)
	return e
}
func (s *Server) zapProcess(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("zaps", true) {
		return nil, 0, denied
	}
	z, e := id(r, "zap")
	if e != nil {
		return nil, 0, e
	}
	var in ZapProcessDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.Status != "apr" && in.Status != "dec" || !validText(in.Answer, 5000, false) {
		return nil, 0, bad("invalid_zap_decision")
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		row, e := s.zap(r, tx, a, d, z)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE core_zaps SET status=?,answer=? WHERE id=?", in.Status, in.Answer, z); e != nil {
			return e
		}
		if in.Status == "apr" {
			dates, e := list(r.Context(), tx, "SELECT id FROM core_zap_dates WHERE zap_id=? AND status<>'cancelled' ORDER BY id", z)
			if e != nil {
				return e
			}
			for _, date := range dates {
				if e = s.linkZapDate(r, tx, d, z, num(row["account_id"]), num(date["id"])); e != nil {
					return e
				}
			}
		} else {
			if _, e = tx.ExecContext(r.Context(), "DELETE FROM core_attendance WHERE division_id=? AND zap_id=?", d, z); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE core_zap_dates SET status='cancelled' WHERE zap_id=?", z); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "zap.process", z)
	})
	return nil, 200, e
}
func (s *Server) zapDateAction(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("zaps", true) {
		return nil, 0, denied
	}
	z, e := id(r, "zap")
	if e != nil {
		return nil, 0, e
	}
	dateID, e := id(r, "date")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		row, e := s.zap(r, tx, a, d, z)
		if e != nil {
			return e
		}
		if _, e := one(r.Context(), tx, "SELECT id FROM core_zap_dates WHERE id=? AND zap_id=?", dateID, z); e != nil {
			return e
		}
		if r.Method == "DELETE" {
			if _, e = tx.ExecContext(r.Context(), "DELETE e FROM core_attendance e JOIN core_class_days cd ON cd.id=e.class_day_id JOIN core_zap_dates zd ON zd.date=cd.date WHERE zd.id=? AND zd.zap_id=? AND e.zap_id=? AND e.division_id=?", dateID, z, z, d); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE core_zap_dates SET status='cancelled' WHERE id=?", dateID); e != nil {
				return e
			}
		} else {
			if row["status"] != "apr" {
				return &APIError{409, "zap_not_approved"}
			}
			if e = s.linkZapDate(r, tx, d, z, num(row["account_id"]), dateID); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "zap.date", dateID)
	})
	return nil, 200, e
}
