package exams

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func snapshotDates(t *testing.T, exams []*Exam, org int64, divisions []int64, from, to string, asOf time.Time) RatingSnapshotDTO {
	t.Helper()
	value, err := RatingSnapshot(exams, org, divisions, from, to, asOf)
	if err != nil {
		t.Fatal(err)
	}
	return value.(RatingSnapshotDTO)
}

func TestExamRatingPeriodAsOfAndCurrentResult(t *testing.T) {
	e, _, p := auxFixture()
	e.Divisions[2].Config.StartAt = dateTime(t, "2026-10-08T21:00:00Z")
	asOf := *dateTime(t, "2026-10-09T10:00:00Z")
	futureCompletion := dateTime(t, "2026-10-10T12:00:00Z")
	student := p.Members[11]
	d := e.Divisions[2]
	d.Attempts = []Attempt{
		{ID: 30, Student: student, AttemptNo: 1, Status: "completed", CalculatedGrade: 5, ResultVersion: 1, CompletedAt: futureCompletion},
		{ID: 31, Student: student, AttemptNo: 2, Status: "in_progress", CalculatedGrade: 0, ResultVersion: 1},
		{ID: 32, Student: p.Members[12], AttemptNo: 1, Status: "completed", CalculatedGrade: 0, ResultVersion: 1},
	}
	e.Divisions[3] = &Division{ID: 3, Revision: 1, Config: Config{StartAt: e.Divisions[2].Config.StartAt}}
	future, _, _ := auxFixture()
	future.ID = 2
	future.Divisions[2].Config.StartAt = dateTime(t, "2026-10-09T10:00:00.000000001Z")
	future.Divisions[3] = &Division{ID: 3, Config: Config{StartAt: dateTime(t, "2026-10-10T10:00:00Z")}}
	equal, _, _ := auxFixture()
	equal.ID = 3
	equal.Divisions[2].Config.StartAt = &asOf
	end, _, _ := auxFixture()
	end.ID = 4
	end.Divisions[2].Config.StartAt = dateTime(t, "2026-10-09T21:00:00Z")
	outside, _, _ := auxFixture()
	outside.ID, outside.Kind = 5, "outside_lms"
	outside.Divisions[2].Date = "2026-10-09"
	outside.Divisions[2].Outside = []OutsideResult{{ID: 40, Student: student, Points: json.Number("2"), Grade: 2, Revision: 1}}
	foreign, _, _ := auxFixture()
	foreign.ID, foreign.OrganizationID = 6, 99
	foreign.Divisions[2].Config.StartAt = &asOf
	foreign.Divisions[2].Attempts = d.Attempts
	exams := []*Exam{nil, foreign, outside, end, equal, future, e}
	snapshot := snapshotDates(t, exams, 1, []int64{3, 2}, "2026-10-09", "2026-10-09", asOf)
	if len(snapshot.Exams) != 4 || len(snapshot.Results) != 3 || snapshot.NextExamStartAt == nil || !snapshot.NextExamStartAt.Equal(*future.Divisions[2].Config.StartAt) {
		t.Fatal(snapshot)
	}
	if snapshot.Results[0].ResultID != 30 || snapshot.Results[0].Grade != 5 || snapshot.Results[1].Grade != 0 || snapshot.Results[1].Missing {
		t.Fatal("pending retake or explicit zero lost", snapshot.Results)
	}
	for _, row := range snapshot.Results {
		if row.DivisionID != 2 || row.ExamID == 6 {
			t.Fatal("rating result crossed scope", row)
		}
	}
	// Even future-dated completions and appeals are the current persisted state.
	// Legacy as_of is a catalogue cutoff, not a temporal history query.
	d.Attempts[1].Status, d.Attempts[1].CompletedAt = "completed", futureCompletion
	d.Attempts[1].CalculatedGrade, d.Attempts[1].ResultVersion = 1, 3
	d.Attempts[1].Appeals = []Appeal{{ID: 41, Grade: 4, At: asOf}, {ID: 42, Grade: 2, At: *futureCompletion}}
	next := snapshotDates(t, exams, 1, []int64{2, 3}, "2026-10-09", "2026-10-09", asOf)
	if next.Results[0].ResultID != 31 || next.Results[0].Grade != 2 || next.Results[0].AttemptNo != 2 || !next.Results[0].HasAppeal {
		t.Fatal("worse completed retake/latest appeal lost", next.Results)
	}
	beforeStart := snapshotDates(t, []*Exam{outside}, 1, []int64{2}, "2026-10-09", "2026-10-09", *dateTime(t, "2026-10-01T00:00:00Z"))
	if len(beforeStart.Exams) != 1 || len(beforeStart.Results) != 1 {
		t.Fatal("outside date incorrectly cut off at as_of", beforeStart)
	}
}

