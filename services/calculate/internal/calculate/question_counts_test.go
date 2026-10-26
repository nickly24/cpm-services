package calculate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLargeTestsGradeEveryQuestionWithoutCountCeiling(t *testing.T) {
	questions := make([]Question, 6000)
	answers := make([]PracticeAnswer, 6000)
	order := make([]int64, 6000)
	for i := range questions {
		id := int64(i + 1)
		questions[i] = Question{ID: id, Type: "text", Text: "Введите", Points: 1, CorrectAnswers: []string{"ответ"}}
		answers[i] = PracticeAnswer{QuestionID: id, Value: json.RawMessage(`"ОТВЕТ"`)}
		order[i] = id
	}
	for _, op := range []string{"initial_grade", "regrade"} {
		result, err := GradeOfficial(CalculationInput{Operation: op, Questions: questions, Answers: answers, QuestionOrder: order})
		if err != nil || result.Score != 100 || result.MaxPoints != 6000 || len(result.Questions) != 6000 {
			t.Fatalf("%s: %+v %v", op, result, err)
		}
	}
	result, err := FinishPractice(PracticeFinishInput{Questions: questions, Answers: answers})
	if err != nil || result.Score != 100 || len(result.Questions) != 6000 {
		t.Fatalf("practice: %+v %v", result, err)
	}
}

func TestLargeChoiceAndTextAnswerSetsRemainUsable(t *testing.T) {
	options := make([]Option, 201)
	aliases := make([]string, 201)
	for i := range options {
		options[i] = Option{ID: fmt.Sprint(i + 1), Text: fmt.Sprint(i + 1), IsCorrect: i == 200}
		aliases[i] = fmt.Sprint(i + 1)
	}
	for _, q := range []Question{{ID: 1, Type: "multiple", Text: "Выберите", Points: 1, Answers: options}, {ID: 2, Type: "text", Text: "Введите", Points: 1, CorrectAnswers: aliases}} {
		value := json.RawMessage(`"201"`)
		if q.Type == "multiple" {
			value = json.RawMessage(`["201"]`)
		}
		result, err := CheckPractice(PracticeInput{Question: q, Value: value})
		if err != nil || !result.IsCorrect {
			t.Fatalf("%s: %+v %v", q.Type, result, err)
		}
	}
}

func TestLargeHistoricalSelectionSurvivesRemovedOption(t *testing.T) {
	selected := make([]string, 201)
	for i := range selected {
		selected[i] = fmt.Sprint(i + 1)
	}
	value, _ := json.Marshal(selected)
	q := Question{ID: 1, Type: "multiple", Text: "Выберите", Points: 1, Answers: []Option{{ID: "1", Text: "Остался", IsCorrect: true}, {ID: "2", Text: "Другой"}}}
	result, err := GradeOfficial(CalculationInput{Operation: "regrade", Questions: []Question{q}, QuestionOrder: []int64{1}, Answers: []PracticeAnswer{{QuestionID: 1, Value: value}}})
	if err != nil || result.MaxPoints != 1 || result.Score != 0 {
		t.Fatalf("historical selection rejected: %+v %v", result, err)
	}
	selected[200] = selected[0]
	value, _ = json.Marshal(selected)
	if historicalRemovedOption(q, value) {
		t.Fatal("duplicates must remain invalid")
	}
}

func TestLargeContentChangeSnapshotsHaveRoomForLargeTests(t *testing.T) {
	body, _ := json.Marshal(Envelope{MessageID: "bd095746-6c38-4b77-bc40-c6d0e62d1b2d", MessageType: "tests.content.changed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Producer: "tests-service", CorrelationID: "large-edit", Payload: mustJSON(map[string]any{"test_id": "507f1f77bcf86cd799439012", "organization_id": 1, "change_batch": map[string]any{"before": strings.Repeat("a", 3<<20), "after": strings.Repeat("b", 3<<20)}})})
	if len(body) <= MaxMessageBytes {
		t.Fatal("fixture too small")
	}
	if _, err := ParseMessage(body, "tests.content.changed.v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMessage(body, "tests.grade.requested.v1", time.Now()); err == nil {
		t.Fatal("unrelated event limits must remain")
	}
}
