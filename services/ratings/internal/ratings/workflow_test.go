package ratings

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestTimedOutAttemptStillPersistsBoundedRetry(t *testing.T) {
	store, mock := mockStore(t)
	j := fixtureJob()
	token := uuid()
	j.LeaseToken = &token
	// A cancelled work context must not poison the independent state write.
	attempt, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	mock.ExpectExec("UPDATE ratings_jobs_v1 SET status=").WithArgs("waiting_sources", "job_processing_timeout", sqlmock.AnyArg(), j.ID, j.LeaseToken).WillDelayFor(time.Millisecond).WillReturnResult(sqlmock.NewResult(0, 1))
	worker := &Worker{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	worker.persistFailure(context.Background(), j, attempt.Err())
}

func TestServiceShutdownLeavesLeaseForRecovery(t *testing.T) {
	store, _ := mockStore(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	worker := &Worker{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// There must be no SQL state mutation after the owning process is stopping.
	worker.persistFailure(parent, fixtureJob(), context.Canceled)
}
