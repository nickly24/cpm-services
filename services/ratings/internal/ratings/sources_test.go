package ratings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func sourceFixtureServer(t *testing.T, makeSnapshot func(SnapshotRequest) Snapshot, mutate func(string, *Snapshot)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		examRequest := r.URL.Path == "/internal/exams/rating-snapshot"
		if (!examRequest && r.Method != "POST") || (examRequest && r.Method != "GET") || r.Header.Get("X-Service-Key") != strings.Repeat("k", 32) || r.Header.Get("Authorization") != "" {
			t.Error("source request did not use service-only authentication")
			w.WriteHeader(401)
			return
		}
		var request SnapshotRequest
		var decodeError error
		if examRequest {
			request.OrganizationID, decodeError = strconv.ParseInt(r.URL.Query().Get("organization_id"), 10, 64)
			request.DivisionID, _ = strconv.ParseInt(r.URL.Query().Get("division_ids"), 10, 64)
			request.DateFrom, request.DateTo, request.AsOf = r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to"), r.URL.Query().Get("as_of")
		} else {
			decodeError = json.NewDecoder(r.Body).Decode(&request)
		}
		if decodeError != nil || request.OrganizationID < 1 || request.DivisionID < 1 || request.AsOf == "" {
			t.Error("source request omitted immutable scope/time")
			w.WriteHeader(400)
			return
		}
		s := makeSnapshot(request)
		if mutate != nil {
			mutate(r.URL.Path, &s)
		}
		var payload any
		switch r.URL.Path {
		case "/internal/core/ratings/snapshot":
			payload = s.Core
		case "/internal/homework/ratings/snapshot":
			payload = s.Homework
		case "/internal/tests/ratings/snapshot":
			payload = s.Tests
		case "/internal/exams/rating-snapshot":
			payload = examSourceFixture(sourceJob(request), s.Exams)
		default:
			t.Error("unknown owner snapshot endpoint", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		reply(w, 200, payload)
	}))
}

func examSourceFixture(j Job, s ExamsSnapshot) examOwnerResponse {
	owner := examOwnerResponse{SourceRevisions: map[string]int64{strconv.FormatInt(j.DivisionID, 10): 1}, Snapshot: examOwnerSnapshot{
		Source: s.Source, OrganizationID: s.OrganizationID, DivisionIDs: []int64{s.DivisionID}, DateFrom: j.DateFrom, DateTo: j.DateTo, AsOf: j.AsOf, SourceRevision: s.InputHash,
		Exams: []examOwnerCatalogue{}, Results: []examOwnerResult{}, Revisions: []examOwnerRevision{}, NextExamStartAt: s.NextTransitionAt,
	}}
	for _, e := range s.Exams {
		id, _ := strconv.ParseInt(e.ID, 10, 64)
		direction := int64(1)
		if e.DirectionID != nil {
			direction = *e.DirectionID
		}
		entry := examOwnerCatalogue{ExamID: id, DivisionID: s.DivisionID, DirectionID: direction, Kind: e.Type, Date: e.Date, Revision: 1, DivisionRevision: 1}
		if e.Type == "classic" {
			start, _ := time.ParseInLocation(time.DateOnly, e.Date, Moscow)
			entry.StartAt = &start
		}
		owner.Snapshot.Exams = append(owner.Snapshot.Exams, entry)
		owner.Snapshot.Revisions = append(owner.Snapshot.Revisions, examOwnerRevision{ExamID: id, DivisionID: s.DivisionID, ExamRevision: 1, DivisionRevision: 1})
		for _, result := range e.Results {
			grade, attempt := 0, 0
			if result.Score != nil {
				grade = int(*result.Score)
			}
			if result.AttemptNo != nil {
				attempt = *result.AttemptNo
			}
			generation := int64(0)
			if e.Type == "classic" && !result.MissingResult {
				generation = 1
				if attempt == 0 {
					attempt = 1
				}
			}
			owner.Snapshot.Results = append(owner.Snapshot.Results, examOwnerResult{ExamID: id, DivisionID: s.DivisionID, AccountID: result.AccountID, Grade: grade, AttemptNo: attempt, Missing: result.MissingResult, HasAppeal: result.HasAppeal, ResultID: id*1000 + result.AccountID, ResultVersion: 1, HistoryGeneration: generation})
		}
	}
	return owner
}

