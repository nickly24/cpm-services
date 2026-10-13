package calculate

import (
	"errors"
	"math"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"

	"golang.org/x/text/cases"
)

const RatingAlgorithm = "legacy-rating-v1"

var ErrInvalidRatingInput = errors.New("invalid immutable rating input")
var ErrRatingPending = errors.New("rating source is not ready")

type RatingInput struct {
	JobID          string                `json:"job_id"`
	Revision       int64                 `json:"revision"`
	OrganizationID int64                 `json:"organization_id"`
	DivisionID     int64                 `json:"division_id"`
	AccountID      int64                 `json:"account_id"`
	InputHash      string                `json:"input_hash"`
	Algorithm      string                `json:"algorithm_version"`
	DateFrom       string                `json:"date_from"`
	DateTo         string                `json:"date_to"`
	AsOf           time.Time             `json:"as_of"`
	Homework       []RatingHomeworkInput `json:"homework"`
	Tests          []RatingTestInput     `json:"tests"`
	Exams          []RatingExamInput     `json:"exams"`
}

type RatingHomeworkInput struct {
	AssignmentID int64    `json:"assignment_id"`
	Name         string   `json:"name"`
	TypeName     string   `json:"type_name"`
	Deadline     string   `json:"deadline"`
	Status       int      `json:"status"`
	Result       *float64 `json:"result"`
	DatePass     *string  `json:"date_pass"`
}

type RatingTestInput struct {
	TestID            string   `json:"test_id"`
	Title             string   `json:"title"`
	Direction         string   `json:"direction"`
	Source            string   `json:"source"`
	CalculationStatus string   `json:"calculation_status"`
	Score             *float64 `json:"score"`
}

type RatingExamInput struct {
	ExamID        string   `json:"exam_id"`
	Name          string   `json:"exam_name"`
	Date          string   `json:"exam_date"`
	Type          string   `json:"exam_type"`
	DirectionID   *int64   `json:"direction_id"`
	AttemptNo     *int     `json:"attempt_no"`
	HasAppeal     bool     `json:"has_appeal"`
	MissingResult bool     `json:"missing_result"`
	Score         *float64 `json:"score"`
}

type RatingResult struct {
	JobID          string                `json:"job_id"`
	Revision       int64                 `json:"revision"`
	OrganizationID int64                 `json:"organization_id"`
	DivisionID     int64                 `json:"division_id"`
	AccountID      int64                 `json:"account_id"`
	InputHash      string                `json:"input_hash"`
	Algorithm      string                `json:"algorithm_version"`
	Status         string                `json:"status"`
	ErrorCode      string                `json:"error_code,omitempty"`
	Homework       *RatingHomeworkResult `json:"homework,omitempty"`
	Exams          *RatingExamResult     `json:"exams,omitempty"`
	Tests          *RatingTestsResult    `json:"tests,omitempty"`
	FinalRating    *float64              `json:"final_rating,omitempty"`
}

type RatingHomeworkResult struct {
	Rating  float64                `json:"rating"`
	Details []RatingHomeworkDetail `json:"details"`
}

type RatingHomeworkDetail struct {
	HomeworkID int64   `json:"homework_id"`
	Name       string  `json:"name"`
	Deadline   string  `json:"deadline"`
	Score      float64 `json:"score"`
	Status     string  `json:"status"`
	DatePass   *string `json:"date_pass"`
}

type RatingExamResult struct {
	Rating  float64            `json:"rating"`
	Details []RatingExamDetail `json:"details"`
}

type RatingExamDetail struct {
	ExamID        string  `json:"exam_id"`
	Name          string  `json:"exam_name"`
	Date          string  `json:"exam_date"`
	Score         float64 `json:"score"`
	Status        string  `json:"status"`
	Type          string  `json:"exam_type"`
	DirectionID   *int64  `json:"direction_id"`
	AttemptNo     *int    `json:"attempt_no"`
	HasAppeal     bool    `json:"has_appeal"`
	MissingResult bool    `json:"missing_result"`
}

type RatingTestsResult struct {
	Rating     float64            `json:"rating"`
	Directions map[string]float64 `json:"directions"`
	Details    []RatingTestDetail `json:"details"`
}

