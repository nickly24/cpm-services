package core

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
)

// homeworkReferences provides bounded, read-only projections for the homework
// service. It grants no permission to modify core or auth-owned records.
func (s *Server) homeworkReferences(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{http.StatusUnauthorized, "invalid_service_key"})
		return
	}
	var in struct {
		OrganizationID int64   `json:"organization_id"`
		DivisionID     int64   `json:"division_id"`
		AccountIDs     []int64 `json:"account_ids"`
		GroupID        *int64  `json:"group_id"`
	}
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if in.OrganizationID < 1 || in.DivisionID < 1 || !positive(in.GroupID) || len(in.AccountIDs) > 1000 {
		fail(w, bad("invalid_reference"))
		return
	}
	seen := make(map[int64]bool, len(in.AccountIDs))
	for _, id := range in.AccountIDs {
		if id < 1 || seen[id] {
			fail(w, bad("invalid_reference"))
			return
		}
		seen[id] = true
	}
	// Memberships and group references belong to one consistent snapshot.
	tx, e := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	_, e = one(r.Context(), tx, `SELECT d.id FROM divisions d JOIN organizations o ON o.id=d.organization_id
 WHERE d.organization_id=? AND d.id=? AND d.is_active=1 AND o.is_active=1`, in.OrganizationID, in.DivisionID)
	if e != nil {
		fail(w, e)
		return
	}
	if in.GroupID != nil {
		_, e = one(r.Context(), tx, `SELECT group_id FROM core_group_scope
 WHERE organization_id=? AND division_id=? AND group_id=? AND is_active=1`, in.OrganizationID, in.DivisionID, *in.GroupID)
		if e != nil {
			fail(w, e)
			return
		}
	}
	query := `SELECT a.id account_id,a.ref_id profile_id,a.role,p.full_name,p.` + "`class`" + `,
 sc.id school_id,sc.name school_name,sc.short_name school_short_name,
 g.group_id,gn.name group_name FROM auth_users a
 JOIN auth_account_state ast ON ast.user_id=a.id AND ast.is_active=1
 JOIN auth_organization_access oa ON oa.user_id=a.id AND oa.organization_id=?
 LEFT JOIN auth_division_access m ON m.user_id=a.id AND m.organization_id=oa.organization_id AND m.division_id=?
	 JOIN ` + profiles + ` p ON p.id=a.ref_id AND p.role=a.role
	 LEFT JOIN core_school_scope school_scope ON school_scope.school_id=p.school_id AND school_scope.organization_id=oa.organization_id
	 LEFT JOIN schools sc ON sc.id=school_scope.school_id
 LEFT JOIN admin_role_users staff ON a.role='staff_admin' AND staff.id=a.ref_id
 LEFT JOIN core_group_scope g ON g.group_id=m.group_id AND g.organization_id=m.organization_id
  AND g.division_id=m.division_id AND g.is_active=1
 LEFT JOIN ` + "`groups`" + ` gn ON gn.id=g.group_id
 WHERE (a.role='admin' OR m.user_id IS NOT NULL)
  AND (a.role<>'staff_admin' OR (staff.is_active=1 AND m.role_id IS NOT NULL))
  AND (a.role<>'admin' OR NOT EXISTS (SELECT 1 FROM auth_organization_access other_access
   JOIN organizations other_org ON other_org.id=other_access.organization_id AND other_org.is_active=1
   WHERE other_access.user_id=a.id AND other_access.organization_id<>oa.organization_id))`
	args := []any{in.OrganizationID, in.DivisionID}
	if in.GroupID != nil {
		query += " AND g.group_id=?"
		args = append(args, *in.GroupID)
	}
	if len(in.AccountIDs) > 0 {
		query += " AND a.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(in.AccountIDs)), ",") + ")"
		for _, id := range in.AccountIDs {
			args = append(args, id)
		}
	}
	query += " ORDER BY a.id LIMIT 1001"
	members, e := list(r.Context(), tx, query, args...)
	if e != nil {
		fail(w, e)
		return
	}
	if len(members) > 1000 {
		fail(w, bad("reference_limit_exceeded"))
		return
	}
	if len(in.AccountIDs) > 0 && len(members) != len(in.AccountIDs) {
		fail(w, missing)
		return
	}
	query = `SELECT g.group_id id,gn.name,
 CASE WHEN ast.is_active=1 AND m.user_id IS NOT NULL THEN p.user_id ELSE NULL END proctor_account_id
 FROM core_group_scope g JOIN ` + "`groups`" + ` gn ON gn.id=g.group_id
 LEFT JOIN core_proctor_groups p ON p.group_id=g.group_id AND p.division_id=g.division_id
 LEFT JOIN auth_account_state ast ON ast.user_id=p.user_id
 LEFT JOIN auth_division_access m ON m.user_id=p.user_id AND m.organization_id=g.organization_id
  AND m.division_id=g.division_id AND m.group_id=g.group_id
 WHERE g.organization_id=? AND g.division_id=? AND g.is_active=1`
	args = []any{in.OrganizationID, in.DivisionID}
	if in.GroupID != nil {
		query += " AND g.group_id=?"
		args = append(args, *in.GroupID)
	}
	query += " ORDER BY g.group_id LIMIT 1001"
	groups, e := list(r.Context(), tx, query, args...)
	if e != nil {
		fail(w, e)
		return
	}
	if len(groups) > 1000 {
		fail(w, bad("reference_limit_exceeded"))
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, e)
		return
	}
	reply(w, http.StatusOK, map[string]any{"status": true, "data": map[string]any{
		"organization_id": in.OrganizationID, "division_id": in.DivisionID,
		"members": members, "groups": groups,
	}})
}
