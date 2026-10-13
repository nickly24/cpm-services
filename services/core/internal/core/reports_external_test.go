package core

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func externalReportFixture(t *testing.T) testsReportSnapshot {
	t.Helper()
	source := decodeReportFixture[testsReportSnapshot](t, testReportJSON)
	date, e := time.Parse(time.RFC3339, "2026-09-30T21:00:00Z")
	if e != nil {
		t.Fatal(e)
	}
	zero := 0.0
	source.ExternalTests = []reportTest{{ID: "abcdef0123456789abcdef01", Name: "External zero and unknown", DirectionID: 9, DirectionName: "Math", StartAt: date, Published: true, SourceRevision: 4, Results: []reportTestResult{
		{AccountID: 10, SubmissionID: "abcdef0123456789abcdef11", Revision: 2, Status: "ready", Score: &zero},
		{AccountID: 11, SubmissionID: "abcdef0123456789abcdef12", Revision: 3, Status: "missing"},
	}}, {ID: "abcdef0123456789abcdef02", Name: "External without submissions", DirectionID: 9, DirectionName: "Math", StartAt: date.AddDate(0, 0, 1), Published: true, SourceRevision: 1, Results: []reportTestResult{}}}
	return source
}

func reportCellMap(report CoreReport) map[string]ReportCell {
	values := map[string]ReportCell{}
	for _, cell := range report.Cells {
		values[cell.ItemID+"/"+strconv.FormatInt(cell.AccountID, 10)] = cell
	}
	return values
}

func TestExternalReportsCombineNativeTargetsPreserveZeroNullAndMissing(t *testing.T) {
	source := externalReportFixture(t)
	// Different owner collections may hold the same ObjectID. They must remain
	// distinct report columns and preserve native target IDs for any future link.
	source.Tests[0].ID = source.ExternalTests[0].ID
	out, e := translateTestsReport(reportFixture(), source)
	if e != nil || len(out.Items) != 4 || len(out.Cells) != 8 {
		t.Fatal("complete combined source matrix lost", e, out)
	}
	var external ReportItem
	for _, item := range out.Items {
		if item.ID == "external:"+source.ExternalTests[0].ID {
			external = item
		}
	}
	if external.Kind != "external_test" || external.TargetID != source.ExternalTests[0].ID || external.Date != "2026-10-01" || external.Name != source.ExternalTests[0].Name || external.SourceRevision != 4 || external.DirectionName != "Math" {
		t.Fatal("canonical external target/date/direction/revision lost", external)
	}
	cells := reportCellMap(out)
	zero := cells[external.ID+"/10"]
	unknown := cells[external.ID+"/11"]
	absent := cells["external:"+source.ExternalTests[1].ID+"/10"]
	if zero.Status != "ready" || zero.Score == nil || *zero.Score != 0 || !zero.Recorded || zero.ExternalResultID != source.ExternalTests[0].Results[0].SubmissionID || zero.ResultVersion != 2 {
		t.Fatal("external recorded zero became missing", zero)
	}
	if unknown.Status != "missing" || unknown.Score != nil || !unknown.Recorded || unknown.ExternalResultID == "" || unknown.ResultVersion != 3 || absent.Status != "missing" || absent.Score != nil || absent.Recorded || absent.ExternalResultID != "" || absent.ResultVersion != 0 {
		t.Fatal("recorded unknown is indistinguishable from no result", unknown, absent)
	}
	if current := cells[source.Tests[0].ID+"/10"]; current.Status != "ready" || current.Score == nil || *current.Score != 0 || current.Recorded {
		t.Fatal("internal column collided with external ID", current)
	}
	if out.SourceInputHash != source.InputHash || out.ExternalSourceVersion != source.ExternalSourceVersion || strings.Contains(strings.Join(out.Limitations, ","), "not_connected") {
		t.Fatal("connected scoped source fingerprint or capabilities lost")
	}
	fraction := 27.25
	source.ExternalTests[0].Results[0].Score = &fraction
	out, e = translateTestsReport(reportFixture(), source)
	if e != nil || *reportCellMap(out)[external.ID+"/10"].Score != fraction {
		t.Fatal("external decimal score rounded", e)
	}
}

