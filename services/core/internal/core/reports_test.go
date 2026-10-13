package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func reportFixture() CoreReport {
	return CoreReport{Source: "tests", OrganizationID: 1, DivisionID: 2, DateFrom: "2026-10-01", DateTo: "2026-10-15", Students: []map[string]any{{"account_id": int64(10), "profile_id": int64(21)}, {"account_id": int64(11), "profile_id": int64(22)}}, Items: []ReportItem{}, Cells: []ReportCell{}}
}
func decodeReportFixture[T any](t *testing.T, raw string) T {
	t.Helper()
	var out T
	if e := json.Unmarshal([]byte(raw), &out); e != nil {
		t.Fatal(e)
	}
	return out
}

const testReportJSON = `{"source":"tests","organization_id":1,"division_id":2,"external_tests":[],"external_source_version":"1111111111111111111111111111111111111111111111111111111111111111","input_hash":"2222222222222222222222222222222222222222222222222222222222222222","external_domain":{"schema_version":1,"status":"ready","organization_id":1,"division_id":2},"tests":[{"test_id":"a","title":"New tests","direction_id":9,"direction_name":"Math","start_at":"2026-09-30T21:00:00Z","published":true,"is_archived":false,"results":[{"account_id":10,"submission_id":"s1","calculation_revision":2,"calculation_status":"ready","score":0},{"account_id":11,"submission_id":"s2","calculation_revision":3,"calculation_status":"processing","score":98}]},{"test_id":"b","title":"Missing test","direction_id":9,"start_at":"2026-10-02T10:00:00Z","published":true,"results":[]}]}`
const examReportJSON = `{"source":"exams","organization_id":1,"division_ids":[2],"date_from":"2026-10-01","date_to":"2026-10-15","exams":[{"exam_id":7,"division_id":2,"exam_name":"Classic exam","direction_id":9,"exam_type":"classic","exam_date":"2026-10-02"},{"exam_id":8,"division_id":2,"exam_name":"Outside exam","direction_id":9,"exam_type":"outside_lms","exam_date":"2026-10-03"}],"results":[{"exam_id":7,"division_id":2,"account_id":10,"grade":0,"missing_result":false,"result_id":13,"result_version":4,"attempt_no":2,"has_appeal":true},{"exam_id":8,"division_id":2,"account_id":10,"grade":0,"missing_result":true}]}`
const attendanceReportJSON = `{"revision":4,"value":{"organization_id":1,"division_id":2,"unmarked_is_absent":false,"days":[{"id":6,"division_id":2,"date":"2026-10-03","comment":"Lesson"}],"entries":[{"id":8,"division_id":2,"class_day_id":6,"account_id":10,"is_deleted":false,"type":{"code":"present","name":"Присутствовал","mark":"+"}}]}}`

