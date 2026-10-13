package core

import (
	"database/sql"
	"net/http"
	"regexp"
	"time"
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var roomPattern = regexp.MustCompile(`^[0-9A-Za-zА-Яа-яЁё][0-9A-Za-zА-Яа-яЁё\s-]*$`)

func validLesson(in LessonDTO) bool {
	a, e1 := time.Parse("15:04", in.Start)
	b, e2 := time.Parse("15:04", in.End)
	return validDate(in.Date) && e1 == nil && e2 == nil && a.Format("15:04") == in.Start && b.Format("15:04") == in.End && in.Start < in.End && validText(in.Name, 200, true) && validText(in.Teacher, 200, true) && validText(in.Location, 200, true) && validText(in.Classroom, 50, true) && roomPattern.MatchString(in.Classroom) && colorPattern.MatchString(in.Color) && positive(in.SchoolID) && ((in.Public && in.SchoolID == nil) || (!in.Public && in.SchoolID != nil))
}
func (s *Server) lessons(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("schedule", false) && a.Role != "student" && a.Role != "proctor" && a.Role != "examinator" && a.Role != "supervisor" {
		return nil, 0, denied
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	if e := period(from, to); e != nil {
		return nil, 0, e
	}
	q := "SELECT id,date,start_time,end_time,lesson_name,teacher_name,location,classroom,color,is_changed,is_in_person,is_for_all,is_public,school_id FROM core_lessons WHERE organization_id=? AND division_id=? AND date BETWEEN ? AND ?"
	args := []any{a.OrganizationID, d, from, to}
	if a.Role == "student" {
		q += " AND (is_public=1 OR school_id=(SELECT school_id FROM students WHERE id=?))"
		args = append(args, a.ID)
	}
	rows, e := list(r.Context(), s.DB, q+" ORDER BY date,start_time,id LIMIT 2000", args...)
	return rows, 200, e
}
func (s *Server) lessonSaveTx(r *http.Request, tx *sql.Tx, a Actor, d, n int64, in LessonDTO) (int64, error) {
	if !validLesson(in) {
		return 0, bad("invalid_lesson")
	}
	if in.SchoolID != nil {
		if _, e := s.school(r, tx, a, *in.SchoolID, true); e != nil {
			return 0, e
		}
	}
	if n > 0 {
		var found int64
		if e := tx.QueryRowContext(r.Context(), "SELECT id FROM core_lessons WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d).Scan(&found); e != nil {
			return 0, e
		}
	}
	var overlap int
	args := []any{d, in.Date, in.End, in.Start, n, in.Public, in.SchoolID}
	if e := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_lessons WHERE division_id=? AND date=? AND start_time<? AND end_time>? AND id<>? AND is_public=? AND school_id<=>?", args...).Scan(&overlap); e != nil {
		return 0, e
	}
	if overlap > 0 {
		return 0, &APIError{409, "schedule_conflict"}
	}
	fields := []any{in.Date, in.Start, in.End, in.Name, in.Teacher, in.Location, in.Classroom, in.Color, in.Changed, in.InPerson, in.ForAll, in.Public, in.SchoolID}
	if n == 0 {
		res, e := tx.ExecContext(r.Context(), "INSERT INTO core_lessons(date,start_time,end_time,lesson_name,teacher_name,location,classroom,color,is_changed,is_in_person,is_for_all,is_public,school_id,organization_id,division_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", append(fields, a.OrganizationID, d)...)
		if e != nil {
			return 0, e
		}
		n, e = res.LastInsertId()
		if e != nil {
			return 0, e
		}
	} else {
		if _, e := tx.ExecContext(r.Context(), "UPDATE core_lessons SET date=?,start_time=?,end_time=?,lesson_name=?,teacher_name=?,location=?,classroom=?,color=?,is_changed=?,is_in_person=?,is_for_all=?,is_public=?,school_id=? WHERE id=?", append(fields, n)...); e != nil {
			return 0, e
		}
	}
	return n, audit(r.Context(), tx, a, &d, "lesson.save", n)
}
func (s *Server) lessonSave(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("schedule", true) {
		return nil, 0, denied
	}
	var in LessonDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	var n int64
	var e error
	if r.Method == "PUT" {
		n, e = id(r, "lesson")
		if e != nil {
			return nil, 0, e
		}
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var err error
		n, err = s.lessonSaveTx(r, tx, a, d, n, in)
		return err
	})
	code := 200
	if r.Method == "POST" {
		code = 201
	}
	return map[string]any{"id": n}, code, e
}
func (s *Server) lessonsBulk(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("schedule", true) {
		return nil, 0, denied
	}
	var in struct {
		Lessons []LessonDTO `json:"lessons"`
	}
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if len(in.Lessons) < 1 || len(in.Lessons) > 100 {
		return nil, 0, bad("invalid_batch_size")
	}
	ids := []int64{}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		for _, lesson := range in.Lessons {
			n, e := s.lessonSaveTx(r, tx, a, d, 0, lesson)
			if e != nil {
				return e
			}
			ids = append(ids, n)
		}
		return nil
	})
	return ids, 201, e
}
func (s *Server) lessonDelete(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("schedule", true) {
		return nil, 0, denied
	}
	n, e := id(r, "lesson")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		res, e := tx.ExecContext(r.Context(), "DELETE FROM core_lessons WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d)
		if e != nil {
			return e
		}
		count, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if count == 0 {
			return missing
		}
		return audit(r.Context(), tx, a, &d, "lesson.delete", n)
	})
	return nil, 200, e
}
