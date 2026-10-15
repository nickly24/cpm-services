package homework

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type bulkSkipGraded struct{}

func (s *Server) types(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && !a.Can("assignments", false) {
		return nil, 0, denied
	}
	rows, e := list(r.Context(), s.DB, "SELECT id,name,archived FROM hw_types WHERE organization_id=? AND division_id=? AND (?=1 OR archived=0) ORDER BY name,id", a.OrganizationID, d, r.URL.Query().Get("archived") == "true" && a.Can("assignments", false))
	return map[string]any{"items": rows}, 200, e
}
func (s *Server) typeSave(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.Can("assignments", true) {
		return nil, 0, denied
	}
	var in TypeDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !validName(in.Name, 100) {
		return nil, 0, bad("invalid_name")
	}
	var n int64
	code := 201
	if r.Method == "PUT" {
		var e error
		n, e = id(r, "type")
		if e != nil {
			return nil, 0, e
		}
		code = 200
	}
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if n == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO hw_types(organization_id,division_id,name) VALUES(?,?,?)", a.OrganizationID, d, strings.TrimSpace(in.Name))
			if e != nil {
				return e
			}
			n, _ = res.LastInsertId()
		} else {
			if _, e := one(r.Context(), tx, "SELECT id FROM hw_types WHERE id=? AND organization_id=? AND division_id=? AND archived=0 FOR UPDATE", n, a.OrganizationID, d); e != nil {
				return e
			}
			if _, e := tx.ExecContext(r.Context(), "UPDATE hw_types SET name=? WHERE id=?", strings.TrimSpace(in.Name), n); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, d, "type.save", n)
	})
	return map[string]any{"id": n}, code, e
}
func (s *Server) typeArchive(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.Can("assignments", true) {
		return nil, 0, denied
	}
	n, e := id(r, "type")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := one(r.Context(), tx, "SELECT id FROM hw_types WHERE id=? AND organization_id=? AND division_id=? FOR UPDATE", n, a.OrganizationID, d); e != nil {
			return e
		}
		if _, e := tx.ExecContext(r.Context(), "UPDATE hw_types SET archived=1 WHERE id=?", n); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "type.archive", n)
	})
	return map[string]any{"id": n}, 200, e
}
func (s *Server) assignmentSave(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.Can("assignments", true) {
		return nil, 0, denied
	}
	var in AssignmentDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	deadline, e := parseDay(in.Deadline)
	if e != nil || !validName(in.Name, 200) || in.TypeID < 1 {
		return nil, 0, bad("invalid_assignment")
	}
	n := int64(0)
	code := 201
	if r.Method == "PUT" {
		n, e = id(r, "assignment")
		if e != nil {
			return nil, 0, e
		}
		code = 200
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		published := true
		currentType := int64(0)
		if n > 0 {
			row, e := s.assignment(r.Context(), tx, a, d, n, true)
			if e != nil {
				return e
			}
			published = num(row["published"]) == 1
			currentType = num(row["type_id"])
		}
		if _, e := one(r.Context(), tx, "SELECT id FROM hw_types WHERE id=? AND organization_id=? AND division_id=? AND (archived=0 OR id=?) FOR UPDATE", in.TypeID, a.OrganizationID, d, currentType); e != nil {
			return e
		}
		if in.Published != nil {
			published = *in.Published
		}
		if n == 0 {
			res, e := tx.ExecContext(r.Context(), "INSERT INTO hw_assignments(organization_id,division_id,type_id,name,deadline,published) VALUES(?,?,?,?,?,?)", a.OrganizationID, d, in.TypeID, strings.TrimSpace(in.Name), deadline, published)
			if e != nil {
				return e
			}
			n, _ = res.LastInsertId()
		} else {
			if _, e := tx.ExecContext(r.Context(), "UPDATE hw_assignments SET name=?,type_id=?,deadline=?,published=? WHERE id=?", strings.TrimSpace(in.Name), in.TypeID, deadline, published, n); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, d, "assignment.save", n)
	})
	return map[string]any{"id": n}, code, e
}
func (s *Server) assignmentGet(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && !a.Can("assignments", false) {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	h, e := s.assignment(r.Context(), s.DB, a, d, n, false)
	return h, 200, e
}
func (s *Server) assignments(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && !a.Can("assignments", false) {
		return nil, 0, denied
	}
	if r.URL.Query().Has("page") {
		return s.studentAssignments(r, a, d, ref)
	}
	limit, after, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := "SELECT h.id,h.name,h.type_id,t.name type_name,h.deadline,h.published,h.created_at FROM hw_assignments h JOIN hw_types t ON t.id=h.type_id WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL"
	args := []any{a.OrganizationID, d}
	published := r.URL.Query().Get("published")
	if published != "" && published != "all" && published != "true" && published != "false" {
		return nil, 0, bad("invalid_filter")
	}
	if a.Role == "student" {
		q += " AND h.published=1"
	} else if published == "true" || published == "false" {
		q += " AND h.published=?"
		args = append(args, published == "true")
	}
	typeID, e := queryID(r, "type_id")
	if e != nil {
		return nil, 0, e
	}
	if typeID > 0 {
		if _, e := one(r.Context(), s.DB, "SELECT id FROM hw_types WHERE id=? AND organization_id=? AND division_id=?", typeID, a.OrganizationID, d); e != nil {
			return nil, 0, e
		}
		q += " AND h.type_id=?"
		args = append(args, typeID)
	}
	search := r.URL.Query().Get("search")
	if search == "" {
		search = r.URL.Query().Get("q")
	}
	if search != "" {
		if len([]rune(search)) > 200 {
			return nil, 0, bad("invalid_search")
		}
		q += " AND h.name LIKE ?"
		args = append(args, "%"+search+"%")
	}
	q += " AND h.id>? ORDER BY h.id LIMIT ?"
	args = append(args, after, limit+1)
	rows, e := list(r.Context(), s.DB, q, args...)
	if e != nil {
		return nil, 0, e
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	var cursor any
	if len(rows) > 0 {
		cursor = rows[len(rows)-1]["id"]
	}
	if a.Role == "student" {
		for _, h := range rows {
			v, e := s.workspaceData(r, a, d, ref, h, a.AccountID)
			if e != nil {
				return nil, 0, e
			}
			h["workspace"] = v
		}
	}
	return map[string]any{"items": rows, "has_more": more, "next_cursor": cursor}, 200, nil
}
func (s *Server) assignmentDelete(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.Can("assignments", true) {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if _, e := s.assignment(r.Context(), tx, a, d, n, true); e != nil {
			return e
		}
		subs, e := list(r.Context(), tx, "SELECT id FROM hw_submissions WHERE assignment_id=? ORDER BY id FOR UPDATE", n)
		if e != nil {
			return e
		}
		for _, sub := range subs {
			sid := num(sub["id"])
			if e = s.deleteSubmissionFiles(r, tx, sid); e != nil {
				return e
			}
			jobs, e := list(r.Context(), tx, "SELECT source_key,output_key FROM hw_jobs WHERE submission_id=? FOR UPDATE", sid)
			if e != nil {
				return e
			}
			for _, j := range jobs {
				if e = queueDelete(r.Context(), tx, str(j["source_key"])); e != nil {
					return e
				}
				if e = queueDelete(r.Context(), tx, str(j["output_key"])); e != nil {
					return e
				}
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE hw_jobs SET status='cancelled',stage='cancelled',lease_until=NULL WHERE submission_id=? AND status IN ('uploading','queued','running','retry')", sid); e != nil {
				return e
			}
			if _, e = tx.ExecContext(r.Context(), "UPDATE hw_submissions SET state='deleted',generation=generation+1,draft_file_id=NULL,current_file_id=NULL WHERE id=?", sid); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE hw_assignments SET deleted_at=UTC_TIMESTAMP(6),published=0 WHERE id=?", n); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "assignment.delete", n)
	})
	return map[string]any{"id": n}, 200, e
}
func (s *Server) settingsGet(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && !a.Can("assignments", false) && !a.Can("monitoring", false) {
		return nil, 0, denied
	}
	v, e := s.settings(r.Context(), s.DB, d)
	return v, 200, e
}
func (s *Server) settingsPut(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	var v Settings
	if e := decode(r, &v); e != nil {
		return nil, 0, e
	}
	if e := v.Validate(); e != nil {
		return nil, 0, e
	}
	body, _ := json.Marshal(v)
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(r.Context(), "INSERT INTO application_settings(setting_key,setting_value,updated_by) VALUES(?,?,?) ON DUPLICATE KEY UPDATE setting_value=VALUES(setting_value),updated_by=VALUES(updated_by)", fmt.Sprintf("homework.%d.limits", d), body, a.AccountID)
		if e != nil {
			return e
		}
		return audit(r.Context(), tx, a, d, "settings.update", d)
	})
	return v, 200, e
}
func (s *Server) auditList(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, denied
	}
	limit, after, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	rows, e := list(r.Context(), s.DB, "SELECT id,actor_id,action,entity_id,created_at FROM hw_audit WHERE organization_id=? AND division_id=? AND id>? ORDER BY id LIMIT ?", a.OrganizationID, d, after, limit)
	return map[string]any{"items": rows}, 200, e
}
func (s *Server) manual(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "proctor" && !a.Can("assignments", true) {
		return nil, 0, denied
	}
	n, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	var in ManualDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	if r.Method == "PUT" {
		in.AccountID, e = id(r, "account")
		if e != nil {
			return nil, 0, e
		}
	}
	m, e := ref.Member(in.AccountID)
	if e != nil {
		return nil, 0, e
	}
	if m.Role != "student" || !a.Access(m) {
		return nil, 0, denied
	}
	var out any
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		h, e := s.assignment(r.Context(), tx, a, d, n, true)
		if e != nil {
			return e
		}
		sub, e := s.submission(r.Context(), tx, n, in.AccountID, true, true)
		if e != nil {
			return e
		}
		if str(sub["state"]) != "none" || num(sub["current_file_id"]) > 0 || num(sub["draft_file_id"]) > 0 {
			return conflict("use_file_review")
		}
		if skip, _ := r.Context().Value(bulkSkipGraded{}).(bool); skip {
			var previous int
			e = tx.QueryRowContext(r.Context(), "SELECT status FROM hw_results WHERE assignment_id=? AND account_id=? FOR UPDATE", n, in.AccountID).Scan(&previous)
			if e == nil && previous == 1 {
				return conflict("already_graded")
			}
			if e != nil && e != sql.ErrNoRows {
				return e
			}
		}
		status := 1
		result := 0
		var date any
		if in.Status != nil {
			if *in.Status != 0 && *in.Status != 1 {
				return bad("invalid_status")
			}
			status = *in.Status
		}
		if status == 1 {
			dateRaw := in.DatePass
			if dateRaw == "" && r.Method == "PUT" {
				old, e := one(r.Context(), tx, "SELECT date_pass,result FROM hw_results WHERE assignment_id=? AND account_id=? FOR UPDATE", n, in.AccountID)
				if e != nil {
					return e
				}
				if t := stamp(old["date_pass"]); !t.IsZero() {
					dateRaw = t.Format("2006-01-02")
				}
				if in.Result == nil {
					v := int(num(old["result"]))
					in.Result = &v
				}
			}
			day, e := parseDay(dateRaw)
			if e != nil {
				return bad("invalid_date_pass")
			}
			date = day
			result = suggested(stamp(h["deadline"]), time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, moscow))
			if in.Result != nil {
				result = *in.Result
				if result < 0 {
					result = 0
				}
				if result > 100 {
					result = 100
				}
			}
		}
		_, e = tx.ExecContext(r.Context(), "INSERT INTO hw_results(assignment_id,account_id,status,result,date_pass,manual) VALUES(?,?,?,?,?,1) ON DUPLICATE KEY UPDATE status=VALUES(status),result=VALUES(result),date_pass=VALUES(date_pass),manual=1", n, in.AccountID, status, result, date)
		if e != nil {
			return e
		}
		out = map[string]any{"result": result, "status": status, "date_pass": date}
		return audit(r.Context(), tx, a, d, "manual.save", n)
	})
	return out, 200, e
}
func (s *Server) manualBulk(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "proctor" && !a.Can("assignments", true) {
		return nil, 0, denied
	}
	var in ManualDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	g, e := ref.Group(in.GroupID)
	if e != nil {
		return nil, 0, e
	}
	if a.Role == "proctor" && (a.GroupID == nil || *a.GroupID != g.ID) {
		return nil, 0, denied
	}
	if _, e = parseDay(in.DatePass); e != nil {
		return nil, 0, bad("invalid_date_pass")
	}
	assignment, e := id(r, "assignment")
	if e != nil {
		return nil, 0, e
	}
	if _, e = s.assignment(r.Context(), s.DB, a, d, assignment, false); e != nil {
		return nil, 0, e
	}
	passed, skipped, files := 0, 0, 0
	errs := []any{}
	for _, m := range ref.Members {
		if m.Role != "student" || m.GroupID == nil || *m.GroupID != g.ID {
			continue
		}
		var status int
		e := s.DB.QueryRowContext(r.Context(), "SELECT status FROM hw_results WHERE assignment_id=? AND account_id=?", assignment, m.AccountID).Scan(&status)
		if e == nil && status == 1 {
			skipped++
			continue
		}
		if e != nil && e != sql.ErrNoRows {
			return nil, 0, e
		}
		body, _ := json.Marshal(ManualDTO{AccountID: m.AccountID, DatePass: in.DatePass, Result: in.Result})
		clone := r.Clone(context.WithValue(r.Context(), bulkSkipGraded{}, true))
		clone.Body = ioBody(body)
		clone.Header = r.Header.Clone()
		clone.Header.Set("Content-Type", "application/json")
		_, _, e = s.manual(clone, a, d, ref)
		if e == nil {
			passed++
		} else {
			if asError(e).Message == "already_graded" {
				skipped++
				continue
			}
			if asError(e).Message == "use_file_review" {
				files++
			}
			errs = append(errs, map[string]any{"account_id": m.AccountID, "error": asError(e).Message})
		}
	}
	return map[string]any{"passed": passed, "skipped": skipped, "skipped_files": files, "errors": errs}, 200, nil
}
func (s *Server) assignmentStudents(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "proctor" && !a.Can("assignments", false) {
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
	limit, after, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	items := []any{}
	total := 0
	search := strings.ToLower(r.URL.Query().Get("search"))
	group, e := queryID(r, "group_id")
	if e != nil {
		return nil, 0, e
	}
	filter := r.URL.Query().Get("status")
	if filter != "" && filter != "all" && filter != "submitted" && filter != "overdue" && filter != "in_progress" && filter != "not_started" {
		return nil, 0, bad("invalid_status")
	}
	for _, m := range ref.Members {
		if m.Role != "student" || !a.Access(m) || group > 0 && (m.GroupID == nil || *m.GroupID != group) || search != "" && !strings.Contains(strings.ToLower(m.FullName), search) {
			continue
		}
		v, e := s.workspaceData(r, a, d, ref, h, m.AccountID)
		if e != nil {
			return nil, 0, e
		}
		result, _ := v["legacy_result"].(map[string]any)
		done := result != nil && num(result["status"]) == 1
		over := stamp(h["deadline"]).Format("2006-01-02") < time.Now().In(moscow).Format("2006-01-02")
		if filter == "submitted" && !done || filter == "overdue" && (done || !over) || filter == "in_progress" && (done || over) || filter == "not_started" && result != nil {
			continue
		}
		total++
		if m.AccountID > after && len(items) < limit+1 {
			items = append(items, map[string]any{"account_id": m.AccountID, "full_name": m.FullName, "group_id": m.GroupID, "group_name": m.GroupName, "workspace": v})
		}
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var cursor any
	if len(items) > 0 {
		cursor = items[len(items)-1].(map[string]any)["account_id"]
	}
	return map[string]any{"items": items, "total": total, "has_more": more, "next_cursor": cursor}, 200, nil
}
func (s *Server) report(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" && a.Role != "proctor" && a.Role != "supervisor" && !a.Can("assignments", false) {
		return nil, 0, denied
	}
	from, to := r.URL.Query().Get("date_from"), r.URL.Query().Get("date_to")
	start, e := parseDay(from)
	if e != nil {
		return nil, 0, bad("invalid_period")
	}
	end, e := parseDay(to)
	if e != nil || end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return nil, 0, bad("invalid_period")
	}
	account, e := queryID(r, "account_id")
	if e != nil {
		return nil, 0, e
	}
	if account > 0 {
		m, e := ref.Member(account)
		if e != nil {
			return nil, 0, e
		}
		if m.Role != "student" || a.Role != "supervisor" && !a.Access(m) {
			return nil, 0, denied
		}
	}
	group, e := queryID(r, "group_id")
	if e != nil {
		return nil, 0, e
	}
	if group > 0 {
		if _, e := ref.Group(group); e != nil {
			return nil, 0, e
		}
	}
	typeID, e := queryID(r, "type_id")
	if e != nil {
		return nil, 0, e
	}
	if typeID > 0 {
		if _, e := one(r.Context(), s.DB, "SELECT id FROM hw_types WHERE id=? AND organization_id=? AND division_id=?", typeID, a.OrganizationID, d); e != nil {
			return nil, 0, e
		}
	}
	q := "SELECT h.id,h.name,h.type_id,t.name type_name,h.deadline FROM hw_assignments h JOIN hw_types t ON t.id=h.type_id WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL AND h.deadline BETWEEN ? AND ?"
	args := []any{a.OrganizationID, d, start, end}
	if a.Role == "student" {
		q += " AND h.published=1"
	}
	if typeID > 0 {
		q += " AND h.type_id=?"
		args = append(args, typeID)
	}
	q += " ORDER BY h.id LIMIT 501"
	rows, e := list(r.Context(), s.DB, q, args...)
	if e != nil {
		return nil, 0, e
	}
	if len(rows) > 500 {
		return nil, 0, bad("report_too_large")
	}
	members := []Member{}
	for _, m := range ref.Members {
		if m.Role != "student" || a.Role != "supervisor" && !a.Access(m) || account > 0 && m.AccountID != account || group > 0 && (m.GroupID == nil || *m.GroupID != group) {
			continue
		}
		members = append(members, m)
	}
	if len(rows)*len(members) > 100000 {
		return nil, 0, bad("report_too_large")
	}
	indexed := map[[2]int64]map[string]any{}
	if len(rows) > 0 && len(members) > 0 {
		placeholders := func(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
		query := "SELECT assignment_id,account_id,status,result,date_pass FROM hw_results WHERE assignment_id IN (" + placeholders(len(rows)) + ") AND account_id IN (" + placeholders(len(members)) + ")"
		params := []any{}
		for _, h := range rows {
			params = append(params, h["id"])
		}
		for _, m := range members {
			params = append(params, m.AccountID)
		}
		grades, e := list(r.Context(), s.DB, query, params...)
		if e != nil {
			return nil, 0, e
		}
		for _, v := range grades {
			indexed[[2]int64{num(v["assignment_id"]), num(v["account_id"])}] = map[string]any{"status": v["status"], "result": v["result"], "date_pass": v["date_pass"]}
		}
	}
	items := []any{}
	for _, m := range members {
		results := []any{}
		for _, h := range rows {
			v := indexed[[2]int64{num(h["id"]), m.AccountID}]
			results = append(results, map[string]any{"assignment_id": h["id"], "result": v})
		}
		items = append(items, map[string]any{"account_id": m.AccountID, "full_name": m.FullName, "group_name": m.GroupName, "class": m.Class, "results": results})
	}
	return map[string]any{"organization_id": a.OrganizationID, "division_id": d, "assignments": rows, "students": items}, 200, nil
}
