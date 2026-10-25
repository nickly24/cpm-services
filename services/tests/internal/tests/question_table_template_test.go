package tests

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestQuestionTableTemplateRoundTripWithAllQuestionTypes(t *testing.T) {
	file, err := jsonTemplateBytes("Право & Конституция", "question_table")
	if err != nil {
		t.Fatal(err)
	}
	if file.MIME != importXLSXMIME || !strings.HasSuffix(file.Filename, ".xlsx") {
		t.Fatal("wrong attachment type")
	}
	archive, err := zip.NewReader(bytes.NewReader(file.Body), int64(len(file.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var sheet struct {
		Rows []struct {
			Cells []struct {
				Text string `xml:"is>t"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	for _, part := range archive.File {
		if part.Name == "xl/worksheets/sheet1.xml" {
			r, e := part.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			r.Close()
			if e != nil || xml.Unmarshal(data, &sheet) != nil {
				t.Fatal("bad template sheet")
			}
		}
	}
	if len(sheet.Rows) != 4 || len(sheet.Rows[0].Cells) != 8 || sheet.Rows[0].Cells[1].Text != "Название теста" || sheet.Rows[0].Cells[7].Text != "Правильный ответ" {
		t.Fatal("actual Excel headers/example rows lost")
	}
	rows := []map[string]any{}
	for i, row := range sheet.Rows[1:] {
		if len(row.Cells) != 8 || row.Cells[0].Text != "Право & Конституция" {
			t.Fatal("XML escaped direction lost")
		}
		rows = append(rows, map[string]any{"row": i + 2, "title": row.Cells[1].Text, "number": row.Cells[3].Text, "category": row.Cells[4].Text, "question": row.Cells[5].Text, "options": row.Cells[6].Text, "answer": row.Cells[7].Text})
	}
	payload, _ := json.Marshal(map[string]any{"importFormat": "question_table", "rows": rows, "metadata": map[string]any{"direction": "Право & Конституция", "startDate": "2099-01-01T10:00", "endDate": "2099-01-01T11:00", "published": false, "visible": false, "timeLimitMinutes": 30}})
	preview := buildJSONImportPreview(JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: payload}, "Право & Конституция")
	if !preview.Valid || preview.Summary.QuestionsTotal != 3 || preview.Summary.SingleCount != 1 || preview.Summary.MultipleCount != 1 || preview.Summary.TextCount != 1 || preview.Preview.Settings.Published || preview.Preview.Settings.AnswersVisible {
		t.Fatalf("Excel example not importable: %+v", preview)
	}
}

func TestQuestionTableTemplateHTTPAllowsReadOnlyButRejectsStudent(t *testing.T) {
	server := &Server{Dependencies: templateTestDependencies(t)}
	path := "/v1/tests/divisions/17/imports/json/template?direction_id=31&format=question_table"
	response := templateHTTP(t, server, path, "reader", 200)
	if response.Header().Get("Content-Type") != importXLSXMIME || !strings.HasPrefix(response.Body.String(), "PK") {
		t.Fatal("Excel template not returned to reader")
	}
	templateHTTP(t, server, path, "student", 403)
	schema := templateHTTP(t, server, strings.Replace(path, "/template?", "/schema?", 1), "reader", 200)
	if !strings.Contains(schema.Body.String(), "question_table") || !strings.Contains(schema.Body.String(), "Название теста") {
		t.Fatal("Excel schema not declared")
	}
}