func TestExternalReportsRequireReadyScopedDomainAndCompleteVersions(t *testing.T) {
	for name, mutate := range map[string]func(*testsReportSnapshot){
		"missing external array":     func(s *testsReportSnapshot) { s.ExternalTests = nil },
		"missing internal array":     func(s *testsReportSnapshot) { s.Tests = nil },
		"missing domain":             func(s *testsReportSnapshot) { s.ExternalDomain = nil },
		"unready domain":             func(s *testsReportSnapshot) { s.ExternalDomain.Status = "initializing" },
		"wrong schema":               func(s *testsReportSnapshot) { s.ExternalDomain.SchemaVersion = 2 },
		"foreign domain org":         func(s *testsReportSnapshot) { s.ExternalDomain.OrganizationID = 9 },
		"foreign domain division":    func(s *testsReportSnapshot) { s.ExternalDomain.DivisionID = 9 },
		"foreign envelope org":       func(s *testsReportSnapshot) { s.OrganizationID = 9 },
		"foreign envelope division":  func(s *testsReportSnapshot) { s.DivisionID = 9 },
		"wrong source":               func(s *testsReportSnapshot) { s.Source = "legacy" },
		"missing external version":   func(s *testsReportSnapshot) { s.ExternalSourceVersion = "" },
		"malformed external version": func(s *testsReportSnapshot) { s.ExternalSourceVersion = strings.Repeat("x", 64) },
		"missing source hash":        func(s *testsReportSnapshot) { s.InputHash = "" },
		"short source hash":          func(s *testsReportSnapshot) { s.InputHash = "abc" },
	} {
		t.Run(name, func(t *testing.T) {
			source := externalReportFixture(t)
			mutate(&source)
			_, e := translateTestsReport(reportFixture(), source)
			assertReportBad(t, e)
		})
	}
	source := externalReportFixture(t)
	source.Tests, source.ExternalTests = []reportTest{}, []reportTest{}
	out, e := translateTestsReport(reportFixture(), source)
	if e != nil || len(out.Items) != 0 || len(out.Cells) != 0 || out.ExternalSourceVersion != source.ExternalSourceVersion {
		t.Fatal("empty ready domain incorrectly rejected or fabricated", e)
	}
}

func assertReportBad(t *testing.T, e error) {
	t.Helper()
	api, ok := e.(*APIError)
	if !ok || api.Code != 503 || api.Message != "invalid_report_source" {
		t.Fatal("malformed owner source accepted", e)
	}
}

func TestExternalReportsRejectInvalidCatalogueAndResultIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*testsReportSnapshot){
		"legacy catalogue id":        func(s *testsReportSnapshot) { s.ExternalTests[0].ID = "21" },
		"zero object id":             func(s *testsReportSnapshot) { s.ExternalTests[0].ID = strings.Repeat("0", 24) },
		"missing catalogue revision": func(s *testsReportSnapshot) { s.ExternalTests[0].SourceRevision = 0 },
		"missing direction":          func(s *testsReportSnapshot) { s.ExternalTests[0].DirectionID = 0 },
		"empty title":                func(s *testsReportSnapshot) { s.ExternalTests[0].Name = "  " },
		"unpublished":                func(s *testsReportSnapshot) { s.ExternalTests[0].Published = false },
		"archived":                   func(s *testsReportSnapshot) { s.ExternalTests[0].Archived = true },
		"before requested date": func(s *testsReportSnapshot) {
			s.ExternalTests[0].StartAt = s.ExternalTests[0].StartAt.AddDate(0, 0, -1)
		},
		"after requested date":    func(s *testsReportSnapshot) { s.ExternalTests[0].StartAt = s.ExternalTests[0].StartAt.AddDate(0, 1, 0) },
		"missing results array":   func(s *testsReportSnapshot) { s.ExternalTests[0].Results = nil },
		"duplicate catalogue":     func(s *testsReportSnapshot) { s.ExternalTests = append(s.ExternalTests, s.ExternalTests[0]) },
		"legacy result id":        func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].SubmissionID = "21" },
		"zero account":            func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].AccountID = 0 },
		"missing result revision": func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].Revision = 0 },
		"duplicate account": func(s *testsReportSnapshot) {
			s.ExternalTests[0].Results = append(s.ExternalTests[0].Results, s.ExternalTests[0].Results[0])
		},
		"duplicate result identity": func(s *testsReportSnapshot) {
			s.ExternalTests[0].Results[1].SubmissionID = s.ExternalTests[0].Results[0].SubmissionID
		},
		"ready null":                 func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].Score = nil },
		"missing with stale score":   func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].Status = "missing" },
		"external not a calculation": func(s *testsReportSnapshot) { s.ExternalTests[0].Results[0].Status = "queued" },
		"nan":                        func(s *testsReportSnapshot) { value := math.NaN(); s.ExternalTests[0].Results[0].Score = &value },
		"infinite":                   func(s *testsReportSnapshot) { value := math.Inf(1); s.ExternalTests[0].Results[0].Score = &value },
		"negative":                   func(s *testsReportSnapshot) { value := -0.1; s.ExternalTests[0].Results[0].Score = &value },
		"above percent":              func(s *testsReportSnapshot) { value := 100.1; s.ExternalTests[0].Results[0].Score = &value },
	} {
		t.Run(name, func(t *testing.T) {
			source := externalReportFixture(t)
			mutate(&source)
			_, e := translateTestsReport(reportFixture(), source)
			assertReportBad(t, e)
		})
	}
}

