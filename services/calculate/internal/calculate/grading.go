package calculate

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

var ErrInvalidGradeInput = errors.New("invalid immutable grading input")

// CalculationInput is served by tests from the immutable job selected by the
// requested revision. Original raw answers are never rewritten by calculate.
type CalculationInput struct {
	SubmissionID        string           `json:"submission_id"`
	OrganizationID      int64            `json:"organization_id"`
	DivisionID          int64            `json:"division_id"`
	AccountID           int64            `json:"account_id"`
	TestID              string           `json:"test_id"`
	CalculationRevision int64            `json:"calculation_revision"`
	CalculationStatus   string           `json:"calculation_status"`
	GradingVersionID    string           `json:"grading_test_version_id"`
	InputHash           string           `json:"input_hash"`
	Operation           string           `json:"operation"`
	Questions           []Question       `json:"questions"`
	Answers             []PracticeAnswer `json:"answers"`
	QuestionOrder       []int64          `json:"question_order"`
	ExcludedQuestionIDs []int64          `json:"excluded_question_ids"`
}

type CalculationResult struct {
	CalculationRevision int64   `json:"calculation_revision"`
	GradingVersionID    string  `json:"grading_test_version_id"`
	InputHash           string  `json:"input_hash"`
	ExcludedQuestionIDs []int64 `json:"excluded_question_ids"`
	GradeResult
}

// GradeOfficial uses exactly the legacy all-or-zero score. During a regrade,
// removed options remain valid historical raw answers and score zero. New
// questions are absent from the effective input supplied by tests, so they do
// not enlarge the historical maximum. An empty effective test grades to zero.
func GradeOfficial(input CalculationInput) (GradeResult, error) {
	result := GradeResult{Questions: []QuestionResult{}}
	regrade := input.Operation == "regrade"
	if input.Operation != "initial_grade" && !regrade || len(input.Questions) > 200 || !regrade && len(input.Questions) == 0 || len(input.QuestionOrder) > 200 || len(input.Answers) > 200 {
		return result, ErrInvalidGradeInput
	}
	excluded := map[int64]bool{}
	for _, id := range input.ExcludedQuestionIDs {
		if id <= 0 || excluded[id] {
			return result, ErrInvalidGradeInput
		}
		excluded[id] = true
	}
	questions := map[int64]Question{}
	for _, q := range input.Questions {
		if questions[q.ID].ID != 0 || excluded[q.ID] {
			return result, ErrInvalidGradeInput
		}
		if _, err := CheckPractice(PracticeInput{Question: q, Value: placeholderValue(q)}); err != nil {
			return result, ErrInvalidGradeInput
		}
		questions[q.ID] = q
	}
	answers := map[int64]json.RawMessage{}
	for _, a := range input.Answers {
		if a.QuestionID <= 0 || len(a.Value) == 0 || answers[a.QuestionID] != nil {
			return result, ErrInvalidGradeInput
		}
		if questions[a.QuestionID].ID == 0 && !regrade {
			return result, ErrInvalidGradeInput
		}
		answers[a.QuestionID] = a.Value
	}
	seen := map[int64]bool{}
	for _, id := range input.QuestionOrder {
		if id <= 0 || seen[id] {
			return result, ErrInvalidGradeInput
		}
		seen[id] = true
		q, exists := questions[id]
		if !exists {
			if regrade {
				continue
			}
			return result, ErrInvalidGradeInput
		}
		item := QuestionResult{QuestionID: id}
		if value, answered := answers[id]; answered {
			feedback, err := CheckPractice(PracticeInput{Question: q, Value: value})
			if err != nil {
				if !regrade || !historicalRemovedOption(q, value) {
					return result, ErrInvalidGradeInput
				}
			} else {
				item.Points, item.IsCorrect = feedback.Points, feedback.IsCorrect
			}
		}
		result.Questions = append(result.Questions, item)
		result.EarnedPoints += item.Points
		result.MaxPoints += q.Points
	}
	for id := range questions {
		if !seen[id] {
			return result, ErrInvalidGradeInput
		}
	}
	result.Score = legacyPercentage(result.EarnedPoints, result.MaxPoints)
	return result, nil
}

func placeholderValue(q Question) json.RawMessage {
	switch q.Type {
	case "single":
		if len(q.Answers) > 0 {
			value, _ := json.Marshal(q.Answers[0].ID)
			return value
		}
	case "multiple":
		return json.RawMessage(`[]`)
	case "text":
		return json.RawMessage(`""`)
	}
	return nil
}

func historicalRemovedOption(q Question, value json.RawMessage) bool {
	known := map[string]bool{}
	for _, option := range q.Answers {
		known[option.ID] = true
	}
	switch q.Type {
	case "single":
		var selected string
		return string(value) != "null" && json.Unmarshal(value, &selected) == nil && !known[selected]
	case "multiple":
		var selected []string
		if json.Unmarshal(value, &selected) != nil || selected == nil || len(selected) > 100 {
			return false
		}
		seen, removed := map[string]bool{}, false
		for _, id := range selected {
			if seen[id] {
				return false
			}
			seen[id] = true
			removed = removed || !known[id]
		}
		return removed
	}
	return false
}

type RecalcDecision struct {
	NeedsRecalc         bool     `json:"needs_recalc"`
	Reasons             []string `json:"reasons"`
	ExcludedQuestionIDs []int64  `json:"excluded_question_ids"`
}

// DecideRecalc preserves the old policy, including additions that deliberately
// do not trigger recalculation. Type changes exclude that question permanently
// from historical attempts; changing it back must not restore those points.
func DecideRecalc(before, after []Question) RecalcDecision {
	decision := RecalcDecision{Reasons: []string{}, ExcludedQuestionIDs: []int64{}}
	previous, current := map[int64]Question{}, map[int64]Question{}
	for _, q := range before {
		previous[q.ID] = q
	}
	for _, q := range after {
		current[q.ID] = q
	}
	reasons := map[string]bool{}
	add := func(reason string) {
		if !reasons[reason] {
			reasons[reason] = true
			decision.Reasons = append(decision.Reasons, reason)
		}
	}
	for id := range previous {
		if _, exists := current[id]; !exists {
			add("question_removed")
			break
		}
	}
	ids := []int64{}
	for id := range previous {
		if _, exists := current[id]; exists {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return strconv.FormatInt(ids[i], 10) < strconv.FormatInt(ids[j], 10) })
	for _, id := range ids {
		old, next := previous[id], current[id]
		if old.Type != next.Type {
			add("question_type_changed")
			decision.ExcludedQuestionIDs = append(decision.ExcludedQuestionIDs, id)
			continue
		}
		if old.Points != next.Points {
			add("points_changed")
		}
		oldOptions, newOptions := map[string]Option{}, map[string]Option{}
		for _, option := range old.Answers {
			oldOptions[option.ID] = option
		}
		for _, option := range next.Answers {
			newOptions[option.ID] = option
		}
		for id := range oldOptions {
			if _, exists := newOptions[id]; !exists {
				add("option_removed")
				break
			}
		}
		for id, option := range oldOptions {
			if nextOption, exists := newOptions[id]; exists && option.IsCorrect != nextOption.IsCorrect {
				add("correct_flag_changed")
				break
			}
		}
		newTexts := map[string]bool{}
		for _, text := range next.CorrectAnswers {
			newTexts[text] = true
		}
		for _, text := range old.CorrectAnswers {
			if !newTexts[text] {
				add("text_correct_removed")
				break
			}
		}
	}
	decision.NeedsRecalc = len(decision.Reasons) > 0
	return decision
}
