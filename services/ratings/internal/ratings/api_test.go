package ratings

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func readyResult(j Job, account int64) Result {
	zero := 0.0
	component := json.RawMessage(`{"rating":0,"details":[]}`)
	tests := json.RawMessage(`{"rating":0,"details":[],"directions":{}}`)
	return Result{JobID: j.ID, Revision: j.Revision, OrganizationID: j.OrganizationID, DivisionID: j.DivisionID, AccountID: account, InputHash: strings.Repeat("a", 64), Algorithm: Algorithm, Status: "ready", Final: &zero, Homework: component, Tests: tests, Exams: component}
}

func TestResultRequiresExplicitFinalIncludingZeroAndCompleteComponents(t *testing.T) {
	valid := readyResult(fixtureJob(), 71)
	if e := validateResult(valid); e != nil {
		t.Fatal("explicit zero result was rejected", e)
	}
	raw, _ := json.Marshal(valid)
	if !strings.Contains(string(raw), `"final_rating":0`) {
		t.Fatal("ready zero final disappeared from the wire contract")
	}
	for _, mutate := range []func(*Result){
		func(r *Result) { r.Final = nil },
		func(r *Result) { r.Tests = nil },
		func(r *Result) { r.Exams = json.RawMessage(`{"rating":0,"details":null}`) },
		func(r *Result) { r.Homework = json.RawMessage(`{"details":[]}`) },
	} {
		invalid := valid
		mutate(&invalid)
		if e := validateResult(invalid); e == nil {
			t.Fatal("incomplete result was silently accepted as ready")
		}
	}
	failed := valid
	failed.Status, failed.ErrorCode = "failed", ""
	assertAPIError(t, validateResult(failed), 400, "error_code_required")
	failed.ErrorCode = "invalid_input"
	if e := validateResult(failed); e != nil {
		t.Fatal("explicit calculation failure was rejected", e)
	}
}

func TestDecodeRejectsFalseJSONMediaTypeAndNullObject(t *testing.T) {
	for _, contentType := range []string{"application/jsonish", "application/jsonp", "text/plain"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", contentType)
		var input struct{}
		assertAPIError(t, decode(r, &input), 415, "")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	var input struct{}
	if e := decode(r, &input); e != nil {
		t.Fatal("legitimate JSON parameters rejected", e)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`null`))
	r.Header.Set("Content-Type", "application/json")
	if e := decode(r, &input); e == nil {
		t.Fatal("null mutation body was accepted")
	}
}

func TestPublicScopeRejectsOtherDivisionBeforeCoreOrBusinessCall(t *testing.T) {
	division := int64(17)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "active": true, "user": Actor{AccountID: 71, OrganizationID: 11, DivisionID: &division, Role: "staff_admin", Permissions: map[string]Permission{"ratings": {View: true, Edit: true}}}})
	}))
	defer auth.Close()
	var coreCalls atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { coreCalls.Add(1); w.WriteHeader(500) }))
	defer core.Close()
	config := Config{AuthURL: auth.URL, CoreURL: core.URL, ServiceKey: strings.Repeat("k", 32)}
	srv := &Server{Config: config, Deps: NewDependencies(config)}
	businessCalled := false
	handler := srv.public(func(w http.ResponseWriter, r *http.Request, a Actor, d int64) { businessCalled = true })
	r := httptest.NewRequest("GET", "/v1/ratings/divisions/18/ratings", nil)
	r.SetPathValue("division", "18")
	r.Header.Set("Authorization", "Bearer synthetic-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 || coreCalls.Load() != 0 || businessCalled {
		t.Fatal("division path expanded delegated rights", w.Code, coreCalls.Load(), businessCalled)
	}
}

func TestCallbackObtainsCoreRatingLockBeforeFirstResultWrite(t *testing.T) {
	for _, coreAvailable := range []bool{false, true} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status = "running"
		result := readyResult(j, 71)
		mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\?").WithArgs(j.ID).WillReturnRows(jobRows(j))
		mock.ExpectQuery("SELECT input_hash FROM ratings_items_v1 WHERE job_id=\\? AND account_id=\\?").WithArgs(j.ID, int64(71)).
			WillReturnRows(sqlmock.NewRows([]string{"input_hash"}).AddRow(result.InputHash))
		var lockCalls atomic.Int32
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lockCalls.Add(1)
			if r.URL.Path != "/internal/core/rating-locks" || r.Header.Get("X-Service-Key") != strings.Repeat("k", 32) {
				t.Error("result did not use the core history-lock contract")
			}
			var body map[string]int64
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["account_id"] != 71 || body["division_id"] != 17 {
				t.Error("core history lock used a profile ID or wrong division")
			}
			if !coreAvailable {
				w.WriteHeader(503)
				return
			}
			reply(w, 200, map[string]bool{"locked": true})
		}))
		if coreAvailable {
			expectResultLocks(mock, j, j.Revision, &j.ID, "queued", nil)
			mock.ExpectExec("UPDATE ratings_items_v1 SET status=").WithArgs("ready", sqlmock.AnyArg(), sqlmock.AnyArg(), "", sqlmock.AnyArg(), j.ID, int64(71)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE ratings_jobs_v1 SET processed_count=processed_count\\+1").WithArgs(sqlmock.AnyArg(), j.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
		}
		config := Config{CoreURL: core.URL, ServiceKey: strings.Repeat("k", 32)}
		srv := &Server{Config: config, Store: store, Deps: NewDependencies(config)}
		raw, _ := json.Marshal(result)
		r := httptest.NewRequest("POST", "/internal/ratings/jobs/"+j.ID+"/accounts/71/result", strings.NewReader(string(raw)))
		r.SetPathValue("job", j.ID)
		r.SetPathValue("account", "71")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", config.ServiceKey)
		w := httptest.NewRecorder()
		srv.result(w, r)
		core.Close()
		want := 503
		if coreAvailable {
			want = 200
		}
		if w.Code != want || lockCalls.Load() != 1 {
			t.Fatal("core-lock failure did not block saving or success lost callback", w.Code, lockCalls.Load())
		}
	}
}

