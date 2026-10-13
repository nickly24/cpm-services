package core

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
)

type examReferencesDTO struct {
	OrganizationID int64    `json:"organization_id"`
	DivisionIDs    []int64  `json:"division_ids"`
	DirectionID    *int64   `json:"direction_id"`
	AccountIDs     []int64  `json:"account_ids"`
	Logins         []string `json:"logins"`
}

func (in examReferencesDTO) validate() error {
	if in.OrganizationID < 1 || !positive(in.DirectionID) || len(in.DivisionIDs) > 100 || len(in.AccountIDs)+len(in.Logins) > 1000 {
		return bad("invalid_reference")
	}
	for _, ids := range [][]int64{in.DivisionIDs, in.AccountIDs} {
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			if id < 1 || seen[id] {
				return bad("invalid_reference")
			}
			seen[id] = true
		}
	}
	seen := make(map[string]bool, len(in.Logins))
	for _, login := range in.Logins {
		if !validText(login, 50, true) || strings.ContainsAny(login, " \t\r\n") || seen[login] {
			return bad("invalid_reference")
		}
		seen[login] = true
	}
	return nil
}

func examReferencePlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// examReferences returns bounded core-owned references in one read-only snapshot.
// A service key grants reference access only; user tokens and forwarded identity
// headers cannot grant access to this internal endpoint.
func (s *Server) examReferences(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{http.StatusUnauthorized, "invalid_service_key"})
		return
	}
	var in examReferencesDTO
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if e := in.validate(); e != nil {
		fail(w, e)
		return
	}
	tx, e := s.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	if _, e = one(r.Context(), tx, "SELECT id FROM organizations WHERE id=? AND is_active=1", in.OrganizationID); e != nil {
		fail(w, e)
		return
	}
	divisions := make([]map[string]any, 0, len(in.DivisionIDs))
	for _, id := range in.DivisionIDs {
		row, err := one(r.Context(), tx, "SELECT id,name,is_active FROM divisions WHERE id=? AND organization_id=? AND is_active=1", id, in.OrganizationID)
		if err != nil {
			fail(w, err)
			return
		}
		divisions = append(divisions, row)
	}
	query := `SELECT p.id,p.name,x.is_active FROM directions p JOIN core_direction_scope x ON x.direction_id=p.id
 WHERE x.organization_id=? AND x.is_active=1`
	args := []any{in.OrganizationID}
	if in.DirectionID != nil {
		query += " AND p.id=?"
		args = append(args, *in.DirectionID)
	}
	query += " ORDER BY p.id LIMIT 1001"
	directions, e := list(r.Context(), tx, query, args...)
	if e != nil || (in.DirectionID != nil && len(directions) == 0) {
		if e == nil {
			e = missing
		}
		fail(w, e)
		return
	}
	if len(directions) > 1000 {
		fail(w, bad("reference_limit_exceeded"))
		return
	}
	members := []map[string]any{}
	if len(in.AccountIDs)+len(in.Logins) > 0 {
		query = `SELECT a.id account_id,a.ref_id profile_id,a.role,a.username login,p.full_name,ast.is_active
 FROM auth_users a JOIN auth_account_state ast ON ast.user_id=a.id AND ast.is_active=1
 JOIN auth_organization_access oa ON oa.user_id=a.id AND oa.organization_id=?
 JOIN ` + profiles + ` p ON p.id=a.ref_id AND p.role=a.role
 LEFT JOIN admin_role_users staff ON a.role='staff_admin' AND staff.id=a.ref_id
 WHERE (a.role<>'staff_admin' OR staff.is_active=1) AND (`
		args = []any{in.OrganizationID}
		if len(in.AccountIDs) > 0 {
			query += "a.id IN (" + examReferencePlaceholders(len(in.AccountIDs)) + ")"
			for _, id := range in.AccountIDs {
				args = append(args, id)
			}
		}
		if len(in.Logins) > 0 {
			if len(in.AccountIDs) > 0 {
				query += " OR "
			}
			query += "BINARY a.username IN (" + examReferencePlaceholders(len(in.Logins)) + ")"
			for _, login := range in.Logins {
				args = append(args, login)
			}
		}
		query += ") ORDER BY a.id LIMIT 1001"
		members, e = list(r.Context(), tx, query, args...)
		if e != nil {
			fail(w, e)
			return
		}
		if len(members) > 1000 {
			fail(w, bad("reference_limit_exceeded"))
			return
		}
		ids, logins := map[int64]bool{}, map[string]bool{}
		byID := map[int64]map[string]any{}
		for _, member := range members {
			accountID := num(member["account_id"])
			ids[accountID] = true
			login, _ := member["login"].(string)
			if logins[login] {
				fail(w, bad("ambiguous_reference"))
				return
			}
			logins[login] = true
			member["division_ids"] = []int64{}
			byID[accountID] = member
		}
		for _, id := range in.AccountIDs {
			if !ids[id] {
				fail(w, missing)
				return
			}
		}
		for _, login := range in.Logins {
			if !logins[login] {
				fail(w, missing)
				return
			}
		}
		query = `SELECT m.user_id account_id,m.division_id FROM auth_division_access m
 JOIN divisions d ON d.id=m.division_id AND d.organization_id=m.organization_id AND d.is_active=1
 WHERE m.organization_id=? AND m.user_id IN (` + examReferencePlaceholders(len(members)) + ")"
		args = []any{in.OrganizationID}
		for _, member := range members {
			args = append(args, num(member["account_id"]))
		}
		if len(in.DivisionIDs) > 0 {
			query += " AND m.division_id IN (" + examReferencePlaceholders(len(in.DivisionIDs)) + ")"
			for _, id := range in.DivisionIDs {
				args = append(args, id)
			}
		}
		query += " ORDER BY m.user_id,m.division_id"
		memberships, err := list(r.Context(), tx, query, args...)
		if err != nil {
			fail(w, err)
			return
		}
		for _, row := range memberships {
			member := byID[num(row["account_id"])]
			member["division_ids"] = append(member["division_ids"].([]int64), num(row["division_id"]))
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, e)
		return
	}
	reply(w, http.StatusOK, map[string]any{"status": true, "data": map[string]any{
		"organization_id": in.OrganizationID, "directions": directions, "divisions": divisions, "members": members,
	}})
}
