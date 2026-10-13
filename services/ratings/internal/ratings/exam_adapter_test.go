package ratings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func examAdapterFixture(j Job) examOwnerResponse {
	start, future := j.AsOf.Add(-time.Hour), j.AsOf.Add(time.Hour)
	return examOwnerResponse{
		SourceRevisions: map[string]int64{strconv.FormatInt(j.DivisionID, 10): 3},
		Snapshot: examOwnerSnapshot{Source: "exams", OrganizationID: j.OrganizationID, DivisionIDs: []int64{j.DivisionID}, DateFrom: j.DateFrom, DateTo: j.DateTo, AsOf: j.AsOf, SourceRevision: strings.Repeat("a", 64), NextExamStartAt: &future,
			Exams:     []examOwnerCatalogue{{ExamID: 41, DivisionID: j.DivisionID, DirectionID: 7, Kind: "classic", Date: start.In(Moscow).Format(time.DateOnly), StartAt: &start, Revision: 2, DivisionRevision: 3}, {ExamID: 42, DivisionID: j.DivisionID, DirectionID: 7, Kind: "outside_lms", Date: "2026-10-06", Revision: 1, DivisionRevision: 1}},
			Results:   []examOwnerResult{{ExamID: 41, DivisionID: j.DivisionID, AccountID: 71, Grade: 4, ResultID: 55, AttemptNo: 2, HasAppeal: true, ResultVersion: 3, HistoryGeneration: 1}, {ExamID: 42, DivisionID: j.DivisionID, AccountID: 71, Grade: 0, ResultID: 56, ResultVersion: 1}},
			Revisions: []examOwnerRevision{{ExamID: 41, DivisionID: j.DivisionID, ExamRevision: 2, DivisionRevision: 3}, {ExamID: 42, DivisionID: j.DivisionID, ExamRevision: 1, DivisionRevision: 1}},
		},
	}
}

func TestExamAdapterUsesRealGETContractAndPreservesEffectiveOwnerResults(t *testing.T) {
	j := fixtureJob()
	owner := examAdapterFixture(j)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/internal/exams/rating-snapshot" || r.Header.Get("X-Service-Key") != strings.Repeat("k", 32) || r.Header.Get("Authorization") != "" {
			t.Error("adapter did not use real authenticated owner contract")
		}
		q := r.URL.Query()
		if q.Get("organization_id") != "11" || q.Get("division_ids") != "17" || q.Get("date_from") != j.DateFrom || q.Get("date_to") != j.DateTo || q.Get("as_of") != j.AsOf.Format(time.RFC3339Nano) || len(q) != 5 {
			t.Error("scope/time was not fixed in query", q)
		}
		reply(w, 200, owner)
	}))
	defer server.Close()
	d := NewDependencies(Config{ExamsURL: server.URL, ServiceKey: strings.Repeat("k", 32)})
	output, err := d.collectExams(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if output.Source != "exams" || output.OrganizationID != j.OrganizationID || output.DivisionID != j.DivisionID || len(output.Exams) != 2 || len(output.InputHash) != 64 || output.NextTransitionAt == nil || !output.NextTransitionAt.Equal(*owner.Snapshot.NextExamStartAt) {
		t.Fatal("snapshot metadata was lost", output)
	}
	classic := output.Exams[0]
	if classic.ID != "41" || classic.Name != "Экзамен 41" || *classic.Results[0].Score != 4 || *classic.Results[0].AttemptNo != 2 || !classic.Results[0].HasAppeal {
		t.Fatal("owner's completed retake/appeal was not preserved", classic)
	}
	outside := output.Exams[1]
	if outside.Results[0].Score == nil || *outside.Results[0].Score != 0 || outside.Results[0].MissingResult || outside.Results[0].AttemptNo != nil {
		t.Fatal("present zero outside grade became missing")
	}
}

