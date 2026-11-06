package exams

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Projects only the signed-in student's small result records. Neither banks,
// definitions, other students nor question/vote protocols cross the SQL wire.
func (s *Server) studentResults(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if a.Role != "student" || a.DivisionID == nil || !contains(a.DivisionIDs, *a.DivisionID) {
		failure(w, Denied)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	if _, _, _, err = pageWindow(0, p); err != nil {
		failure(w, err)
		return
	}
	dateFilter, err := ParseExamDateFilter(p.Query)
	if err != nil {
		failure(w, err)
		return
	}
	directionNames := map[int64]string{}
	if strings.TrimSpace(p.Query["q"]) != "" || p.Query["sort"] == "direction_asc" {
		provider, ok := s.Deps.(interface {
			Fetch(*http.Request, int64, []int64, int64, []int64, []string) (ReferenceSet, error)
		})
		if !ok {
			failure(w, &APIError{503, "dependency_unavailable"})
			return
		}
		refs, er := provider.Fetch(r, a.OrganizationID, []int64{p.DivisionID}, 0, nil, nil)
		if er != nil {
			failure(w, er)
			return
		}
		for _, d := range refs.Directions {
			directionNames[d.ID] = d.Name
		}
	}
	path := fmt.Sprintf("$.divisions.\"%d\"", p.DivisionID)
	student := strconv.FormatInt(a.AccountID, 10)
	attempts := `(SELECT JSON_ARRAYAGG(JSON_SET(JSON_REMOVE(at.doc,'$.questions_snapshot','$.parts_snapshot','$.presented','$.usage','$.progress','$.cycles','$.ready','$.ready_at'),'$.question_count',COALESCE(JSON_LENGTH(JSON_EXTRACT(at.doc,'$.presented')),0),'$.config_snapshot',CASE WHEN COALESCE(JSON_UNQUOTE(JSON_EXTRACT(at.doc,'$.definition_key')),'')<>'' THEN JSON_EXTRACT(document,CONCAT('$.immutable_definitions."',JSON_UNQUOTE(JSON_EXTRACT(at.doc,'$.definition_key')),'".config')) ELSE JSON_EXTRACT(at.doc,'$.config_snapshot') END)) FROM JSON_TABLE(JSON_EXTRACT(document,'` + path + `.attempts'),'$[*]' COLUMNS(doc JSON PATH '$',student_id BIGINT PATH '$.student.account_id')) at WHERE at.student_id=` + student + `)`
	outside := `(SELECT JSON_ARRAYAGG(o.doc) FROM JSON_TABLE(JSON_EXTRACT(document,'` + path + `.outside_results'),'$[*]' COLUMNS(doc JSON PATH '$',student_id BIGINT PATH '$.student.account_id')) o WHERE o.student_id=` + student + `)`
	assignments := `(SELECT JSON_ARRAYAGG(JSON_REMOVE(aa.doc,'$.members')) FROM JSON_TABLE(JSON_EXTRACT(document,'` + path + `.assignments'),'$[*]' COLUMNS(doc JSON PATH '$',student_id BIGINT PATH '$.student.account_id')) aa WHERE aa.student_id=` + student + `)`
	query := `SELECT ` + catalogProjection() + `,JSON_OBJECT('date',JSON_EXTRACT(document,'` + path + `.date'),'config',JSON_EXTRACT(document,'` + path + `.config')),` + attempts + `,` + outside + `,` + assignments + ` FROM exams_catalog_v1 WHERE organization_id=? AND JSON_CONTAINS_PATH(document,'one',?)`
	rows, err := s.DB.QueryContext(r.Context(), query, a.OrganizationID, path)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	type record struct {
		exam   *Exam
		result map[string]any
	}
	values := []record{}
	for rows.Next() {
		var meta, division, attemptRaw, outsideRaw, assignmentRaw []byte
		if err = rows.Scan(&meta, &division, &attemptRaw, &outsideRaw, &assignmentRaw); err != nil {
			failure(w, err)
			return
		}
		e := &Exam{}
		d := &Division{ID: p.DivisionID}
		if err = json.Unmarshal(meta, e); err != nil {
			failure(w, err)
			return
		}
		if err = json.Unmarshal(division, d); err != nil {
			failure(w, err)
			return
		}
		e.Divisions = map[int64]*Division{p.DivisionID: d}
		if !dateFilter.Match(e, p.DivisionID) || p.Query["direction_id"] != "" && p.Query["direction_id"] != strconv.FormatInt(e.DirectionID, 10) || p.Query["type"] != "" && p.Query["type"] != e.Kind || !queryMatches(p, e.Name, directionNames[e.DirectionID]) {
			continue
		}
		var result map[string]any
		if e.Kind == "outside_lms" {
			if len(outsideRaw) > 0 {
				if err = json.Unmarshal(outsideRaw, &d.Outside); err != nil {
					failure(w, err)
					return
				}
			}
			if len(d.Outside) > 0 {
				result = outsideDTO(e, d, d.Outside[0], a)
			}
		} else {
			var ats []struct {
				Attempt
				QuestionCount int `json:"question_count"`
			}
			if len(attemptRaw) > 0 {
				if err = json.Unmarshal(attemptRaw, &ats); err != nil {
					failure(w, err)
					return
				}
			}
			if len(assignmentRaw) > 0 {
				if err = json.Unmarshal(assignmentRaw, &d.Assignments); err != nil {
					failure(w, err)
					return
				}
			}
			counts := map[int64]int{}
			for _, at := range ats {
				d.Attempts = append(d.Attempts, at.Attempt)
				counts[at.ID] = at.QuestionCount
			}
			if at := effectiveAttempt(d, a.AccountID); at != nil {
				result = resultDTO(e, d, at, a)
				result["question_count"] = counts[at.ID]
			}
		}
		if result == nil {
			continue
		}
		if grade := p.Query["grade"]; grade != "" && grade != fmt.Sprint(result["grade"]) {
			continue
		}
		values = append(values, record{e, result})
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	sort.SliceStable(values, func(i, j int) bool {
		left, right := values[i], values[j]
		switch p.Query["sort"] {
		case "direction_asc":
			return catalogDirectionLess(left.exam, right.exam, directionNames)
		case "name_asc":
			return strings.ToLower(left.exam.Name) < strings.ToLower(right.exam.Name)
		case "grade_desc":
			return fmt.Sprint(left.result["grade"]) > fmt.Sprint(right.result["grade"])
		default:
			return ExamCatalogDateLess(left.exam, right.exam, p.DivisionID, p.Query["sort"] == "date_asc")
		}
	})
	out, err := selectedPage(values, p, func(v record) any {
		row := summary(v.exam, p.DivisionID)
		for _, key := range []string{"connected_division_ids", "owner_division_id", "origin_exam_id", "in_bank"} {
			delete(row, key)
		}
		return map[string]any{"exam": row, "result": v.result}
	})
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, out)
}
