package calculate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPersonalRatingLegacyPythonGolden(t *testing.T) {
	var fixture struct {
		Runtime string `json:"runtime"`
		Cases   []struct {
			Name     string      `json:"name"`
			Input    RatingInput `json:"input"`
			Expected struct {
				Homework   float64            `json:"homework"`
				Exams      float64            `json:"exams"`
				Tests      float64            `json:"tests"`
				Directions map[string]float64 `json:"directions"`
				Final      float64            `json:"final_rating"`
			} `json:"expected"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("testdata/ratings_legacy_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Runtime != "CPython 3.12.14" || len(fixture.Cases) != 4 {
		t.Fatal("unexpected golden provenance or missing cases")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := CalculateRating(tc.Input)
			if err != nil {
				t.Fatal(err)
			}
			values := []struct {
				name string
				got  float64
				want float64
			}{
				{"homework", got.Homework.Rating, tc.Expected.Homework},
				{"exams", got.Exams.Rating, tc.Expected.Exams},
				{"tests", got.Tests.Rating, tc.Expected.Tests},
				{"final", *got.FinalRating, tc.Expected.Final},
			}
			for _, value := range values {
				if math.Float64bits(value.got) != math.Float64bits(value.want) {
					t.Errorf("%s differs from %s: %.17g, want %.17g", value.name, fixture.Runtime, value.got, value.want)
				}
			}
			if !reflect.DeepEqual(got.Tests.Directions, tc.Expected.Directions) {
				t.Errorf("direction means differ from actual legacy function: %v, want %v", got.Tests.Directions, tc.Expected.Directions)
			}
		})
	}
}

func TestPersonalRatingHomeworkMoscowMidnightAndPeriod(t *testing.T) {
	// Legacy CURDATE() excludes today's deadline; the immutable as_of fixes
	// that day even if processing finishes after Moscow midnight.
	for _, tc := range []struct {
		name       string
		asOf       time.Time
		wantIDs    []int64
		wantRating float64
	}{
		{"before-midnight", time.Date(2026, 10, 8, 20, 59, 59, 999999999, time.UTC), []int64{1}, 40},
		{"at-midnight", time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC), []int64{1, 2}, 60},
		{"after-midnight", time.Date(2026, 10, 8, 21, 0, 0, 1, time.UTC), []int64{1, 2}, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ratingTestInput()
			input.DateTo, input.AsOf = "2026-10-08", tc.asOf
			passed := "2026-10-31T12:00:00Z"
			input.Homework = []RatingHomeworkInput{
				{AssignmentID: 1, TypeName: "ОВ", Deadline: input.DateFrom, Status: 1, Result: ratingFloat(40), DatePass: &passed},
				{AssignmentID: 2, TypeName: "ОВ", Deadline: input.DateTo, Status: 1, Result: ratingFloat(80)},
				{AssignmentID: 3, TypeName: "ОВ", Deadline: "2026-09-30", Status: 1, Result: ratingFloat(100)},
				{AssignmentID: 4, TypeName: "ОВ", Deadline: "2026-10-09", Status: 1, Result: ratingFloat(100)},
				{AssignmentID: 5, TypeName: "other", Deadline: "2026-10-02", Status: 1, Result: ratingFloat(100)},
			}
			got, err := CalculateRating(input)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]int64, 0, len(got.Homework.Details))
			for _, detail := range got.Homework.Details {
				ids = append(ids, detail.HomeworkID)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) || got.Homework.Rating != tc.wantRating {
				t.Fatalf("inclusive period/strict Moscow day changed: ids=%v, H=%v", ids, got.Homework.Rating)
			}
			if got.Homework.Details[0].DatePass == nil || *got.Homework.Details[0].DatePass != passed {
				t.Fatal("date_pass was used as an eligibility cutoff or lost from details")
			}
		})
	}
}

func TestPersonalRatingFuturePeriodHomeworkExcludedButSelectedTestsRemain(t *testing.T) {
	input := ratingTestInput()
	input.DateFrom, input.DateTo = "2026-10-10", "2026-10-31"
	input.AsOf = time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC)
	input.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: "2026-10-10", Status: 1, Result: ratingFloat(100)}}
	// The Tests owner has selected these published tests by start date in
	// the period. Legacy includes future tests; absent sessions count as zero.
	input.Tests = []RatingTestInput{
		{TestID: "scheduled-1", Direction: "D", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(100)},
		{TestID: "scheduled-2", Direction: "D", Source: "internal", CalculationStatus: "missing"},
		{TestID: "scheduled-outside", Direction: "D", Source: "external", CalculationStatus: "ready", Score: ratingFloat(50)},
	}
	// Outside LMS exam dates also have no as_of eligibility cutoff in legacy.
	input.Exams = []RatingExamInput{{ExamID: "outside", Date: input.DateTo, Type: "outside_lms", Score: ratingFloat(4)}}
	got, err := CalculateRating(input)
	if err != nil || len(got.Homework.Details) != 0 || got.Tests.Rating != 50 || len(got.Tests.Details) != 3 || got.Exams.Rating != 4 || *got.FinalRating != 46.5 {
		t.Fatalf("future source-selected test/exam lost or future homework included: %+v, %v", got, err)
	}
	for _, state := range []string{"queued", "processing", "retrying", "failed"} {
		input.Tests[1].CalculationStatus = state
		got, err = CalculateRating(input)
		if !errors.Is(err, ErrRatingPending) || got.Status == "ready" || got.FinalRating != nil {
			t.Fatalf("source-selected future test %s published as a zero: %+v %v", state, got, err)
		}
	}
}

func TestPersonalRatingLeapDayAndCalendarYearBoundaries(t *testing.T) {
	for _, day := range []string{"2024-02-29", "2026-12-31"} {
		t.Run(day, func(t *testing.T) {
			input := ratingTestInput()
			parsed, _ := time.Parse(time.DateOnly, day)
			input.DateFrom, input.DateTo = day, day
			input.AsOf = parsed.Add(21 * time.Hour) // Moscow midnight on the next day.
			input.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: day, Status: 1, Result: ratingFloat(100)}}
			input.Exams = []RatingExamInput{{ExamID: "e", Date: day, Type: "outside_lms", Score: ratingFloat(5)}}
			got, err := CalculateRating(input)
			if err != nil || got.Homework.Rating != 100 || got.Exams.Rating != 5 || *got.FinalRating != 55 {
				t.Fatalf("valid inclusive single-day period failed: %+v %v", got, err)
			}
		})
	}
}

func TestPersonalRatingExternalNullZeroAndSourceIdentity(t *testing.T) {
	input := ratingTestInput()
	input.Tests = []RatingTestInput{
		{TestID: "7", Direction: "D", Source: "external", CalculationStatus: "missing", Score: nil},
		{TestID: "8", Direction: "D", Source: "external", CalculationStatus: "missing", Score: nil},
		{TestID: "9", Direction: "D", Source: "external", CalculationStatus: "ready", Score: ratingFloat(0)},
		{TestID: "7", Direction: "D", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(80)},
	}
	got, err := CalculateRating(input)
	if err != nil || got.Tests.Rating != 20 || len(got.Tests.Details) != 4 || *got.FinalRating != 9 {
		t.Fatalf("null, absent, explicit zero, or owner ID separation changed: %+v %v", got, err)
	}
	for i := 0; i < 3; i++ {
		if got.Tests.Details[i].Score != 0 || got.Tests.Details[i].Source != "external" {
			t.Fatal("external missing and completed-zero scores must both contribute zero")
		}
	}
	input.Tests = append(input.Tests, input.Tests[0])
	if got, err := CalculateRating(input); !errors.Is(err, ErrInvalidRatingInput) || got.FinalRating != nil {
		t.Fatal("duplicate identity within the same test owner was counted twice")
	}
}

func TestPersonalRatingTestDetailsDistinguishCompletedZeroFromMissing(t *testing.T) {
	input := ratingTestInput()
	for _, source := range []string{"internal", "external"} {
		input.Tests = append(input.Tests,
			RatingTestInput{TestID: "completed-zero", Direction: "D", Source: source, CalculationStatus: "ready", Score: ratingFloat(0)},
			RatingTestInput{TestID: "missing-result", Direction: "D", Source: source, CalculationStatus: "missing"},
		)
	}
	got, err := CalculateRating(input)
	if err != nil || got.Tests.Rating != 0 || *got.FinalRating != 0 || len(got.Tests.Details) != 4 {
		t.Fatalf("zero/missing detail status changed legacy rating: %+v %v", got, err)
	}
	for _, detail := range got.Tests.Details {
		want := "missing"
		if detail.TestID == "completed-zero" {
			want = "ready"
		}
		if detail.CalculationStatus != want || detail.Score != 0 {
			t.Fatalf("%s/%s lost result presence: %+v", detail.Source, detail.TestID, detail)
		}
		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil || wire["calculation_status"] != want {
			t.Fatalf("result presence absent from callback JSON: %s %v", raw, err)
		}
	}
	for _, source := range []string{"internal", "external"} {
		for _, score := range []float64{0, 99} {
			t.Run(fmt.Sprintf("%s-missing-with-score-%v", source, score), func(t *testing.T) {
				input := ratingTestInput()
				input.Tests = []RatingTestInput{{TestID: "t", Source: source, CalculationStatus: "missing", Score: ratingFloat(score)}}
				got, err := CalculateRating(input)
				if !errors.Is(err, ErrInvalidRatingInput) || got.FinalRating != nil || got.Status == "ready" {
					t.Fatalf("contradictory missing result was accepted: %+v %v", got, err)
				}
			})
		}
	}
}

func TestPersonalRatingHistoricalTestDetailsKeepStatusAbsent(t *testing.T) {
	// Durable outputs produced before calculation_status was introduced remain
	// valid, and replay does not invent whether their zero was missing/ready.
	old := []byte(`{"test_id":"t","title":"T","direction":"D","score":0,"source":"external"}`)
	var detail RatingTestDetail
	if err := json.Unmarshal(old, &detail); err != nil || detail.Score != 0 || detail.CalculationStatus != "" {
		t.Fatalf("historical detail is no longer readable: %+v %v", detail, err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["calculation_status"]; exists {
		t.Fatalf("historical replay invented a status: %s", raw)
	}
}

func TestPersonalRatingDirectionsHaveEqualWeightAndKeepFirstLabel(t *testing.T) {
	input := ratingTestInput()
	for i := 1; i <= 10; i++ {
		input.Tests = append(input.Tests, RatingTestInput{TestID: fmt.Sprint(i), Direction: "  Математика\u00a0", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(100)})
	}
	input.Tests = append(input.Tests,
		RatingTestInput{TestID: "11", Direction: "\x1cМАТЕМАТИКА\x1f", Source: "external", CalculationStatus: "ready", Score: ratingFloat(100)},
		RatingTestInput{TestID: "12", Direction: "Русский", Source: "external", CalculationStatus: "missing"},
	)
	got, err := CalculateRating(input)
	if err != nil || got.Tests.Rating != 50 || *got.FinalRating != 22.5 || len(got.Tests.Directions) != 2 || got.Tests.Directions["Математика"] != 100 || got.Tests.Directions["Русский"] != 0 {
		t.Fatalf("test count changed direction weight or labels diverged: %+v %v", got, err)
	}
	for _, detail := range got.Tests.Details[:11] {
		if detail.Direction != "Математика" {
			t.Fatal("a later spelling replaced the first normalized direction label")
		}
	}
}

func TestPersonalRatingEffectiveExamAttemptAppealAndIndependentCopy(t *testing.T) {
	input := ratingTestInput()
	attempt, direction := 2, int64(1001)
	// The Exam owner selected a completed second attempt, even though its
	// grade is lower, and already applied the latest appeal to its score.
	input.Exams = []RatingExamInput{
		{ExamID: "shared", Date: input.DateFrom, Type: "classic", DirectionID: &direction, AttemptNo: &attempt, HasAppeal: true, Score: ratingFloat(2)},
		{ExamID: "independent-copy", Date: input.DateTo, Type: "outside_lms", DirectionID: &direction, MissingResult: true},
		{ExamID: "completed-zero", Date: input.DateFrom, Type: "outside_lms", Score: ratingFloat(0)},
	}
	got, err := CalculateRating(input)
	if err != nil || got.Exams.Rating != 2.0/3.0 || *got.FinalRating != 4 || len(got.Exams.Details) != 3 {
		t.Fatalf("owner-selected grades or missing-exam denominator changed: %+v %v", got, err)
	}
	selected := got.Exams.Details[0]
	if selected.AttemptNo == nil || *selected.AttemptNo != 2 || !selected.HasAppeal || selected.DirectionID == nil || *selected.DirectionID != direction || selected.Score != 2 || selected.Status != "Сдан" {
		t.Fatal("selected attempt/appeal metadata was lost or regraded")
	}
	if got.Exams.Details[1].Status != "Не сдавал" || !got.Exams.Details[1].MissingResult || got.Exams.Details[2].Status != "Сдан" || got.Exams.Details[2].MissingResult {
		t.Fatal("missing copy and completed zero exam became indistinguishable")
	}
	input.Revision++
	input.Exams[0].Score = ratingFloat(4) // New immutable version after an appeal.
	updated, err := CalculateRating(input)
	if err != nil || updated.Revision != 2 || *updated.FinalRating != 8 || *got.FinalRating != 4 {
		t.Fatal("new appeal snapshot failed or changed a previously calculated version")
	}
	input.Exams = append(input.Exams, input.Exams[0])
	if _, err := CalculateRating(input); !errors.Is(err, ErrInvalidRatingInput) {
		t.Fatal("raw multiple attempts of one exam were counted as separate exams")
	}
}

func TestPersonalRatingSharedIDsAreIndependentAcrossDivisionsAndAccounts(t *testing.T) {
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			input := ratingTestInput()
			input.DivisionID, input.AccountID, input.Revision = int64(i+1), int64(i+101), int64(i+1)
			score := float64(i)
			input.Tests = []RatingTestInput{{TestID: "shared-test", Direction: "D", Source: "internal", CalculationStatus: "ready", Score: ratingFloat(score)}}
			input.Exams = []RatingExamInput{{ExamID: "shared-exam", Date: input.DateFrom, Type: "outside_lms", Score: ratingFloat(score)}}
			before, _ := json.Marshal(input)
			got, err := CalculateRating(input)
			after, _ := json.Marshal(input)
			want := score*6 + (score*45)/100
			if err != nil || got.DivisionID != input.DivisionID || got.AccountID != input.AccountID || got.Revision != input.Revision || got.InputHash != input.InputHash || *got.FinalRating != want || !reflect.DeepEqual(before, after) {
				t.Fatalf("shared IDs leaked state between scopes or input was mutated: %+v %v", got, err)
			}
		})
	}
}

func TestPersonalRatingInvalidPeriodDatesAndNumericSourcesNeverPublish(t *testing.T) {
	changes := map[string]func(*RatingInput){
		"reversed-period":   func(i *RatingInput) { i.DateFrom, i.DateTo = "2026-10-02", "2026-10-01" },
		"invalid-leap-day":  func(i *RatingInput) { i.DateFrom = "2026-02-29" },
		"non-calendar-date": func(i *RatingInput) { i.DateTo = "2026-10-31T23:59:59Z" },
		"missing-as-of":     func(i *RatingInput) { i.AsOf = time.Time{} },
		"as-of-not-utc":     func(i *RatingInput) { i.AsOf = i.AsOf.In(time.FixedZone("MSK", 3*3600)) },
		"invalid-homework-deadline": func(i *RatingInput) {
			i.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: "2026-02-29"}}
		},
		"exam-before-period": func(i *RatingInput) {
			i.Exams = []RatingExamInput{{ExamID: "e", Date: "2026-09-30", MissingResult: true}}
		},
		"exam-after-period": func(i *RatingInput) {
			i.Exams = []RatingExamInput{{ExamID: "e", Date: "2026-11-01", MissingResult: true}}
		},
		"ready-exam-without-score": func(i *RatingInput) {
			i.Exams = []RatingExamInput{{ExamID: "e", Date: i.DateFrom}}
		},
		"ready-external-without-score": func(i *RatingInput) {
			i.Tests = []RatingTestInput{{TestID: "t", Source: "external", CalculationStatus: "ready"}}
		},
	}
	for _, source := range []string{"homework", "exam", "internal", "external"} {
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			source, value := source, value
			changes[fmt.Sprintf("nonfinite-%s-%v", source, value)] = func(i *RatingInput) {
				switch source {
				case "homework":
					i.Homework = []RatingHomeworkInput{{AssignmentID: 1, TypeName: "ОВ", Deadline: i.DateFrom, Status: 1, Result: ratingFloat(value)}}
				case "exam":
					i.Exams = []RatingExamInput{{ExamID: "e", Date: i.DateFrom, Score: ratingFloat(value)}}
				default:
					i.Tests = []RatingTestInput{{TestID: "t", Source: source, CalculationStatus: "ready", Score: ratingFloat(value)}}
				}
			}
		}
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := ratingTestInput()
			change(&input)
			got, err := CalculateRating(input)
			if !errors.Is(err, ErrInvalidRatingInput) || got.FinalRating != nil || got.Status == "ready" {
				t.Fatalf("invalid source published usable rating: %+v %v", got, err)
			}
		})
	}
}

func TestPersonalRatingAggregateOverflowNeverPublishes(t *testing.T) {
	for _, source := range []string{"homework", "exams", "tests", "final"} {
		t.Run(source, func(t *testing.T) {
			input := ratingTestInput()
			for n := 1; n <= 2; n++ {
				switch source {
				case "homework":
					input.Homework = append(input.Homework, RatingHomeworkInput{AssignmentID: int64(n), TypeName: "ОВ", Deadline: input.DateFrom, Status: 1, Result: ratingFloat(math.MaxFloat64)})
				case "exams":
					input.Exams = append(input.Exams, RatingExamInput{ExamID: fmt.Sprint(n), Date: input.DateFrom, Score: ratingFloat(math.MaxFloat64)})
				case "tests":
					input.Tests = append(input.Tests, RatingTestInput{TestID: fmt.Sprint(n), Direction: "D", Source: "external", CalculationStatus: "ready", Score: ratingFloat(math.MaxFloat64)})
				case "final":
					if n == 1 {
						input.Exams = []RatingExamInput{{ExamID: "e", Date: input.DateFrom, Score: ratingFloat(math.MaxFloat64)}}
					}
				}
			}
			got, err := CalculateRating(input)
			if !errors.Is(err, ErrInvalidRatingInput) || got.FinalRating != nil || got.Status == "ready" {
				t.Fatalf("finite source overflow published unusable number: %+v %v", got, err)
			}
		})
	}
}
