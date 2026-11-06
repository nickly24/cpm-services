package core

import (
	"database/sql"
	"net/http"
	"strconv"
)

type examParticipantView struct {
	AccountID    int64  `json:"account_id"`
	ProfileID    int64  `json:"profile_id"`
	Login        string `json:"login"`
	FullName     string `json:"full_name"`
	Role         string `json:"role"`
	DivisionID   int64  `json:"division_id"`
	DivisionName string `json:"division_name"`
}
type examParticipantPageView struct {
	Items  []examParticipantView `json:"items"`
	Total  *int64                `json:"total,omitempty"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

// This projection deliberately omits credentials, contacts and unrelated profiles.
// Offset pages serve pickers; after_id pages let exports traverse large divisions
// without a fixed page count or increasingly expensive offsets.
func (s *Server) examParticipants(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("exams", false) && !a.Can("users", false) {
		return nil, 0, denied
	}
	limit, offset, err := page(r)
	if err != nil {
		return nil, 0, err
	}
	role, term := r.URL.Query().Get("role"), r.URL.Query().Get("q")
	if role != "" && role != "student" && role != "examinator" {
		return nil, 0, bad("invalid_role")
	}
	if !validText(term, 100, false) {
		return nil, 0, bad("invalid_search")
	}
	var after int64
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 || offset != 0 {
			return nil, 0, bad("invalid_pagination")
		}
	}
	from := ` FROM auth_division_access x JOIN auth_users a ON a.id=x.user_id JOIN auth_account_state s ON s.user_id=a.id AND s.is_active=1 JOIN auth_organization_access o ON o.user_id=a.id AND o.organization_id=x.organization_id JOIN divisions d ON d.id=x.division_id AND d.organization_id=x.organization_id AND d.is_active=1 LEFT JOIN students st ON a.role='student' AND st.id=a.ref_id LEFT JOIN examinators ex ON a.role='examinator' AND ex.id=a.ref_id WHERE x.organization_id=? AND x.division_id=? AND a.role IN ('student','examinator') AND (st.id IS NOT NULL OR ex.id IS NOT NULL)`
	args := []any{a.OrganizationID, d}
	if role != "" {
		from += " AND a.role=?"
		args = append(args, role)
	}
	if term != "" {
		from += " AND (COALESCE(st.full_name,ex.full_name) LIKE ? OR a.username LIKE ? OR CAST(a.id AS CHAR)=?)"
		args = append(args, "%"+term+"%", "%"+term+"%", term)
	}
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int64
	// Cursor exports only need the next slice; avoid rescanning the complete
	// membership set to count it again on every exported page.
	if !r.URL.Query().Has("after_id") {
		if err = tx.QueryRowContext(r.Context(), "SELECT COUNT(*)"+from, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	if after > 0 {
		from += " AND a.id>?"
		args = append(args, after)
	}
	query := "SELECT a.id account_id,a.ref_id profile_id,a.username login,COALESCE(st.full_name,ex.full_name) full_name,a.role,d.id division_id,d.name division_name" + from + " ORDER BY a.id LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	items, err := list(r.Context(), tx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	result := map[string]any{"items": items, "limit": limit, "offset": offset}
	if !r.URL.Query().Has("after_id") {
		result["total"] = total
	}
	return result, 200, nil
}
