package core

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
)

// Read-only references for tests. Service credentials grant no write access.
func (s *Server) testReferences(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	var in struct {
		OrganizationID  int64   `json:"organization_id"`
		DivisionIDs     []int64 `json:"division_ids"`
		DirectionID     *int64  `json:"direction_id"`
		DirectionIDs    []int64 `json:"direction_ids"`
		IncludeStudents bool    `json:"include_students"`
	}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if in.OrganizationID < 1 || len(in.DivisionIDs) > 100 || !positive(in.DirectionID) || len(in.DirectionIDs) > 1000 || in.DirectionID != nil && len(in.DirectionIDs) > 0 || in.IncludeStudents && len(in.DivisionIDs) != 1 {
		fail(w, bad("invalid_reference"))
		return
	}
	seen := map[int64]bool{}
	for _, division := range in.DivisionIDs {
		if division < 1 || seen[division] {
			fail(w, bad("invalid_reference"))
			return
		}
		seen[division] = true
	}
	seenDirections := map[int64]bool{}
	for _, direction := range in.DirectionIDs {
		if direction < 1 || seenDirections[direction] {
			fail(w, bad("invalid_reference"))
			return
		}
		seenDirections[direction] = true
	}
	tx, e := s.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	org, e := one(r.Context(), tx, "SELECT id,name FROM organizations WHERE id=? AND is_active=1", in.OrganizationID)
	if e != nil {
		fail(w, e)
		return
	}
	divisions := []map[string]any{}
	if len(in.DivisionIDs) > 1 {
		divisions, e = referenceBatch(r, tx, in.OrganizationID, in.DivisionIDs, true)
		if e != nil {
			fail(w, e)
			return
		}
	} else {
		for _, id := range in.DivisionIDs {
			row, e := one(r.Context(), tx, "SELECT id,name,is_active FROM divisions WHERE id=? AND organization_id=? AND is_active=1", id, in.OrganizationID)
			if e != nil {
				fail(w, e)
				return
			}
			divisions = append(divisions, row)
		}
	}
	var direction any
	if in.DirectionID != nil {
		direction, e = one(r.Context(), tx, "SELECT p.id,p.name,x.is_active FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id WHERE p.id=? AND x.organization_id=?", *in.DirectionID, in.OrganizationID)
		if e != nil {
			fail(w, e)
			return
		}
	}
	data := map[string]any{"organization": org, "divisions": divisions, "direction": direction}
	if len(in.DirectionIDs) > 0 {
		directions, err := referenceBatch(r, tx, in.OrganizationID, in.DirectionIDs, false)
		if err != nil {
			fail(w, err)
			return
		}
		data["directions"] = directions
	}
	if in.IncludeStudents {
		accounts, err := list(r.Context(), tx, `SELECT a.id account_id,a.ref_id profile_id,p.full_name,NULLIF(p.`+"`class`"+`,0) class,st.is_active
 FROM auth_division_access x JOIN auth_users a ON a.id=x.user_id AND a.role='student'
 JOIN students p ON p.id=a.ref_id JOIN auth_account_state st ON st.user_id=a.id
 WHERE x.organization_id=? AND x.division_id=? ORDER BY a.id LIMIT 2001`, in.OrganizationID, in.DivisionIDs[0])
		if err != nil {
			fail(w, err)
			return
		}
		if len(accounts) > 2000 {
			fail(w, &APIError{413, "too_many_reference_accounts"})
			return
		}
		data["accounts"], data["accounts_complete"], data["account_projection_version"] = accounts, true, 1
	}
	if e = tx.Commit(); e != nil {
		fail(w, e)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": data})
}

// A reference page has a fixed number of database round trips, regardless of
// the number of directions or placement owners. Return the caller's order.
func referenceBatch(r *http.Request, tx *sql.Tx, organization int64, ids []int64, divisions bool) ([]map[string]any, error) {
	args := make([]any, 1, len(ids)+1)
	args[0] = organization
	for _, id := range ids {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := "SELECT p.id,p.name,x.is_active FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id WHERE x.organization_id=? AND p.id IN (" + placeholders + ")"
	if divisions {
		query = "SELECT id,name,is_active FROM divisions WHERE organization_id=? AND is_active=1 AND id IN (" + placeholders + ")"
	}
	rows, err := list(r.Context(), tx, query, args...)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]map[string]any, len(rows))
	for _, row := range rows {
		id := num(row["id"])
		if id < 1 {
			return nil, unavailable
		}
		row["id"] = id
		byID[id] = row
	}
	ordered := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			return nil, missing
		}
		ordered = append(ordered, row)
	}
	return ordered, nil
}