func TestExamRatingStrictEmptyPeriodAndAliases(t *testing.T) {
	e, a, p := auxFixture()
	for _, test := range []struct{ from, to, code string }{
		{"", "2026-10-09", "invalid_date_from"},
		{"2026-10-09", "", "invalid_date_to"},
		{"0000-01-01", "2026-10-09", "invalid_date_from"},
		{"2026-10-09", "2026-02-29", "invalid_date_to"},
		{"2026-10-10", "2026-10-09", "invalid_date_to"},
	} {
		_, err := RatingSnapshot(nil, 1, []int64{2}, test.from, test.to, p.Now)
		auxError(t, err, test.code)
	}
	_, err := RatingSnapshot(nil, 1, []int64{2, -1}, "2026-10-09", "2026-10-09", p.Now)
	auxError(t, err, "invalid_division_id")
	_, err = RatingSnapshot(nil, 1, nil, "2026-10-09", "2026-10-09", p.Now)
	auxError(t, err, "invalid_rating_scope")
	p.Query = map[string]string{"from_date": "2026-10-09", "to_date": "2026-10-09"}
	if _, err = AuxQuery(e, a, "rating-snapshot", p); err != nil {
		t.Fatal(err)
	}
	p.Query["date_from"] = "2026-10-08"
	_, err = AuxQuery(e, a, "rating-snapshot", p)
	auxError(t, err, "conflicting_date_from")
	p.Query = nil
	_, err = AuxQuery(e, Actor{AccountID: 11, OrganizationID: 1, Role: "student", DivisionIDs: []int64{2}}, "rating-snapshot", p)
	auxError(t, err, "forbidden")
	_, err = AuxQuery(e, Actor{AccountID: 10, OrganizationID: 1, Role: "staff_admin", DivisionIDs: []int64{3}, Permissions: map[string]Permission{"exams": {View: true}}}, "rating-snapshot", p)
	auxError(t, err, "forbidden")
}

