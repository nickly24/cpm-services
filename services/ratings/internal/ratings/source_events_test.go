package ratings

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func sourceEventFixture() SourceChange {
	var event SourceChange
	event.MessageID = uuid()
	event.CorrelationID = uuid()
	event.MessageType = examSourceRoute
	event.Producer = "exams-service"
	event.SchemaVersion = 1
	event.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	event.Payload.OrganizationID = 2001
	event.Payload.DivisionID = 2002
	event.Payload.ExamID = 42
	event.Payload.ExamRevision = 3
	event.Payload.SourceRevision = 7
	event.Payload.Reason = "vote"
	return event
}

func TestSourceEventsRejectForgedScopeIdentityAndUnsupportedContracts(t *testing.T) {
	event := sourceEventFixture()
	raw, _ := json.Marshal(event)
	if _, _, err := decodeSourceChange(event.MessageID, raw); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*SourceChange){
		func(e *SourceChange) { e.Payload.OrganizationID = 0 }, func(e *SourceChange) { e.Payload.DivisionID = 0 }, func(e *SourceChange) { e.Payload.SourceRevision = 0 },
		func(e *SourceChange) { e.Producer = "tests-service" }, func(e *SourceChange) { e.MessageType = "other" }, func(e *SourceChange) { e.SchemaVersion = 2 }, func(e *SourceChange) { e.OccurredAt = "yesterday" },
	} {
		invalid := event
		mutate(&invalid)
		body, _ := json.Marshal(invalid)
		if _, _, err := decodeSourceChange(invalid.MessageID, body); err == nil {
			t.Fatal("invalid source contract accepted", invalid)
		}
	}
	if _, _, err := decodeSourceChange(uuid(), raw); err == nil {
		t.Fatal("AMQP identity disagreed with envelope")
	}
	duplicate := append(raw[:len(raw)-1], []byte(`,"schema_version":1}`)...)
	if _, _, err := decodeSourceChange(event.MessageID, duplicate); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
}

func expectSourceInbox(mock sqlmock.Sqlmock, event SourceChange, h string) {
	p := event.Payload
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO ratings_source_events_v1").WithArgs(event.MessageID, h, p.OrganizationID, p.DivisionID, p.SourceRevision, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT payload_hash FROM ratings_source_events_v1 WHERE message_id=\\? FOR UPDATE").WithArgs(event.MessageID).WillReturnRows(sqlmock.NewRows([]string{"payload_hash"}).AddRow(h))
}

func TestSourceEventsCommitDedupAndMonotonicRevisionInExactOrganizationDivision(t *testing.T) {
	store, mock := mockStore(t)
	event := sourceEventFixture()
	// Duplicate deliveries and older revisions use the same GREATEST update;
	// neither inserts a rating job nor touches any other scope's pointer.
	for _, revision := range []int64{7, 7, 3} {
		event.Payload.SourceRevision = revision
		expectSourceInbox(mock, event, "canonical-hash")
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ratings_source_revisions_v1(source,organization_id,division_id,revision,updated_at) VALUES('exams',?,?,?,?) ON DUPLICATE KEY UPDATE updated_at=IF(VALUES(revision)>revision,VALUES(updated_at),updated_at),revision=GREATEST(revision,VALUES(revision))")).WithArgs(int64(2001), int64(2002), revision, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := store.ApplySourceChange(context.Background(), event, "canonical-hash"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceEventIdentityConflictAndStorageFailureCannotACK(t *testing.T) {
	store, mock := mockStore(t)
	event := sourceEventFixture()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO ratings_source_events_v1").WithArgs(event.MessageID, "new-hash", event.Payload.OrganizationID, event.Payload.DivisionID, event.Payload.SourceRevision, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT payload_hash FROM ratings_source_events_v1 WHERE message_id=\\? FOR UPDATE").WithArgs(event.MessageID).WillReturnRows(sqlmock.NewRows([]string{"payload_hash"}).AddRow("old-hash"))
	mock.ExpectRollback()
	assertAPIError(t, store.ApplySourceChange(context.Background(), event, "new-hash"), 409, "source_event_identity_conflict")
}

func TestSourceEventStorageFailureRollsBackInboxAndRevision(t *testing.T) {
	store, mock := mockStore(t)
	event := sourceEventFixture()
	expectSourceInbox(mock, event, "hash")
	mock.ExpectExec("INSERT INTO ratings_source_revisions_v1").WillReturnError(errors.New("storage unavailable"))
	mock.ExpectRollback()
	if err := store.ApplySourceChange(context.Background(), event, "hash"); err == nil {
		t.Fatal("uncommitted event could be acknowledged")
	}
}

func TestFreshnessUsesSourceEventWithoutReadingUnavailableOwners(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	snap := fixtureSnapshot(j)
	snap.ExamsMode = "required"
	snap.Exams.SourceRevision = 7
	j.Snapshot, _ = json.Marshal(snap)
	mock.ExpectQuery("SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=\\? AND division_id=\\?").WithArgs(j.OrganizationID, j.DivisionID).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(8))
	fresh := (&Server{Store: store}).freshness(context.Background(), j)
	if fresh["is_stale"] != true || fresh["reason"] != "source_changed" || fresh["version_id"] != j.ID {
		t.Fatal(fresh)
	}
}

func TestPublishFencesSourceEventCommittedAfterLastHTTPSnapshot(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	j.Status = "validating"
	j.Total = 1
	lease := "worker"
	j.LeaseToken = &lease
	snap := fixtureSnapshot(j)
	snap.ExamsMode = "required"
	snap.Exams.SourceRevision = 7
	j.Snapshot, _ = json.Marshal(snap)
	expectPublicationLocks(mock, j, j.Revision, j)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\),COALESCE\\(SUM\\(status='ready'\\),0\\) FROM ratings_items_v1 WHERE job_id=\\?").WithArgs(j.ID).WillReturnRows(sqlmock.NewRows([]string{"count", "ready"}).AddRow(1, 1))
	mock.ExpectQuery("SELECT revision FROM ratings_source_revisions_v1 WHERE source='exams' AND organization_id=\\? AND division_id=\\? FOR UPDATE").WithArgs(j.OrganizationID, j.DivisionID).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(8))
	mock.ExpectRollback()
	assertAPIError(t, store.Publish(context.Background(), j), 409, "source_changed_before_publication")
}
