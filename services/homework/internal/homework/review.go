package homework

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) transition(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	section := "review-queue"
	if action == "edit-grade" || action == "resubmit" {
		section = "homework-archive"
	}
	if a.Role != "proctor" && !a.Can(section, true) {
		return nil, 0, denied
	}
	n, e := id(r, "submission")
	if e != nil {
		return nil, 0, e
	}
	var in TransitionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	var requested *int
	if len(in.Result) > 0 {
		var v int
		if string(in.Result) == "null" || json.Unmarshal(in.Result, &v) != nil || v < 0 || v > 100 {
			return nil, 0, bad("invalid_result")
		}
		requested = &v
	}
	if action == "edit-grade" && requested == nil {
		return nil, 0, bad("invalid_result")
	}
	var result any
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		pre, e := one(r.Context(), tx, "SELECT s.assignment_id FROM hw_submissions s JOIN hw_assignments h ON h.id=s.assignment_id WHERE s.id=? AND h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL", n, a.OrganizationID, d)
		if e != nil {
			return e
		}
		h, e := s.assignment(r.Context(), tx, a, d, num(pre["assignment_id"]), true)
		if e != nil {
			return e
		}
		sub, e := one(r.Context(), tx, "SELECT * FROM hw_submissions WHERE id=? FOR UPDATE", n)
		if e != nil {
			return e
		}
		m, e := ref.Member(num(sub["account_id"]))
		if e != nil {
			return e
		}
		if !a.Access(m) {
			return denied
		}
		state := str(sub["state"])
		reviewer := func() error {
			if state != "in_review" {
				return conflict("invalid_state")
			}
			if a.Role == "proctor" && num(sub["reviewer_account_id"]) != a.AccountID {
				return conflict("not_reviewer")
			}
			return nil
		}
		switch action {
		case "claim":
			if state == "in_review" && num(sub["reviewer_account_id"]) == a.AccountID {
				return nil
			}
			if state != "submitted" {
				return conflict("invalid_state")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='in_review',reviewer_account_id=? WHERE id=?", a.AccountID, n)
		case "takeover":
			if a.Role == "proctor" || state != "in_review" {
				return conflict("invalid_state")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET reviewer_account_id=? WHERE id=?", a.AccountID, n)
		case "release":
			if state == "submitted" {
				return nil
			}
			if e = reviewer(); e != nil {
				return e
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='submitted',reviewer_account_id=NULL WHERE id=?", n)
		case "request-revision":
			comment := strings.TrimSpace(in.Message)
			if state == "revision_requested" && str(sub["revision_comment"]) == comment {
				return nil
			}
			if e = reviewer(); e != nil {
				return e
			}
			if comment == "" {
				return conflict("revision_message_required")
			}
			if len([]rune(comment)) > 1000 {
				return bad("revision_message_too_long")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='revision_requested',reviewer_account_id=NULL,revision_comment=?,revision_count=revision_count+1 WHERE id=?", comment, n)
		case "grade":
			if state == "graded" {
				old, e := one(r.Context(), tx, "SELECT result FROM hw_results WHERE assignment_id=? AND account_id=?", sub["assignment_id"], sub["account_id"])
				if e != nil {
					return e
				}
				if requested != nil && int(num(old["result"])) != *requested {
					return conflict("already_graded")
				}
				result = old["result"]
				return nil
			}
			if e = reviewer(); e != nil {
				return e
			}
			submitted := stamp(sub["submitted_at_utc"])
			if submitted.IsZero() {
				return conflict("submission_timestamp_missing")
			}
			v := suggested(stamp(h["deadline"]), submitted)
			if requested != nil {
				v = *requested
			}
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO hw_results(assignment_id,account_id,result,status,date_pass,manual) VALUES(?,?,?,1,?,0) ON DUPLICATE KEY UPDATE result=VALUES(result),status=1,date_pass=VALUES(date_pass),manual=0", sub["assignment_id"], sub["account_id"], v, submitted.In(moscow).Format("2006-01-02")); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE hw_files SET status='final' WHERE id=?", sub["current_file_id"]); e != nil {
				return e
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='graded',reviewer_account_id=NULL,revision_comment='' WHERE id=?", n)
			result = v
		case "edit-grade":
			if state != "graded" {
				return conflict("invalid_state")
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_results SET result=? WHERE assignment_id=? AND account_id=?", *requested, sub["assignment_id"], sub["account_id"])
			result = *requested
		case "resubmit":
			if state == "none" && num(sub["current_file_id"]) == 0 && num(sub["draft_file_id"]) == 0 {
				return nil
			}
			if state != "graded" {
				return conflict("invalid_state")
			}
			j, err := active(r.Context(), tx, n)
			if err != nil {
				return err
			}
			if j != nil {
				return conflict("upload_in_progress")
			}
			if e = s.deleteSubmissionFiles(r, tx, n); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='none',draft_file_id=NULL,current_file_id=NULL,reviewer_account_id=NULL,submitted_at_utc=NULL,revision_comment='',generation=generation+1 WHERE id=?", n); e != nil {
				return e
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE hw_results SET status=0,result=0,date_pass=NULL WHERE assignment_id=? AND account_id=?", sub["assignment_id"], sub["account_id"])
		default:
			return missing
		}
		if e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "submission."+action, n)
	})
	return map[string]any{"ok": true, "result": result}, 200, e
}
func (s *Server) reviewQueue(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "proctor" && !a.Can("review-queue", false) {
		return nil, 0, denied
	}
	if a.Role == "proctor" || a.Can("review-queue", true) {
		e := s.transact(r.Context(), func(tx *sql.Tx) error {
			rows, e := list(r.Context(), tx, "SELECT sub.id,sub.account_id,sub.reviewer_account_id FROM hw_submissions sub JOIN hw_assignments h ON h.id=sub.assignment_id WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL AND sub.state='in_review' FOR UPDATE", a.OrganizationID, d)
			if e != nil {
				return e
			}
			for _, sub := range rows {
				m, me := ref.Member(num(sub["account_id"]))
				reviewer, re := ref.Member(num(sub["reviewer_account_id"]))
				if re == nil && reviewer.Role != "proctor" {
					continue
				}
				if me != nil || re != nil || reviewer.GroupID == nil || m.GroupID == nil || *reviewer.GroupID != *m.GroupID {
					if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='submitted',reviewer_account_id=NULL WHERE id=?", sub["id"]); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if e != nil {
			return nil, 0, e
		}
	}
	return s.reviewList(r, a, d, ref, false)
}
func (s *Server) archive(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "proctor" && !a.Can("homework-archive", false) {
		return nil, 0, denied
	}
	return s.reviewList(r, a, d, ref, true)
}
func (s *Server) reviewList(r *http.Request, a Actor, d int64, ref References, archive bool) (any, int, error) {
	limit, after, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	state := r.URL.Query().Get("state")
	if !archive && state != "" && state != "all" && state != "submitted" && state != "in_review" && state != "revision_requested" {
		return nil, 0, bad("invalid_state")
	}
	account, e := queryID(r, "account_id")
	if e != nil {
		return nil, 0, e
	}
	assignment, e := queryID(r, "assignment_id")
	if e != nil {
		return nil, 0, e
	}
	group, e := queryID(r, "group_id")
	if e != nil {
		return nil, 0, e
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	for _, v := range []string{from, to} {
		if v != "" {
			if _, e = parseDay(v); e != nil {
				return nil, 0, bad("invalid_filter")
			}
		}
	}
	q := "SELECT sub.id,sub.assignment_id,sub.account_id,sub.state,sub.submitted_at_utc,sub.reviewer_account_id,sub.revision_comment,h.name homework_name,h.deadline,f.size_bytes,f.page_count,res.result,res.date_pass FROM hw_submissions sub JOIN hw_assignments h ON h.id=sub.assignment_id LEFT JOIN hw_files f ON f.id=sub.current_file_id LEFT JOIN hw_results res ON res.assignment_id=sub.assignment_id AND res.account_id=sub.account_id WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL"
	args := []any{a.OrganizationID, d}
	if archive {
		q += " AND sub.state='graded' AND sub.current_file_id IS NOT NULL"
	} else if state != "" && state != "all" {
		q += " AND sub.state=?"
		args = append(args, state)
	} else {
		q += " AND sub.state IN ('submitted','in_review','revision_requested')"
	}
	if account > 0 {
		q += " AND sub.account_id=?"
		args = append(args, account)
	}
	if assignment > 0 {
		q += " AND sub.assignment_id=?"
		args = append(args, assignment)
	}
	if from != "" {
		q += " AND DATE(DATE_ADD(sub.submitted_at_utc,INTERVAL 3 HOUR))>=?"
		args = append(args, from)
	}
	if to != "" {
		q += " AND DATE(DATE_ADD(sub.submitted_at_utc,INTERVAL 3 HOUR))<=?"
		args = append(args, to)
	}
	if archive {
		q += " ORDER BY sub.id DESC"
	} else {
		q += " ORDER BY sub.id"
	}
	q += " LIMIT 10001"
	rows, e := list(r.Context(), s.DB, q, args...)
	if e != nil {
		return nil, 0, e
	}
	if len(rows) > 10000 {
		return nil, 0, bad("queue_too_large")
	}
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	items := []any{}
	total := 0
	for _, row := range rows {
		m, e := ref.Member(num(row["account_id"]))
		if e != nil || !a.Access(m) || group > 0 && (m.GroupID == nil || *m.GroupID != group) {
			continue
		}
		groupName := ""
		if m.GroupName != nil {
			groupName = *m.GroupName
		}
		if search != "" && !strings.Contains(strings.ToLower(m.FullName+" "+str(row["homework_name"])+" "+groupName), search) {
			continue
		}
		total++
		n := num(row["id"])
		if after > 0 && (archive && n >= after || !archive && n <= after) || len(items) >= limit+1 {
			continue
		}
		row["homework_id"] = row["assignment_id"]
		row["student_name"] = m.FullName
		row["group_name"] = m.GroupName
		row["group_id"] = m.GroupID
		row["deadline"] = stamp(row["deadline"]).Format("2006-01-02")
		row["suggested_score"] = suggested(stampFromDeadline(str(row["deadline"])), stamp(row["submitted_at_utc"]))
		row["filename"] = fileName(m.FullName, str(row["homework_name"]), stamp(row["submitted_at_utc"]))
		row["reviewer_name"] = nil
		if rev, e := ref.Member(num(row["reviewer_account_id"])); e == nil {
			row["reviewer_name"] = rev.FullName
		}
		items = append(items, row)
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var cursor any
	if len(items) > 0 {
		cursor = items[len(items)-1].(map[string]any)["id"]
	}
	return map[string]any{"items": items, "total": total, "has_more": more, "next_cursor": cursor}, 200, nil
}
func stampFromDeadline(day string) time.Time { t, _ := time.Parse("2006-01-02", day); return t }
func (s *Server) fileURL(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	n, e := id(r, "submission")
	if e != nil {
		return nil, 0, e
	}
	sub, e := one(r.Context(), s.DB, "SELECT sub.*,h.name homework_name FROM hw_submissions sub JOIN hw_assignments h ON h.id=sub.assignment_id WHERE sub.id=? AND h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL", n, a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	m, e := ref.Member(num(sub["account_id"]))
	if e != nil {
		return nil, 0, e
	}
	if !a.Access(m) {
		return nil, 0, denied
	}
	draft := r.URL.Query().Get("draft") == "1"
	if draft && a.Role != "student" {
		return nil, 0, &APIError{403, "draft_private"}
	}
	if a.Role != "student" {
		state := str(sub["state"])
		if state != "submitted" && state != "in_review" && state != "revision_requested" && state != "graded" {
			return nil, 0, &APIError{403, "draft_private"}
		}
		section := "review-queue"
		if state == "graded" {
			section = "homework-archive"
		}
		if a.Role != "proctor" && !a.Can(section, false) {
			return nil, 0, denied
		}
	}
	fid := num(sub["current_file_id"])
	if draft {
		fid = num(sub["draft_file_id"])
	}
	if fid == 0 {
		return nil, 0, missing
	}
	f, e := one(r.Context(), s.DB, "SELECT * FROM hw_files WHERE id=? AND submission_id=?", fid, n)
	if e != nil {
		return nil, 0, e
	}
	t := stamp(sub["submitted_at_utc"])
	if t.IsZero() {
		t = stamp(f["created_at"])
	}
	name := fileName(m.FullName, str(sub["homework_name"]), t)
	limits, e := s.settings(r.Context(), s.DB, d)
	if e != nil {
		return nil, 0, e
	}
	u, e := s.Storage.Download(r.Context(), str(f["object_key"]), name, limits.PresignTTLSeconds, r.URL.Query().Get("download") == "1")
	return map[string]any{"url": u, "filename": name, "size_bytes": f["size_bytes"], "page_count": f["page_count"], "expires_in": limits.PresignTTLSeconds}, 200, e
}
func (s *Server) monitoring(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.Can("monitoring", false) {
		return nil, 0, denied
	}
	rows, e := list(r.Context(), s.DB, "SELECT status,COUNT(*) count FROM hw_jobs WHERE organization_id=? AND division_id=? GROUP BY status", a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	outbox, e := one(r.Context(), s.DB, "SELECT COUNT(*) count FROM hw_outbox WHERE published_at IS NULL AND JSON_EXTRACT(body_json,'$.payload.organization_id')=? AND JSON_EXTRACT(body_json,'$.payload.division_id')=?", a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	jobs, e := list(r.Context(), s.DB, "SELECT id,status,stage,error_code,attempts,manual_attempts,created_at FROM hw_jobs WHERE organization_id=? AND division_id=? ORDER BY created_at DESC LIMIT 50", a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	deletes, e := list(r.Context(), s.DB, "SELECT status,COUNT(*) count FROM hw_delete_jobs WHERE object_key LIKE ? GROUP BY status", fmt.Sprintf("homework/%d/%d/%%", a.OrganizationID, d))
	worker, storage, ready := s.Workers.snapshot(time.Now().UTC())
	broker := s.BusReady.Load()
	return map[string]any{"organization_id": a.OrganizationID, "division_id": d, "jobs": rows, "recent_jobs": jobs, "delete_jobs": deletes, "outbox_pending": outbox["count"], "rabbitmq": broker, "worker": worker, "storage": storage, "processing_ready": broker && ready, "service": "homework", "worker_service": "file-work-service"}, 200, e
}
