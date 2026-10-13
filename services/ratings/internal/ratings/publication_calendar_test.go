package ratings

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPublishRejectsCalendarTransitionCrossedDuringPublicationLocks(t *testing.T) {
	for _, source := range []string{"homework", "exams"} {
		t.Run(source, func(t *testing.T) {
			store, mock := mockStore(t)
			j := fixtureJob()
			j.Status, j.Total = "validating", 1
			lease := "calendar-worker"
			j.LeaseToken = &lease
			snapshot := fixtureSnapshot(j)
			boundary := time.Now().UTC().Add(500 * time.Millisecond)
			if source == "homework" {
				snapshot.Homework.NextTransitionAt = &boundary
				snapshot.ExamsMode = "dev_empty"
			} else {
				snapshot.Exams.NextTransitionAt = &boundary
				snapshot.ExamsMode = "required"
				snapshot.Exams.SourceRevision = 7
			}
			if transitioned(snapshot, time.Now().UTC()) {
				t.Fatal("test snapshot already expired before the simulated SQL wait")
			}
			j.Snapshot, _ = json.Marshal(snapshot)
			mock.ExpectBegin()
			scope := mock.ExpectQuery(regexp.QuoteMeta("SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE")).WithArgs(j.OrganizationID, j.DivisionID)
			if source == "homework" {
				scope.WillDelayFor(1100 * time.Millisecond)
			}
			scope.WillReturnRows(sqlmock.NewRows([]string{"revision", "active_job_id"}).AddRow(j.Revision, j.ID))
			mock.ExpectQuery("SELECT .* FROM ratings_jobs_v1 WHERE id=\\? FOR UPDATE").WithArgs(j.ID).WillReturnRows(jobRows(j))
			mock.ExpectQuery("SELECT COUNT\\(\\*\\),COALESCE\\(SUM\\(status='ready'\\),0\\) FROM ratings_items_v1 WHERE job_id=\\?").WithArgs(j.ID).WillReturnRows(sqlmock.NewRows([]string{"count", "ready"}).AddRow(1, 1))
			if source == "exams" {
				mock.ExpectQuery("SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=\\? AND division_id=\\? FOR UPDATE").WithArgs(j.OrganizationID, j.DivisionID).WillDelayFor(1100 * time.Millisecond).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(7))
			}
			mock.ExpectRollback()
			assertAPIError(t, store.Publish(context.Background(), j), 409, "calendar_transition")
			// No publication pointer update or completion can happen after expiry.
		})
	}
}

func TestPublishAllowsUnexpiredCalendarSnapshotAfterAllFences(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status, j.Total = "validating", 1
	lease := "future-calendar-worker"
	j.LeaseToken = &lease
	snapshot := fixtureSnapshot(j)
	boundary := time.Now().UTC().Add(24 * time.Hour)
	snapshot.Homework.NextTransitionAt = &boundary
	snapshot.Exams.NextTransitionAt = &boundary
	snapshot.ExamsMode, snapshot.Exams.SourceRevision = "required", 7
	j.Snapshot, _ = json.Marshal(snapshot)
	expectPublicationLocks(mock, j, j.Revision, j)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\),COALESCE\\(SUM\\(status='ready'\\),0\\) FROM ratings_items_v1 WHERE job_id=\\?").WithArgs(j.ID).WillReturnRows(sqlmock.NewRows([]string{"count", "ready"}).AddRow(1, 1))
	mock.ExpectQuery("SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=\\? AND division_id=\\? FOR UPDATE").WithArgs(j.OrganizationID, j.DivisionID).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(7))
	mock.ExpectExec("UPDATE ratings_scopes_v1 SET published_job_id=").WithArgs(j.ID, sqlmock.AnyArg(), j.OrganizationID, j.DivisionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE ratings_jobs_v1 SET status='completed'").WithArgs(1, sqlmock.AnyArg(), j.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Publish(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}
