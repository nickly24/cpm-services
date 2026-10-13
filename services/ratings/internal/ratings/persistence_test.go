package ratings

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func mockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := mock.ExpectationsWereMet(); e != nil {
			t.Error(e)
		}
		_ = db.Close()
	})
	return &Store{DB: db}, mock
}

func jobRows(j Job) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "organization_id", "division_id", "revision", "status", "date_from", "date_to", "as_of", "created_by", "total_count", "processed_count", "error_code", "attempts", "created_at", "completed_at", "snapshot", "source_hash", "lease_token"}).
		AddRow(j.ID, j.OrganizationID, j.DivisionID, j.Revision, j.Status, j.DateFrom, j.DateTo, j.AsOf, j.CreatedBy, j.Total, j.Processed, j.Error, j.Attempts, j.CreatedAt, j.CompletedAt, []byte(j.Snapshot), j.SourceHash, j.LeaseToken)
}

func expectResultLocks(mock sqlmock.Sqlmock, j Job, scopeRevision int64, active *string, itemStatus string, oldHash *string) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE")).WithArgs(j.OrganizationID, j.DivisionID).
		WillReturnRows(sqlmock.NewRows([]string{"revision", "active_job_id"}).AddRow(scopeRevision, active))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM ratings_jobs_v1 WHERE id=? FOR UPDATE")).WithArgs(j.ID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(j.Status))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status,output_hash FROM ratings_items_v1 WHERE job_id=? AND account_id=? FOR UPDATE")).WithArgs(j.ID, int64(71)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "output_hash"}).AddRow(itemStatus, oldHash))
}

func TestSaveResultDuplicateDoesNotIncrementProgressAndConflictingRepeatIsRejected(t *testing.T) {
	for _, same := range []bool{true, false} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status = "running"
		oldHash := "already-saved-hash"
		expectResultLocks(mock, j, j.Revision, &j.ID, "ready", &oldHash)
		mock.ExpectRollback()
		incoming := oldHash
		if !same {
			incoming = "different-hash"
		}
		e := store.SaveResult(context.Background(), j, 71, "ready", "", []byte(`{"result":0}`), incoming)
		if same {
			if e != nil {
				t.Fatal("identical redelivery was not acknowledged", e)
			}
		} else {
			assertAPIError(t, e, 409, "result_idempotency_conflict")
		}
		// No UPDATE or COMMIT is expected for either repeat.
	}
}

func TestSaveResultCannotWriteAcrossNewerScopeRevisionOrClosedJob(t *testing.T) {
	for _, change := range []func(*Job, *int64, **string){
		func(_ *Job, rev *int64, _ **string) { *rev++ },
		func(_ *Job, _ *int64, active **string) { *active = nil },
		func(j *Job, _ *int64, _ **string) { j.Status = "cancelled" },
		func(_ *Job, _ *int64, active **string) {
			other := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			*active = &other
		},
	} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status = "running"
		revision := j.Revision
		active := &j.ID
		change(&j, &revision, &active)
		expectResultLocks(mock, j, revision, active, "queued", nil)
		mock.ExpectRollback()
		e := store.SaveResult(context.Background(), j, 71, "ready", "", []byte(`{}`), "new-hash")
		assertAPIError(t, e, 409, "stale_rating_job")
	}
}

func TestSaveResultAtomicallyStoresOutputAndAdvancesOneItem(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status = "running"
	raw := []byte(`{"result":0}`)
	expectResultLocks(mock, j, j.Revision, &j.ID, "queued", nil)
	mock.ExpectExec("UPDATE ratings_items_v1 SET status=").WithArgs("ready", raw, "new-hash", "", sqlmock.AnyArg(), j.ID, int64(71)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE ratings_jobs_v1 SET processed_count=processed_count\\+1").WithArgs(sqlmock.AnyArg(), j.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if e := store.SaveResult(context.Background(), j, 71, "ready", "", raw, "new-hash"); e != nil {
		t.Fatal("result and progress did not commit together", e)
	}
}

