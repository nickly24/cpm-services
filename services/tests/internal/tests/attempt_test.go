package tests

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"strings"
	"testing"
	"time"
)

func TestOfflineTransportHashKeepsBrowserRepresentation(t *testing.T) {
	version := bson.NewObjectID().Hex()
	raw := `{"test_version_id":"` + version + `","completed_at":"2026-10-07T15:00:00.000+03:00","reason":"manual","answers":[{"question_id":3,"value":"<ответ>\u2028&"},{"question_id":2,"value":["b","a"]}]}`
	var tree any
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		t.Fatal(err)
	}
	client := hashJSON(tree)
	var body map[string]any
	_ = json.Unmarshal([]byte(raw), &body)
	body["snapshot_hash"] = client
	encoded, _ := json.Marshal(body)
	var input FinalizeDTO
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if input.ClientHash != client {
		t.Fatal("original browser timestamp/order checksum changed")
	}
	if strings.Contains(string(canonicalJSON(tree)), `\u003c`) || strings.Contains(string(canonicalJSON(tree)), `\u2028`) {
		t.Fatal("canonical JSON escapes browser Unicode or HTML")
	}
	if err := json.Unmarshal([]byte(`{"reason":"manual","unexpected":true}`), &input); err == nil {
		t.Fatal("strict decoding lost inside snapshot unmarshaler")
	}
}
func TestAnswerValidationAndLocking(t *testing.T) {
	questions := []Question{
		{QuestionID: 1, Type: "single", Answers: []AnswerOption{{ID: "a"}, {ID: "b"}}},
		{QuestionID: 2, Type: "multiple", Answers: []AnswerOption{{ID: "a"}, {ID: "b"}}},
		{QuestionID: 3, Type: "text"},
	}
	answers, err := normalizedAnswers(questions, []SubmittedAnswer{{2, []any{"b", "a"}}, {3, "  Ответ  "}})
	if err != nil {
		t.Fatal(err)
	}
	if !equalValue(answers[0].Value, []string{"a", "b"}) || answers[1].Value != "  Ответ  " {
		t.Fatal("multi answer set normalization or raw text persistence broken")
	}
	for _, bad := range [][]SubmittedAnswer{{{1, []string{"a"}}}, {{2, []string{"a", "a"}}}, {{2, []string{"unknown"}}}, {{99, "a"}}, {{1, "a"}, {1, "b"}}} {
		if _, err := normalizedAnswers(questions, bad); err == nil {
			t.Fatalf("invalid answer accepted: %#v", bad)
		}
	}
	attempt := Attempt{}
	if err := putAnswer(&attempt, SubmittedAnswer{1, "a"}); err != nil {
		t.Fatal(err)
	}
	if err := putAnswer(&attempt, SubmittedAnswer{1, "a"}); err != nil {
		t.Fatal("same committed answer must be retriable")
	}
	if err := putAnswer(&attempt, SubmittedAnswer{1, "b"}); err == nil {
		t.Fatal("committed answer changed")
	}
	if len(attempt.Answers) != 1 {
		t.Fatal("retry duplicated a question")
	}
}
func TestSubmissionScopeAndKeyVisibility(t *testing.T) {
	a := Actor{OrganizationID: 1, AccountID: 7}
	test := bson.NewObjectID()
	first, second := attemptFilter(a, 10, test, false), attemptFilter(a, 20, test, false)
	if first["division_id"] == second["division_id"] {
		t.Fatal("shared test erased selected division")
	}
	grade := &GradeResult{Score: 100, Questions: []QuestionResult{{QuestionID: 1, Points: 5, IsCorrect: true}}}
	attempt := Attempt{DivisionID: 10, Result: grade, Status: "accepted"}
	view := submissionView(attempt)
	if view.Result.Questions != nil || view.DivisionID != 10 {
		t.Fatal("hidden review correctness leaked or division lost")
	}
	if len(grade.Questions) != 1 {
		t.Fatal("public sanitization mutated stored results")
	}
	if actorResults(Actor{Role: "staff_admin", Permissions: map[string]Permission{"tests": {View: true, Edit: true}}}, true) {
		t.Fatal("catalog rights allowed changing student results")
	}
}
func TestLegacyPercentRoundingBoundary(t *testing.T) {
	for _, tc := range []struct{ earned, max, want int }{{1, 3, 33}, {2, 3, 66}, {399, 20000, 1}, {0, 20, 0}, {20, 20, 100}} {
		if got := legacyPercentage(tc.earned, tc.max); got != tc.want {
			t.Fatalf("%d/%d got %d want %d", tc.earned, tc.max, got, tc.want)
		}
	}
}
func TestEventStoredOnceAndWorkerFieldsPreserved(t *testing.T) {
	event := NewEvent("tests.grade.requested", map[string]any{"division_id": 12, "submission_id": bson.NewObjectID().Hex()})
	stored, err := storeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	again, err := storeEvent(event)
	if err != nil || !equalValue(stored, again) {
		t.Fatal("retry changed immutable event bytes")
	}
	var decoded Event
	if err := json.Unmarshal(stored.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MessageID != event.MessageID || decoded.SchemaVersion != 1 || stored.RoutingKey != "tests.grade.requested.v1" {
		t.Fatal("invalid bus contract")
	}
	fields, err := documentFields(Attempt{ID: bson.NewObjectID(), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["_id"]; exists {
		t.Fatal("CAS tries to replace immutable identity")
	}
	if _, exists := fields["outbox"]; exists {
		t.Fatal("business update would overwrite publisher bookkeeping")
	}
}

func TestFinalSnapshotPreservesAllLockedAnswers(t *testing.T) {
	questions := []Question{
		{QuestionID: 1, Type: "single", Answers: []AnswerOption{{ID: "a"}, {ID: "b"}}},
		{QuestionID: 2, Type: "multiple", Answers: []AnswerOption{{ID: "a"}, {ID: "b"}}},
		{QuestionID: 3, Type: "text"},
	}
	locked := []SubmittedAnswer{{1, "b"}, {2, bson.A{"b", "a"}}, {3, "  Москва "}}
	cases := []struct {
		name    string
		answers []SubmittedAnswer
		reject  bool
	}{
		{"same including normalized multiple", []SubmittedAnswer{{1, "b"}, {2, []string{"a", "b"}}, {3, "  Москва "}}, false},
		{"changed single", []SubmittedAnswer{{1, "a"}, {2, []string{"a", "b"}}, {3, "  Москва "}}, true},
		{"omitted locked", []SubmittedAnswer{{1, "b"}, {2, []string{"a", "b"}}}, true},
		{"raw text changed", []SubmittedAnswer{{1, "b"}, {2, []string{"a", "b"}}, {3, "Москва"}}, true},
		{"empty", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLockedAnswers(questions, locked, tc.answers)
			if (err != nil) != tc.reject {
				t.Fatalf("reject=%v, got %v", tc.reject, err)
			}
		})
	}
	if err := validateLockedAnswers(questions, locked[:1], cases[0].answers); err != nil {
		t.Fatal("new offline answers should be allowed:", err)
	}
}
