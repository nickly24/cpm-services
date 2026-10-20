package calculate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAddedTextAnswerRegradesPreviouslyWrongAndPreservesOtherAnswers(t *testing.T) {
	before := []Question{{ID: 1, Type: "text", Points: 1, CorrectAnswers: []string{"Питер"}}}
	after := cloneQuestions(before)
	after[0].CorrectAnswers = append(after[0].CorrectAnswers, "Москва")
	decision := DecideRecalc(before, after)
	if !decision.NeedsRecalc || !reflect.DeepEqual(decision.Reasons, []string{"text_correct_added"}) || len(decision.ExcludedQuestionIDs) != 0 {
		t.Fatalf("text addition wasn't a scoring change: %+v", decision)
	}
	for _, tc := range []struct {
		name, answer string
		old, next    int
	}{
		{"newly accepted", `"Москва"`, 0, 100},
		{"normalized", `"  мОсКвА  "`, 0, 100},
		{"previously accepted", `"Питер"`, 100, 100},
		{"still incorrect", `"Омск"`, 0, 0},
		{"unanswered", `null`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := CalculationInput{Operation: "regrade", Questions: before, QuestionOrder: []int64{1}, Answers: []PracticeAnswer{{QuestionID: 1, Value: json.RawMessage(tc.answer)}}}
			old, err := GradeOfficial(input)
			if err != nil || old.Score != tc.old {
				t.Fatalf("unexpected old result: %+v %v", old, err)
			}
			input.Questions = after
			next, err := GradeOfficial(input)
			if err != nil || next.Score != tc.next || next.MaxPoints != old.MaxPoints || string(input.Answers[0].Value) != tc.answer {
				t.Fatalf("wrong new result or changed historical input: %+v %v", next, err)
			}
		})
	}
}

func TestContentWorkerRequestsRegradeForAddedTextAnswer(t *testing.T) {
	before := gradeQuestions()
	after := cloneQuestions(before)
	after[2].CorrectAnswers = append(after[2].CorrectAnswers, "Moscow")
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-c6d0e62d1b2d", MessageType: "tests.content.changed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "tests-service", CorrelationID: "alias-edit", Payload: mustJSON(map[string]any{"test_id": "507f1f77bcf86cd799439012", "organization_id": 1, "before_version_id": "507f1f77bcf86cd799439013", "after_version_id": "507f1f77bcf86cd799439014", "change_batch": map[string]any{"before": map[string]any{"questions": before}, "after": map[string]any{"questions": after}}})})
	m, err := ParseMessage(body, "tests.content.changed.v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input PrepareRecalculation
		if r.URL.Path != "/internal/tests/recalculations/prepare" || json.NewDecoder(r.Body).Decode(&input) != nil || !input.NeedsRecalc || !reflect.DeepEqual(input.Reasons, []string{"text_correct_added"}) || len(input.ExcludedQuestionIDs) != 0 || input.OrganizationID != 1 || input.TestID != "507f1f77bcf86cd799439012" {
			t.Error("added alias lost the regrade decision or scope")
		}
		reply(w, 200, PreparedRecalculation{Status: "queued", ProcessedCount: 2})
	}))
	defer server.Close()
	worker := &Worker{Store: &processingMemory{}, Tests: NewTestsClient(server.URL, strings.Repeat("k", 48))}
	status, reason, _, err := worker.Process(context.Background(), m)
	if err != nil || status != "ready" || reason != "" || calls != 1 {
		t.Fatalf("content addition was skipped: %s %s %v calls=%d", status, reason, err, calls)
	}
}