func sourceDependencies(endpoint, mode, exams string) *Dependencies {
	return NewDependencies(Config{CoreURL: endpoint, HomeworkURL: endpoint, TestsURL: endpoint, ExamsURL: exams, ExamsMode: mode, ServiceKey: strings.Repeat("k", 32)})
}

func sourceJob(request SnapshotRequest) Job {
	j := fixtureJob()
	j.OrganizationID, j.DivisionID, j.DateFrom, j.DateTo = request.OrganizationID, request.DivisionID, request.DateFrom, request.DateTo
	j.AsOf, _ = time.Parse(time.RFC3339Nano, request.AsOf)
	return j
}

func TestCollectRejectsForeignScopeWrongSourceMalformedHashAndDuplicateAccounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(string, *Snapshot)
	}{
		{"foreign organization", func(_ string, s *Snapshot) { s.Core.OrganizationID++ }},
		{"foreign homework division", func(_ string, s *Snapshot) { s.Homework.DivisionID++ }},
		{"foreign tests division", func(_ string, s *Snapshot) { s.Tests.DivisionID++ }},
		{"foreign exams division", func(_ string, s *Snapshot) { s.Exams.DivisionID++ }},
		{"wrong source owner", func(_ string, s *Snapshot) { s.Tests.Source = "homework" }},
		{"non-hex source hash", func(_ string, s *Snapshot) { s.Homework.InputHash = strings.Repeat("x", 64) }},
		{"short source hash", func(_ string, s *Snapshot) { s.Core.InputHash = "small" }},
		{"duplicate account", func(_ string, s *Snapshot) { s.Core.Accounts = append(s.Core.Accounts, s.Core.Accounts[0]) }},
		{"invalid account", func(_ string, s *Snapshot) { s.Core.Accounts[0].AccountID = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot { return fixtureSnapshot(sourceJob(request)) }, tc.mutate)
			defer server.Close()
			_, e := sourceDependencies(server.URL, "required", server.URL).Collect(context.Background(), fixtureJob())
			if e == nil {
				t.Fatal("untrusted owner response entered the rating snapshot")
			}
		})
	}
}

func TestCollectRequiredExamsAndExplicitDevEmptyMode(t *testing.T) {
	var examCalls atomic.Int32
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot { return fixtureSnapshot(sourceJob(request)) }, func(path string, _ *Snapshot) {
		if path == "/internal/exams/rating-snapshot" {
			examCalls.Add(1)
		}
	})
	defer server.Close()
	_, e := sourceDependencies(server.URL, "required", "").Collect(context.Background(), fixtureJob())
	assertAPIError(t, e, 409, "exams_not_connected")
	s, e := sourceDependencies(server.URL, "dev_empty", server.URL).Collect(context.Background(), fixtureJob())
	if e != nil || examCalls.Load() != 0 || s.ExamsMode != "dev_empty" || s.Exams.Source != "exams" || len(s.Exams.Exams) != 0 || s.Exams.Exams == nil || len(s.Exams.InputHash) != 64 {
		t.Fatal("explicit dev empty exams was not labelled or consulted a real owner", s, e, examCalls.Load())
	}
	s, e = sourceDependencies(server.URL, "required", server.URL).Collect(context.Background(), fixtureJob())
	if e != nil || examCalls.Load() != 1 || s.ExamsMode != "required" {
		t.Fatal("required mode silently substituted empty exams", e, examCalls.Load())
	}
}

