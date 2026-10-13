package uploads

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const importExecuteRoute = "uploads.import.execute.v1"
const importRetryRoute = "uploads.import.retry.v1"
const importDeadRoute = "uploads.import.dead.v1"

// ImportEnvelope contains references and a pinned scope only. The worker obtains
// current authorization from Auth and content from the protected owner session.
type ImportEnvelope struct {
	MessageID     string        `json:"message_id"`
	MessageType   string        `json:"message_type"`
	SchemaVersion int           `json:"schema_version"`
	OccurredAt    time.Time     `json:"occurred_at"`
	Producer      string        `json:"producer"`
	CorrelationID string        `json:"correlation_id"`
	Payload       ImportCommand `json:"payload"`
}

type ImportCommand struct {
	Scope
	JobID           string `json:"job_id"`
	SessionID       string `json:"session_id"`
	Generation      int64  `json:"generation"`
	Type            string `json:"type"`
	Owner           string `json:"owner"`
	OwnerSessionID  string `json:"owner_session_id"`
	SourceHash      string `json:"source_hash"`
	PreviewRevision int64  `json:"preview_revision"`
	OwnerRevision   int64  `json:"owner_revision"`
}

var errInvalidDelivery = errors.New("invalid_import_event")
var errDeliveryBusy = errors.New("import_delivery_busy")
var errOutcomeNotSaved = errors.New("import_outcome_not_persisted")

func hexSize(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && len(s) == n*2
}

func decodeImport(body []byte) (ImportEnvelope, error) {
	var ev ImportEnvelope
	if len(body) > 8192 {
		return ev, errInvalidDelivery
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&ev) != nil || d.Decode(new(any)) != io.EOF {
		return ev, errInvalidDelivery
	}
	p := ev.Payload
	t, e := findType(p.Type)
	if e != nil || !t.Commit || t.Owner != p.Owner || !hexSize(ev.MessageID, 16) ||
		ev.MessageType != importExecuteRoute || ev.SchemaVersion != 1 || ev.Producer != "uploads" ||
		ev.OccurredAt.IsZero() || ev.CorrelationID != p.JobID || !hexSize(p.JobID, 16) ||
		!hexSize(p.SessionID, 16) || p.OrganizationID < 1 || p.DivisionID < 1 || p.ActorID < 1 ||
		p.Generation < 1 || p.PreviewRevision < 1 || p.OwnerRevision < 1 ||
		p.OwnerSessionID == "" || len(p.OwnerSessionID) > 128 || !hexSize(p.SourceHash, 32) {
		return ev, errInvalidDelivery
	}
	return ev, nil
}

type deliveryJob struct {
	ImportCommand
	Status     string
	Attempt    int
	Available  time.Time
	LeaseUntil sql.NullTime
}

// All delivery transitions lock job, then inbox. The owner invocation runs
// outside this transaction and retains the stable owner command key of the job.
func deliveryJobLocked(ctx context.Context, tx *sql.Tx, id string) (deliveryJob, error) {
	var j deliveryJob
	e := tx.QueryRowContext(ctx, `SELECT j.id,j.session_id,j.organization_id,j.division_id,j.actor_account_id,
	 j.type,j.owner,j.delivery_generation,j.status,j.attempt,j.available_at,j.lease_until,
	 s.owner_session_id,s.source_hash,s.revision,s.owner_revision
	 FROM uploads_jobs_v1 j JOIN uploads_sessions_v1 s ON s.id=j.session_id
	 AND s.organization_id=j.organization_id AND s.division_id=j.division_id AND s.actor_account_id=j.actor_account_id
	 AND s.type=j.type AND s.owner=j.owner WHERE j.id=? FOR UPDATE`, id).Scan(
		&j.JobID, &j.SessionID, &j.OrganizationID, &j.DivisionID, &j.ActorID, &j.Type, &j.Owner,
		&j.Generation, &j.Status, &j.Attempt, &j.Available, &j.LeaseUntil, &j.OwnerSessionID,
		&j.SourceHash, &j.PreviewRevision, &j.OwnerRevision)
	return j, e
}

func envelopeFor(p ImportCommand) ImportEnvelope {
	return ImportEnvelope{MessageID: uuid(), MessageType: importExecuteRoute, SchemaVersion: 1,
		OccurredAt: time.Now().UTC(), Producer: "uploads", CorrelationID: p.JobID, Payload: p}
}