func TestReportsKeepOfficialZeroPendingAndMissingDistinct(t *testing.T) {
	in := decodeReportFixture[testsReportSnapshot](t, testReportJSON)
	out, e := translateTestsReport(reportFixture(), in)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Items) != 2 || out.Items[0].Date != "2026-10-01" || len(out.Cells) != 4 {
		t.Fatal("MSK dates or complete matrix lost", out)
	}
	if out.Cells[0].Score == nil || *out.Cells[0].Score != 0 || out.Cells[0].Status != "ready" {
		t.Fatal("official zero was lost")
	}
	if out.Cells[1].Status != "missing" || out.Cells[1].Score != nil || out.Cells[2].Status != "processing" || out.Cells[2].Score != nil {
		t.Fatal("pending old score or missing was published as a grade")
	}
	// Results of former/foreign roster members must not enter the current roster.
	in.Tests[0].Results[0].AccountID = 99
	out, e = translateTestsReport(reportFixture(), in)
	if e != nil {
		t.Fatal(e)
	}
	if out.Cells[0].Status != "missing" {
		t.Fatal("source-only account leaked")
	}
	for _, status := range []string{"queued", "processing", "retrying", "failed"} {
		in.Tests[0].Results[0].AccountID = 10
		in.Tests[0].Results[0].Status = status
		out, e = translateTestsReport(reportFixture(), in)
		if e != nil || out.Cells[0].Score != nil {
			t.Fatalf("pending %s score leaked: %v", status, e)
		}
	}
}
func TestReportsRejectAmbiguousSourceGradesAndCatalogue(t *testing.T) {
	for _, mutate := range []func(*testsReportSnapshot){
		func(v *testsReportSnapshot) { v.Tests[0].Published = false }, func(v *testsReportSnapshot) { v.Tests[0].Archived = true },
		func(v *testsReportSnapshot) { v.Tests[0].StartAt = v.Tests[0].StartAt.AddDate(0, 0, -1) },
		func(v *testsReportSnapshot) { v.Tests[0].Results[0].Score = nil }, func(v *testsReportSnapshot) { v.Tests[0].Results[0].Status = "unknown" },
		func(v *testsReportSnapshot) { v.Tests[0].Results = append(v.Tests[0].Results, v.Tests[0].Results[0]) },
		func(v *testsReportSnapshot) { v.Tests = append(v.Tests, v.Tests[0]) },
	} {
		v := decodeReportFixture[testsReportSnapshot](t, testReportJSON)
		mutate(&v)
		if _, e := translateTestsReport(reportFixture(), v); e == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
func TestExamsReportUsesOwnerEffectiveRetakeAndNamedOutsideResult(t *testing.T) {
	in := decodeReportFixture[examsReportSnapshot](t, examReportJSON)
	out, e := translateExamsReport(reportFixture(), in)
	if e != nil {
		t.Fatal(e)
	}
	if out.Items[0].Name != "Classic exam" || out.Items[1].Name != "Outside exam" || out.Items[1].Kind != "outside_lms" {
		t.Fatal("owner names or kind lost")
	}
	first := out.Cells[0]
	if first.Score == nil || *first.Score != 0 || first.AttemptNo != 2 || !first.HasAppeal || first.ResultVersion != 4 {
		t.Fatal("worse completed retake zero or appeal metadata lost", first)
	}
	if out.Cells[1].Score != nil || out.Cells[1].Status != "missing" {
		t.Fatal("owner missing grade0 fabricated as completed")
	}
	in.Exams[0].Name = ""
	out, e = translateExamsReport(reportFixture(), in)
	if e != nil || out.Items[0].Name != "Экзамен 7" {
		t.Fatal("old optional name compatibility lost")
	}
	for _, mutate := range []func(*examsReportSnapshot){func(v *examsReportSnapshot) { v.Exams[0].DivisionID = 3 }, func(v *examsReportSnapshot) { v.Results[0].DivisionID = 3 }, func(v *examsReportSnapshot) { v.Results[0].Grade = nil }, func(v *examsReportSnapshot) { f := 2.5; v.Results[0].Grade = &f }, func(v *examsReportSnapshot) { v.Results[0].ResultVersion = 0 }, func(v *examsReportSnapshot) { v.Results = append(v.Results, v.Results[0]) }} {
		v := decodeReportFixture[examsReportSnapshot](t, examReportJSON)
		mutate(&v)
		if _, e := translateExamsReport(reportFixture(), v); e == nil {
			t.Fatal("ambiguous/foreign exam source accepted")
		}
	}
}
func TestAttendanceReportUsesCurrentOwnerAndUnmarkedIsNotAbsence(t *testing.T) {
	in := decodeReportFixture[attendanceReportSnapshot](t, attendanceReportJSON)
	out, e := translateAttendanceReport(reportFixture(), in)
	if e != nil {
		t.Fatal(e)
	}
	if out.SourceRevision == nil || *out.SourceRevision != 4 || out.UnmarkedIsAbsent == nil || *out.UnmarkedIsAbsent || out.Cells[0].Mark != "+" || out.Cells[1].Status != "missing" || out.Cells[1].Score != nil {
		t.Fatal("attendance owner semantics lost", out)
	}
	in.Value.Entries[0].Deleted = true
	if _, e := translateAttendanceReport(reportFixture(), in); e == nil {
		t.Fatal("deleted source mark accepted")
	}
	if !decodeReportFixture[attendanceReportSnapshot](t, strings.ReplaceAll(attendanceReportJSON, `"is_deleted":false`, `"is_deleted":true`)).Value.Entries[0].Deleted {
		t.Fatal("actual owner deletion field not read")
	}
}
func TestReportMatrixBound(t *testing.T) {
	out := reportFixture()
	out.Items = make([]ReportItem, 50001)
	if _, e := finishReport(out, nil); e == nil {
		t.Fatal("oversized matrix accepted")
	}
}
func TestReportsRequireExactRightsAndRejectScopeOverridesBeforeSources(t *testing.T) {
	s := &Server{}
	for _, source := range []string{"tests", "exams", "attendance"} {
		for _, role := range []string{"student", "proctor", "examinator"} {
			if _, e := s.newReport(httptest.NewRequest("GET", "/?date_from=2026-10-01&date_to=2026-10-15", nil), Actor{Role: role}, 2, source); e != denied {
				t.Fatalf("%s accessed %s: %v", role, source, e)
			}
		}
		if reportAllowed(Actor{Role: "staff_admin", Permissions: map[string]Permission{"ratings": {View: true, Edit: true}}}, source) {
			t.Fatal("ratings right escalated into live report")
		}
	}
	for _, query := range []string{"date_from=2026-10-01&date_to=2026-10-15&division_id=3", "date_from=2026-10-01&date_to=2026-10-15&account_id=9", "date_from=2026-10-01&date_from=2026-10-02&date_to=2026-10-15", "date_from=2026-02-30&date_to=2026-10-15"} {
		if _, _, e := reportQuery(httptest.NewRequest("GET", "/?"+query, nil)); e == nil {
			t.Fatal("invalid query accepted", query)
		}
	}
	for _, a := range []Actor{{Role: "supervisor"}, {OrganizationAdmin: true}, {Role: "staff_admin", Permissions: map[string]Permission{"test_results": {View: true}}}, {Role: "staff_admin", Permissions: map[string]Permission{"test-results": {View: true}}}} {
		if !reportAllowed(a, "tests") {
			t.Fatal("authorized reader blocked")
		}
	}
}
func TestReportClientUsesSeparateSecretsAndNeverFollowsRedirect(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/leak")
			w.WriteHeader(302)
			return
		}
		if r.URL.Path == "/attendance" {
			if r.Header.Get("Authorization") != "Bearer caller" || r.Header.Get("X-Service-Key") != "" {
				t.Error("attendance credentials crossed")
			}
		} else if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "service-secret" {
			t.Error("source caller credentials leaked")
		}
		io.WriteString(w, `{"status":true,"data":{"ok":true}}`)
	}))
	defer upstream.Close()
	p := NewReportClient(Config{ReportTestsURL: upstream.URL, ReportAttendanceURL: upstream.URL, InternalKey: "service-secret"})
	var out map[string]bool
	if e := p.call(context.Background(), "tests", "POST", "/tests", "Bearer caller", map[string]int{"division_id": 2}, &out); e != nil || !out["ok"] {
		t.Fatal(e)
	}
	if e := p.call(context.Background(), "attendance", "GET", "/attendance", "Bearer caller", nil, &out); e != nil {
		t.Fatal(e)
	}
	if e := p.call(context.Background(), "tests", "GET", "/redirect", "Bearer caller", nil, &out); e == nil || hits != 3 {
		t.Fatal("redirect followed or accepted", e, hits)
	}
	if e := p.call(context.Background(), "exams", "GET", "/exams", "", nil, &out); e == nil {
		t.Fatal("unconnected owner silently empty")
	}
}
func reportRosterDB() (*sql.DB, *ratingSnapshotConnector) {
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{{contains: []string{"m.organization_id=? AND m.division_id=?", "a.role='student'", "gs.group_id,g.name group_name,sc.id school_id", "ss.organization_id=m.organization_id", "ORDER BY a.id LIMIT 2001"}, args: []int64{1, 2}, columns: []string{"account_id", "profile_id", "full_name", "class", "group_id", "group_name", "school_id", "school_name"}, rows: [][]driver.Value{{int64(10), int64(21), "Student", int64(7), nil, nil, nil, nil}}}}}
	return sql.OpenDB(c), c
}
func TestTestsReportInjectsLiveScopeAndRejectsForeignSnapshot(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		db, c := reportRosterDB()
		hits := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			if r.URL.Path != "/internal/tests/ratings/snapshot" || r.Method != "POST" {
				t.Error("wrong source contract")
			}
			var in RatingSnapshotRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID != 1 || in.DivisionID != 2 || in.DateFrom != "2026-10-01" || in.validate() != nil {
				t.Error("caller scope/date/asof not injected")
			}
			raw := testReportJSON
			if foreign {
				raw = strings.Replace(raw, `"division_id":2`, `"division_id":3`, 1)
			}
			io.WriteString(w, `{"status":true,"data":`+raw+`}`)
		}))
		s := &Server{DB: db, Reports: NewReportClient(Config{ReportTestsURL: upstream.URL, InternalKey: "key"})}
		out, code, e := s.testsReport(httptest.NewRequest("GET", "/?date_from=2026-10-01&date_to=2026-10-15", nil), Actor{Role: "supervisor", OrganizationID: 1}, 2)
		if foreign {
			if e == nil {
				t.Fatal("foreign owner snapshot exposed")
			}
		} else if e != nil || code != 200 || out.(CoreReport).Cells[0].Score == nil || *out.(CoreReport).Cells[0].Score != 0 {
			t.Fatal(code, e, out)
		}
		if hits != 1 || len(c.queries) != 0 {
			t.Fatal("source/roster not read exactly once")
		}
		upstream.Close()
		db.Close()
	}
}
func TestExamsAndAttendanceReportRejectForeignEnvelope(t *testing.T) {
	for _, source := range []string{"exams", "attendance"} {
		for _, foreign := range []bool{false, true} {
			db, _ := reportRosterDB()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw string
				if source == "exams" {
					if r.URL.Path != "/internal/exams/rating-snapshot" || r.URL.Query().Get("organization_id") != "1" || r.URL.Query().Get("division_ids") != "2" {
						t.Error("exam scope not injected")
					}
					raw = examReportJSON
					if foreign {
						raw = strings.Replace(raw, `"organization_id":1`, `"organization_id":9`, 1)
					}
					raw = `{"snapshot":` + raw + `}`
				} else {
					if r.URL.Path != "/v1/attendance/divisions/2/attendance-report" || r.Header.Get("Authorization") != "Bearer caller" {
						t.Error("current attendance owner/caller not used")
					}
					raw = attendanceReportJSON
					if foreign {
						raw = strings.Replace(raw, `"division_id":2`, `"division_id":3`, 1)
					}
				}
				io.WriteString(w, `{"status":true,"data":`+raw+`}`)
			}))
			s := &Server{DB: db, Reports: NewReportClient(Config{ReportExamsURL: upstream.URL, ReportAttendanceURL: upstream.URL, InternalKey: "key"})}
			r := httptest.NewRequest("GET", "/?date_from=2026-10-01&date_to=2026-10-15", nil)
			r.Header.Set("Authorization", "Bearer caller")
			a := Actor{Role: "supervisor", OrganizationID: 1}
			var e error
			if source == "exams" {
				_, _, e = s.examsReport(r, a, 2)
			} else {
				_, _, e = s.currentAttendanceReport(r, a, 2)
			}
			if foreign && e == nil || !foreign && e != nil {
				t.Fatal(source, foreign, e)
			}
			upstream.Close()
			db.Close()
		}
	}
}
func TestOptionalReportURLsOnlyAllowConfiguredLocalOwners(t *testing.T) {
	for _, v := range []string{"http://tests:8004", "http://127.0.0.1:8004", "http://localhost:8004"} {
		if !validReportURL(v, "tests", "8004") {
			t.Fatal(v)
		}
	}
	if !validReportURL("http://host.docker.internal:8010", "exams", "8010") {
		t.Fatal("native exams host blocked")
	}
	for _, v := range []string{"https://tests:8004", "http://tests:8003", "http://production.example:8004", "http://tests:8004/path", "http://tests:8004?token=secret", "http://user:secret@tests:8004", "http://host.docker.internal:8004"} {
		if validReportURL(v, "tests", "8004") {
			t.Fatal("unsafe source URL accepted", v)
		}
	}
}