func TestExamAdapterSupportsSharedExamAndIndependentCopyPerDivision(t *testing.T) {
	sharedID, copiedID := "41", "99"
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		j := sourceJob(request)
		s := fixtureSnapshot(j)
		score := 3.0
		if request.DivisionID == 18 {
			score = 4
		}
		s.Exams.Exams = []Exam{{ID: sharedID, Name: "Shared exam", Type: "classic", Date: "2026-10-02", Results: []ExamResult{{AccountID: 71, Score: &score}}}}
		if request.DivisionID == 18 {
			copiedScore := 2.0
			s.Exams.Exams = append(s.Exams.Exams, Exam{ID: copiedID, Name: "Independent copy", Type: "classic", Date: "2026-10-03", Results: []ExamResult{{AccountID: 71, Score: &copiedScore}}})
		}
		return s
	}, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "required", server.URL)
	j := fixtureJob()
	s1, e := deps.Collect(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	p1, e := personal(j, s1, s1.Core.Accounts[0])
	if e != nil {
		t.Fatal(e)
	}
	j.DivisionID = 18
	s2, e := deps.Collect(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	p2, e := personal(j, s2, s2.Core.Accounts[0])
	if e != nil || len(p1.Exams) != 1 || len(p2.Exams) != 2 || p1.Exams[0].ID != sharedID || p2.Exams[0].ID != sharedID || p2.Exams[1].ID != copiedID || *p1.Exams[0].Score != 3 || *p2.Exams[0].Score != 4 || *p2.Exams[1].Score != 2 || p1.InputHash == p2.InputHash {
		t.Fatal("shared exam identity or division-local result was collapsed", p1.Exams, p2.Exams, e)
	}
}

func TestDependencyCallUsesServiceKeyAndNeverFollowsRedirects(t *testing.T) {
	var leakedCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedCalls.Add(1)
		reply(w, 200, map[string]bool{"unexpected": true})
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, 307)
	}))
	defer redirect.Close()
	deps := sourceDependencies(redirect.URL, "required", "")
	var result map[string]any
	e := deps.Call(context.Background(), redirect.URL, "POST", "/source", map[string]int{"division_id": 17}, &result)
	if e == nil || leakedCalls.Load() != 0 {
		t.Fatal("dependency redirect forwarded service credentials", e, leakedCalls.Load())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != deps.Config.ServiceKey || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("owner call did not use the internal service credential")
		}
		var input map[string]int
		if json.NewDecoder(r.Body).Decode(&input) != nil || !reflect.DeepEqual(input, map[string]int{"division_id": 17}) {
			t.Error("dependency body changed")
		}
		reply(w, 200, map[string]bool{"accepted": true})
	}))
	defer server.Close()
	e = deps.Call(context.Background(), server.URL, "POST", "/source", map[string]int{"division_id": 17}, &result)
	if e != nil || result["accepted"] != true {
		t.Fatal("valid internal dependency response was lost", e)
	}
}

func TestDependencyFailuresCloseAccessAndRespectSourceConflict(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want int
	}{
		{404, `{"status":false,"error":"not_found"}`, 404},
		{409, `{"status":false,"error":"rating_source_changed"}`, 409},
		{503, `<html>gateway unavailable</html>`, 503},
		{200, `{"status":false,"data":{}}`, 503},
		{200, `{"status":true,"data":"wrong-shape"}`, 503},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(tc.body))
		}))
		var data CoreSnapshot
		e := sourceDependencies(server.URL, "required", "").Call(context.Background(), server.URL, "POST", "/source", struct{}{}, &data)
		server.Close()
		assertAPIError(t, e, tc.want, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := sourceDependencies("http://127.0.0.1:1", "required", "").Call(ctx, "http://127.0.0.1:1", "POST", "/source", struct{}{}, nil)
	if !errors.Is(e, unavailable) {
		t.Fatal("cancelled source request did not fail closed", e)
	}
}

func TestAuthIntrospectionUsesLiveBearerAndNormalizesAdminFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/auth/introspect" || r.Header.Get("Authorization") != "Bearer synthetic-token" || r.Header.Get("X-Service-Key") != "" {
			t.Error("live introspection used the wrong auth contract")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": Actor{AccountID: 71, OrganizationID: 11, Role: "student", OrganizationAdmin: true}})
	}))
	defer server.Close()
	deps := NewDependencies(Config{AuthURL: server.URL, ServiceKey: strings.Repeat("k", 32)})
	r := httptest.NewRequest("GET", "/ratings", nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	a, e := deps.Identify(r)
	if e != nil || a.OrganizationAdmin || a.AccountID != 71 || a.OrganizationID != 11 {
		t.Fatal("auth account context was not verified/normalized", a, e)
	}
	for _, header := range []string{"", "synthetic-token", "Bearer", "Basic synthetic-token"} {
		r.Header.Set("Authorization", header)
		_, e = deps.Identify(r)
		assertAPIError(t, e, 401, "invalid_token")
	}
}