func TestExamAdapterHashStableAcrossAsOfAndSerializationOrderButChangesForGradesAndRevisions(t *testing.T) {
	j := fixtureJob()
	owner := examAdapterFixture(j)
	first, err := translateExamSnapshot(j, owner)
	if err != nil {
		t.Fatal(err)
	}
	// Query time itself is not a subject-data revision.
	later := j
	later.AsOf = later.AsOf.Add(time.Minute)
	owner.Snapshot.AsOf = later.AsOf
	owner.Snapshot.Exams[0], owner.Snapshot.Exams[1] = owner.Snapshot.Exams[1], owner.Snapshot.Exams[0]
	owner.Snapshot.Results[0], owner.Snapshot.Results[1] = owner.Snapshot.Results[1], owner.Snapshot.Results[0]
	owner.Snapshot.Revisions[0], owner.Snapshot.Revisions[1] = owner.Snapshot.Revisions[1], owner.Snapshot.Revisions[0]
	next, err := translateExamSnapshot(later, owner)
	if err != nil || next.InputHash != first.InputHash {
		t.Fatal("unchanged inputs acquired another fingerprint", err)
	}
	owner = examAdapterFixture(j)
	owner.Snapshot.Results[0].Grade = 3
	changed, err := translateExamSnapshot(j, owner)
	if err != nil || changed.InputHash == first.InputHash {
		t.Fatal("grade update failed to invalidate fingerprint")
	}
	owner = examAdapterFixture(j)
	owner.SourceRevisions["17"]++
	changed, err = translateExamSnapshot(j, owner)
	if err != nil || changed.InputHash == first.InputHash {
		t.Fatal("source revision update failed to invalidate fingerprint")
	}
}

func TestExamAdapterPreservesSharedExamIDWithIndependentDivisionResultsAndCopyID(t *testing.T) {
	j := fixtureJob()
	a := examAdapterFixture(j)
	left, err := translateExamSnapshot(j, a)
	if err != nil {
		t.Fatal(err)
	}
	other := j
	other.DivisionID = 18
	b := examAdapterFixture(other)
	b.Snapshot.Results[0].Grade = 2
	right, err := translateExamSnapshot(other, b)
	if err != nil {
		t.Fatal(err)
	}
	if left.Exams[0].ID != right.Exams[0].ID || *left.Exams[0].Results[0].Score == *right.Exams[0].Results[0].Score || left.DivisionID == right.DivisionID {
		t.Fatal("shared content leaked grades between divisions")
	}
	copyEntry := a.Snapshot.Exams[0]
	copyEntry.ExamID = 99
	a.Snapshot.Exams = append(a.Snapshot.Exams, copyEntry)
	a.Snapshot.Revisions = append(a.Snapshot.Revisions, examOwnerRevision{ExamID: 99, DivisionID: j.DivisionID, ExamRevision: 2, DivisionRevision: 3})
	out, err := translateExamSnapshot(j, a)
	if err != nil || len(out.Exams) != 3 || out.Exams[2].ID != "99" || len(out.Exams[2].Results) != 0 {
		t.Fatal("independent copy inherited original identity/results", err)
	}
}

