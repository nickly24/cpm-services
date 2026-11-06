package exams

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// The overview needs counts and configuration, not question text, examiner
// votes or hydrated immutable bank definitions from every historical attempt.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !a.Can(false) {
		failure(w, Denied)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	if p.DivisionID < 1 {
		failure(w, Bad("division_context_required"))
		return
	}
	path := fmt.Sprintf("$.divisions.\"%d\"", p.DivisionID)
	division := `JSON_OBJECT('division_id',?,'revision',JSON_EXTRACT(document,'` + path + `.revision'),'date',JSON_EXTRACT(document,'` + path + `.date'),'config',JSON_EXTRACT(document,'` + path + `.config'),'training_enabled',JSON_EXTRACT(document,'` + path + `.training_enabled'))`
	query := `SELECT JSON_SET(` + catalogProjection() + `,'$.parts',JSON_EXTRACT(document,'$.parts'),'$.questions',(SELECT JSON_ARRAYAGG(JSON_OBJECT('part_id',q.part_id)) FROM JSON_TABLE(document,'$.questions[*]' COLUMNS(part_id BIGINT PATH '$.part_id')) q),'$.divisions',IF(JSON_CONTAINS_PATH(document,'one',?),JSON_OBJECT(?,` + division + `),JSON_OBJECT())),JSON_KEYS(JSON_EXTRACT(document,'$.divisions')),COALESCE(JSON_LENGTH(JSON_EXTRACT(document,'` + path + `.commissions')),0),COALESCE(JSON_LENGTH(JSON_EXTRACT(document,'` + path + `.assignments')),0),(SELECT JSON_OBJECT('pending',COALESCE(SUM(t.status='pending_ready'),0),'in_progress',COALESCE(SUM(t.status='in_progress'),0),'completed',COALESCE(SUM(t.status='completed'),0),'results',COUNT(DISTINCT CASE WHEN t.status='completed' THEN t.student_id END)) FROM JSON_TABLE(JSON_EXTRACT(document,'` + path + `.attempts'),'$[*]' COLUMNS(status VARCHAR(32) PATH '$.status',student_id BIGINT PATH '$.student.account_id')) t),(SELECT JSON_OBJECT('count',COUNT(*),'average',COALESCE(AVG(t.grade),0)) FROM JSON_TABLE(JSON_EXTRACT(document,'` + path + `.outside_results'),'$[*]' COLUMNS(grade INT PATH '$.grade')) t) FROM exams_catalog_v1 WHERE organization_id=? AND id=?`
	var raw, keys, attempts, outside []byte
	var commissions, assignments int
	err = s.DB.QueryRowContext(r.Context(), query, path, strconv.FormatInt(p.DivisionID, 10), p.DivisionID, a.OrganizationID, id).Scan(&raw, &keys, &commissions, &assignments, &attempts, &outside)
	if err != nil {
		if err == sql.ErrNoRows {
			err = Missing
		}
		failure(w, err)
		return
	}
	e := &Exam{}
	if err = json.Unmarshal(raw, e); err != nil {
		failure(w, err)
		return
	}
	var ids []string
	if len(keys) > 0 {
		if err = json.Unmarshal(keys, &ids); err != nil {
			failure(w, err)
			return
		}
	}
	for _, value := range ids {
		division, er := strconv.ParseInt(value, 10, 64)
		if er != nil {
			failure(w, er)
			return
		}
		if e.Divisions[division] == nil {
			e.Divisions[division] = &Division{ID: division}
		}
	}
	if err = scope(e, a, p, false, false); err != nil {
		failure(w, err)
		return
	}
	detail, err := Query(e, a, "detail", p)
	if err != nil {
		failure(w, err)
		return
	}
	counts := map[string]int{}
	if err = json.Unmarshal(attempts, &counts); err != nil {
		failure(w, err)
		return
	}
	counts["parts"], counts["questions"], counts["commissions"], counts["assignments"] = len(e.Parts), len(e.Questions), commissions, assignments
	var ext struct {
		Count   int     `json:"count"`
		Average float64 `json:"average"`
	}
	if err = json.Unmarshal(outside, &ext); err != nil {
		failure(w, err)
		return
	}
	var average any
	if e.Kind == "outside_lms" {
		counts["results"] = ext.Count
		if ext.Count > 0 {
			average = ext.Average
		}
	}
	respond(w, 200, map[string]any{"exam": detail, "counts": counts, "average_grade": average})
}
