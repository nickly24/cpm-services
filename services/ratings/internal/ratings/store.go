package ratings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Store struct{ DB *sql.DB }

const jobColumns = "id,organization_id,division_id,revision,status,DATE_FORMAT(date_from,'%Y-%m-%d'),DATE_FORMAT(date_to,'%Y-%m-%d'),as_of,created_by,total_count,processed_count,error_code,attempts,created_at,completed_at,snapshot,source_hash,lease_token"

type scanner interface{ Scan(...any) error }

func scanJob(r scanner) (Job, error) {
	var j Job
	var raw []byte
	e := r.Scan(&j.ID, &j.OrganizationID, &j.DivisionID, &j.Revision, &j.Status, &j.DateFrom, &j.DateTo, &j.AsOf, &j.CreatedBy, &j.Total, &j.Processed, &j.Error, &j.Attempts, &j.CreatedAt, &j.CompletedAt, &raw, &j.SourceHash, &j.LeaseToken)
	j.Snapshot = raw
	if errors.Is(e, sql.ErrNoRows) {
		return j, missing
	}
	return j, e
}
func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	return scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE id=?", id))
}
func (s *Store) Create(ctx context.Context, a Actor, div int64, from, to, key string) (Job, bool, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, false, e
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	_, e = tx.ExecContext(ctx, "INSERT IGNORE INTO ratings_scopes_v1(organization_id,division_id,updated_at) VALUES(?,?,?)", a.OrganizationID, div, now)
	if e != nil {
		return Job{}, false, e
	}
	var rev int64
	var active *string
	if e = tx.QueryRowContext(ctx, "SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE", a.OrganizationID, div).Scan(&rev, &active); e != nil {
		return Job{}, false, e
	}
	requestHash := hash([]string{from, to})
	var previousID, previousHash string
	e = tx.QueryRowContext(ctx, "SELECT id,request_hash FROM ratings_jobs_v1 WHERE organization_id=? AND division_id=? AND created_by=? AND idempotency_key=?", a.OrganizationID, div, a.AccountID, key).Scan(&previousID, &previousHash)
	if e == nil {
		if previousHash != requestHash {
			return Job{}, false, conflict("idempotency_conflict")
		}
		j, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE id=?", previousID))
		return j, false, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Job{}, false, e
	}
	if active != nil {
		return Job{}, false, conflict("rating_job_active")
	}
	id := uuid()
	_, e = tx.ExecContext(ctx, "INSERT INTO ratings_jobs_v1(id,organization_id,division_id,revision,status,date_from,date_to,as_of,created_by,idempotency_key,request_hash,next_attempt_at,created_at) VALUES(?,?,?,?,'preparing',?,?,?,?,?,?,?,?)", id, a.OrganizationID, div, rev+1, from, to, now, a.AccountID, key, requestHash, now, now)
	if e != nil {
		return Job{}, false, e
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_scopes_v1 SET revision=?,active_job_id=?,updated_at=? WHERE organization_id=? AND division_id=?", rev+1, id, now, a.OrganizationID, div)
	if e != nil {
		return Job{}, false, e
	}
	if e = tx.Commit(); e != nil {
		return Job{}, false, e
	}
	j, e := s.Job(ctx, id)
	return j, true, e
}
func (s *Store) Claim(ctx context.Context) (Job, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	j, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE status IN ('preparing','waiting_sources','queued','running','validating') AND next_attempt_at<=? AND (lease_until IS NULL OR lease_until<?) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED", now, now))
	if e != nil {
		return j, e
	}
	token := uuid()
	_, e = tx.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET lease_token=?,lease_until=? WHERE id=?", token, now.Add(90*time.Second), j.ID)
	if e != nil {
		return j, e
	}
	if e = tx.Commit(); e != nil {
		return j, e
	}
	j.LeaseToken = &token
	return j, nil
}
func (s *Store) Yield(ctx context.Context, j Job, status, reason string, delay time.Duration) error {
	res, e := s.DB.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET status=?,error_code=?,next_attempt_at=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=? AND status NOT IN ('completed','failed','cancelled','superseded')", status, reason, time.Now().UTC().Add(delay), j.ID, j.LeaseToken)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return conflict("job_lease_lost")
	}
	return nil
}
func (s *Store) Finish(ctx context.Context, j Job, status, reason string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var org int64
	if e = tx.QueryRowContext(ctx, "SELECT organization_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE", j.OrganizationID, j.DivisionID).Scan(&org); e != nil {
		return e
	}
	var current string
	var token *string
	e = tx.QueryRowContext(ctx, "SELECT status,lease_token FROM ratings_jobs_v1 WHERE id=? FOR UPDATE", j.ID).Scan(&current, &token)
	if e != nil {
		return e
	}
	if terminal(current) {
		return nil
	}
	if j.LeaseToken != nil && (token == nil || *token != *j.LeaseToken) {
		return conflict("job_lease_lost")
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET status=?,error_code=?,completed_at=?,lease_token=NULL,lease_until=NULL WHERE id=?", status, reason, time.Now().UTC(), j.ID)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_scopes_v1 SET active_job_id=NULL WHERE organization_id=? AND division_id=? AND active_job_id=?", j.OrganizationID, j.DivisionID, j.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func terminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "superseded"
}
func (s *Store) Prepare(ctx context.Context, j Job, snap Snapshot) error {
	// Validate every input before beginning writes, without retaining N copies
	// of the source catalogues in memory.
	raw, e := json.Marshal(snap)
	if e != nil || len(raw) > MaxSnapshotBytes {
		return bad("snapshot_too_large")
	}
	// Core's full roster also scopes access to historical publications. Keep
	// that source snapshot intact, but calculate a new version for active members.
	accounts := make([]Account, 0, len(snap.Core.Accounts))
	for _, a := range snap.Core.Accounts {
		if a.IsActive {
			accounts = append(accounts, a)
		}
	}
	for _, a := range accounts {
		in, e := personal(j, snap, a)
		if e != nil {
			return e
		}
		b, _ := json.Marshal(in)
		if len(b) > 2<<20 {
			return bad("personal_snapshot_too_large")
		}
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var status string
	var token *string
	if e = tx.QueryRowContext(ctx, "SELECT status,lease_token FROM ratings_jobs_v1 WHERE id=? FOR UPDATE", j.ID).Scan(&status, &token); e != nil {
		return e
	}
	if terminal(status) || token == nil || j.LeaseToken == nil || *token != *j.LeaseToken {
		return conflict("job_lease_lost")
	}
	now := time.Now().UTC()
	for _, a := range accounts {
		in, e := personal(j, snap, a)
		if e != nil {
			return e
		}
		b, _ := json.Marshal(in)
		if len(b) > 2<<20 {
			return bad("personal_snapshot_too_large")
		}
		p, _ := json.Marshal(a)
		mid := uuid()
		_, e = tx.ExecContext(ctx, "INSERT INTO ratings_items_v1(job_id,account_id,input_hash,input,profile,message_id,created_at) VALUES(?,?,?,?,?,?,?)", j.ID, in.AccountID, in.InputHash, b, p, mid, now)
		if e != nil {
			return e
		}
		event := map[string]any{"message_id": mid, "message_type": "ratings.calculate.requested", "schema_version": 1, "occurred_at": now.Format(time.RFC3339Nano), "producer": "ratings-service", "correlation_id": j.ID, "payload": map[string]any{"job_id": j.ID, "revision": j.Revision, "organization_id": j.OrganizationID, "division_id": j.DivisionID, "account_id": in.AccountID, "input_hash": in.InputHash}}
		body, _ := json.Marshal(event)
		_, e = tx.ExecContext(ctx, "INSERT INTO ratings_outbox_v1(message_id,job_id,body,next_attempt_at,created_at) VALUES(?,?,?,?,?)", mid, j.ID, body, now, now)
		if e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET status='queued',snapshot=?,source_hash=?,total_count=?,error_code='',next_attempt_at=?,lease_token=NULL,lease_until=NULL WHERE id=?", raw, fingerprint(snap), len(accounts), now, j.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Publish(ctx context.Context, j Job) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var rev int64
	var active *string
	if e = tx.QueryRowContext(ctx, "SELECT revision,active_job_id FROM ratings_scopes_v1 WHERE organization_id=? AND division_id=? FOR UPDATE", j.OrganizationID, j.DivisionID).Scan(&rev, &active); e != nil {
		return e
	}
	current, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM ratings_jobs_v1 WHERE id=? FOR UPDATE", j.ID))
	if e != nil {
		return e
	}
	if rev != j.Revision || active == nil || *active != j.ID || terminal(current.Status) || current.LeaseToken == nil || j.LeaseToken == nil || *current.LeaseToken != *j.LeaseToken {
		return conflict("stale_rating_job")
	}
	var count, ready int
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(status='ready'),0) FROM ratings_items_v1 WHERE job_id=?", j.ID).Scan(&count, &ready); e != nil {
		return e
	}
	if count != j.Total || ready != j.Total {
		return conflict("rating_staging_incomplete")
	}
	var snapshot Snapshot
	if len(current.Snapshot) > 0 {
		if json.Unmarshal(current.Snapshot, &snapshot) != nil {
			return unavailable
		}
		if snapshot.ExamsMode == "required" {
			revision, err := sourceRevision(ctx, tx, j, true)
			if err != nil {
				return err
			}
			if revision > snapshot.Exams.SourceRevision {
				return conflict("source_changed_before_publication")
			}
		}
	}
	now := time.Now().UTC()
	// SQL lock waits may cross a homework midnight or an exam start after the
	// worker's remote validation. Check the saved snapshot again with the time
	// used for this publication, after every publication fence has been locked.
	if len(current.Snapshot) > 0 && transitioned(snapshot, now) {
		return conflict("calendar_transition")
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_scopes_v1 SET published_job_id=?,active_job_id=NULL,updated_at=? WHERE organization_id=? AND division_id=?", j.ID, now, j.OrganizationID, j.DivisionID)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "UPDATE ratings_jobs_v1 SET status='completed',processed_count=?,completed_at=?,error_code='',lease_token=NULL,lease_until=NULL WHERE id=?", ready, now, j.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