func TestExamAdapterRejectsForeignAmbiguousOrUnselectedSourceData(t *testing.T) {
	j := fixtureJob()
	for _, tc := range []struct {
		name   string
		mutate func(*examOwnerResponse)
	}{
		{"foreign organization", func(o *examOwnerResponse) { o.Snapshot.OrganizationID++ }},
		{"extra division", func(o *examOwnerResponse) { o.Snapshot.DivisionIDs = append(o.Snapshot.DivisionIDs, 18) }},
		{"foreign result division", func(o *examOwnerResponse) { o.Snapshot.Results[0].DivisionID++ }},
		{"foreign catalogue division", func(o *examOwnerResponse) { o.Snapshot.Exams[0].DivisionID++ }},
		{"foreign source counter", func(o *examOwnerResponse) { o.SourceRevisions = map[string]int64{"18": 3} }},
		{"invalid checksum", func(o *examOwnerResponse) { o.Snapshot.SourceRevision = strings.Repeat("x", 64) }},
		{"other period", func(o *examOwnerResponse) { o.Snapshot.DateFrom = "2026-09-01" }},
		{"other asof", func(o *examOwnerResponse) { o.Snapshot.AsOf = o.Snapshot.AsOf.Add(time.Second) }},
		{"future included classic", func(o *examOwnerResponse) { v := j.AsOf.Add(time.Minute); o.Snapshot.Exams[0].StartAt = &v }},
		{"unselected date", func(o *examOwnerResponse) { o.Snapshot.Exams[1].Date = "2026-09-01" }},
		{"duplicate catalogue", func(o *examOwnerResponse) { o.Snapshot.Exams = append(o.Snapshot.Exams, o.Snapshot.Exams[0]) }},
		{"duplicate effective result", func(o *examOwnerResponse) { o.Snapshot.Results = append(o.Snapshot.Results, o.Snapshot.Results[0]) }},
		{"result without catalogue", func(o *examOwnerResponse) { o.Snapshot.Results[0].ExamID = 999 }},
		{"null results", func(o *examOwnerResponse) { o.Snapshot.Results = nil }},
		{"missing revision catalogue", func(o *examOwnerResponse) { o.Snapshot.Revisions = o.Snapshot.Revisions[:1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := examAdapterFixture(j)
			tc.mutate(&owner)
			if _, err := translateExamSnapshot(j, owner); err == nil {
				t.Fatal("untrusted owner data entered scoped rating")
			}
		})
	}
}

func TestExamAdapterReturnsExplicitEmptySnapshotAndDoesNotMutateOwner(t *testing.T) {
	j := fixtureJob()
	owner := examAdapterFixture(j)
	before, _ := json.Marshal(owner)
	_, err := translateExamSnapshot(j, owner)
	after, _ := json.Marshal(owner)
	if err != nil || string(before) != string(after) {
		t.Fatal("adapter mutated the immutable owner snapshot")
	}
	owner.Snapshot.Exams = []examOwnerCatalogue{}
	owner.Snapshot.Results = []examOwnerResult{}
	owner.Snapshot.Revisions = []examOwnerRevision{}
	owner.SourceRevisions["17"] = 0
	out, err := translateExamSnapshot(j, owner)
	if err != nil || out.Exams == nil || len(out.Exams) != 0 || len(out.InputHash) != 64 {
		t.Fatal("real empty owner response was confused with unavailable owner", err)
	}
}

func TestExamAdapterDoesNotTurnMalformedGradeIntoZero(t *testing.T) {
	for _, raw := range []string{
		`{"exam_id":41,"division_id":17,"account_id":71,"missing_result":false,"has_appeal":false}`,
		`{"exam_id":41,"division_id":17,"account_id":71,"grade":null,"missing_result":false,"has_appeal":false}`,
		`{"exam_id":41,"division_id":17,"account_id":71,"grade":0,"grade":4,"missing_result":false,"has_appeal":false}`,
		`{"exam_id":41,"division_id":17,"account_id":71,"grade":0,"missing_result":null,"has_appeal":false}`,
	} {
		var row examOwnerResult
		if err := json.Unmarshal([]byte(raw), &row); err == nil {
			t.Fatal("malformed grade became zero/missing")
		}
	}
	var row examOwnerResult
	if err := json.Unmarshal([]byte(`{"exam_id":41,"division_id":17,"account_id":71,"grade":0,"missing_result":false,"has_appeal":false}`), &row); err != nil || row.Grade != 0 || row.Missing {
		t.Fatal("genuine zero grade was rejected", err)
	}
}

func TestExamAdapterPreservesOptionalNamesAndInvalidatesCapturedInputs(t *testing.T) {
	j := fixtureJob()
	owner := examAdapterFixture(j)
	owner.Snapshot.Exams[0].Name = "Классический экзамен по анализу"
	first, err := translateExamSnapshot(j, owner)
	if err != nil || first.Exams[0].Name != owner.Snapshot.Exams[0].Name || first.Exams[1].Name != "Экзамен 42" {
		t.Fatal("owner label or older catalogue fallback was lost", first, err)
	}
	s := fixtureSnapshot(j)
	s.Exams = first
	before, err := personal(j, s, s.Core.Accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	owner.Snapshot.Exams[0].Name = "Новый заголовок экзамена"
	owner.Snapshot.Exams[0].Revision++
	owner.Snapshot.Revisions[0].ExamRevision++
	next, err := translateExamSnapshot(j, owner)
	if err != nil || next.InputHash == first.InputHash || first.Exams[0].Name != "Классический экзамен по анализу" {
		t.Fatal("rename reused the prior fingerprint or mutated the captured snapshot", next, err)
	}
	s.Exams = next
	after, err := personal(j, s, s.Core.Accounts[0])
	if err != nil || before.InputHash == after.InputHash || before.Exams[0].Name == after.Exams[0].Name {
		t.Fatal("personal calculation cache ignored the renamed exam", before, after, err)
	}
	owner.Snapshot.Exams[0].Name = " "
	legacy, err := translateExamSnapshot(j, owner)
	if err != nil || legacy.Exams[0].Name != "Экзамен 41" {
		t.Fatal("empty optional label lost the backward-compatible fallback", err)
	}
}

func TestExamAdapterAcceptsInclusiveMoscowDatesAndExactAsOfStart(t *testing.T) {
	j := fixtureJob()
	j.AsOf = time.Date(2026, 10, 31, 20, 59, 59, 999999999, time.UTC)
	for _, tc := range []struct {
		name  string
		start time.Time
		date  string
	}{
		{"first Moscow midnight", time.Date(2026, 9, 30, 21, 0, 0, 0, time.UTC), j.DateFrom},
		{"last Moscow instant equals as_of", j.AsOf, j.DateTo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := examAdapterFixture(j)
			owner.Snapshot.Exams[0].Date = tc.date
			owner.Snapshot.Exams[0].StartAt = &tc.start
			owner.Snapshot.Exams[1].Date = j.DateTo
			out, err := translateExamSnapshot(j, owner)
			if err != nil || len(out.Exams) != 2 || out.Exams[0].Date != tc.date || out.Exams[1].Date != j.DateTo {
				t.Fatal("inclusive owner period or the exact classic start boundary was rejected", out, err)
			}
		})
	}
	owner := examAdapterFixture(j)
	tooLate := j.AsOf.Add(time.Nanosecond)
	owner.Snapshot.Exams[0].Date = tooLate.In(Moscow).Format(time.DateOnly)
	owner.Snapshot.Exams[0].StartAt = &tooLate
	if _, err := translateExamSnapshot(j, owner); err == nil {
		t.Fatal("a future classic start entered the captured result")
	}
	owner = examAdapterFixture(j)
	firstMidnight := time.Date(2026, 9, 30, 21, 0, 0, 0, time.UTC)
	owner.Snapshot.Exams[0].StartAt = &firstMidnight
	owner.Snapshot.Exams[0].Date = "2026-09-30" // UTC day differs from the required Moscow day.
	if _, err := translateExamSnapshot(j, owner); err == nil {
		t.Fatal("a UTC calendar label displaced the inclusive Moscow period")
	}
}

func TestExamAdapterOwnerVersionsInvalidateSnapshotEvenWhenEffectiveGradeIsUnchanged(t *testing.T) {
	j := fixtureJob()
	baseline, err := translateExamSnapshot(j, examAdapterFixture(j))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*examOwnerResponse)
	}{
		{"same-grade retake selects different result", func(o *examOwnerResponse) { o.Snapshot.Results[0].ResultID++; o.Snapshot.Results[0].AttemptNo++ }},
		{"same-grade appeal increments version", func(o *examOwnerResponse) { o.Snapshot.Results[0].ResultVersion++ }},
		{"reset history generation", func(o *examOwnerResponse) { o.Snapshot.Results[0].HistoryGeneration++ }},
		{"appeal presence", func(o *examOwnerResponse) { o.Snapshot.Results[0].HasAppeal = false }},
		{"outside result revision", func(o *examOwnerResponse) { o.Snapshot.Results[1].ResultVersion++ }},
		{"global exam revision", func(o *examOwnerResponse) { o.Snapshot.Exams[0].Revision++; o.Snapshot.Revisions[0].ExamRevision++ }},
		{"division revision", func(o *examOwnerResponse) {
			o.Snapshot.Exams[0].DivisionRevision++
			o.Snapshot.Revisions[0].DivisionRevision++
		}},
		{"unselected exam revision", func(o *examOwnerResponse) {
			o.Snapshot.Revisions = append(o.Snapshot.Revisions, examOwnerRevision{ExamID: 999, DivisionID: j.DivisionID, ExamRevision: 1, DivisionRevision: 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := examAdapterFixture(j)
			tc.mutate(&owner)
			next, err := translateExamSnapshot(j, owner)
			if err != nil || next.InputHash == baseline.InputHash || *next.Exams[0].Results[0].Score != *baseline.Exams[0].Results[0].Score || *next.Exams[1].Results[0].Score != 0 {
				t.Fatal("owner revision was lost because the effective grade stayed equal", next, err)
			}
			s := fixtureSnapshot(j)
			s.Exams = baseline
			oldFingerprint := fingerprint(s)
			s.Exams = next
			if fingerprint(s) == oldFingerprint {
				t.Fatal("changed retake/appeal owner version did not fence publication")
			}
		})
	}
}

func TestExamAdapterRejectsInvalidGradesResultVersionsAndInconsistentCatalogue(t *testing.T) {
	j := fixtureJob()
	for _, tc := range []struct {
		name   string
		mutate func(*examOwnerResponse)
	}{
		{"negative grade", func(o *examOwnerResponse) { o.Snapshot.Results[0].Grade = -1 }},
		{"grade above owner scale", func(o *examOwnerResponse) { o.Snapshot.Results[0].Grade = 6 }},
		{"missing row has invalid grade", func(o *examOwnerResponse) { o.Snapshot.Results[0].Missing = true; o.Snapshot.Results[0].Grade = 6 }},
		{"completed result without identity", func(o *examOwnerResponse) { o.Snapshot.Results[0].ResultID = 0 }},
		{"completed result without version", func(o *examOwnerResponse) { o.Snapshot.Results[0].ResultVersion = 0 }},
		{"outside result without version", func(o *examOwnerResponse) { o.Snapshot.Results[1].ResultVersion = 0 }},
		{"classic completed result without attempt", func(o *examOwnerResponse) { o.Snapshot.Results[0].AttemptNo = 0 }},
		{"classic completed result without history", func(o *examOwnerResponse) { o.Snapshot.Results[0].HistoryGeneration = 0 }},
		{"different catalogue exam revision", func(o *examOwnerResponse) { o.Snapshot.Exams[0].Revision++ }},
		{"different catalogue division revision", func(o *examOwnerResponse) { o.Snapshot.Revisions[0].DivisionRevision++ }},
		{"duplicate revision row", func(o *examOwnerResponse) {
			o.Snapshot.Revisions = append(o.Snapshot.Revisions, o.Snapshot.Revisions[0])
		}},
		{"foreign revision division", func(o *examOwnerResponse) { o.Snapshot.Revisions[0].DivisionID++ }},
		{"transition already reached", func(o *examOwnerResponse) { o.Snapshot.NextExamStartAt = &j.AsOf }},
		{"transition before as_of", func(o *examOwnerResponse) { past := j.AsOf.Add(-time.Nanosecond); o.Snapshot.NextExamStartAt = &past }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := examAdapterFixture(j)
			tc.mutate(&owner)
			_, err := translateExamSnapshot(j, owner)
			assertAPIError(t, err, 503, "dependency_unavailable")
		})
	}
}