func TestExternalReportsUseCurrentAccountRosterNeverProfileOrForeignIDs(t *testing.T) {
	source := externalReportFixture(t)
	source.ExternalTests[0].Results[0].AccountID = 21 // profile ID of account10
	source.ExternalTests[0].Results[1].AccountID = 99 // former/foreign membership
	out, e := translateTestsReport(reportFixture(), source)
	if e != nil {
		t.Fatal(e)
	}
	for _, cell := range out.Cells {
		if cell.AccountID != 10 && cell.AccountID != 11 || cell.Recorded || strings.HasPrefix(cell.ItemID, "external:") && cell.Status != "missing" {
			t.Fatal("profile identity or owner-only student leaked", cell)
		}
	}
}

func TestExternalReportsBoundCombinedCataloguesResultsAndMatrix(t *testing.T) {
	for _, count := range []int{999, 1000} {
		source := externalReportFixture(t)
		source.ExternalTests = make([]reportTest, count)
		_, e := translateTestsReport(reportFixture(), source)
		api, ok := e.(*APIError)
		if !ok || api.Code != 422 || api.Message != "report_too_large" {
			t.Fatal("combined catalogue bound not applied before decode", e)
		}
	}
	source := externalReportFixture(t)
	source.ExternalTests[0].Results = make([]reportTestResult, reportMaxCells+1)
	if _, e := translateTestsReport(reportFixture(), source); e == nil {
		t.Fatal("oversized result source accepted")
	}
	out := reportFixture()
	out.Students = make([]map[string]any, 25001)
	if _, e := translateTestsReport(out, externalReportFixture(t)); e == nil {
		t.Fatal("combined matrix exceeded bound")
	}
}

func TestExternalReportsReadOneScopedAggregateWithPeriodBeforeAnyPaging(t *testing.T) {
	db, c := reportRosterDB()
	defer db.Close()
	source := externalReportFixture(t)
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/internal/tests/ratings/snapshot" || r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "key" {
			t.Error("wrong source endpoint or caller credential leakage")
		}
		var in RatingSnapshotRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 1 || in.DivisionID != 2 || in.DateFrom != "2026-10-01" || in.DateTo != "2026-10-15" || in.validate() != nil {
			t.Error("filters did not reach bounded owner snapshot")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": source})
	}))
	defer upstream.Close()
	s := &Server{DB: db, Reports: NewReportClient(Config{ReportTestsURL: upstream.URL, InternalKey: "key"})}
	r := httptest.NewRequest("GET", "/?date_from=2026-10-01&date_to=2026-10-15", nil)
	r.Header.Set("Authorization", "Bearer supervisor-private")
	out, code, e := s.testsReport(r, Actor{Role: "supervisor", OrganizationID: 1}, 2)
	if e != nil || code != 200 || len(out.(CoreReport).Items) != 4 || hits != 1 || len(c.queries) != 0 {
		t.Fatal("external report performs N+1 reads or returns incomplete source", e, hits)
	}
	if _, _, e := s.testsReport(r, Actor{Role: "staff_admin", OrganizationID: 1, Permissions: map[string]Permission{"test-results": {View: false, Edit: true}}}, 2); e != denied || hits != 1 {
		t.Fatal("withdrawn result view still reached the owner", e)
	}
	if _, _, e := s.testsReport(r, Actor{Role: "staff_admin", OrganizationID: 1, Permissions: map[string]Permission{"test-results": {View: false}, "test_results": {View: true}}}, 2); e != denied || hits != 1 {
		t.Fatal("legacy alias bypassed explicit canonical result denial", e)
	}
}

func TestExternalReportUnavailableSourceNeverBecomesInternalOnlySuccess(t *testing.T) {
	db, _ := reportRosterDB()
	defer db.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"status":false,"error":"external_domain_unavailable"}`)
	}))
	defer upstream.Close()
	s := &Server{DB: db, Reports: NewReportClient(Config{ReportTestsURL: upstream.URL, InternalKey: "key"})}
	_, _, e := s.testsReport(httptest.NewRequest("GET", "/?date_from=2026-10-01&date_to=2026-10-15", nil), Actor{Role: "supervisor", OrganizationID: 1}, 2)
	api, ok := e.(*APIError)
	if !ok || api.Code != 503 {
		t.Fatal("unavailable external domain silently became empty", e)
	}
}