func connectedExternalFixture(j Job) TestsSnapshot {
	direction, zero := int64(31), 0.0
	return TestsSnapshot{Source: "tests", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, InputHash: strings.Repeat("a", 64),
		Tests: []Test{}, ExternalDomain: &ExternalDomain{SchemaVersion: 1, Status: "ready", OrganizationID: j.OrganizationID, DivisionID: j.DivisionID}, ExternalSourceVersion: strings.Repeat("b", 64), ExternalTests: []Test{{ID: "external-one", Title: "Внешний", DirectionID: &direction, DirectionName: "Математика", SourceRevision: 2, StartAt: "2026-10-01T21:00:00Z", Published: true,
			Results: []TestResult{{AccountID: 71, SubmissionID: "record-zero", Revision: 2, Status: "ready", Score: &zero}, {AccountID: 72, SubmissionID: "record-unknown", Revision: 1, Status: "missing"}}}}}
}

func TestRequiredExternalSourcePreservesZeroAndExplicitUnknown(t *testing.T) {
	j := fixtureJob()
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		s := fixtureSnapshot(sourceJob(request))
		s.Tests = connectedExternalFixture(sourceJob(request))
		return s
	}, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "dev_empty", "")
	deps.Config.ExternalTestsMode = "required"
	s, e := deps.Collect(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	in, e := personal(j, s, s.Core.Accounts[0])
	if e != nil || len(in.Tests) != 1 || in.Tests[0].Source != "external" || in.Tests[0].Score == nil || *in.Tests[0].Score != 0 {
		t.Fatal("zero external grade changed", in, e)
	}
	unknown, e := personal(j, s, Account{AccountID: 72})
	if e != nil || unknown.Tests[0].Status != "missing" || unknown.Tests[0].Score != nil {
		t.Fatal("unknown result became known zero", unknown, e)
	}
	absent, e := personal(j, s, Account{AccountID: 73})
	if e != nil || absent.Tests[0].Status != "missing" || absent.Tests[0].Score != nil {
		t.Fatal("absent result omitted test", absent, e)
	}
	changed := s
	changed.Tests.ExternalSourceVersion = strings.Repeat("c", 64)
	if fingerprint(changed) == fingerprint(s) {
		t.Fatal("external source revision omitted from freshness")
	}
}

func TestRequiredExternalSourceRejectsInvalidVersionsAndRecords(t *testing.T) {
	for _, mutate := range []func(*TestsSnapshot){
		func(s *TestsSnapshot) { s.ExternalSourceVersion = "" },
		func(s *TestsSnapshot) { s.ExternalSourceVersion = strings.Repeat("x", 64) },
		func(s *TestsSnapshot) { s.ExternalTests[0].SourceRevision = 0 },
		func(s *TestsSnapshot) { s.ExternalTests[0].Published = false },
		func(s *TestsSnapshot) { s.ExternalTests[0].Results = nil },
		func(s *TestsSnapshot) { s.ExternalTests[0].Results[0].SubmissionID = "" },
		func(s *TestsSnapshot) { s.ExternalTests[0].Results[0].Revision = 0 },
		func(s *TestsSnapshot) { s.ExternalTests[0].Results[0].Score = nil },
		func(s *TestsSnapshot) { s.ExternalTests[0].Results[1].Score = s.ExternalTests[0].Results[0].Score },
		func(s *TestsSnapshot) { s.ExternalTests = append(s.ExternalTests, s.ExternalTests[0]) },
		func(s *TestsSnapshot) {
			s.ExternalTests[0].Results = append(s.ExternalTests[0].Results, s.ExternalTests[0].Results[0])
		},
	} {
		s := connectedExternalFixture(fixtureJob())
		mutate(&s)
		if e := validateExternalTestsSource(s, fixtureJob(), true); e == nil {
			t.Fatal("incomplete external source accepted", s)
		}
	}
}

func TestExternalSourceUsesInclusiveMoscowCalendarBoundaries(t *testing.T) {
	j := fixtureJob()
	for _, tc := range []struct {
		name, start string
		accepted    bool
	}{
		{"first Moscow midnight", "2026-09-30T21:00:00Z", true},
		{"first midnight with offset", "2026-10-01T00:00:00+03:00", true},
		{"last Moscow nanosecond", "2026-10-31T20:59:59.999999999Z", true},
		{"before first Moscow date", "2026-09-30T20:59:59.999999999Z", false},
		{"after last Moscow date", "2026-10-31T21:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := connectedExternalFixture(j)
			s.ExternalTests[0].StartAt = tc.start
			err := validateExternalTestsSource(s, j, true)
			if tc.accepted && err != nil {
				t.Fatal("inclusive Moscow calendar date rejected", err)
			}
			if !tc.accepted {
				assertAPIError(t, err, 503, "dependency_unavailable")
			}
		})
	}
}