type RatingTestDetail struct {
	TestID            string  `json:"test_id"`
	Title             string  `json:"title"`
	Direction         string  `json:"direction"`
	Score             float64 `json:"score"`
	Source            string  `json:"source"`
	CalculationStatus string  `json:"calculation_status,omitempty"`
}

func ratingMetadata(r RatingRequest) RatingResult {
	return RatingResult{JobID: r.JobID, Revision: r.Revision, OrganizationID: r.OrganizationID, DivisionID: r.DivisionID, AccountID: r.AccountID, InputHash: r.InputHash, Algorithm: RatingAlgorithm}
}

func ratingInputRequest(input RatingInput) RatingRequest {
	return RatingRequest{JobID: input.JobID, Revision: input.Revision, OrganizationID: input.OrganizationID, DivisionID: input.DivisionID, AccountID: input.AccountID, InputHash: input.InputHash}
}

func finiteRating(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// legacyRatingSum preserves the float sum() behavior of the dev legacy
// CPython 3.12+ runtime. Tests and exam snapshots use sum(); homework uses
// ordinary += instead, and must retain that different operation order.
type legacyRatingSum struct {
	total        float64
	compensation float64
}

func (s *legacyRatingSum) add(value float64) {
	next := s.total + value
	if math.Abs(s.total) >= math.Abs(value) {
		s.compensation += (s.total - next) + value
	} else {
		s.compensation += (value - next) + s.total
	}
	s.total = next
}

func (s legacyRatingSum) value() float64 {
	// Like CPython, avoid turning an overflowed sum into NaN by adding a
	// nonfinite compensation. CalculateRating still rejects that overflow.
	if s.compensation != 0 && finiteRating(s.compensation) {
		return s.total + s.compensation
	}
	return s.total
}

// CalculateRating aggregates an immutable personal snapshot. Source owners
// select eligible tests/exams and the effective exam attempt/appeal. Calculate
// does not regrade answers, select another attempt or persist owner data.
func CalculateRating(input RatingInput) (RatingResult, error) {
	result := ratingMetadata(ratingInputRequest(input))
	_, fromErr := time.Parse(time.DateOnly, input.DateFrom)
	_, toErr := time.Parse(time.DateOnly, input.DateTo)
	_, offset := input.AsOf.Zone()
	if !validRatingRequest(ratingInputRequest(input)) || input.Algorithm != RatingAlgorithm || fromErr != nil || toErr != nil || input.DateFrom > input.DateTo || input.AsOf.IsZero() || offset != 0 {
		return result, ErrInvalidRatingInput
	}
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return result, ErrInvalidRatingInput
	}
	today := input.AsOf.In(moscow).Format(time.DateOnly)
	result.Homework = &RatingHomeworkResult{Details: []RatingHomeworkDetail{}}
	result.Exams = &RatingExamResult{Details: []RatingExamDetail{}}
	result.Tests = &RatingTestsResult{Directions: map[string]float64{}, Details: []RatingTestDetail{}}
	seenHomework := map[int64]bool{}
	for _, hw := range input.Homework {
		if hw.AssignmentID < 1 || seenHomework[hw.AssignmentID] || hw.Result != nil && !finiteRating(*hw.Result) {
			return result, ErrInvalidRatingInput
		}
		seenHomework[hw.AssignmentID] = true
		if hw.TypeName != "ОВ" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, hw.Deadline); err != nil {
			return result, ErrInvalidRatingInput
		}
		if hw.Deadline < input.DateFrom || hw.Deadline > input.DateTo || hw.Deadline >= today {
			continue
		}
		score, status := 0.0, "Не сдано"
		if hw.Status == 1 && hw.Result != nil {
			score, status = *hw.Result, "Сдано"
		}
		result.Homework.Rating += score
		result.Homework.Details = append(result.Homework.Details, RatingHomeworkDetail{HomeworkID: hw.AssignmentID, Name: hw.Name, Deadline: hw.Deadline, Score: score, Status: status, DatePass: hw.DatePass})
	}
	if len(result.Homework.Details) > 0 {
		result.Homework.Rating /= float64(len(result.Homework.Details))
	}
	seenExams := map[string]bool{}
	examTotal := legacyRatingSum{}
	for _, exam := range input.Exams {
		if exam.ExamID == "" || seenExams[exam.ExamID] || exam.Score != nil && !finiteRating(*exam.Score) || !exam.MissingResult && exam.Score == nil {
			return result, ErrInvalidRatingInput
		}
		if _, err := time.Parse(time.DateOnly, exam.Date); err != nil || exam.Date < input.DateFrom || exam.Date > input.DateTo {
			return result, ErrInvalidRatingInput
		}
		seenExams[exam.ExamID] = true
		score, status := 0.0, "Не сдавал"
		if !exam.MissingResult {
			score, status = *exam.Score, "Сдан"
		}
		examTotal.add(score)
		result.Exams.Details = append(result.Exams.Details, RatingExamDetail{ExamID: exam.ExamID, Name: exam.Name, Date: exam.Date, Score: score, Status: status, Type: exam.Type, DirectionID: exam.DirectionID, AttemptNo: exam.AttemptNo, HasAppeal: exam.HasAppeal, MissingResult: exam.MissingResult})
	}
	result.Exams.Rating = examTotal.value()
	if len(result.Exams.Details) > 0 {
		result.Exams.Rating /= float64(len(result.Exams.Details))
	}
	type bucket struct {
		label   string
		total   legacyRatingSum
		count   int
		details []RatingTestDetail
	}
	buckets, indexes, seenTests := []bucket{}, map[string]int{}, map[string]bool{}
	fold := cases.Fold()
	for _, test := range input.Tests {
		identity := test.Source + ":" + test.TestID
		if test.TestID == "" || seenTests[identity] || test.Source != "internal" && test.Source != "external" || test.Score != nil && !finiteRating(*test.Score) {
			return result, ErrInvalidRatingInput
		}
		seenTests[identity] = true
		score := 0.0
		switch test.CalculationStatus {
		case "ready":
			if test.Score == nil {
				return result, ErrInvalidRatingInput
			}
			score = *test.Score
		case "missing":
			if test.Score != nil {
				return result, ErrInvalidRatingInput
			}
		case "queued", "processing", "retrying", "failed":
			return result, ErrRatingPending
		default:
			return result, ErrInvalidRatingInput
		}
		label := strings.TrimFunc(test.Direction, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
		if label == "" {
			label = "Неизвестное направление"
		}
		key := fold.String(label)
		i, exists := indexes[key]
		if !exists {
			i = len(buckets)
			indexes[key] = i
			buckets = append(buckets, bucket{label: label})
		}
		buckets[i].total.add(score)
		buckets[i].count++
		buckets[i].details = append(buckets[i].details, RatingTestDetail{TestID: test.TestID, Title: test.Title, Direction: buckets[i].label, Score: score, Source: test.Source, CalculationStatus: test.CalculationStatus})
	}
	directionTotal := legacyRatingSum{}
	for _, bucket := range buckets {
		average := bucket.total.value() / float64(bucket.count)
		result.Tests.Directions[bucket.label] = average
		directionTotal.add(average)
		result.Tests.Details = append(result.Tests.Details, bucket.details...)
	}
	result.Tests.Rating = directionTotal.value()
	if len(buckets) > 0 {
		result.Tests.Rating /= float64(len(buckets))
	}
	// Keep legacy's intermediate float precision as well as operation order.
	// Explicit conversions prevent Go from fusing the exam multiply/add and
	// omitting the rounding that Python performs for its separate components.
	// There is no redistribution, clamp or decimal rounding.
	homeworkComponent := float64((result.Homework.Rating * 25) / 100)
	examComponent := float64(result.Exams.Rating * 6)
	testComponent := float64((result.Tests.Rating * 45) / 100)
	final := homeworkComponent + examComponent + testComponent
	if !finiteRating(result.Homework.Rating) || !finiteRating(result.Exams.Rating) || !finiteRating(result.Tests.Rating) || !finiteRating(final) {
		return result, ErrInvalidRatingInput
	}
	result.FinalRating, result.Status = &final, "ready"
	return result, nil
}