func insertImportOutbox(ctx context.Context, tx *sql.Tx, ev ImportEnvelope, available time.Time) error {
	b, e := json.Marshal(ev)
	if e != nil {
		return e
	}
	if _, e = decodeImport(b); e != nil {
		return e
	}
	p := ev.Payload
	_, e = tx.ExecContext(ctx, `INSERT INTO uploads_outbox_v1
	 (id,job_id,generation,organization_id,division_id,actor_account_id,type,owner,route,body,available_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, ev.MessageID, p.JobID, p.Generation, p.OrganizationID, p.DivisionID, p.ActorID, p.Type, p.Owner, importExecuteRoute, b, available)
	return e
}

// EnqueueImport must be called in the same transaction that creates or manually
// retries the job. A new generation invalidates older deliveries; the owner
// commit key remains tied to jobID across all generations and restarts.
func EnqueueImport(ctx context.Context, tx *sql.Tx, jobID string, sc Scope) error {
	j, e := deliveryJobLocked(ctx, tx, jobID)
	if e != nil {
		return e
	}
	if j.Scope != sc || (j.Status != "queued" && j.Status != "retry") {
		return errInvalidDelivery
	}
	j.Generation++
	if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET delivery_generation=? WHERE id=?", j.Generation, jobID); e != nil {
		return e
	}
	return insertImportOutbox(ctx, tx, envelopeFor(j.ImportCommand), j.Available)
}

type deliveryClaim struct {
	Run      bool
	Token    string
	Attempts int
}

func (b *DeliveryBroker) claim(ctx context.Context, ev ImportEnvelope) (deliveryClaim, error) {
	var out deliveryClaim
	e := b.transact(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, ev.Payload.JobID)
		if errors.Is(e, sql.ErrNoRows) {
			return errInvalidDelivery
		}
		if e != nil {
			return e
		}
		p := ev.Payload
		// Compare the immutable owner reference as well as scope. The broker is
		// transport, not authority for selecting a different actor or target.
		if p.Scope != j.Scope || p.SessionID != j.SessionID || p.Type != j.Type || p.Owner != j.Owner ||
			p.OwnerSessionID != j.OwnerSessionID || p.SourceHash != j.SourceHash || p.PreviewRevision != j.PreviewRevision || p.OwnerRevision != j.OwnerRevision {
			return errInvalidDelivery
		}
		raw, _ := json.Marshal(ev)
		var trusted []byte
		if e = tx.QueryRowContext(ctx, "SELECT body FROM uploads_outbox_v1 WHERE id=? AND job_id=? AND generation=?", ev.MessageID, p.JobID, p.Generation).Scan(&trusted); errors.Is(e, sql.ErrNoRows) {
			return errInvalidDelivery
		}
		if e != nil {
			return e
		}
		var recorded ImportEnvelope
		if json.Unmarshal(trusted, &recorded) != nil || recorded != ev {
			return errInvalidDelivery
		}
		// Hash the canonical struct to tolerate MySQL JSON whitespace ordering.
		hash := digest(raw)
		_, e = tx.ExecContext(ctx, `INSERT INTO uploads_inbox_v1
		 (id,job_id,generation,organization_id,division_id,actor_account_id,type,owner,body_hash,state)
		 VALUES(?,?,?,?,?,?,?,?,?,'pending') ON DUPLICATE KEY UPDATE id=id`, ev.MessageID, p.JobID, p.Generation, p.OrganizationID, p.DivisionID, p.ActorID, p.Type, p.Owner, hash)
		if e != nil {
			return e
		}
		var state, savedHash string
		var lease sql.NullTime
		if e = tx.QueryRowContext(ctx, "SELECT state,body_hash,attempts,lease_until FROM uploads_inbox_v1 WHERE id=? FOR UPDATE", ev.MessageID).Scan(&state, &savedHash, &out.Attempts, &lease); e != nil {
			return e
		}
		if savedHash != hash {
			return errInvalidDelivery
		}
		if state == "done" || state == "dead" {
			return nil
		}
		if p.Generation != j.Generation || terminalDeliveryStatus(j.Status) {
			_, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='done',completed_at=UTC_TIMESTAMP(6),lease_token=NULL,lease_until=NULL WHERE id=?", ev.MessageID)
			return e
		}
		if (state == "processing" && lease.Valid && lease.Time.After(time.Now())) || (j.Status == "running" && j.LeaseUntil.Valid && j.LeaseUntil.Time.After(time.Now())) {
			return errDeliveryBusy
		}
		if j.Available.After(time.Now()) {
			return errDeliveryBusy
		}
		if j.Status == "running" {
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='retry',lease_token=NULL,lease_until=NULL WHERE id=?", p.JobID); e != nil {
				return e
			}
		}
		out.Token = uuid()
		out.Run = true
		out.Attempts++
		_, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='processing',attempts=attempts+1,lease_token=?,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 180 SECOND),error_code='' WHERE id=?", out.Token, ev.MessageID)
		return e
	})
	return out, e
}

func terminalDeliveryStatus(s string) bool {
	return s == "completed" || s == "failed" || s == "awaiting_authorization" || s == "cancelled" || s == "outcome_unknown" || s == "access_required" || s == "authorization_expired" || s == "revoked"
}

// Finish persists ACK eligibility and any next retry in one transaction.
func (b *DeliveryBroker) finish(ctx context.Context, ev ImportEnvelope, token string) error {
	return b.transact(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, ev.Payload.JobID)
		if e != nil {
			return e
		}
		var saved string
		if e = tx.QueryRowContext(ctx, "SELECT COALESCE(lease_token,'') FROM uploads_inbox_v1 WHERE id=? FOR UPDATE", ev.MessageID).Scan(&saved); e != nil {
			return e
		}
		if saved != token {
			return errDeliveryBusy
		}
		if j.Generation != ev.Payload.Generation || terminalDeliveryStatus(j.Status) {
			// Persisted domain/authorization failure is a visible outcome, not a
			// transport failure. It does not belong in the transport dead-letter queue.
		} else if j.Status == "retry" {
			next := envelopeFor(j.ImportCommand)
			if e = insertImportOutbox(ctx, tx, next, j.Available); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET route=? WHERE id=?", importRetryRoute, next.MessageID); e != nil {
				return e
			}
		} else {
			return errOutcomeNotSaved
		}
		_, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='done',completed_at=UTC_TIMESTAMP(6),lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?", ev.MessageID, token)
		return e
	})
}

// deferDelivery durably schedules re-delivery before Rabbit ACK. Reusing the
// message ID closes the publish/ACK loss window; completed inbox rows dedupe it.
func (b *DeliveryBroker) deferDelivery(ctx context.Context, ev ImportEnvelope, code string) error {
	return b.transact(ctx, func(tx *sql.Tx) error {
		j, e := deliveryJobLocked(ctx, tx, ev.Payload.JobID)
		if e != nil {
			return e
		}
		var state string
		var attempts int
		var lease sql.NullTime
		e = tx.QueryRowContext(ctx, "SELECT state,attempts,lease_until FROM uploads_inbox_v1 WHERE id=? FOR UPDATE", ev.MessageID).Scan(&state, &attempts, &lease)
		if errors.Is(e, sql.ErrNoRows) {
			attempts = 0
		} else if e != nil {
			return e
		}
		if state == "done" || state == "dead" {
			return nil
		}
		if terminalDeliveryStatus(j.Status) || j.Generation != ev.Payload.Generation {
			_, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='done',completed_at=UTC_TIMESTAMP(6),lease_token=NULL,lease_until=NULL WHERE id=?", ev.MessageID)
			return e
		}
		delay := deliveryBackoff(attempts)
		available := time.Now().UTC().Add(delay)
		if j.Available.After(available) {
			available = j.Available
		}
		if j.LeaseUntil.Valid && j.LeaseUntil.Time.After(available) {
			available = j.LeaseUntil.Time
		}
		// An active executor keeps its inbox lease. A duplicate must not clear
		// that lease, or it could allow concurrent execution after an ACK loss.
		if state == "processing" && code == "delivery_busy" && lease.Valid && lease.Time.After(time.Now()) {
			if lease.Time.After(available) {
				available = lease.Time
			}
		} else {
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='retry',lease_token=NULL,lease_until=NULL,error_code=? WHERE id=?", code, ev.MessageID); e != nil {
				return e
			}
		}
		route := importRetryRoute
		if attempts >= 20 && !terminalDeliveryStatus(j.Status) && !(j.Status == "running" && j.LeaseUntil.Valid && j.LeaseUntil.Time.After(time.Now())) {
			outcome := "failed"
			if b.AuthorizationEnabled {
				p, e := loadExecutionPlan(ctx, tx, j.Scope, j.JobID, true)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if e == nil && (p.ApplyStartedAt.Valid || p.GrantMode == "receipt") {
					outcome = "outcome_unknown"
				}
			}
			route = importDeadRoute
			available = time.Now().UTC()
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='dead',completed_at=UTC_TIMESTAMP(6),lease_token=NULL,lease_until=NULL,error_code='delivery_retry_exhausted' WHERE id=?", ev.MessageID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status=?,error_code='delivery_retry_exhausted',lease_token=NULL,lease_until=NULL WHERE id=?", outcome, j.JobID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_sessions_v1 SET state=? WHERE id=?", outcome, j.SessionID); e != nil {
				return e
			}
			t, _ := findType(j.Type)
			if e = event(ctx, tx, j.Scope, t, j.JobID, "job.delivery_retry_exhausted", j.PreviewRevision); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET route=?,published_at=NULL,lease_token=NULL,lease_until=NULL,available_at=? WHERE id=?", route, available, ev.MessageID)
		return e
	})
}

func deliveryBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6
	}
	return time.Duration(1<<n) * time.Second
}

// RecoverDelivery repairs only missing delivery proofs/expired leases. It never
// calls Execute: all recovered work must pass through Rabbit again. Published
// messages are replayed with their original ID so a late duplicate is harmless.
func (b *DeliveryBroker) RecoverDelivery(ctx context.Context) error {
	rows, e := b.DB.QueryContext(ctx, `SELECT id FROM uploads_jobs_v1 WHERE
	 (status='running' AND (lease_until IS NULL OR lease_until<UTC_TIMESTAMP(6))) OR
	 (status IN ('queued','retry') AND updated_at<DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 15 MINUTE))
	 ORDER BY updated_at LIMIT 30`)
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
		if e = b.transact(ctx, func(tx *sql.Tx) error {
			j, e := deliveryJobLocked(ctx, tx, id)
			if e != nil {
				return e
			}
			if terminalDeliveryStatus(j.Status) || (j.Status == "running" && j.LeaseUntil.Valid && j.LeaseUntil.Time.After(time.Now())) {
				return nil
			}
			if j.Status == "running" {
				if _, e = tx.ExecContext(ctx, "UPDATE uploads_jobs_v1 SET status='retry',lease_token=NULL,lease_until=NULL,available_at=UTC_TIMESTAMP(6) WHERE id=?", id); e != nil {
					return e
				}
			}
			var mid string
			var published sql.NullTime
			e = tx.QueryRowContext(ctx, "SELECT id,published_at FROM uploads_outbox_v1 WHERE job_id=? AND generation=? AND route<>? ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE", id, j.Generation, importDeadRoute).Scan(&mid, &published)
			if errors.Is(e, sql.ErrNoRows) {
				// Compatibility for dev jobs created before Rabbit integration. No
				// new user authorization is invented; Execute still checks Auth.
				return EnqueueImport(ctx, tx, id, j.Scope)
			}
			if e != nil {
				return e
			}
			if !published.Valid {
				return nil
			}
			var state string
			var lease sql.NullTime
			e = tx.QueryRowContext(ctx, "SELECT state,lease_until FROM uploads_inbox_v1 WHERE id=? FOR UPDATE", mid).Scan(&state, &lease)
			if !errors.Is(e, sql.ErrNoRows) && e != nil {
				return e
			}
			if state == "processing" && lease.Valid && lease.Time.After(time.Now()) {
				return nil
			}
			if state == "done" || state == "dead" {
				return nil
			}
			if _, e = tx.ExecContext(ctx, "UPDATE uploads_inbox_v1 SET state='retry',lease_token=NULL,lease_until=NULL,error_code='delivery_lease_recovered' WHERE id=?", mid); e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE uploads_outbox_v1 SET published_at=NULL,lease_token=NULL,lease_until=NULL,available_at=UTC_TIMESTAMP(6),route=? WHERE id=?", importRetryRoute, mid)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}