func TestCollectForwardsOneImmutableUTCPeriodToEveryOwner(t *testing.T) {
	j := fixtureJob()
	j.AsOf = time.Date(2026, 9, 30, 21, 0, 0, 123456789, time.UTC)
	var requests atomic.Int32
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		requests.Add(1)
		if request.OrganizationID != j.OrganizationID || request.DivisionID != j.DivisionID || request.DateFrom != j.DateFrom || request.DateTo != j.DateTo || request.AsOf != j.AsOf.Format(time.RFC3339Nano) {
			t.Error("source call changed the immutable organization, division, period or UTC as_of")
		}
		return fixtureSnapshot(sourceJob(request))
	}, nil)
	defer server.Close()
	s, err := sourceDependencies(server.URL, "required", server.URL).Collect(context.Background(), j)
	if err != nil || requests.Load() != 4 || s.Exams.SourceRevision != 1 {
		t.Fatal("not every owner received the same source request or exam counter was lost", requests.Load(), err)
	}
}

func TestPersonalHomeworkPreservesFutureEligibilityForCalculateAndCalendarFence(t *testing.T) {
	j := fixtureJob()
	j.AsOf = time.Date(2026, 10, 8, 20, 59, 59, 999999999, time.UTC)
	s := fixtureSnapshot(j)
	zero, hundred := 0.0, 100.0
	transition := time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC)
	s.Homework.NextTransitionAt = &transition
	s.Homework.Assignments = []Assignment{
		{ID: 1, Name: "Past OV zero", TypeName: "ОВ", Deadline: "2026-10-01", Results: []HWResult{{AccountID: 71, Status: 1, Result: &zero}}},
		{ID: 2, Name: "Today OV", TypeName: "ОВ", Deadline: "2026-10-08", Results: []HWResult{{AccountID: 71, Status: 1, Result: &hundred}}},
		{ID: 3, Name: "Future OV missing", TypeName: "ОВ", Deadline: j.DateTo, Results: []HWResult{}},
		{ID: 4, Name: "Different legacy type", TypeName: "ов", Deadline: j.DateFrom, Results: []HWResult{}},
	}
	in, err := personal(j, s, s.Core.Accounts[0])
	if err != nil || len(in.Homework) != 4 || in.Homework[0].Result == nil || *in.Homework[0].Result != 0 || in.Homework[1].Deadline != "2026-10-08" || in.Homework[1].Result == nil || *in.Homework[1].Result != hundred || in.Homework[2].Result != nil || in.Homework[3].TypeName != "ов" {
		t.Fatal("rating capture altered the homework data used by the legacy calculate formula", in.Homework, err)
	}
	if transitioned(s, j.AsOf) || !transitioned(s, transition) || !transitioned(s, transition.Add(time.Nanosecond)) {
		t.Fatal("OV eligibility did not change exactly at Moscow midnight")
	}
	// A new calculation must use a new as_of/hash; the captured old input stays fixed.
	later := j
	later.AsOf = transition
	next, err := personal(later, s, s.Core.Accounts[0])
	if err != nil || next.InputHash == in.InputHash || in.AsOf != j.AsOf.Format(time.RFC3339Nano) || in.Homework[1].Deadline != next.Homework[1].Deadline {
		t.Fatal("calendar transition mutated old input or reused its calculate cache", err)
	}
}

