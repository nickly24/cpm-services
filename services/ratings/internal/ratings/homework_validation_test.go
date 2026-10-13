package ratings

import (
	"math"
	"testing"
	"time"
)

func homeworkValidationFixture(j Job) HomeworkSnapshot {
	s := fixtureSnapshot(j).Homework
	hundred, zero := 100.0, 0.0
	boundary := time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC)
	s.Assignments = []Assignment{
		{ID: 1, TypeName: "ОВ", Deadline: j.DateFrom, Results: []HWResult{{AccountID: 71, Status: 1, Result: &zero}}},
		{ID: 2, TypeName: "ОВ", Deadline: "2026-10-08", Results: []HWResult{{AccountID: 71, Status: 1, Result: &hundred}}},
		{ID: 3, TypeName: "ОВ", Deadline: j.DateTo, Results: []HWResult{}},
		{ID: 4, TypeName: "Другое", Deadline: j.DateTo, Results: []HWResult{}},
	}
	s.NextTransitionAt = &boundary
	return s
}

func TestHomeworkSourceAcceptsInclusivePeriodZeroMissingAndFutureCalendarBoundary(t *testing.T) {
	j := fixtureJob()
	s := homeworkValidationFixture(j)
	if err := validateHomeworkSource(s, j); err != nil {
		t.Fatal("inclusive assignment period, present zero or future OV calendar boundary rejected", err)
	}
	// Legacy statuses and grades are inputs to the formula, including other types.
	value := 120.0
	s.Assignments[0].Results[0].Status = 9
	s.Assignments[0].Results[0].Result = &value
	if err := validateHomeworkSource(s, j); err != nil {
		t.Fatal("source validation invented non-legacy score/status restrictions", err)
	}
	// Explicitly no selected assignments is a connected empty source.
	s.Assignments = []Assignment{}
	s.NextTransitionAt = nil
	if err := validateHomeworkSource(s, j); err != nil {
		t.Fatal("empty homework source was rejected", err)
	}
}

func TestHomeworkSourceRejectsForeignDatesAmbiguousResultsAndInvalidTransition(t *testing.T) {
	j := fixtureJob()
	for _, tc := range []struct {
		name   string
		mutate func(*HomeworkSnapshot)
	}{
		{"zero assignment ID", func(s *HomeworkSnapshot) { s.Assignments[0].ID = 0 }},
		{"duplicate assignment ID", func(s *HomeworkSnapshot) { s.Assignments[1].ID = s.Assignments[0].ID }},
		{"null assignments", func(s *HomeworkSnapshot) { s.Assignments = nil }},
		{"deadline before inclusive period", func(s *HomeworkSnapshot) { s.Assignments[0].Deadline = "2026-09-30" }},
		{"deadline after inclusive period", func(s *HomeworkSnapshot) { s.Assignments[3].Deadline = "2026-11-01" }},
		{"invalid deadline on other legacy type", func(s *HomeworkSnapshot) { s.Assignments[3].Deadline = "2026-02-30" }},
		{"timestamp instead of calendar deadline", func(s *HomeworkSnapshot) { s.Assignments[0].Deadline += "T00:00:00Z" }},
		{"null result array", func(s *HomeworkSnapshot) { s.Assignments[0].Results = nil }},
		{"zero result account", func(s *HomeworkSnapshot) { s.Assignments[0].Results[0].AccountID = 0 }},
		{"duplicate result account", func(s *HomeworkSnapshot) {
			s.Assignments[0].Results = append(s.Assignments[0].Results, s.Assignments[0].Results[0])
		}},
		{"nonfinite source grade", func(s *HomeworkSnapshot) { v := math.Inf(1); s.Assignments[0].Results[0].Result = &v }},
		{"NaN source grade", func(s *HomeworkSnapshot) { v := math.NaN(); s.Assignments[0].Results[0].Result = &v }},
		{"missing required calendar transition", func(s *HomeworkSnapshot) { s.NextTransitionAt = nil }},
		{"calendar transition already reached", func(s *HomeworkSnapshot) { s.NextTransitionAt = &j.AsOf }},
		{"calendar transition is not Moscow midnight", func(s *HomeworkSnapshot) { v := s.NextTransitionAt.Add(time.Hour); s.NextTransitionAt = &v }},
		{"later OV transition skipped first one", func(s *HomeworkSnapshot) {
			v := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)
			s.NextTransitionAt = &v
		}},
		{"unrelated Moscow midnight", func(s *HomeworkSnapshot) { v := s.NextTransitionAt.Add(24 * time.Hour); s.NextTransitionAt = &v }},
		{"transition without OV assignment", func(s *HomeworkSnapshot) {
			for i := range s.Assignments {
				s.Assignments[i].TypeName = "Другое"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := homeworkValidationFixture(j)
			tc.mutate(&s)
			assertAPIError(t, validateHomeworkSource(s, j), 503, "dependency_unavailable")
		})
	}
}

func TestHomeworkSourceCalendarBoundaryTracksNextEligibleDeadlineAtMoscowMidnight(t *testing.T) {
	j := fixtureJob()
	j.AsOf = time.Date(2026, 10, 8, 20, 59, 59, 999999999, time.UTC)
	s := homeworkValidationFixture(j)
	if err := validateHomeworkSource(s, j); err != nil {
		t.Fatal("instant before Moscow midnight lost the current OV deadline", err)
	}
	boundary := *s.NextTransitionAt
	j.AsOf = boundary
	// The owner must now advance to the next selected OV deadline: date_to + 1.
	next := time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC)
	s.NextTransitionAt = &next
	if err := validateHomeworkSource(s, j); err != nil || transitioned(Snapshot{Homework: s}, boundary) {
		t.Fatal("exact midnight did not advance the owner's next transition", err)
	}
	s.NextTransitionAt = &boundary
	assertAPIError(t, validateHomeworkSource(s, j), 503, "dependency_unavailable")
	j.AsOf = next
	s.NextTransitionAt = nil
	if err := validateHomeworkSource(s, j); err != nil {
		t.Fatal("all period deadlines passed but a new calendar transition was required", err)
	}
}
