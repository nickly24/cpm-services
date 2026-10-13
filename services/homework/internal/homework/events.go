package homework

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/minio/minio-go/v7"
	"io"
	"net/http"
	"time"
)

const commandRoute = "files.pdf.process.requested.v1"
const resultRoute = "homework.pdf.completed.v1"

// Every mutation takes the assignment, submission, then job lock. Public
// cancellation, worker completion and recovery share that order.
func (s *Server) lockInternalJob(ctx context.Context, tx *sql.Tx, jid string) (map[string]any, map[string]any, map[string]any, error) {
	pre, e := one(ctx, tx, "SELECT assignment_id,submission_id FROM hw_jobs WHERE id=?", jid)
	if e != nil {
		return nil, nil, nil, e
	}
	h, e := one(ctx, tx, "SELECT * FROM hw_assignments WHERE id=? FOR UPDATE", pre["assignment_id"])
	if e != nil {
		return nil, nil, nil, e
	}
	sub, e := one(ctx, tx, "SELECT * FROM hw_submissions WHERE id=? FOR UPDATE", pre["submission_id"])
	if e != nil {
		return nil, nil, nil, e
	}
	j, e := one(ctx, tx, "SELECT * FROM hw_jobs WHERE id=? FOR UPDATE", jid)
	return h, sub, j, e
}
func currentJob(h, sub, j map[string]any) bool {
	return h["deleted_at"] == nil && str(sub["state"]) != "deleted" && num(sub["generation"]) == num(j["generation"]) && editable(sub)
}
func (s *Server) claimJob(w http.ResponseWriter, r *http.Request) {
	if !s.internal(r) {
		fail(w, denied)
		return
	}
	var in struct {
		Generation int `json:"generation"`
	}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	jid := r.PathValue("job")
	if !validUUID(jid) || in.Generation < 1 {
		fail(w, bad("invalid_job"))
		return
	}
	out := map[string]any{"run": false}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		h, sub, j, e := s.lockInternalJob(r.Context(), tx, jid)
		if e == missing {
			return nil
		}
		if e != nil {
			return e
		}
		if in.Generation != int(num(j["generation"])) || !currentJob(h, sub, j) {
			return nil
		}
		status := str(j["status"])
		if status != "queued" && status != "running" && status != "retry" {
			return nil
		}
		if status == "running" {
			return conflict("job_busy")
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			if asError(e).Code == 409 {
				return nil
			}
			return e
		}
		token := uuid()
		_, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='running',stage='compressing',progress=20,lease_token=?,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 300 SECOND),attempts=attempts+1 WHERE id=?", token, jid)
		if e != nil {
			return e
		}
		out = map[string]any{"run": true, "claim_token": token}
		return nil
	})
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": out})
}
func (s *Server) releaseJob(w http.ResponseWriter, r *http.Request) {
	if !s.internal(r) {
		fail(w, denied)
		return
	}
	var in struct {
		Generation int    `json:"generation"`
		ClaimToken string `json:"claim_token"`
	}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if !validUUID(r.PathValue("job")) || !validUUID(in.ClaimToken) || in.Generation < 1 {
		fail(w, bad("invalid_job"))
		return
	}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		_, _, j, e := s.lockInternalJob(r.Context(), tx, r.PathValue("job"))
		if e == missing {
			return nil
		}
		if e != nil {
			return e
		}
		if str(j["status"]) != "running" || num(j["generation"]) != int64(in.Generation) || str(j["lease_token"]) != in.ClaimToken {
			return nil
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='queued',stage='queued',lease_until=NULL,lease_token=NULL,available_at=UTC_TIMESTAMP(6) WHERE id=?", j["id"])
		return e
	})
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"ok": true}})
}

type resultEnvelope struct {
	MessageID     string        `json:"message_id"`
	MessageType   string        `json:"message_type"`
	SchemaVersion int           `json:"schema_version"`
	OccurredAt    time.Time     `json:"occurred_at"`
	Producer      string        `json:"producer"`
	CorrelationID string        `json:"correlation_id"`
	Payload       ProcessResult `json:"payload"`
}