func TestCallbackRejectsMismatchedRevisionOrAccountBeforeCoreAndItems(t *testing.T) {
	for _, mismatchAccount := range []bool{false, true} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status = "running"
		mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\?").WithArgs(j.ID).WillReturnRows(jobRows(j))
		result := readyResult(j, 71)
		if mismatchAccount {
			result.AccountID++
		} else {
			result.Revision++
		}
		srv := &Server{Store: store, Config: Config{ServiceKey: strings.Repeat("k", 32)}}
		raw, _ := json.Marshal(result)
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(raw)))
		r.SetPathValue("job", j.ID)
		r.SetPathValue("account", strconv.FormatInt(71, 10))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", srv.Config.ServiceKey)
		w := httptest.NewRecorder()
		srv.result(w, r)
		if w.Code != 409 {
			t.Fatal("late/foreign callback reached the item store", w.Code)
		}
	}
}

func TestProctorReadUsesLiveGroupMembershipInsteadOfHistoricalProfiles(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status = "completed"
	group, otherGroup := int64(8), int64(9)
	profiles := []Account{
		{AccountID: 71, FullName: "First", GroupID: &group},
		{AccountID: 72, FullName: "Moved out", GroupID: &group},
		{AccountID: 73, FullName: "Moved in", GroupID: &otherGroup},
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT published_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=?")).WithArgs(j.OrganizationID, j.DivisionID).
		WillReturnRows(sqlmock.NewRows([]string{"published_job_id"}).AddRow(j.ID))
	mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\? AND organization_id=\\? AND division_id=\\? AND status='completed'").WithArgs(j.ID, j.OrganizationID, j.DivisionID).WillReturnRows(jobRows(j))
	rows := sqlmock.NewRows([]string{"profile", "output"})
	for i, p := range profiles {
		result := readyResult(j, p.AccountID)
		final := float64(10 * (i + 1))
		result.Final = &final
		profile, _ := json.Marshal(p)
		output, _ := json.Marshal(result)
		rows.AddRow(profile, output)
	}
	mock.ExpectQuery("SELECT profile,output FROM ratings_items_v1 WHERE job_id=\\? AND status='ready' ORDER BY account_id").WithArgs(j.ID).WillReturnRows(rows)
	mock.ExpectCommit()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request SnapshotRequest
		if r.URL.Path != "/internal/core/ratings/snapshot" || json.NewDecoder(r.Body).Decode(&request) != nil || request.OrganizationID != j.OrganizationID || request.DivisionID != j.DivisionID {
			t.Error("proctor live membership lookup lost its scope")
		}
		live := fixtureSnapshot(j).Core
		live.Accounts = []Account{{AccountID: 71, GroupID: &group}, {AccountID: 72, GroupID: &otherGroup}, {AccountID: 73, GroupID: &group}}
		reply(w, 200, live)
	}))
	defer core.Close()
	config := Config{CoreURL: core.URL, ServiceKey: strings.Repeat("k", 32)}
	srv := &Server{Store: store, Config: config, Deps: NewDependencies(config)}
	actor := Actor{AccountID: 44, OrganizationID: j.OrganizationID, DivisionID: &j.DivisionID, GroupID: &group, Role: "proctor"}
	_, output, e := srv.readPublished(httptest.NewRequest("GET", "/", nil), actor, j.DivisionID)
	if e != nil || len(output) != 2 || output[0].Account.AccountID != 73 || output[1].Account.AccountID != 71 || output[0].Place != 1 || output[1].Place != 3 {
		t.Fatal("historical group assignment leaked data or altered global ranking", e, output)
	}
}

func TestReadPublishedValidatesExplicitVersionBeforeLookingUpCurrentPublication(t *testing.T) {
	actor := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 11}
	srv := &Server{} // Invalid version must not require a database at all.
	_, _, err := srv.readPublished(httptest.NewRequest("GET", "/?version=invalid", nil), actor, 17)
	assertAPIError(t, err, 400, "invalid_version")

	store, mock := mockStore(t)
	srv.Store = store
	j := fixtureJob()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\? AND organization_id=\\? AND division_id=\\? AND status='completed'").WithArgs(j.ID, actor.OrganizationID, int64(17)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, _, err = srv.readPublished(httptest.NewRequest("GET", "/?version="+j.ID, nil), actor, 17)
	assertAPIError(t, err, 404, "not_found")
}

func TestReadPublishedDoesNotMaskScopeStorageFailureAsNeverCalculated(t *testing.T) {
	store, mock := mockStore(t)
	failure := errors.New("scope storage failure")
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT published_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=?")).WithArgs(int64(11), int64(17)).WillReturnError(failure)
	mock.ExpectRollback()
	srv := &Server{Store: store}
	_, _, err := srv.readPublished(httptest.NewRequest("GET", "/", nil), Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 11}, 17)
	if !errors.Is(err, failure) {
		t.Fatal("storage outage was presented as an empty unpublished rating", err)
	}
}
