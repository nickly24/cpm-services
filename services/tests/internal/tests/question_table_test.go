package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQuestionTableOwnerValidationAndWarnings(t *testing.T) {
	p := map[string]any{"importFormat": "question_table", "metadata": map[string]any{"startDate": "2026-10-19T10:00", "endDate": "2031-10-19T10:00", "timeLimitMinutes": 180}, "rows": []any{
		map[string]any{"row": 8, "title": "Тест", "number": "7", "category": "Тест с одним вариантом ответа", "question": "Выберите", "options": "А. Первый\nБ. Второй", "answer": "Г. Второй"},
		map[string]any{"row": 10, "title": "Тест", "number": "12", "category": "Напишите слово", "question": "Ответ?", "answer": "слово / синоним", "note": "Допустимо: «третий»"},
	}}
	raw, _ := json.Marshal(p)
	out := buildJSONImportPreview(JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: raw}, "Право")
	if !out.Valid || out.Source != "question_table" || out.Preview.Settings.Published || out.Preview.Settings.AnswersVisible || out.Summary.QuestionsTotal != 2 || !out.Preview.Questions[0].Answers[1].IsCorrect || len(out.Preview.Questions[1].CorrectAnswers) != 2 || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "Строка 8") {
		t.Fatalf("%+v", out)
	}
	if strings.Contains(strings.Join(out.Preview.Questions[1].CorrectAnswers, " "), "третий") {
		t.Fatal("note became an answer")
	}
	p["rows"].([]any)[0].(map[string]any)["answer"] = "Д. Неизвестный"
	raw, _ = json.Marshal(p)
	out = buildJSONImportPreview(JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: raw}, "Право")
	if out.Valid || !jsonImportHasPath(out, "rows[0].answer") {
		t.Fatal("ambiguous key accepted", out)
	}
}
func TestQuestionTableDoesNotSplitAbbreviations(t *testing.T) {
	opts, ok := tableOptions("А. суд г. Тула\nБ. другой суд")
	if !ok || len(opts) != 2 || opts[0].text != "суд г. Тула" {
		t.Fatal(opts)
	}
	opts, ok = tableOptions("А. Один    Б. Два")
	if !ok || len(opts) != 2 {
		t.Fatal(opts)
	}
}

func TestQuestionTableCellErrorsAndCorrectionRevalidateWholeSnapshot(t *testing.T) {
	row := map[string]any{"row": 8, "title": "Тест", "number": "7", "question": "Первый?", "answer": "#VALUE!", "cell_errors": map[string]any{"answer": "error"}}
	second := map[string]any{"row": 12, "number": "9", "question": "Второй?", "answer": "Ответ"}
	payload := map[string]any{"importFormat": "question_table", "rows": []any{row, second}, "metadata": map[string]any{"timeLimitMinutes": 30, "startDate": "2026-10-25T10:00", "endDate": "2031-10-25T10:00"}}
	preview := func() JSONImportPreview {
		raw, _ := json.Marshal(payload)
		return buildJSONImportPreview(JSONImportPreviewDTO{OrganizationID: 11, DivisionID: 17, DirectionID: 31, Payload: raw}, "Право")
	}
	bad := preview()
	if bad.Valid || len(bad.Preview.Questions) != 2 || !jsonImportHasPath(bad, "rows[0].answer") {
		t.Fatalf("%+v", bad)
	}
	delete(row, "cell_errors")
	row["answer"] = "Исправлено"
	good := preview()
	if !good.Valid || len(good.Preview.Questions) != 2 || good.PreviewHash == bad.PreviewHash || good.Preview.Questions[1].QuestionID != 9 {
		t.Fatalf("%+v", good)
	}
	row["answer"] = ""
	if preview().Valid {
		t.Fatal("empty correction was accepted")
	}
}
