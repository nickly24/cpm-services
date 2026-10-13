package calculate

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type Option struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	IsCorrect bool   `json:"is_correct"`
}

type Question struct {
	ID             int64    `json:"question_id"`
	Type           string   `json:"type"`
	Text           string   `json:"text"`
	Points         int64    `json:"points"`
	Answers        []Option `json:"answers"`
	CorrectAnswers []string `json:"correct_answers"`
}

type PracticeInput struct {
	Question Question        `json:"question"`
	Value    json.RawMessage `json:"value"`
}

type PracticeResult struct {
	IsCorrect      bool  `json:"is_correct"`
	Points         int64 `json:"points"`
	PossiblePoints int64 `json:"possible_points"`
	CorrectAnswer  any   `json:"correct_answer"`
}

type PracticeAnswer struct {
	QuestionID int64           `json:"question_id"`
	Value      json.RawMessage `json:"value"`
}

type PracticeFinishInput struct {
	Questions []Question       `json:"questions"`
	Answers   []PracticeAnswer `json:"answers"`
}

type QuestionResult struct {
	QuestionID int64 `json:"question_id"`
	Points     int64 `json:"points"`
	IsCorrect  bool  `json:"is_correct"`
}

type GradeResult struct {
	Score        int              `json:"score"`
	EarnedPoints int64            `json:"earned_points"`
	MaxPoints    int64            `json:"max_points"`
	Questions    []QuestionResult `json:"question_results"`
}

var errPractice = errors.New("invalid practice question or answer")

// CheckPractice preserves the legacy all-or-zero grading for one training
// question. It writes no attempt, submission, official score, or rating.
func CheckPractice(input PracticeInput) (PracticeResult, error) {
	q := input.Question
	result := PracticeResult{PossiblePoints: q.Points}
	if q.ID <= 0 || q.Points <= 0 || q.Points > 1_000_000 || len(q.Text) > 100_000 || len(input.Value) == 0 {
		return result, errPractice
	}
	if q.Type == "text" {
		if len(q.Answers) != 0 || len(q.CorrectAnswers) == 0 || len(q.CorrectAnswers) > 100 {
			return result, errPractice
		}
		var value string
		if json.Unmarshal(input.Value, &value) != nil || string(input.Value) == "null" || len(value) > 100_000 {
			return result, errPractice
		}
		for _, alias := range q.CorrectAnswers {
			if strings.TrimSpace(alias) == "" || len(alias) > 100_000 {
				return result, errPractice
			}
			if lowerText(value) == lowerText(alias) {
				result.IsCorrect = true
			}
		}
		result.CorrectAnswer = append([]string(nil), q.CorrectAnswers...)
	} else {
		if q.Type != "single" && q.Type != "multiple" || len(q.Answers) < 2 || len(q.Answers) > 100 || len(q.CorrectAnswers) != 0 {
			return result, errPractice
		}
		options := map[string]bool{}
		correct := []string{}
		for _, option := range q.Answers {
			if option.ID == "" || len(option.ID) > 128 || len(option.Text) > 100_000 {
				return result, errPractice
			}
			if _, exists := options[option.ID]; exists {
				return result, errPractice
			}
			options[option.ID] = option.IsCorrect
			if option.IsCorrect {
				correct = append(correct, option.ID)
			}
		}
		if len(correct) == 0 || q.Type == "single" && len(correct) != 1 {
			return result, errPractice
		}
		if q.Type == "single" {
			var value string
			if json.Unmarshal(input.Value, &value) != nil || string(input.Value) == "null" {
				return result, errPractice
			}
			isCorrect, exists := options[value]
			if !exists {
				return result, errPractice
			}
			result.IsCorrect = isCorrect
			result.CorrectAnswer = correct[0]
		} else {
			var values []string
			if json.Unmarshal(input.Value, &values) != nil || values == nil || len(values) > len(options) {
				return result, errPractice
			}
			seen := map[string]bool{}
			result.IsCorrect = len(values) == len(correct)
			for _, value := range values {
				isCorrect, exists := options[value]
				if !exists || seen[value] {
					return result, errPractice
				}
				seen[value] = true
				result.IsCorrect = result.IsCorrect && isCorrect
			}
			result.CorrectAnswer = correct
		}
	}
	if result.IsCorrect {
		result.Points = q.Points
	}
	return result, nil
}

func lowerText(value string) string {
	return cases.Lower(language.Und).String(strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }))
}

// FinishPractice grades only the caller's pinned training snapshot. It remains
// a synchronous, stateless calculation without official submissions or events.
func FinishPractice(input PracticeFinishInput) (GradeResult, error) {
	result := GradeResult{Questions: []QuestionResult{}}
	if len(input.Questions) == 0 || len(input.Questions) > 200 || len(input.Answers) > len(input.Questions) {
		return result, errPractice
	}
	answers := map[int64]json.RawMessage{}
	for _, answer := range input.Answers {
		if answer.QuestionID <= 0 || answers[answer.QuestionID] != nil || len(answer.Value) == 0 {
			return result, errPractice
		}
		answers[answer.QuestionID] = answer.Value
	}
	seen := map[int64]bool{}
	for _, q := range input.Questions {
		if seen[q.ID] {
			return result, errPractice
		}
		seen[q.ID] = true
		value, answered := answers[q.ID]
		if !answered {
			// Validate the question even when unanswered; a valid placeholder
			// is used only for validation and never awards points.
			switch q.Type {
			case "single":
				if len(q.Answers) == 0 {
					return result, errPractice
				}
				value, _ = json.Marshal(q.Answers[0].ID)
			case "multiple":
				value = json.RawMessage(`[]`)
			case "text":
				value = json.RawMessage(`""`)
			default:
				return result, errPractice
			}
		}
		feedback, err := CheckPractice(PracticeInput{Question: q, Value: value})
		if err != nil {
			return result, err
		}
		item := QuestionResult{QuestionID: q.ID}
		if answered {
			item.Points, item.IsCorrect = feedback.Points, feedback.IsCorrect
		}
		result.Questions = append(result.Questions, item)
		result.MaxPoints += q.Points
		result.EarnedPoints += item.Points
	}
	for id := range answers {
		if !seen[id] {
			return result, errPractice
		}
	}
	result.Score = legacyPercentage(result.EarnedPoints, result.MaxPoints)
	return result, nil
}

// Match Python int(round(earned / maximum * 100, 2)), including the original
// floating-point operation order and ties-to-even decimal rounding.
func legacyPercentage(earned, maximum int64) int {
	if maximum <= 0 {
		return 0
	}
	percentage := float64(earned) / float64(maximum) * 100
	cents := new(big.Rat).SetFloat64(percentage)
	cents.Mul(cents, big.NewRat(100, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(cents.Num(), cents.Denom(), remainder)
	comparison := new(big.Int).Lsh(remainder, 1).Cmp(cents.Denom())
	if comparison > 0 || comparison == 0 && quotient.Bit(0) == 1 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return int(quotient.Int64() / 100)
}
