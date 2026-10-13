package homework

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func ioBody(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }
func editable(sub map[string]any) bool {
	switch str(sub["state"]) {
	case "none", "uploading", "processing", "draft", "revision_requested":
		return true
	}
	return false
}
func restored(sub map[string]any) string {
	if num(sub["current_file_id"]) > 0 {
		return "revision_requested"
	}
	if num(sub["draft_file_id"]) > 0 {
		return "draft"
	}
	return "none"
}
func active(ctx context.Context, db DBTX, sid int64) (map[string]any, error) {
	row, e := one(ctx, db, "SELECT * FROM hw_jobs WHERE submission_id=? AND status IN ('uploading','queued','running','retry') ORDER BY created_at DESC LIMIT 1 FOR UPDATE", sid)
	if e == missing {
		return nil, nil
	}
	return row, e
}
func editableResult(ctx context.Context, db DBTX, sub map[string]any) error {
	if !editable(sub) {
		return conflict("file_locked_after_submit")
	}
	var status int
	e := db.QueryRowContext(ctx, "SELECT status FROM hw_results WHERE assignment_id=? AND account_id=? FOR UPDATE", sub["assignment_id"], sub["account_id"]).Scan(&status)
	if e == nil && status == 1 {
		return conflict("already_graded")
	}
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	return nil
}
func jobDTO(j map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "assignment_id", "status", "stage", "progress", "error_code", "attempts", "manual_attempts", "created_at", "updated_at", "generation"} {
		out[k] = j[k]
	}
	out["homework_id"] = j["assignment_id"]
	// A running upload keeps the policy accepted at creation, even if an admin
	// changes division settings before its result reaches the student.
	var limits Settings
	if json.Unmarshal([]byte(str(j["settings_json"])), &limits) == nil && limits.Validate() == nil {
		out["limits"] = map[string]any{"max_source_bytes": limits.MaxSourceBytes, "max_output_bytes": limits.MaxOutputBytes, "max_pages": limits.MaxPages, "dpi": limits.DPI, "jpeg_quality": limits.JPEGQuality}
	} else {
		out["limits"] = nil
	}
	return out
}
func (s *Server) fileMeta(ctx context.Context, db DBTX, fileID int64, sub, h map[string]any, m Member) (any, error) {
	if fileID == 0 {
		return nil, nil
	}
	f, e := one(ctx, db, "SELECT id,size_bytes,page_count,created_at FROM hw_files WHERE id=? AND submission_id=?", fileID, num(sub["id"]))
	if e != nil {
		return nil, e
	}
	t := stamp(sub["submitted_at_utc"])
	if t.IsZero() {
		t = stamp(f["created_at"])
	}
	f["filename"] = fileName(m.FullName, str(h["name"]), t)
	return f, nil
}
func (s *Server) workspaceData(r *http.Request, a Actor, d int64, ref References, h map[string]any, account int64) (map[string]any, error) {
	m, e := ref.Member(account)
	if e != nil {
		return nil, e
	}
	if m.Role != "student" || !a.Access(m) {
		return nil, denied
	}
	sub, e := s.submission(r.Context(), s.DB, num(h["id"]), account, false, false)
	if e != nil && e != missing {
		return nil, e
	}
	if e == missing {
		sub = nil
	}
	state := "none"
	if sub != nil {
		state = str(sub["state"])
	}
	if a.Role == "staff_admin" {
		section := "review-queue"
		if state == "graded" {
			section = "homework-archive"
		}
		if !a.Can(section, false) && !a.Can("assignments", false) {
			return nil, denied
		}
	}
	result, e := one(r.Context(), s.DB, "SELECT id,status,result,date_pass FROM hw_results WHERE assignment_id=? AND account_id=?", h["id"], account)
	if e != nil && e != missing {
		return nil, e
	}
	if e == missing {
		result = nil
	}
	graded := result != nil && num(result["status"]) == 1
	var j map[string]any
	if sub != nil {
		j, e = one(r.Context(), s.DB, "SELECT * FROM hw_jobs WHERE submission_id=? AND status IN ('uploading','queued','running','retry') ORDER BY created_at DESC LIMIT 1", sub["id"])
		if e != nil && e != missing {
			return nil, e
		}
		if e == missing {
			j = nil
		}
	}
	limits, e := s.settings(r.Context(), s.DB, d)
	if e != nil {
		return nil, e
	}
	visible := map[string]any{"state": "none", "has_file": false, "has_draft": false}
	private := a.Role != "student" && (state == "none" || state == "uploading" || state == "processing" || state == "draft")
	if sub != nil && !private {
		visible = map[string]any{"id": sub["id"], "state": state, "submitted_at_utc": sub["submitted_at_utc"], "revision_count": sub["revision_count"], "revision_comment": sub["revision_comment"], "has_file": num(sub["current_file_id"]) > 0, "has_draft": a.Role == "student" && num(sub["draft_file_id"]) > 0}
		current, e := s.fileMeta(r.Context(), s.DB, num(sub["current_file_id"]), sub, h, m)
		if e != nil {
			return nil, e
		}
		visible["current_file"] = current
		visible["draft_file"] = nil
		if a.Role == "student" {
			draft, e := s.fileMeta(r.Context(), s.DB, num(sub["draft_file_id"]), sub, h, m)
			if e != nil {
				return nil, e
			}
			visible["draft_file"] = draft
		}
		visible["reviewer"] = nil
		if rid := num(sub["reviewer_account_id"]); rid > 0 {
			reviewer, e := ref.Member(rid)
			if e == nil {
				visible["reviewer"] = map[string]any{"account_id": rid, "full_name": reviewer.FullName, "role": reviewer.Role}
			}
		}
	}
	can := a.Role == "student" && !graded && j == nil && (sub == nil || editable(sub))
	ready := can && sub != nil && num(sub["draft_file_id"]) > 0 && (state == "draft" || state == "revision_requested")
	var activeJob any
	if a.Role == "student" && j != nil {
		activeJob = jobDTO(j)
	}
	t := time.Now().UTC()
	if sub != nil && !stamp(sub["submitted_at_utc"]).IsZero() {
		t = stamp(sub["submitted_at_utc"])
	}
	return map[string]any{"homework": map[string]any{"id": h["id"], "name": h["name"], "type_id": h["type_id"], "deadline": stamp(h["deadline"]).Format("2006-01-02"), "published": num(h["published"]) == 1}, "legacy_result": result, "submission": visible, "active_job": activeJob, "limits": limits, "suggested_score": suggested(stamp(h["deadline"]), t), "permissions": map[string]bool{"upload": can, "submit": ready, "remove_draft": ready}}, nil
}
func (s *Server) workspace(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && !a.Can("review-queue", false) && !a.Can("homework-archive", false) && !a.Can("assignments", false) {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	h, e := s.assignment(r.Context(), s.DB, a, d, n, false)
	if e != nil {
		return nil, 0, e
	}
	account, e := queryID(r, "account_id")
	if e != nil {
		return nil, 0, e
	}
	if account == 0 {
		account = a.AccountID
	}
	v, e := s.workspaceData(r, a, d, ref, h, account)
	return v, 200, e
}
func (s *Server) createUpload(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	var in UploadDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if in.ClientUploadID == "" {
		in.ClientUploadID = r.Header.Get("Idempotency-Key")
	}
	if !validUUID(in.ClientUploadID) {
		return nil, 0, bad("invalid_client_upload_id")
	}
	in.ClientUploadID = strings.ToLower(in.ClientUploadID)
	var job map[string]any
	var limits Settings
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := s.assignment(r.Context(), tx, a, d, n, true); e != nil {
			return e
		}
		sub, e := s.submission(r.Context(), tx, n, a.AccountID, true, true)
		if e != nil {
			return e
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		job, e = one(r.Context(), tx, "SELECT * FROM hw_jobs WHERE account_id=? AND client_upload_id=? FOR UPDATE", a.AccountID, in.ClientUploadID)
		if e == nil {
			if num(job["submission_id"]) != num(sub["id"]) {
				return conflict("client_upload_id_conflict")
			}
			if str(job["status"]) == "uploading" {
				if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET updated_at=UTC_TIMESTAMP(6) WHERE id=?", job["id"]); e != nil {
					return e
				}
			}
			return json.Unmarshal([]byte(str(job["settings_json"])), &limits)
		}
		if e != missing {
			return e
		}
		j, e := active(r.Context(), tx, num(sub["id"]))
		if e != nil {
			return e
		}
		if j != nil {
			return conflict("upload_in_progress")
		}
		limits, e = s.settings(r.Context(), tx, d)
		if e != nil {
			return e
		}
		generation := int(num(sub["generation"])) + 1
		jid := uuid()
		source := fmt.Sprintf("homework/%d/%d/staging/%s.pdf", a.OrganizationID, d, jid)
		output := fmt.Sprintf("homework/%d/%d/processed/%s/%d.pdf", a.OrganizationID, d, jid, generation)
		body, _ := json.Marshal(limits)
		_, e = tx.ExecContext(r.Context(), "INSERT INTO hw_jobs(id,client_upload_id,submission_id,assignment_id,account_id,organization_id,division_id,generation,status,stage,source_key,output_key,settings_json) VALUES(?,?,?,?,?,?,?,?,'uploading','uploading',?,?,?)", jid, in.ClientUploadID, sub["id"], n, a.AccountID, a.OrganizationID, d, generation, source, output, body)
		if e != nil {
			return e
		}
		next := "uploading"
		if num(sub["current_file_id"]) > 0 {
			next = "revision_requested"
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET generation=?,state=? WHERE id=?", generation, next, sub["id"]); e != nil {
			return e
		}
		job, e = one(r.Context(), tx, "SELECT * FROM hw_jobs WHERE id=?", jid)
		if e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "upload.create", jid)
	})
	if e != nil {
		return nil, 0, e
	}
	var upload any
	if str(job["status"]) == "uploading" {
		upload, e = s.Storage.Upload(r.Context(), str(job["source_key"]), a.AccountID, limits)
		if e != nil {
			return nil, 0, e
		}
	}
	return map[string]any{"job": jobDTO(job), "upload": upload, "max_bytes": limits.MaxSourceBytes, "poll_after_seconds": 10}, 201, nil
}
func (s *Server) lockedJob(r *http.Request, tx *sql.Tx, a Actor, d int64, ref References) (map[string]any, map[string]any, error) {
	jid := r.PathValue("job")
	if !validUUID(jid) {
		return nil, nil, missing
	}
	pre, e := one(r.Context(), tx, "SELECT assignment_id,account_id FROM hw_jobs WHERE id=? AND organization_id=? AND division_id=?", jid, a.OrganizationID, d)
	if e != nil {
		return nil, nil, e
	}
	m, e := ref.Member(num(pre["account_id"]))
	if e != nil {
		return nil, nil, e
	}
	if !a.Access(m) {
		return nil, nil, denied
	}
	if _, e = s.assignment(r.Context(), tx, a, d, num(pre["assignment_id"]), true); e != nil {
		return nil, nil, e
	}
	sub, e := s.submission(r.Context(), tx, num(pre["assignment_id"]), num(pre["account_id"]), false, true)
	if e != nil {
		return nil, nil, e
	}
	job, e := one(r.Context(), tx, "SELECT * FROM hw_jobs WHERE id=? FOR UPDATE", jid)
	return sub, job, e
}
func command(j map[string]any) any {
	var limits Settings
	json.Unmarshal([]byte(str(j["settings_json"])), &limits)
	return map[string]any{"job_id": j["id"], "generation": j["generation"], "organization_id": j["organization_id"], "division_id": j["division_id"], "source_key": j["source_key"], "source_etag": j["source_etag"], "output_key": j["output_key"], "limits": map[string]any{"max_source_bytes": limits.MaxSourceBytes, "max_output_bytes": limits.MaxOutputBytes, "max_pages": limits.MaxPages, "dpi": limits.DPI, "jpeg_quality": limits.JPEGQuality}}
}
func (s *Server) completeUpload(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	var out any
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		sub, j, e := s.lockedJob(r, tx, a, d, ref)
		if e != nil {
			return e
		}
		if str(j["status"]) != "uploading" {
			out = jobDTO(j)
			return nil
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		var limits Settings
		if e = json.Unmarshal([]byte(str(j["settings_json"])), &limits); e != nil {
			return e
		}
		head, e := s.Storage.Head(r.Context(), str(j["source_key"]))
		if e != nil {
			return sourceHeadError(e, "staging_object_not_found")
		}
		if head.Size < 1 || head.Size > limits.MaxSourceBytes {
			return &APIError{413, "source_too_large"}
		}
		if metadata(head, "account-id") != fmt.Sprint(a.AccountID) {
			return denied
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='queued',stage='queued',progress=5,source_etag=?,source_size=?,available_at=UTC_TIMESTAMP(6) WHERE id=?", head.ETag, head.Size, j["id"]); e != nil {
			return e
		}
		state := "processing"
		if num(sub["current_file_id"]) > 0 {
			state = "revision_requested"
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state=? WHERE id=?", state, sub["id"]); e != nil {
			return e
		}
		j["source_etag"] = head.ETag
		j["status"] = "queued"
		j["stage"] = "queued"
		j["progress"] = int64(5)
		if e = enqueue(r.Context(), tx, "files.pdf.process.requested.v1", command(j)); e != nil {
			return e
		}
		out = jobDTO(j)
		return audit(r.Context(), tx, a, d, "upload.complete", j["id"])
	})
	return out, 202, e
}
func (s *Server) activeJobs(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	rows, e := list(r.Context(), s.DB, "SELECT j.* FROM hw_jobs j JOIN hw_assignments h ON h.id=j.assignment_id WHERE j.account_id=? AND j.organization_id=? AND j.division_id=? AND h.deleted_at IS NULL AND j.status IN ('uploading','queued','running','retry') ORDER BY j.created_at LIMIT 100", a.AccountID, a.OrganizationID, d)
	items := []any{}
	for _, j := range rows {
		items = append(items, jobDTO(j))
	}
	return map[string]any{"items": items, "polling_required": len(items) > 0, "poll_after_seconds": 10}, 200, e
}
func (s *Server) jobGet(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	j, e := one(r.Context(), s.DB, "SELECT j.* FROM hw_jobs j JOIN hw_assignments h ON h.id=j.assignment_id WHERE j.id=? AND j.account_id=? AND j.organization_id=? AND j.division_id=? AND h.deleted_at IS NULL", r.PathValue("job"), a.AccountID, a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	return jobDTO(j), 200, nil
}
func (s *Server) cancelJob(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	var out any
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		sub, j, e := s.lockedJob(r, tx, a, d, ref)
		if e != nil {
			return e
		}
		state := str(j["status"])
		if state == "cancelled" {
			out = jobDTO(j)
			return nil
		}
		if state == "ready" || state == "failed" {
			return conflict("job_not_cancellable")
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='cancelled',stage='cancelled',lease_until=NULL WHERE id=?", j["id"]); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state=?,generation=generation+1 WHERE id=?", restored(sub), sub["id"]); e != nil {
			return e
		}
		if e = queueDelete(r.Context(), tx, str(j["source_key"])); e != nil {
			return e
		}
		if e = queueDelete(r.Context(), tx, str(j["output_key"])); e != nil {
			return e
		}
		j["status"] = "cancelled"
		j["stage"] = "cancelled"
		out = jobDTO(j)
		return audit(r.Context(), tx, a, d, "upload.cancel", j["id"])
	})
	return out, 200, e
}
func (s *Server) retryJob(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && !a.Can("monitoring", true) {
		return nil, 0, denied
	}
	var out any
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		sub, j, e := s.lockedJob(r, tx, a, d, ref)
		if e != nil {
			return e
		}
		if str(j["status"]) != "failed" {
			if num(j["manual_attempts"]) > 0 && (str(j["status"]) == "queued" || str(j["status"]) == "running" || str(j["status"]) == "ready") {
				out = jobDTO(j)
				return nil
			}
			return conflict("job_not_retryable")
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		current, e := active(r.Context(), tx, num(sub["id"]))
		if e != nil {
			return e
		}
		if current != nil {
			return conflict("upload_in_progress")
		}
		if num(sub["generation"]) != num(j["generation"]) {
			return conflict("job_superseded")
		}
		if num(j["manual_attempts"]) >= 3 {
			return conflict("manual_retry_limit")
		}
		deletion, e := one(r.Context(), tx, "SELECT status FROM hw_delete_jobs WHERE object_key=? FOR UPDATE", j["source_key"])
		if e != nil && e != missing {
			return e
		}
		if e == nil && (str(deletion["status"]) == "running" || str(deletion["status"]) == "done") {
			return conflict("staging_object_expired")
		}
		head, e := s.Storage.Head(r.Context(), str(j["source_key"]))
		if e != nil {
			return sourceHeadError(e, "staging_object_expired")
		}
		if str(j["source_etag"]) == "" {
			// An upload can reach S3 while complete is lost. A manual retry must
			// perform the same acceptance checks before emitting its first command.
			var limits Settings
			if json.Unmarshal([]byte(str(j["settings_json"])), &limits) != nil || limits.Validate() != nil {
				return unavailable
			}
			if head.Size < 1 || head.Size > limits.MaxSourceBytes {
				return &APIError{413, "source_too_large"}
			}
			if metadata(head, "account-id") != fmt.Sprint(num(j["account_id"])) {
				return denied
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET source_etag=?,source_size=? WHERE id=?", head.ETag, head.Size, j["id"]); e != nil {
				return e
			}
			j["source_etag"] = head.ETag
			j["source_size"] = head.Size
		}
		if _, e = tx.ExecContext(r.Context(), "DELETE FROM hw_delete_jobs WHERE object_key=? AND status IN ('queued','retry','failed')", j["source_key"]); e != nil {
			return e
		}
		generation := num(sub["generation"]) + 1
		output := fmt.Sprintf("homework/%d/%d/processed/%s/%d.pdf", a.OrganizationID, d, j["id"], generation)
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='queued',stage='queued',progress=5,error_code=NULL,manual_attempts=manual_attempts+1,generation=?,output_key=?,lease_until=NULL,lease_token=NULL WHERE id=?", generation, output, j["id"]); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET generation=?,state=? WHERE id=?", generation, func() string {
			if num(sub["current_file_id"]) > 0 {
				return "revision_requested"
			}
			return "processing"
		}(), sub["id"]); e != nil {
			return e
		}
		j["generation"] = generation
		j["output_key"] = output
		j["status"] = "queued"
		j["stage"] = "queued"
		j["manual_attempts"] = num(j["manual_attempts"]) + 1
		if e = enqueue(r.Context(), tx, "files.pdf.process.requested.v1", command(j)); e != nil {
			return e
		}
		out = jobDTO(j)
		return audit(r.Context(), tx, a, d, "upload.retry", j["id"])
	})
	return out, 202, e
}
func (s *Server) submit(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	var out any
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := s.assignment(r.Context(), tx, a, d, n, true); e != nil {
			return e
		}
		sub, e := s.submission(r.Context(), tx, n, a.AccountID, false, true)
		if e != nil {
			return conflict("draft_not_ready")
		}
		state := str(sub["state"])
		if (state == "submitted" || state == "in_review" || state == "graded") && num(sub["current_file_id"]) > 0 {
			out = map[string]any{"state": state, "submitted_at_utc": sub["submitted_at_utc"]}
			return nil
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		j, e := active(r.Context(), tx, num(sub["id"]))
		if e != nil {
			return e
		}
		if j != nil {
			return conflict("upload_in_progress")
		}
		if num(sub["draft_file_id"]) == 0 {
			return conflict("draft_not_ready")
		}
		if state != "draft" && state != "revision_requested" {
			return conflict("invalid_state")
		}
		if e = s.deleteFile(r, tx, num(sub["current_file_id"])); e != nil {
			return e
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_files SET status='current' WHERE id=?", sub["draft_file_id"]); e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='submitted',current_file_id=draft_file_id,draft_file_id=NULL,submitted_at_utc=?,reviewer_account_id=NULL,revision_comment='' WHERE id=?", now, sub["id"]); e != nil {
			return e
		}
		out = map[string]any{"state": "submitted", "submitted_at_utc": now}
		return audit(r.Context(), tx, a, d, "submission.submit", sub["id"])
	})
	return out, 200, e
}
func (s *Server) deleteFile(r *http.Request, tx *sql.Tx, file int64) error {
	if file == 0 {
		return nil
	}
	row, e := one(r.Context(), tx, "SELECT object_key FROM hw_files WHERE id=?", file)
	if e == missing {
		return nil
	}
	if e != nil {
		return e
	}
	if e = queueDelete(r.Context(), tx, str(row["object_key"])); e != nil {
		return e
	}
	_, e = tx.ExecContext(r.Context(), "DELETE FROM hw_files WHERE id=?", file)
	return e
}
func (s *Server) deleteSubmissionFiles(r *http.Request, tx *sql.Tx, sid int64) error {
	rows, e := list(r.Context(), tx, "SELECT id FROM hw_files WHERE submission_id=?", sid)
	if e != nil {
		return e
	}
	for _, row := range rows {
		if e = s.deleteFile(r, tx, num(row["id"])); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) removeDraft(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	state := "none"
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := s.assignment(r.Context(), tx, a, d, n, true); e != nil {
			return e
		}
		sub, e := s.submission(r.Context(), tx, n, a.AccountID, false, true)
		if e == missing {
			return nil
		}
		if e != nil {
			return e
		}
		if e = editableResult(r.Context(), tx, sub); e != nil {
			return e
		}
		j, e := active(r.Context(), tx, num(sub["id"]))
		if e != nil {
			return e
		}
		if j != nil {
			return conflict("upload_in_progress")
		}
		if e = s.deleteFile(r, tx, num(sub["draft_file_id"])); e != nil {
			return e
		}
		if num(sub["current_file_id"]) > 0 {
			state = "revision_requested"
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET draft_file_id=NULL,state=? WHERE id=?", state, sub["id"]); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "draft.remove", sub["id"])
	})
	return map[string]any{"state": state}, 200, e
}
