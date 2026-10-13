package exams

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func importTemplateHeaders(kind string) ([]string, error) {
	switch kind {
	case "questions":
		return []string{"part_code", "question_text", "answer_text"}, nil
	case "outside":
		return []string{"student_id", "student_login", "points", "grade", "examinator"}, nil
	case "assignments":
		headers := []string{"student_id", "student_login"}
		for i := 1; i <= 6; i++ {
			headers = append(headers, fmt.Sprintf("examinator_%d_id", i), fmt.Sprintf("examinator_%d_login", i))
		}
		return append(headers, "replacement_limit"), nil
	default:
		return nil, Bad("invalid_import_type")
	}
}
func templateXML(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}
func BuildImportTemplate(kind string) ([]byte, error) {
	headers, err := importTemplateHeaders(kind)
	if err != nil {
		return nil, err
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultRowHeight="15"/><cols>`)
	for i := range headers {
		fmt.Fprintf(&sheet, `<col min="%d" max="%d" width="28" customWidth="1"/>`, i+1, i+1)
	}
	sheet.WriteString(`</cols><sheetData><row r="1">`)
	for i, header := range headers {
		fmt.Fprintf(&sheet, `<c r="%s1" t="inlineStr"><is><t>%s</t></is></c>`, string(rune('A'+i)), templateXML(header))
	}
	sheet.WriteString(`</row></sheetData><autoFilter ref="A1:` + string(rune('A'+len(headers)-1)) + `1"/></worksheet>`)
	entries := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Import" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, entry := range entries {
		file, err := archive.Create(entry.name)
		if err != nil {
			return nil, err
		}
		if _, err = file.Write([]byte(entry.body)); err != nil {
			return nil, err
		}
	}
	if err = archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
func (s *Server) importTemplateRoute(w http.ResponseWriter, r *http.Request) {
	actor, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !importPermission(actor, false) {
		failure(w, Denied)
		return
	}
	p, err := params(r, actor)
	if err != nil {
		failure(w, err)
		return
	}
	if p.DivisionID < 1 {
		failure(w, Bad("division_context_required"))
		return
	}
	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	kind := r.PathValue("kind")
	if !importKind(kind) {
		failure(w, Missing)
		return
	}
	exam, err := load(r.Context(), s.DB, actor.OrganizationID, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err = importRequestScope(exam, actor, p, false, kind); err != nil {
		failure(w, err)
		return
	}
	content, err := BuildImportTemplate(kind)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="exams-`+kind+`.xlsx"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
