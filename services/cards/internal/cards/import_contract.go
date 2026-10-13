package cards

import (
	"fmt"
	"github.com/xuri/excelize/v2"
	"net/http"
)

// The owner defines its subject format; Uploads keeps the original file/job.
func (s *Server) importContract(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(false) {
		return nil, denied
	}
	return map[string]any{
		"type": "cards.xlsx", "format_version": 1, "scope": "organization", "permission": "train",
		"columns": []string{"Вопрос", "Ответ"}, "extensions": []string{".xlsx"},
		"max_rows": 1000, "max_request_bytes": 10 << 20, "max_uncompressed_bytes": 32 << 20,
		"max_question_characters": 4000, "max_answer_characters": 16000,
		"targets_path":  "/v1/cards/imports/targets",
		"template_path": "/v1/cards/imports/template", "parse_path": "/v1/cards/imports/parse",
		"receipt_path": "/v1/cards/receipts/{command}", "preview_ttl_hours": 72,
		"source_storage": "uploads", "retains_source_bytes": false,
		"source_access_path":  "/v1/cards/sessions/{session}/source",
		"preserves_multiline": true, "duplicate_policy": "warning", "empty_row_policy": "reject",
		"transform": map[string]any{"permission": "train.edit + tests.edit", "scope": "organization; destination pinned to verified division", "max_cards": 200, "max_answer_characters": 1000, "create_path": "/v1/cards/transforms"},
	}, nil
}
func templateBytes() ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(f.GetActiveSheetIndex())
	for i, value := range []string{"Вопрос", "Ответ"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if e := f.SetCellStr(sheet, cell, value); e != nil {
			return nil, e
		}
	}
	if e := f.SetColWidth(sheet, "A", "B", 55); e != nil {
		return nil, e
	}
	b, e := f.WriteToBuffer()
	if e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func (s *Server) importTemplate(w http.ResponseWriter, r *http.Request) {
	a, _, e := s.Deps.Identify(r)
	if e != nil {
		fail(w, e)
		return
	}
	if !a.Can(false) {
		fail(w, denied)
		return
	}
	b, e := templateBytes()
	if e != nil {
		fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="cards-template.xlsx"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(b)))
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
func (s *Server) sessionSource(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(true) {
		return nil, denied
	}
	ss, e := s.loadSession(r, s.DB, a, scope, false)
	if e != nil {
		return nil, e
	}
	if ss.Kind != "import" {
		return nil, missing
	}
	return map[string]any{"session_id": ss.ID, "organization_id": a.OrganizationID, "direction_id": ss.DirectionID,
		"source_sha256": ss.Preview.SourceSHA256, "source_filename": ss.Preview.SourceFilename, "source_sheet": ss.Preview.SourceSheet,
		"content_type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "retains_source_bytes": false}, nil
}

// Manual targets deliberately do not depend on the Tests projection service.
func (s *Server) importTargets(r *http.Request, a Actor, scope Scope) (any, error) {
	if !a.Can(false) {
		return nil, denied
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT id,direction_id,name,revision FROM cards_themes_v1 WHERE organization_id=? ORDER BY direction_id,name,id", a.OrganizationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	themes := []Theme{}
	for rows.Next() {
		var t Theme
		if e = rows.Scan(&t.ID, &t.DirectionID, &t.Name, &t.Revision); e != nil {
			return nil, e
		}
		if scope.Allows(t.DirectionID) {
			themes = append(themes, t)
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return map[string]any{"organization_id": a.OrganizationID, "directions": scope.Directions, "themes": themes}, nil
}