func TestFreshnessReportsCalendarTransitionWithUnchangedOwnerHashes(t *testing.T) {
	j := fixtureJob()
	now := time.Now().In(Moscow)
	transition := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Moscow).UTC()
	j.AsOf = transition.Add(-time.Nanosecond)
	j.DateFrom = j.AsOf.In(Moscow).Format(time.DateOnly)
	j.DateTo = j.DateFrom
	makeSnapshot := func(request SnapshotRequest) Snapshot {
		if request.AsOf != j.AsOf.Format(time.RFC3339Nano) {
			t.Error("freshness silently replaced the captured as_of")
		}
		s := fixtureSnapshot(sourceJob(request))
		s.ExamsMode = "dev_empty"
		s.Homework.Assignments = []Assignment{{ID: 1, TypeName: "ОВ", Deadline: j.DateFrom, Results: []HWResult{}}}
		s.Homework.NextTransitionAt = &transition
		return s
	}
	server := sourceFixtureServer(t, makeSnapshot, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "dev_empty", "")
	snapshot, err := deps.Collect(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	j.Snapshot, _ = json.Marshal(snapshot)
	sourceHash := fingerprint(snapshot)
	j.SourceHash = &sourceHash
	state := (&Server{Deps: deps}).freshness(context.Background(), j)
	if state["is_stale"] != true || state["reason"] != "calendar_transition" || state["version_id"] != j.ID {
		t.Fatal("unchanged owner data hid the calendar eligibility change", state)
	}
}

func TestConcurrentSourcesKeepRepeatedIDsIndependentAcrossOrganizationsAndDivisions(t *testing.T) {
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		j := sourceJob(request)
		s := fixtureSnapshot(j)
		grade := float64((j.OrganizationID-11)*2 + j.DivisionID - 16)
		testScore := grade * 20
		direction := int64(7)
		s.Core.InputHash = hash([]int64{j.OrganizationID, j.DivisionID})
		s.Homework.InputHash, s.Tests.InputHash, s.Exams.InputHash = s.Core.InputHash, s.Core.InputHash, s.Core.InputHash
		s.Homework.Assignments = []Assignment{{ID: 4, TypeName: "ОВ", Deadline: j.DateFrom, Results: []HWResult{{AccountID: 71, Status: 1, Result: &testScore}}}}
		s.Tests.Tests = []Test{{ID: "shared-test", DirectionID: &direction, DirectionName: "Math", StartAt: "2026-10-01T00:00:00Z", Published: true, Results: []TestResult{{AccountID: 71, Status: "ready", Score: &testScore, SubmissionID: "own-submission", Revision: 1}}}}
		s.Exams.Exams = []Exam{{ID: "41", Date: j.DateFrom, Type: "classic", DirectionID: &direction, Results: []ExamResult{{AccountID: 71, Score: &grade}}}}
		return s
	}, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "required", server.URL)
	var wg sync.WaitGroup
	var hashes sync.Map
	for _, scope := range [][2]int64{{11, 17}, {11, 18}, {12, 17}, {12, 18}} {
		wg.Add(1)
		go func(scope [2]int64) {
			defer wg.Done()
			j := fixtureJob()
			j.OrganizationID, j.DivisionID = scope[0], scope[1]
			s, err := deps.Collect(context.Background(), j)
			if err != nil {
				t.Error("scoped collection failed", err)
				return
			}
			in, err := personal(j, s, s.Core.Accounts[0])
			grade := float64((scope[0]-11)*2 + scope[1] - 16)
			if err != nil || in.OrganizationID != scope[0] || in.DivisionID != scope[1] || len(in.Exams) != 1 || *in.Exams[0].Score != grade || *in.Tests[0].Score != grade*20 || *in.Homework[0].Result != grade*20 {
				t.Error("repeated source IDs mixed data between independently requested scopes", in, err)
				return
			}
			if _, exists := hashes.LoadOrStore(in.InputHash, scope); exists {
				t.Error("different scopes reused the personal calculation cache")
			}
		}(scope)
	}
	wg.Wait()
}