func TestExamAdapterKeepsAbsentResultsDistinctFromPresentZeroAndDivisionLocalRevision(t *testing.T) {
	j := fixtureJob()
	owner := examAdapterFixture(j)
	owner.Snapshot.Results[0].Missing = true
	owner.Snapshot.Results[0].Grade = 0
	owner.Snapshot.Results[0].ResultID = 0
	owner.Snapshot.Results[0].ResultVersion = 0
	owner.Snapshot.Results[0].AttemptNo = 0
	owner.Snapshot.Results[0].HistoryGeneration = 0
	left, err := translateExamSnapshot(j, owner)
	if err != nil || left.Exams[0].Results[0].Score != nil || !left.Exams[0].Results[0].MissingResult || left.Exams[1].Results[0].Score == nil || *left.Exams[1].Results[0].Score != 0 || left.SourceRevision != 3 {
		t.Fatal("unknown classic result, present outside zero or division counter was lost", left, err)
	}
	other := j
	other.DivisionID = 18
	otherOwner := examAdapterFixture(other)
	otherOwner.SourceRevisions["18"] = 12
	right, err := translateExamSnapshot(other, otherOwner)
	if err != nil || right.SourceRevision != 12 || right.Exams[0].ID != left.Exams[0].ID || right.InputHash == left.InputHash {
		t.Fatal("shared exam reused another division's owner version", right, err)
	}
	owner.SourceRevisions["17"]++
	changedLeft, err := translateExamSnapshot(j, owner)
	unchangedRight, otherErr := translateExamSnapshot(other, otherOwner)
	if err != nil || otherErr != nil || changedLeft.InputHash == left.InputHash || unchangedRight.InputHash != right.InputHash {
		t.Fatal("an update to a shared exam scope invalidated an unrelated division", err, otherErr)
	}
}
