package homework

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

// These predicates mirror the established student tabs. Revision work also
// belongs to "undone". A cleared manual result (status=0) is never completed.
var studentStatusSQL = map[string]string{
	"all":       "1=1",
	"done":      "(submission_state='graded' OR (submission_state='none' AND result IS NOT NULL))",
	"in_review": "submission_state IN ('submitted','in_review')",
	"revision":  "submission_state='revision_requested'",
	"undone":    "(submission_state NOT IN ('submitted','in_review','graded') AND NOT (submission_state='none' AND result IS NOT NULL))",
}

type studentCatalogQuery struct {
	Page, Limit          int
	Status, Type, Search string
}

func parseStudentCatalog(r *http.Request) (studentCatalogQuery, error) {
	q := r.URL.Query()
	out := studentCatalogQuery{Page: 1, Limit: 12, Status: "all", Type: q.Get("type"), Search: strings.TrimSpace(q.Get("search"))}
	for _, key := range []string{"page", "limit", "status", "type", "search"} {
		if len(q[key]) > 1 {
			return out, bad("invalid_filter")
		}
	}
	var err error
	out.Page, err = strconv.Atoi(q.Get("page"))
	if err != nil || out.Page < 1 || out.Page > 1000000 {
		return out, bad("invalid_pagination")
	}
	if q.Has("limit") {
		out.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || out.Limit < 1 || out.Limit > 100 {
			return out, bad("invalid_pagination")
		}
	}
	if q.Has("status") {
		out.Status = q.Get("status")
	}
	if _, ok := studentStatusSQL[out.Status]; !ok {
		return out, bad("invalid_filter")
	}
	if q.Has("after") || q.Has("account_id") || q.Has("type_id") || q.Has("published") {
		return out, bad("invalid_filter")
	}
	if len([]rune(out.Type)) > 100 || len([]rune(out.Search)) > 200 {
		return out, bad("invalid_filter")
	}
	return out, nil
}

func (s *Server) studentAssignments(r *http.Request, a Actor, d int64, ref References) (any, int, error) {
	if a.Role != "student" {
		return nil, 0, denied
	}
	member, err := ref.Member(a.AccountID)
	if err != nil {
		return nil, 0, err
	}
	if member.Role != "student" {
		return nil, 0, denied
	}
	query, err := parseStudentCatalog(r)
	if err != nil {
		return nil, 0, err
	}
	// Only the selected student's card fields. No workspaces, jobs, files,
	// settings, other students or per-assignment SQL calls are loaded here.
	base := `SELECT h.id homework_id,h.name homework_name,t.name homework_type,h.deadline,
 s.id submission_id,COALESCE(s.state,'none') submission_state,
 COALESCE(s.current_file_id,0)>0 has_file,COALESCE(s.draft_file_id,0)>0 has_draft,
 s.submitted_at_utc,s.revision_comment,CASE WHEN r.status=1 THEN r.result ELSE NULL END result
 FROM hw_assignments h JOIN hw_types t ON t.id=h.type_id AND t.organization_id=h.organization_id AND t.division_id=h.division_id
 LEFT JOIN hw_submissions s ON s.assignment_id=h.id AND s.account_id=?
 LEFT JOIN hw_results r ON r.assignment_id=h.id AND r.account_id=?
 WHERE h.organization_id=? AND h.division_id=? AND h.deleted_at IS NULL AND h.published=1`
	args := []any{a.AccountID, a.AccountID, a.OrganizationID, d}
	if query.Type != "" && query.Type != "all" {
		base += " AND t.name=?"
		args = append(args, query.Type)
	}
	if query.Search != "" {
		// Search is a literal substring, including '%'/'_', as in the existing UI.
		literal := strings.NewReplacer("=", "==", "%", "=%", "_", "=_").Replace(query.Search)
		base += " AND CONCAT(h.name,' ',t.name) LIKE ? ESCAPE '='"
		args = append(args, "%"+literal+"%")
	}
	countSQL := "SELECT COUNT(*) all_count"
	for _, status := range []string{"undone", "in_review", "revision", "done"} {
		countSQL += ",COUNT(CASE WHEN " + studentStatusSQL[status] + " THEN 1 END) " + status + "_count"
	}
	countSQL += " FROM (" + base + ") catalog"
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	countRow, err := one(r.Context(), tx, countSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	counts := map[string]int64{}
	for _, status := range []string{"all", "undone", "in_review", "revision", "done"} {
		counts[status] = num(countRow[status+"_count"])
	}
	total := counts[query.Status]
	pages := max(int64(1), (total+int64(query.Limit)-1)/int64(query.Limit))
	current := min(int64(query.Page), pages)
	pageSQL := "SELECT * FROM (" + base + ") catalog WHERE " + studentStatusSQL[query.Status] + " ORDER BY deadline,homework_id LIMIT ? OFFSET ?"
	pageArgs := append(append([]any(nil), args...), query.Limit, (current-1)*int64(query.Limit))
	items, err := list(r.Context(), tx, pageSQL, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return map[string]any{"items": items, "counts": counts, "pagination": map[string]any{"current_page": current, "total_pages": pages, "total_items": total, "items_per_page": query.Limit}}, 200, nil
}