func decodeResult(body []byte) (ProcessResult, error) {
	var ev resultEnvelope
	if len(body) > 32768 {
		return ev.Payload, bad("invalid_result_event")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&ev) != nil || d.Decode(new(any)) != io.EOF {
		return ev.Payload, bad("invalid_result_event")
	}
	p := ev.Payload
	if ev.SchemaVersion != 1 || ev.MessageType != resultRoute || ev.Producer != "file-work-service" || ev.OccurredAt.IsZero() || len(ev.CorrelationID) > 128 || !validUUID(p.JobID) || !validUUID(p.ClaimToken) || p.Generation < 1 || p.OrganizationID < 1 || p.DivisionID < 1 || ev.MessageID != fmt.Sprintf("filework:%s:%d:%s:result", p.JobID, p.Generation, p.ClaimToken) {
		return p, bad("invalid_result_event")
	}
	if p.OutputKey != fmt.Sprintf("homework/%d/%d/processed/%s/%d.pdf", p.OrganizationID, p.DivisionID, p.JobID, p.Generation) {
		return p, bad("invalid_result_event")
	}
	if p.ErrorCode == "" {
		b, e := hex.DecodeString(p.SHA256)
		if e != nil || len(b) != 32 || p.SizeBytes < 1 || p.SizeBytes > 100<<20 || p.PageCount < 1 || p.PageCount > 200 {
			return p, bad("invalid_result_event")
		}
	} else {
		switch p.ErrorCode {
		case "corrupt_pdf", "invalid_pdf_signature", "processed_too_large", "metadata_not_removed", "source_not_found", "invalid_pdf", "encrypted_pdf", "active_content", "too_many_pages", "source_too_large", "output_too_large", "source_changed", "processing_failed", "empty_pdf", "pdf_validation_failed", "unsafe_pdf", "page_count_changed", "text_lost":
		default:
			return p, bad("invalid_result_event")
		}
	}
	return p, nil
}
func (s *Server) applyResult(ctx context.Context, p ProcessResult) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		h, sub, j, e := s.lockInternalJob(ctx, tx, p.JobID)
		if e == missing {
			return nil
		}
		if e != nil {
			return e
		}
		if num(j["organization_id"]) != p.OrganizationID || num(j["division_id"]) != p.DivisionID {
			return bad("invalid_result_scope")
		}
		// A duplicate successful completion must never enqueue its live object for deletion.
		if str(j["status"]) == "ready" && num(j["generation"]) == int64(p.Generation) && str(j["lease_token"]) == p.ClaimToken {
			return nil
		}
		stale := num(j["generation"]) != int64(p.Generation) || str(j["lease_token"]) != p.ClaimToken || str(j["status"]) != "running" || !currentJob(h, sub, j)
		if stale {
			if str(j["output_key"]) != p.OutputKey || str(j["status"]) == "cancelled" || h["deleted_at"] != nil {
				return queueUnreferenced(ctx, tx, p.OutputKey)
			}
			return nil
		}
		if e = editableResult(ctx, tx, sub); e != nil {
			if asError(e).Code == 409 {
				return queueUnreferenced(ctx, tx, p.OutputKey)
			}
			return e
		}
		var limits Settings
		if json.Unmarshal([]byte(str(j["settings_json"])), &limits) != nil || limits.Validate() != nil {
			return unavailable
		}
		if p.ErrorCode != "" {
			return s.failJob(ctx, tx, sub, j, p.ErrorCode)
		}
		if p.SizeBytes > limits.MaxOutputBytes || p.PageCount > limits.MaxPages {
			return s.failJob(ctx, tx, sub, j, "output_policy_violation")
		}
		head, e := s.Storage.Head(ctx, p.OutputKey)
		if e != nil {
			return unavailable
		}
		if head.Size != p.SizeBytes || metadata(head, "sha256") != p.SHA256 || metadata(head, "page_count") != fmt.Sprint(p.PageCount) || metadata(head, "job_id") != p.JobID || metadata(head, "generation") != fmt.Sprint(p.Generation) || metadata(head, "organization_id") != fmt.Sprint(p.OrganizationID) || metadata(head, "division_id") != fmt.Sprint(p.DivisionID) {
			return bad("output_metadata_mismatch")
		}
		if old := num(sub["draft_file_id"]); old > 0 {
			if e = s.deleteFileContext(ctx, tx, old); e != nil {
				return e
			}
		}
		res, e := tx.ExecContext(ctx, "INSERT INTO hw_files(submission_id,object_key,status,size_bytes,page_count,sha256,etag) VALUES(?,?,'draft',?,?,?,?)", sub["id"], p.OutputKey, p.SizeBytes, p.PageCount, p.SHA256, head.ETag)
		if e != nil {
			return e
		}
		fid, e := res.LastInsertId()
		if e != nil {
			return e
		}
		state := "draft"
		if num(sub["current_file_id"]) > 0 {
			state = "revision_requested"
		}
		if _, e = tx.ExecContext(ctx, "UPDATE hw_submissions SET draft_file_id=?,state=? WHERE id=?", fid, state, sub["id"]); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE hw_jobs SET status='ready',stage='ready',progress=100,error_code=NULL,lease_until=NULL WHERE id=?", j["id"]); e != nil {
			return e
		}
		if e = queueDelete(ctx, tx, str(j["source_key"])); e != nil {
			return e
		}
		return audit(ctx, tx, Actor{OrganizationID: p.OrganizationID}, p.DivisionID, "upload.ready", p.JobID)
	})
}
func queueUnreferenced(ctx context.Context, tx *sql.Tx, key string) error {
	var n int
	if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hw_files WHERE object_key=?", key).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	return queueDelete(ctx, tx, key)
}
func (s *Server) deleteFileContext(ctx context.Context, tx *sql.Tx, id int64) error {
	f, e := one(ctx, tx, "SELECT object_key FROM hw_files WHERE id=?", id)
	if e == missing {
		return nil
	}
	if e != nil {
		return e
	}
	if e = queueDelete(ctx, tx, str(f["object_key"])); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM hw_files WHERE id=?", id)
	return e
}
func (s *Server) failJob(ctx context.Context, tx *sql.Tx, sub, j map[string]any, code string) error {
	if _, e := tx.ExecContext(ctx, "UPDATE hw_jobs SET status='failed',stage='failed',error_code=?,lease_until=NULL,lease_token=NULL WHERE id=?", code, j["id"]); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "UPDATE hw_submissions SET state=? WHERE id=?", restored(sub), sub["id"]); e != nil {
		return e
	}
	if e := queueUnreferenced(ctx, tx, str(j["output_key"])); e != nil {
		return e
	}
	// Retain the source for a bounded manual retry window. The presigned upload
	// has expired long before this deletion can run.
	_, e := tx.ExecContext(ctx, "INSERT INTO hw_delete_jobs(object_key,status,available_at) VALUES(?,'queued',DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 24 HOUR)) ON DUPLICATE KEY UPDATE available_at=GREATEST(available_at,VALUES(available_at))", j["source_key"])
	return e
}
func (s *Server) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		s.recoverJobs(c)
		s.deleteObjects(c)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (s *Server) recoverJobs(ctx context.Context) {
	rows, e := list(ctx, s.DB, "SELECT id FROM hw_jobs WHERE (status='uploading' AND updated_at<DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 120 SECOND)) OR (status='running' AND lease_until<UTC_TIMESTAMP(6)) OR (status='queued' AND available_at<DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 15 MINUTE)) ORDER BY updated_at LIMIT 30")
	if e != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		s.transact(ctx, func(tx *sql.Tx) error {
			h, sub, j, e := s.lockInternalJob(ctx, tx, str(row["id"]))
			if e != nil {
				return e
			}
			if !currentJob(h, sub, j) {
				return nil
			}
			var limits Settings
			if json.Unmarshal([]byte(str(j["settings_json"])), &limits) != nil {
				return unavailable
			}
			status := str(j["status"])
			if status == "uploading" {
				if time.Since(stamp(j["updated_at"])) < time.Duration(limits.UploadStaleSeconds)*time.Second {
					return nil
				}
				if e = s.failJob(ctx, tx, sub, j, "upload_expired"); e != nil {
					return e
				}
				return queueDelete(ctx, tx, str(j["source_key"]))
			}
			if status == "running" {
				if !stamp(j["lease_until"]).Before(time.Now().UTC()) {
					return nil
				}
				if e = queueUnreferenced(ctx, tx, str(j["output_key"])); e != nil {
					return e
				}
				gen := num(j["generation"]) + 1
				key := fmt.Sprintf("homework/%d/%d/processed/%s/%d.pdf", num(j["organization_id"]), num(j["division_id"]), str(j["id"]), gen)
				if _, e = tx.ExecContext(ctx, "UPDATE hw_submissions SET generation=? WHERE id=?", gen, sub["id"]); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "UPDATE hw_jobs SET generation=?,output_key=?,status='queued',stage='queued',lease_token=NULL,lease_until=NULL WHERE id=?", gen, key, j["id"]); e != nil {
					return e
				}
				j["generation"] = gen
				j["output_key"] = key
			} else if status == "queued" {
				if time.Since(stamp(j["available_at"])) < 15*time.Minute {
					return nil
				}
			} else {
				return nil
			}
			if num(j["attempts"]) >= 20 {
				return s.failJob(ctx, tx, sub, j, "processing_failed")
			}
			if _, e = tx.ExecContext(ctx, "UPDATE hw_jobs SET available_at=UTC_TIMESTAMP(6) WHERE id=?", j["id"]); e != nil {
				return e
			}
			return enqueue(ctx, tx, commandRoute, command(j))
		})
	}
}
func (s *Server) deleteObjects(ctx context.Context) {
	// A crash during the final allowed attempt still produces a visible terminal
	// failure after its lease, rather than a permanently running deletion.
	if _, e := s.DB.ExecContext(ctx, "UPDATE hw_delete_jobs SET status='failed',lease_until=NULL,error_code='delete_retry_exhausted' WHERE status='running' AND lease_until<UTC_TIMESTAMP(6) AND attempts>=10"); e != nil {
		return
	}
	for i := 0; i < 20 && ctx.Err() == nil; i++ {
		var job map[string]any
		e := s.transact(ctx, func(tx *sql.Tx) error {
			var e error
			job, e = one(ctx, tx, "SELECT * FROM hw_delete_jobs WHERE ((status='queued' AND available_at<=UTC_TIMESTAMP(6)) OR (status='running' AND lease_until<UTC_TIMESTAMP(6))) AND attempts<10 ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED")
			if e == missing {
				job = nil
				return nil
			}
			if e != nil {
				return e
			}
			var n int
			if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hw_files WHERE object_key=?", job["object_key"]).Scan(&n); e != nil {
				return e
			}
			if n > 0 {
				_, e = tx.ExecContext(ctx, "UPDATE hw_delete_jobs SET status='done' WHERE id=?", job["id"])
				job = nil
				return e
			}
			_, e = tx.ExecContext(ctx, "UPDATE hw_delete_jobs SET status='running',attempts=attempts+1,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 60 SECOND) WHERE id=?", job["id"])
			return e
		})
		if e != nil || job == nil {
			return
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		e = s.Storage.Client.RemoveObject(c, s.Storage.Bucket, str(job["object_key"]), minio.RemoveObjectOptions{})
		cancel()
		if e == nil {
			s.DB.ExecContext(ctx, "UPDATE hw_delete_jobs SET status='done',lease_until=NULL,error_code=NULL WHERE id=? AND status='running'", job["id"])
		} else {
			s.DB.ExecContext(ctx, "UPDATE hw_delete_jobs SET status=IF(attempts>=10,'failed','queued'),lease_until=NULL,error_code='storage_unavailable',available_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND) WHERE id=? AND status='running'", job["id"])
		}
	}
}
