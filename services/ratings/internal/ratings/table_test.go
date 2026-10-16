package ratings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestTableReturnsPersistedZeroWithoutWaitingForAnySource(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status = "completed"
	j.Snapshot = nil
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer source.Close()
	config := Config{CoreURL: source.URL, HomeworkURL: source.URL, TestsURL: source.URL, ExamsURL: source.URL, ExamsMode: "required", ExternalTestsMode: "required", ServiceKey: strings.Repeat("k", 32)}
	srv := &Server{Store: store, Config: config, Deps: NewDependencies(config)}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT published_job_id FROM ratings_scopes_v1").WithArgs(j.OrganizationID, j.DivisionID).WillReturnRows(sqlmock.NewRows([]string{"published_job_id"}).AddRow(j.ID))
	mock.ExpectQuery("SELECT .*NULL AS snapshot.* FROM ratings_jobs_v1").WithArgs(j.ID, j.OrganizationID, j.DivisionID).WillReturnRows(jobRows(j))
	mock.ExpectQuery("SELECT COUNT.*ratings_items_v1").WithArgs(j.ID, "", "").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(413))
	zero := 0.0
	row := tableRow{Account: Account{AccountID: 71, FullName: "Студент"}, Result: tableResult{JobID: j.ID, Revision: j.Revision, OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, AccountID: 71, Algorithm: Algorithm, Status: "ready", InputHash: strings.Repeat("a", 64), Homework: tableComponent{&zero}, Exams: tableComponent{&zero}, Tests: tableComponent{&zero}, Final: &zero}, Place: 1}
	p, _ := json.Marshal(row.Account)
	o, _ := json.Marshal(row.Result)
	mock.ExpectQuery("WITH ranked AS").WithArgs(j.ID, "", "", 50, 0).WillReturnRows(sqlmock.NewRows([]string{"profile", "summary", "place"}).AddRow(p, o, 1))
	mock.ExpectCommit()
	req := httptest.NewRequest("GET", "/table?limit=50", nil)
	w := httptest.NewRecorder()
	srv.table(w, req, Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: j.OrganizationID}, j.DivisionID)
	if w.Code != 200 || calls.Load() != 0 || strings.Contains(w.Body.String(), "details") || !strings.Contains(w.Body.String(), `"total":413`) || !strings.Contains(w.Body.String(), `"final_rating":0`) {
		t.Fatal("table waited for owners, fetched details, or lost the zero", w.Code, calls.Load(), w.Body.String())
	}
}

func TestTableAndFreshnessDenyStudentsProctorsAndStaffWithoutView(t *testing.T) {
	for _, actor := range []Actor{{Role: "student"}, {Role: "proctor"}, {Role: "staff_admin"}, {Role: "admin"}} {
		srv := &Server{}
		for _, handler := range []business{srv.table, srv.tableFreshness} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest("GET", "/", nil), actor, 17)
			if w.Code != 403 {
				t.Fatal("summary or freshness expanded read access", actor.Role, w.Code)
			}
		}
	}
}

func TestTableRejectsInvalidSummaryInsteadOfInventingZeroOrCrossingScope(t *testing.T) {
	j := fixtureJob()
	zero := 0.0
	valid := tableRow{Account: Account{AccountID: 71}, Result: tableResult{JobID: j.ID, Revision: j.Revision, OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, AccountID: 71, Algorithm: Algorithm, Status: "ready", InputHash: strings.Repeat("a", 64), Final: &zero, Homework: tableComponent{&zero}, Tests: tableComponent{&zero}, Exams: tableComponent{&zero}}, Place: 1}
	if !validTableRow(valid, j) {
		t.Fatal("completed zero rejected")
	}
	for _, mutate := range []func(*tableRow){func(v *tableRow) { v.Result.Final = nil }, func(v *tableRow) { v.Result.Homework.Rating = nil }, func(v *tableRow) { v.Result.Status = "failed" }, func(v *tableRow) { v.Result.OrganizationID++ }, func(v *tableRow) { v.Result.DivisionID++ }, func(v *tableRow) { v.Result.Revision++ }, func(v *tableRow) { v.Account.AccountID++ }, func(v *tableRow) { v.Place = 0 }} {
		v := valid
		mutate(&v)
		if validTableRow(v, j) {
			t.Fatal("malformed summary accepted")
		}
	}
}
