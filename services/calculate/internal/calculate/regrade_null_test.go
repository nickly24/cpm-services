package calculate

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRegradeHistoricalNullMeansUnansweredWithoutChangingSnapshot(t *testing.T) {
	for _, value := range []string{"null", " \nnull\t"} {
		input := CalculationInput{Operation: "regrade", Questions: gradeQuestions(), QuestionOrder: []int64{3, 1, 2}, Answers: []PracticeAnswer{
			{QuestionID: 1, Value: json.RawMessage(value)}, {QuestionID: 2, Value: json.RawMessage(value)}, {QuestionID: 3, Value: json.RawMessage(value)},
		}}
		before, _ := json.Marshal(input)
		grade, err := GradeOfficial(input)
		if err != nil || grade.Score != 0 || grade.MaxPoints != 15 || grade.EarnedPoints != 0 || len(grade.Questions) != 3 {
			t.Fatalf("historical skips failed or changed denominator: %+v %v", grade, err)
		}
		for _, q := range grade.Questions {
			if q.IsCorrect || q.Points != 0 {
				t.Fatal("null awarded points")
			}
		}
		after, _ := json.Marshal(input)
		if string(before) != string(after) {
			t.Fatal("raw immutable input changed")
		}
		// Null is not a new valid answer in the initial-grade or practice API.
		input.Operation = "initial_grade"
		if _, err := GradeOfficial(input); !errors.Is(err, ErrInvalidGradeInput) {
			t.Fatal("new null submission accepted")
		}
		for _, q := range input.Questions {
			if _, err := CheckPractice(PracticeInput{Question: q, Value: json.RawMessage(`null`)}); err == nil {
				t.Fatal("practice accepted null")
			}
		}
	}
}

func TestRegradeNullMatchesOmittedAnswerAndRetainsValidation(t *testing.T) {
	base := CalculationInput{Operation: "regrade", Questions: gradeQuestions(), QuestionOrder: []int64{3, 1, 2}, Answers: []PracticeAnswer{
		{QuestionID: 1, Value: json.RawMessage(`"a"`)}, {QuestionID: 2, Value: json.RawMessage(`null`)}, {QuestionID: 3, Value: json.RawMessage(`"Москва"`)},
	}}
	got, err := GradeOfficial(base)
	if err != nil {
		t.Fatal(err)
	}
	omitted := base
	omitted.Answers = []PracticeAnswer{base.Answers[0], base.Answers[2]}
	want, err := GradeOfficial(omitted)
	if err != nil || !reflect.DeepEqual(got, want) || got.EarnedPoints != 8 || got.MaxPoints != 15 {
		t.Fatalf("null changed non-empty results: %+v %+v %v", got, want, err)
	}
	for name, answers := range map[string][]PracticeAnswer{
		"duplicate_null":   {{QuestionID: 2, Value: json.RawMessage(`null`)}, {QuestionID: 2, Value: json.RawMessage(`null`)}},
		"null_then_value":  {{QuestionID: 2, Value: json.RawMessage(`null`)}, {QuestionID: 2, Value: json.RawMessage(`[]`)}},
		"invalid_id":       {{QuestionID: 0, Value: json.RawMessage(`null`)}},
		"wrong_type":       {{QuestionID: 1, Value: json.RawMessage(`[]`)}},
		"duplicate_option": {{QuestionID: 2, Value: json.RawMessage(`["a","a"]`)}},
		"malformed_null":   {{QuestionID: 1, Value: json.RawMessage(`nullx`)}},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.Answers = answers
			if _, err := GradeOfficial(input); !errors.Is(err, ErrInvalidGradeInput) {
				t.Fatal("corrupt input accepted")
			}
		})
	}
	// Null also cannot hide a duplicate ID belonging to an excluded question.
	base.Questions = base.Questions[:1]
	base.ExcludedQuestionIDs = []int64{2, 3}
	base.Answers = []PracticeAnswer{{QuestionID: 2, Value: json.RawMessage(`null`)}, {QuestionID: 2, Value: json.RawMessage(`null`)}}
	if _, err := GradeOfficial(base); !errors.Is(err, ErrInvalidGradeInput) {
		t.Fatal("excluded duplicate null accepted")
	}
}
