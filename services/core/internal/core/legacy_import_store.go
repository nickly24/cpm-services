package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func legacyCommandKey(r *http.Request, required bool) (string, error) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" && !required {
		return "", nil
	}
	if len(key) != 36 || key != strings.ToLower(key) {
		return "", bad("uuid_idempotency_key_required")
	}
	for i, c := range key {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", bad("uuid_idempotency_key_required")
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", bad("uuid_idempotency_key_required")
		}
	}
	if key == "00000000-0000-0000-0000-000000000000" {
		return "", bad("uuid_idempotency_key_required")
	}
	return key, nil
}
func legacyCommandHash(operation string, session int64, payload any) string {
	b, _ := json.Marshal(struct {
		Operation string
		Session   int64
		Payload   any
	}{operation, session, payload})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func legacyImportPermission(a Actor, d int64, edit bool) error {
	if a.AccountID < 1 || a.OrganizationID < 1 || d < 1 || !a.Can("upload", edit) || !a.Can("users", edit) {
		return denied
	}
	if !a.OrganizationAdmin && (a.DivisionID == nil || *a.DivisionID != d) {
		return denied
	}
	return nil
}
func (s *Server) legacyImportIndex(r *http.Request, db DBTX, a Actor, d int64) (legacyImportIndex, error) {
	idx := legacyImportIndex{Students: map[string][]legacyPersonMatch{}, Proctors: map[string][]legacyPersonMatch{}, Schools: map[string][]legacyNamedMatch{}, Groups: map[string][]legacyNamedMatch{}, GroupOwners: map[int64]int64{}}
	q := `SELECT u.id account_id,u.ref_id profile_id,p.full_name,s.is_active,m.user_id division_member,COALESCE(g.group_id,m.group_id,0) group_id FROM auth_users u JOIN auth_organization_access o ON o.user_id=u.id JOIN auth_account_state s ON s.user_id=u.id JOIN ` + profiles + ` p ON p.id=u.ref_id AND p.role=u.role LEFT JOIN auth_division_access m ON m.user_id=u.id AND m.organization_id=o.organization_id AND m.division_id=? LEFT JOIN core_proctor_groups g ON g.user_id=u.id AND g.division_id=? WHERE o.organization_id=? AND u.role=? ORDER BY u.id`
	for _, role := range []string{"student", "proctor"} {
		rows, e := list(r.Context(), db, q, d, d, a.OrganizationID, role)
		if e != nil {
			return idx, e
		}
		for _, row := range rows {
			name, _ := row["full_name"].(string)
			key := legacyPersonKey(name)
			if key == "" {
				continue
			}
			m := legacyPersonMatch{AccountID: num(row["account_id"]), ProfileID: num(row["profile_id"]), FullName: name, Active: num(row["is_active"]) == 1, InDivision: row["division_member"] != nil, GroupID: num(row["group_id"])}
			if role == "student" {
				idx.Students[key] = append(idx.Students[key], m)
			} else {
				idx.Proctors[key] = append(idx.Proctors[key], m)
			}
		}
	}
	rows, e := list(r.Context(), db, "SELECT s.id,s.name,s.is_active FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE x.organization_id=? ORDER BY s.id", a.OrganizationID)
	if e != nil {
		return idx, e
	}
	for _, row := range rows {
		name, _ := row["name"].(string)
		key := legacyNameKey(name)
		idx.Schools[key] = append(idx.Schools[key], legacyNamedMatch{num(row["id"]), name, num(row["is_active"]) == 1})
	}
	rows, e = list(r.Context(), db, "SELECT g.id,g.name,x.is_active,p.user_id proctor_account_id FROM `groups` g JOIN core_group_scope x ON x.group_id=g.id LEFT JOIN core_proctor_groups p ON p.group_id=g.id AND p.division_id=x.division_id WHERE x.organization_id=? AND x.division_id=? ORDER BY g.id", a.OrganizationID, d)
	if e != nil {
		return idx, e
	}
	for _, row := range rows {
		name, _ := row["name"].(string)
		key := legacyNameKey(name)
		gid := num(row["id"])
		idx.Groups[key] = append(idx.Groups[key], legacyNamedMatch{gid, name, num(row["is_active"]) == 1})
		if row["proctor_account_id"] != nil {
			idx.GroupOwners[gid] = num(row["proctor_account_id"])
		}
	}
	return idx, nil
}
func legacyLoadSession(r *http.Request, db DBTX, a Actor, d, n int64, lock bool) (LegacyImportSession, []LegacyImportRow, error) {
	out := LegacyImportSession{}
	q := "SELECT id,organization_id,division_id,actor_id,revision,status,payload,preview,report,created_at,expires_at FROM core_legacy_imports WHERE id=? AND organization_id=? AND division_id=? AND actor_id=?"
	if lock {
		q += " FOR UPDATE"
	}
	var payload, preview, report []byte
	e := db.QueryRowContext(r.Context(), q, n, a.OrganizationID, d, a.AccountID).Scan(&out.ID, &out.OrganizationID, &out.DivisionID, &out.ActorID, &out.Revision, &out.Status, &payload, &preview, &report, &out.CreatedAt, &out.ExpiresAt)
	if e != nil {
		return out, nil, e
	}
	out.MaxRows = 100
	var in LegacyImportDTO
	if json.Unmarshal(payload, &in) != nil || json.Unmarshal(preview, &out.Preview) != nil {
		return out, nil, unavailable
	}
	if len(report) > 0 && json.Unmarshal(report, &out.Report) != nil {
		return out, nil, unavailable
	}
	if out.Status == "preview" && !time.Now().Before(out.ExpiresAt) {
		out.Status = "expired"
		out.Preview.CanCommit = false
	}
	return out, in.Rows, nil
}
func legacyReceipt(r *http.Request, db DBTX, a Actor, d int64, key, digest string) (LegacyImportSession, int, bool, error) {
	var hash string
	var body []byte
	var code int
	e := db.QueryRowContext(r.Context(), "SELECT command_hash,response,http_status FROM core_legacy_import_receipts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", a.OrganizationID, d, a.AccountID, key).Scan(&hash, &body, &code)
	if errors.Is(e, sql.ErrNoRows) {
		return LegacyImportSession{}, 0, false, nil
	}
	if e != nil {
		return LegacyImportSession{}, 0, false, e
	}
	if hash != digest {
		return LegacyImportSession{}, 0, false, &APIError{409, "idempotency_key_reused"}
	}
	var out LegacyImportSession
	if json.Unmarshal(body, &out) != nil {
		return out, 0, false, unavailable
	}
	if out.Status == "preview" && !time.Now().Before(out.ExpiresAt) {
		out.Status = "expired"
		out.Preview.CanCommit = false
	}
	return out, code, true, nil
}
func legacySaveReceipt(r *http.Request, db DBTX, a Actor, d int64, key, digest string, out LegacyImportSession, code int) error {
	body, e := json.Marshal(out)
	if e != nil {
		return e
	}
	_, e = db.ExecContext(r.Context(), "INSERT INTO core_legacy_import_receipts(organization_id,division_id,actor_id,command_key,command_hash,session_id,response,http_status) VALUES(?,?,?,?,?,?,?,?)", a.OrganizationID, d, a.AccountID, key, digest, out.ID, body, code)
	return e
}

// Serialize matching/apply across imports in the same organization. Explicit
// division lock also shares the existing group/membership mutation boundary.
func legacyLockScope(r *http.Request, db DBTX, a Actor, d int64) error {
	if e := lockDivision(r.Context(), db, a, d); e != nil {
		return e
	}
	var org int64
	return db.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 FOR UPDATE", a.OrganizationID).Scan(&org)
}
func legacyCAS(session LegacyImportSession, revision int64) error {
	if session.Status != "preview" {
		return &APIError{409, "legacy_import_" + session.Status}
	}
	if revision < 1 || session.Revision != revision {
		return &APIError{409, "stale_revision"}
	}
	return nil
}
