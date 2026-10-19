package uploads

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuestionTablePhysicalRowsFormattingAndFormula(t *testing.T) {
	headers := []string{"Название теста", "Номер", "Категория вопроса", "Вопрос", "Варианты ответов", "Правильный ответ", "Примечание к ответу"}
	sheet := `<row r="2">`
	for i, v := range headers {
		sheet += sourceTestCell(string(rune('A'+i))+"2", v)
	}
	sheet += `</row><row r="8">` + sourceTestCell("A8", "Тест") + `<c r="B8"><v>7</v></c>` + sourceTestCell("D8", "Вопрос?") + sourceTestCell("F8", "42") + `<c r="XFD8" s="1"/></row><row r="900"><c r="A900" s="1"/></row>`
	raw, e := questionTableInput(sourceTestArchive(t, sheet, nil), map[string]string{"timeLimitMinutes": "180", "published": "false"})
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		Rows     []map[string]any `json:"rows"`
		Metadata map[string]any   `json:"metadata"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Rows) != 1 || result.Rows[0]["row"] != float64(8) || result.Rows[0]["number"] != "7" || result.Metadata["published"] != false {
		t.Fatal(string(raw))
	}
	_, e = questionTableInput(sourceTestArchive(t, strings.Replace(sheet, `r="XFD8" s="1"/>`, `r="XFD8"><f>1+1</f></c>`, 1), nil), nil)
	sourceTestError(t, e, "formulas_forbidden")
}
func TestTestsPreviewUsesLiveBearerAndPublicScopedOwnerRoute(t *testing.T) {
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tests/divisions/7/imports/json/preview" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("Authorization") != "Bearer actor" {
			t.Error("wrong authentication boundary")
		}
		w.Write([]byte(`{"status":true,"data":{"organization_id":42,"division_id":7,"direction_id":19,"valid":true,"preview_hash":"` + strings.Repeat("a", 64) + `"}}`))
	}))
	defer owner.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{TestsURL: owner.URL, InternalKey: strings.Repeat("u", 32)}}
	ss := Session{Type: "tests.json", Scope: Scope{OrganizationID: 42, DivisionID: 7, ActorID: 9}}
	_, _, e := s.validateStagedInput(context.Background(), ss, "Bearer actor", json.RawMessage(`{"direction_id":19}`), 0)
	if e != nil {
		t.Fatal(e)
	}
}