func TestExamRatingRevisionsIgnoreUnrelatedDivisionOperations(t *testing.T) {
	f := fixtureDomain(t, "round_up")
	f.start(t)
	f.e.Divisions[2].Config.StartAt = f.e.Divisions[1].Config.StartAt
	// Emulate the HTTP producer contract: the aggregate lock is loaded by the
	// codec first; only changed+rating_changed increments a semantic version.
	apply := func(result map[string]any) {
		if result["changed"] == true {
			f.e.Revision++
			if result["rating_changed"] == true {
				f.e.Divisions[f.p.DivisionID].RatingRevision++
			}
		}
	}
	snapshot := func(id int64) RatingSnapshotDTO {
		return snapshotDates(t, []*Exam{f.e}, 10, []int64{id}, "2026-10-08", "2026-10-08", f.p.Now)
	}
	aBefore, bBefore := snapshot(1), snapshot(2)
	apply(f.vote(t, f.x1, 1))
	if snapshot(1).SourceRevision != aBefore.SourceRevision || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("pending vote invalidated rating")
	}
	apply(f.vote(t, f.x2, 1))
	f.next(t)
	apply(f.vote(t, f.x1, 1))
	apply(f.vote(t, f.x2, 1))
	aCompleted := snapshot(1)
	if aCompleted.SourceRevision == aBefore.SourceRevision || len(aCompleted.Results) != 1 || aCompleted.Results[0].Grade != 5 || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("completion invalidation crossed division")
	}
	f.p.StudentID = 11
	apply(f.cmd(t, f.admin, "privilege-put", map[string]any{"expected_revision": 0, "replacement_limit": 2}))
	if snapshot(1).SourceRevision != aCompleted.SourceRevision || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("privilege invalidated rating")
	}
	// Bank changes do not alter immutable completed attempt grading.
	question := f.e.Questions[0]
	f.p.QuestionID = question.ID
	apply(f.cmd(t, f.admin, "question-update", map[string]any{"expected_revision": question.Revision, "question_text": "edited content"}))
	if snapshot(1).SourceRevision != aCompleted.SourceRevision || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("immutable bank edit invalidated rating")
	}
	apply(f.cmd(t, f.admin, "appeal", map[string]any{"expected_result_version": f.attempt().ResultVersion, "grade": 3}))
	aAppealed := snapshot(1)
	if aAppealed.SourceRevision == aCompleted.SourceRevision || aAppealed.Results[0].Grade != 3 || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("current appeal invalidation crossed division")
	}
	// A completed second attempt makes an appeal of the first historical.
	firstID := f.p.AttemptID
	first := *f.attempt()
	second := first
	second.ID, second.AttemptNo, second.CalculatedGrade = f.e.Alloc(), 2, 1
	second.Appeals = nil
	f.e.Divisions[1].Attempts = append(f.e.Divisions[1].Attempts, second)
	f.e.Divisions[1].RatingRevision++
	aRetake := snapshot(1)
	f.p.AttemptID = firstID
	apply(f.cmd(t, f.admin, "appeal", map[string]any{"expected_result_version": f.attempt().ResultVersion, "grade": 4}))
	if !reflect.DeepEqual(snapshot(1), aRetake) || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("historical appeal changed effective input")
	}
	// Current scheduling belongs to one division; name/direction belongs to all.
	f.e.Divisions[1].Config.StartAt = dateTime(t, "2026-10-09T00:00:00Z")
	f.e.Divisions[1].RatingRevision++
	if snapshot(1).SourceRevision == aRetake.SourceRevision || snapshot(2).SourceRevision != bBefore.SourceRevision {
		t.Fatal("date revision crossed division")
	}
	f.e.Name = "New name"
	f.e.RatingRevision++
	if snapshot(2).SourceRevision == bBefore.SourceRevision {
		t.Fatal("shared metadata failed to invalidate rating")
	}
}

func TestExamRatingRevisionLegacyCodecInitialization(t *testing.T) {
	e, _, p := auxFixture()
	e.ContentRevision, e.Divisions[2].Revision = 7, 9
	e.Divisions[2].Config.StartAt = dateTime(t, "2026-10-08T00:00:00Z")
	before := snapshotDates(t, []*Exam{e}, 1, []int64{2}, "2026-10-08", "2026-10-08", p.Now)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeExam(raw, e.NextID)
	if err != nil || loaded.RatingRevision != 7 || loaded.Divisions[2].RatingRevision != 9 {
		t.Fatal(loaded, err)
	}
	if !reflect.DeepEqual(snapshotDates(t, []*Exam{loaded}, 1, []int64{2}, "2026-10-08", "2026-10-08", p.Now), before) {
		t.Fatal("legacy initialization changed existing snapshot inputs")
	}
	loaded.Revision++
	loaded.ContentRevision++
	loaded.Divisions[2].Revision++
	raw, err = encodeExam(loaded)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = decodeExam(raw, loaded.NextID)
	if err != nil || snapshotDates(t, []*Exam{loaded}, 1, []int64{2}, "2026-10-08", "2026-10-08", p.Now).SourceRevision != before.SourceRevision {
		t.Fatal("semantic versions reinitialized after operational change", err)
	}
}
