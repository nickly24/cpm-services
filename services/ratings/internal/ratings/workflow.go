package ratings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	Store *Store
	Deps  *Dependencies
	Log   *slog.Logger
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		j, e := w.Store.Claim(ctx)
		if e == nil {
			w.process(ctx, j)
		} else if !errors.Is(e, missing) && ctx.Err() == nil {
			w.Log.Warn("ratings job claim failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *Worker) process(parent context.Context, j Job) {
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				res, e := w.Store.DB.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET lease_until=? WHERE id=? AND lease_token=? AND status NOT IN ('completed','failed','cancelled','superseded')", time.Now().UTC().Add(90*time.Second), j.ID, j.LeaseToken)
				if e != nil {
					cancel()
					return
				}
				n, _ := res.RowsAffected()
				if n == 0 {
					cancel()
					return
				}
			}
		}
	}()
	var e error
	if len(j.Snapshot) == 0 {
		var first, second Snapshot
		first, e = w.Deps.Collect(ctx, j)
		if e == nil {
			second, e = w.Deps.Collect(ctx, j)
		}
		if e == nil && fingerprint(first) != fingerprint(second) {
			e = conflict("rating_source_changed")
		}
		if e == nil && transitioned(second, time.Now().UTC()) {
			e = conflict("calendar_transition")
		}
		if e == nil {
			e = w.Store.Prepare(ctx, j, second)
		}
	} else {
		var total, ready, failed int
		e = w.Store.DB.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(status='ready'),0),COALESCE(SUM(status='failed'),0) FROM ratings_items_v1 WHERE job_id=?", j.ID).Scan(&total, &ready, &failed)
		if e == nil && failed > 0 {
			e = w.Store.Finish(ctx, j, "failed", "personal_calculation_failed")
			return
		}
		if e == nil && total != j.Total {
			e = w.Store.Finish(ctx, j, "failed", "rating_staging_incomplete")
			return
		}
		if e == nil && ready == total {
			var snap Snapshot
			if json.Unmarshal(j.Snapshot, &snap) != nil {
				e = w.Store.Finish(ctx, j, "failed", "source_snapshot_invalid")
				return
			}
			var current Snapshot
			current, e = w.Deps.Collect(ctx, j)
			if e == nil && (j.SourceHash == nil || *j.SourceHash != fingerprint(current) || transitioned(snap, time.Now().UTC())) {
				e = w.Store.Finish(ctx, j, "superseded", "source_changed_before_publication")
				return
			}
			if e == nil {
				e = w.Store.Publish(ctx, j)
			}
		} else if e == nil {
			if time.Since(j.CreatedAt) > 30*time.Minute {
				e = w.Store.Finish(ctx, j, "failed", "calculation_timeout")
				return
			}
			e = w.reconcile(ctx, j)
			if e == nil {
				e = w.Store.Yield(ctx, j, "running", "", 3*time.Second)
			}
		}
	}
	if e != nil {
		w.persistFailure(parent, j, e)
	}
}

func (w *Worker) persistFailure(parent context.Context, j Job, e error) {
	if parent.Err() == nil {
		// The processing deadline/heartbeat may already have cancelled its context.
		// Persist retries under a fresh deadline while retaining the lease fence.
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		reason := errorName(e)
		if errors.Is(e, context.DeadlineExceeded) {
			reason = "job_processing_timeout"
		}
		if reason == "job_lease_lost" || reason == "stale_rating_job" {
			return
		}
		state := "waiting_sources"
		if len(j.Snapshot) > 0 {
			state = "validating"
		}
		if reason == "tests_calculation_pending" || reason == "exams_not_connected" || reason == "external_tests_not_connected" || reason == "rating_source_changed" {
			_ = w.Store.Yield(ctx, j, state, reason, 5*time.Second)
			return
		}
		if reason == "calendar_transition" || reason == "source_changed_before_publication" {
			_ = w.Store.Finish(ctx, j, "superseded", reason)
			return
		}
		if j.Attempts >= 7 {
			_ = w.Store.Finish(ctx, j, "failed", reason)
			return
		}
		_ = w.Store.retryFailure(ctx, j, state, reason)
		w.Log.Warn("ratings job retry", "job_id", j.ID, "reason", reason)
	}
}
func (s *Store) retryFailure(ctx context.Context, j Job, status, reason string) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET status=?,error_code=?,attempts=attempts+1,next_attempt_at=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=? AND status NOT IN ('completed','failed','cancelled','superseded')", status, reason, time.Now().UTC().Add(time.Duration(1<<min(j.Attempts, 5))*time.Second), j.ID, j.LeaseToken)
	return e
}
func (w *Worker) reconcile(ctx context.Context, j Job) error {
	rows, e := w.Store.DB.QueryContext(ctx, "SELECT o.message_id FROM ratings_outbox_v1 o JOIN ratings_items_v1 i ON i.message_id=o.message_id WHERE o.job_id=? AND i.status='queued' AND o.delivered_at<? AND o.next_attempt_at<=? LIMIT 20", j.ID, time.Now().UTC().Add(-60*time.Second), time.Now().UTC())
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		_ = w.Deps.Call(ctx, w.Deps.Config.CalculateURL, "POST", "/internal/calculate/ratings/messages/"+id+"/retry", struct{}{}, nil)
		_, e = w.Store.DB.ExecContext(ctx, "UPDATE ratings_outbox_v1 SET delivered_at=NULL,next_attempt_at=? WHERE message_id=? AND delivered_at IS NOT NULL", time.Now().UTC().Add(5*time.Second), id)
		if e != nil {
			return e
		}
	}
	return nil
}