func TestPreparePendingTestCreatesNoItemsOrOutbox(t *testing.T) {
	store, _ := mockStore(t)
	j := fixtureJob()
	s := fixtureSnapshot(j)
	s.Tests.Tests = []Test{{ID: "pending", Results: []TestResult{{AccountID: 71, Status: "queued"}}}}
	e := store.Prepare(context.Background(), j, s)
	assertAPIError(t, e, 409, "tests_calculation_pending")
	// The pending check precedes BeginTx, so no persistence expectations exist.
}

func expectPublicationLocks(mock sqlmock.Sqlmock, j Job, scopeRevision int64, current Job) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE")).WithArgs(j.OrganizationID, j.DivisionID).
		WillReturnRows(sqlmock.NewRows([]string{"revision", "active_job_id"}).AddRow(scopeRevision, j.ID))
	mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\? FOR UPDATE").WithArgs(j.ID).WillReturnRows(jobRows(current))
}

func TestPublishCannotReplacePointerWithIncompleteOrOlderCalculation(t *testing.T) {
	for _, older := range []bool{false, true} {
		store, mock := mockStore(t)
		j := fixtureJob()
		j.Status, j.Total = "validating", 3
		lease := "leased-worker"
		j.LeaseToken = &lease
		revision := j.Revision
		if older {
			revision++
		}
		expectPublicationLocks(mock, j, revision, j)
		if !older {
			mock.ExpectQuery("SELECT COUNT\\(\\*\\),COALESCE\\(SUM\\(status='ready'\\),0\\) FROM ratings_items_v1 WHERE job_id=\\?").WithArgs(j.ID).
				WillReturnRows(sqlmock.NewRows([]string{"count", "ready"}).AddRow(3, 2))
		}
		mock.ExpectRollback()
		e := store.Publish(context.Background(), j)
		name := "rating_staging_incomplete"
		if older {
			name = "stale_rating_job"
		}
		assertAPIError(t, e, 409, name)
		// The pointer and completion state cannot be updated in these branches.
	}
}

func TestPublishSwitchesCompleteVersionAndJobStateInOneTransaction(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status, j.Total = "validating", 2
	lease := "leased-worker"
	j.LeaseToken = &lease
	expectPublicationLocks(mock, j, j.Revision, j)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\),COALESCE\\(SUM\\(status='ready'\\),0\\) FROM ratings_items_v1 WHERE job_id=\\?").WithArgs(j.ID).
		WillReturnRows(sqlmock.NewRows([]string{"count", "ready"}).AddRow(2, 2))
	mock.ExpectExec("UPDATE ratings_scopes_v1 SET published_job_id=").WithArgs(j.ID, sqlmock.AnyArg(), j.OrganizationID, j.DivisionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE ratings_jobs_v1 SET status='completed'").WithArgs(2, sqlmock.AnyArg(), j.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if e := store.Publish(context.Background(), j); e != nil {
		t.Fatal("complete version did not publish atomically", e)
	}
}

func TestFreshnessRechecksPublishedJobScopeAndOwnerFingerprint(t *testing.T) {
	j := fixtureJob()
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		if request.OrganizationID != j.OrganizationID || request.DivisionID != j.DivisionID || request.DateFrom != j.DateFrom || request.DateTo != j.DateTo || request.AsOf != j.AsOf.Format(time.RFC3339Nano) {
			t.Error("freshness source request escaped the published scope/period")
		}
		return fixtureSnapshot(sourceJob(request))
	}, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "required", server.URL)
	s, e := deps.Collect(context.Background(), j)
	if e != nil {
		t.Fatal(e)
	}
	j.Snapshot, _ = json.Marshal(s)
	h := fingerprint(s)
	j.SourceHash = &h
	srv := &Server{Deps: deps}
	fresh := srv.freshness(context.Background(), j)
	if fresh["is_stale"] != false || fresh["version_id"] != j.ID {
		t.Fatal("matching source vector was marked stale", fresh)
	}
	changed := "different-published-source-vector"
	j.SourceHash = &changed
	stale := srv.freshness(context.Background(), j)
	if stale["is_stale"] != true || stale["reason"] != "source_changed" {
		t.Fatal("changed owner inputs were declared fresh", stale)
	}
}
