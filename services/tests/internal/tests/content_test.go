package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

func contentFixtures() []Question {
	return []Question{
		{QuestionID: 1, Type: "single", Text: "Столица?", Points: 2, Answers: []AnswerOption{{"a", "Москва", true}, {"b", "Париж", false}}},
		{QuestionID: 2, Type: "multiple", Text: "Чётные?", Points: 3, Answers: []AnswerOption{{"x", "2", true}, {"y", "4", true}, {"z", "3", false}}},
		{QuestionID: 3, Type: "text", Text: "Напишите ответ", Points: 5, CorrectAnswers: []string{"SECRET_alias", "вариант"}},
	}
}

func TestRoutesRegisterWithoutAmbiguity(t *testing.T) {
	// ServeMux rejects overlapping method/path templates when the server starts.
	if (&Server{}).Handler() == nil {
		t.Fatal("HTTP router was not created")
	}
}
func TestQuestionValidationProtectsAnswerSemantics(t *testing.T) {
	valid := contentFixtures()
	if e := validateQuestions(valid, false); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func([]Question){
		"single_multiple_keys":            func(q []Question) { q[0].Answers[1].IsCorrect = true },
		"multiple_no_keys":                func(q []Question) { q[1].Answers[0].IsCorrect = false; q[1].Answers[1].IsCorrect = false },
		"duplicate_option":                func(q []Question) { q[1].Answers[1].ID = q[1].Answers[0].ID },
		"alias_case_whitespace_duplicate": func(q []Question) { q[2].CorrectAnswers = []string{"Москва", "  МОСКВА "} },
		"text_with_options":               func(q []Question) { q[2].Answers = []AnswerOption{{"a", "1", true}} },
		"negative_weight":                 func(q []Question) { q[0].Points = -1 },
		"duplicate_question":              func(q []Question) { q[2].QuestionID = 1 },
		"javascript_unsafe_id":            func(q []Question) { q[2].QuestionID = 9007199254740992 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			q := contentFixtures()
			change(q)
			if validateQuestions(q, false) == nil {
				t.Fatal("invalid grading definition accepted")
			}
		})
	}
	if validateQuestions(nil, false) == nil {
		t.Fatal("empty published body accepted")
	}
	if e := validateQuestions(nil, true); e != nil {
		t.Fatal("empty editor body rejected", e)
	}
}
func TestQuestionAndOptionIDsRemainRetired(t *testing.T) {
	qs := normalizeQuestions(contentFixtures())
	max, used, e := transitionIDs(Document{}, qs)
	if e != nil {
		t.Fatal(e)
	}
	old := Document{Questions: qs, MaxQuestionID: max, UsedOptionIDs: used}
	remaining := []Question{qs[0], qs[1]}
	max, used, e = transitionIDs(old, remaining)
	if e != nil {
		t.Fatal(e)
	}
	without := Document{Questions: remaining, MaxQuestionID: max, UsedOptionIDs: used}
	if _, _, e = transitionIDs(without, qs); e == nil {
		t.Fatal("deleted last question ID reused")
	}
	newq := qs[2]
	newq.QuestionID = 4
	if _, _, e = transitionIDs(without, append(remaining, newq)); e != nil {
		t.Fatal("new question ID rejected", e)
	}
	q := remaining[1]
	q.Answers = []AnswerOption{q.Answers[0], q.Answers[1]}
	kept := []Question{remaining[0], q}
	max, used, e = transitionIDs(without, kept)
	if e != nil {
		t.Fatal(e)
	}
	withoutOption := Document{Questions: kept, MaxQuestionID: max, UsedOptionIDs: used}
	if _, _, e = transitionIDs(withoutOption, remaining); e == nil {
		t.Fatal("deleted option ID reused")
	}
}
func TestStudentQuestionsNeverContainKeys(t *testing.T) {
	raw, e := json.Marshal(SafeQuestions(contentFixtures()))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"is_correct", "correct_answers", "SECRET_alias"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("student payload leaked %s", secret)
		}
	}
	if !strings.Contains(string(raw), "Москва") {
		t.Fatal("safe variant text disappeared")
	}
}
func TestIncompleteDraftCanBeSavedButCannotPublish(t *testing.T) {
	draft := DraftDTO{Questions: []Question{{QuestionID: 1, Type: "single", Answers: []AnswerOption{{ID: "a"}, {ID: "b"}}}}, Layout: []DraftPosition{{QuestionID: 1, X: 10, Y: 20}}}
	if e := validateDraft(draft); e != nil {
		t.Fatal("incomplete editor draft rejected", e)
	}
	if validateQuestions(draft.Questions, false) == nil {
		t.Fatal("incomplete question became publishable")
	}
	draft.Layout[0].QuestionID = 2
	if validateDraft(draft) == nil {
		t.Fatal("layout references an absent question")
	}
}