func TestCallbackRejectsForeignScopeAndSnapshotHashBeforeCoreLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Result)
		hash   bool
	}{
		{"foreign organization", func(r *Result) { r.OrganizationID++ }, false},
		{"foreign division", func(r *Result) { r.DivisionID++ }, false},
		{"foreign job", func(r *Result) { r.JobID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" }, false},
		{"different captured input", func(r *Result) { r.InputHash = strings.Repeat("b", 64) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := mockStore(t)
			j := fixtureJob()
			j.Status = "running"
			mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\?").WithArgs(j.ID).WillReturnRows(jobRows(j))
			result := readyResult(j, 71)
			if tc.hash {
				mock.ExpectQuery("SELECT input_hash FROM ratings_items_v1 WHERE job_id=\\? AND account_id=\\?").WithArgs(j.ID, int64(71)).WillReturnRows(sqlmock.NewRows([]string{"input_hash"}).AddRow(result.InputHash))
			}
			tc.mutate(&result)
			srv := &Server{Store: store, Config: Config{ServiceKey: strings.Repeat("k", 32)}}
			raw, _ := json.Marshal(result)
			r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
			r.SetPathValue("job", j.ID)
			r.SetPathValue("account", "71")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Service-Key", srv.Config.ServiceKey)
			w := httptest.NewRecorder()
			srv.result(w, r)
			if w.Code != 409 || !strings.Contains(w.Body.String(), "stale_rating_input") {
				t.Fatal("foreign callback reached history locks or overwrote a captured snapshot", w.Code, w.Body.String())
			}
		})
	}
}

func TestPublicScopeRequiresTheLiveDivisionFromTheActorsOrganization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		division int64
		active   bool
		want     int
	}{
		{"foreign organization division absent at owner", 404, 17, true, 404},
		{"owner returned another division ID", 200, 18, true, 404},
		{"archived division", 200, 17, false, 404},
		{"active division belongs to actor organization", 200, 17, true, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": Actor{AccountID: 71, OrganizationID: 11, Role: "admin", OrganizationAdmin: true}})
			}))
			defer auth.Close()
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					OrganizationID int64   `json:"organization_id"`
					DivisionIDs    []int64 `json:"division_ids"`
				}
				if r.Method != "POST" || r.URL.Path != "/internal/core/test-references" || r.Header.Get("X-Service-Key") != strings.Repeat("k", 32) || json.NewDecoder(r.Body).Decode(&request) != nil || request.OrganizationID != 11 || !reflect.DeepEqual(request.DivisionIDs, []int64{17}) {
					t.Error("division permission lookup escaped the authenticated organization's scope")
				}
				if tc.code != 200 {
					w.WriteHeader(tc.code)
					_, _ = w.Write([]byte(`{"status":false,"error":"not_found"}`))
					return
				}
				reply(w, 200, map[string]any{"organization": map[string]int64{"id": 11}, "divisions": []map[string]any{{"id": tc.division, "is_active": tc.active}}})
			}))
			defer core.Close()
			config := Config{AuthURL: auth.URL, CoreURL: core.URL, ServiceKey: strings.Repeat("k", 32)}
			srv := &Server{Config: config, Deps: NewDependencies(config)}
			called := false
			handler := srv.public(func(w http.ResponseWriter, r *http.Request, a Actor, division int64) {
				called = true
				w.WriteHeader(204)
			})
			r := httptest.NewRequest("GET", "/v1/ratings/divisions/17/ratings", nil)
			r.SetPathValue("division", "17")
			r.Header.Set("Authorization", "Bearer synthetic-token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 204) {
				t.Fatal("foreign or inactive owner division widened public scope", w.Code, called)
			}
		})
	}
}

func TestScopedJobHidesForeignOrganizationAndDivisionForKnownJobIDs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		organization int64
		division     int64
	}{
		{"same division ID in another organization", 12, 17},
		{"another division in own organization", 11, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := mockStore(t)
			j := fixtureJob()
			mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\?").WithArgs(j.ID).WillReturnRows(jobRows(j))
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("job", j.ID)
			_, err := (&Server{Store: store}).scopedJob(r, Actor{OrganizationID: tc.organization}, tc.division)
			assertAPIError(t, err, 404, "not_found")
		})
	}
}

func TestPeriodLimitCountsBothMoscowCalendarEndpoints(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		valid    bool
	}{
		{"2026-10-08", "2026-10-08", true},
		{"2026-01-01", "2027-01-01", true}, // 366 inclusive dates.
		{"2026-01-01", "2027-01-02", false},
		{"2024-01-01", "2024-12-31", true}, // Leap year, 366 inclusive dates.
		{"2024-01-01", "2025-01-01", false},
		{"2026-10-09", "2026-10-08", false},
		{"0999-01-01", "0999-01-01", false},
	} {
		if validPeriod(tc.from, tc.to) != tc.valid {
			t.Fatal("period bound omitted an inclusive calendar endpoint", tc)
		}
	}
}
