package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func largeQuestionTable(t *testing.T, count int) JSONImportPreviewDTO {
	t.Helper()
	rows := make([]any, count)
	for i := range rows {
		rows[i] = map[string]any{"row": i + 2, "number": fmt.Sprint(i + 1), "title": "Большой тест", "question": strings.Repeat("Вопрос ", 35), "answer": "первый\nвторой / третий"}
	}
	raw, err := json.Marshal(map[string]any{"importFormat": "question_table", "rows": rows, "metadata": map[string]any{"startDate": "2026-10-26T10:00", "endDate": "2031-10-26T10:00", "timeLimitMinutes": 180}})
	if err != nil {
		t.Fatal(err)
	}
	return JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: raw}
}

func TestQuestionTableMultiLetterAndNumericMarkers(t *testing.T) {
	for _, source := range []string{"А. Первый\nАМ. Свыше 10 лет\nБД. Строже\nБЕ. Нет", "A. First    AA. Second    AAA. Third", "1. Один\n62. Много\n99999999999999999999999999. Ещё"} {
		options, ok := tableOptions(source)
		if !ok || len(options) < 3 {
			t.Fatalf("lost labels: %+v", options)
		}
	}
	opts, ok := tableOptions("А. Первый\nАМ. Свыше 10 лет\nБД. Строже\nБЕ. Нет")
	if !ok || len(opts) != 4 || opts[1].label != "АМ" || opts[2].label != "БД" {
		t.Fatal(opts)
	}
	for _, source := range []string{"А. Один\nа. Дубль", "0. Нулевой\n1. Один", "000. Нулевой\n1. Один"} {
		if _, valid := tableOptions(source); valid {
			t.Fatal("invalid labels accepted")
		}
	}
}

func TestQuestionTableSixThousandQuestionsAndTextAlternatives(t *testing.T) {
	in := largeQuestionTable(t, 6000)
	if len(in.Payload) <= 1<<20 {
		t.Fatal("fixture must cross the old wire/input limits")
	}
	preview := buildJSONImportPreview(in, "Математика")
	if !preview.Valid || preview.Summary.QuestionsTotal != 6000 || preview.Summary.TotalPoints != 6000 || preview.Preview.Questions[5999].QuestionID != 6000 {
		t.Fatalf("large preview rejected: %+v", preview.Errors)
	}
	if !reflect.DeepEqual(preview.Preview.Questions[0].CorrectAnswers, []string{"первый", "второй", "третий"}) {
		t.Fatal(preview.Preview.Questions[0])
	}
	if err := validateQuestions(preview.Preview.Questions, false); err != nil {
		t.Fatal(err)
	}
	draft := DraftDTO{Title: "Большой черновик", DirectionID: 31, Questions: preview.Preview.Questions}
	for _, q := range draft.Questions {
		draft.Layout = append(draft.Layout, DraftPosition{QuestionID: q.QuestionID})
	}
	if err := validateDraft(draft); err != nil {
		t.Fatalf("draft still has a question count or byte ceiling: %v", err)
	}
	// Exercise the public handler and its live permission checks, not just the converter.
	s := &Server{Dependencies: templateTestDependencies(t)}
	body, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/v1/tests/divisions/17/imports/json/preview", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("large HTTP preview: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data JSONImportPreview `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Data.Valid || result.Data.Summary.QuestionsTotal != 6000 {
		t.Fatal("large preview response truncated")
	}
	// The signed internal delivery wire must preserve the same large payload.
	r = httptest.NewRequest("POST", "/internal/tests/imports/json/validate", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if wire, err := readImportWire(r); err != nil || !bytes.Equal(wire.Body, body) {
		t.Fatalf("large delivery: %v", err)
	}
}

func TestQuestionAndTextAnswerCountsHaveNoFixedCeiling(t *testing.T) {
	options := make([]AnswerOption, 201)
	aliases := make([]string, 201)
	for i := range options {
		options[i] = AnswerOption{ID: fmt.Sprint(i + 1), Text: fmt.Sprint(i + 1), IsCorrect: i == 200}
		aliases[i] = fmt.Sprint(i + 1)
	}
	qs := []Question{{QuestionID: 1, Type: "multiple", Text: "Выберите", Points: 1, Answers: options}, {QuestionID: 2, Type: "text", Text: "Введите", Points: 1, CorrectAnswers: aliases}}
	if err := validateQuestions(qs, false); err != nil {
		t.Fatal(err)
	}
	qs[0].Answers[1].ID = qs[0].Answers[0].ID
	if validateQuestions(qs, false) == nil {
		t.Fatal("duplicate identities must still fail")
	}
}

func TestQuestionTableDatesRemainWholeAlternatives(t *testing.T) {
	in := largeQuestionTable(t, 1)
	payload, _ := jsonImportObject(in.Payload)
	payload["rows"].([]any)[0].(map[string]any)["answer"] = "01/02/2026\r\n03.04.2026\nдата / DATE"
	in.Payload, _ = json.Marshal(payload)
	p := buildJSONImportPreview(in, "Математика")
	if !p.Valid || !reflect.DeepEqual(p.Preview.Questions[0].CorrectAnswers, []string{"01.02.2026", "03.04.2026", "дата", "DATE"}) {
		t.Fatalf("date split: %+v", p)
	}
}

func TestImportSchemaDoesNotAdvertiseCountCeilings(t *testing.T) {
	props := legacyImportFileSchema()["properties"].(map[string]any)
	questions := props["questions"].(map[string]any)
	if _, exists := questions["maxItems"]; exists {
		t.Fatal("question count ceiling in schema")
	}
	fields := questions["items"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"answers", "correctAnswers"} {
		if _, exists := fields[name].(map[string]any)["maxItems"]; exists {
			t.Fatal("answer count ceiling in schema", name)
		}
	}
}
