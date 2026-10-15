package exams

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Metadata projection deliberately excludes questions, answers, definitions and
// students' exam attempts. The unified browser catalog paginates this with Cards.
func (s *Server) trainingCatalog(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if a.Role != "student" || a.DivisionID == nil || !contains(a.DivisionIDs, *a.DivisionID) {
		failure(w, Denied)
		return
	}
	if len(r.URL.Query()) != 1 {
		failure(w, Bad("invalid_query"))
		return
	}
	direction, err := strconv.ParseInt(r.URL.Query().Get("direction_id"), 10, 64)
	if err != nil || direction < 1 {
		failure(w, Bad("invalid_direction"))
		return
	}
	fetcher, ok := s.Deps.(interface {
		Fetch(*http.Request, int64, []int64, int64, []int64, []string) (ReferenceSet, error)
	})
	if !ok {
		failure(w, &APIError{503, "dependency_unavailable"})
		return
	}
	refs, err := fetcher.Fetch(r, a.OrganizationID, []int64{*a.DivisionID}, direction, nil, nil)
	if err != nil {
		failure(w, err)
		return
	}
	if len(refs.Divisions) != 1 || refs.Divisions[0].ID != *a.DivisionID || !refs.Divisions[0].Active || len(refs.Directions) != 1 || refs.Directions[0].ID != direction || !refs.Directions[0].Active {
		failure(w, Missing)
		return
	}
	path := fmt.Sprintf("$.divisions.\"%d\"", *a.DivisionID)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,JSON_UNQUOTE(JSON_EXTRACT(document,'$.name')),JSON_LENGTH(JSON_EXTRACT(document,'$.parts')),JSON_EXTRACT(document,CONCAT(?,'.config.start_at')),JSON_EXTRACT(document,CONCAT(?,'.config.end_at')) FROM exams_catalog_v1 WHERE organization_id=? AND JSON_EXTRACT(document,'$.direction_id')=? AND JSON_UNQUOTE(JSON_EXTRACT(document,'$.exam_type'))='classic' AND JSON_EXTRACT(document,CONCAT(?,'.training_enabled'))=true AND EXISTS (SELECT 1 FROM JSON_TABLE(document,'$.questions[*]' COLUMNS(part_id BIGINT PATH '$.part_id',question_text TEXT PATH '$.question_text',answer_text TEXT PATH '$.answer_text')) q WHERE TRIM(q.question_text)<>'' AND TRIM(q.answer_text)<>'' AND JSON_CONTAINS(JSON_EXTRACT(document,'$.parts[*].id'),CAST(q.part_id AS JSON))) ORDER BY id LIMIT 1001`, path, path, a.OrganizationID, direction, path)
	if err != nil {
		failure(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var parts int
		var start, end []byte
		if err = rows.Scan(&id, &name, &parts, &start, &end); err != nil {
			failure(w, err)
			return
		}
		var startValue, endValue any
		if len(start) > 0 {
			if err = decodeCatalogTime(start, &startValue); err != nil {
				failure(w, err)
				return
			}
		}
		if len(end) > 0 {
			if err = decodeCatalogTime(end, &endValue); err != nil {
				failure(w, err)
				return
			}
		}
		out = append(out, map[string]any{"kind": "exam", "refId": strconv.FormatInt(id, 10), "exam_id": id, "name": name, "direction_id": direction, "parts_count": parts, "start_at": startValue, "end_at": endValue})
	}
	if err = rows.Err(); err != nil {
		failure(w, err)
		return
	}
	if len(out) > 1000 {
		failure(w, Bad("catalog_limit_exceeded"))
		return
	}
	respond(w, 200, out)
}

func decodeCatalogTime(raw []byte, out *any) error { return json.Unmarshal(raw, out) }
