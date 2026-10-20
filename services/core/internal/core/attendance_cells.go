package core

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

type AttendanceCellsRequest struct {
	OrganizationID int64   `json:"organization_id"`
	DivisionID     int64   `json:"division_id"`
	AccountIDs     []int64 `json:"account_ids"`
	ProctorID      *int64  `json:"proctor_account_id"`
	GroupID        *int64  `json:"group_id"`
	OperationKey   string  `json:"operation_key"`
	ScopeOnly      bool    `json:"scope_only"`
}
type AttendanceCellMember struct {
	AccountID     int64  `json:"account_id"`
	Role          string `json:"role"`
	Name          string `json:"full_name"`
	GroupID       *int64 `json:"group_id"`
	ReservationID string `json:"reservation_id"`
	Permanent     bool   `json:"permanent"`
}

func decodeAttendanceCells(r *http.Request, out *AttendanceCellsRequest) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(raw) > 8192 || !utf8.Valid(raw) {
		return bad("invalid_request")
	}
	// The DTO has scalar fields and one scalar array. Reject duplicate/case aliases
	// before Go's case-insensitive struct decoder can silently choose a value.
	d := json.NewDecoder(bytes.NewReader(raw))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return bad("invalid_request")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		lower := strings.ToLower(name)
		if err != nil || !ok || seen[lower] {
			return bad("invalid_request")
		}
		seen[lower] = true
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return bad("invalid_request")
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return bad("invalid_request")
	}
	if _, err = d.Token(); err != io.EOF {
		return bad("invalid_request")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return bad("invalid_request")
	}
	if out.OrganizationID < 1 || out.DivisionID < 1 || !seen["account_ids"] || len(out.AccountIDs) > 100 || !out.ScopeOnly && len(out.AccountIDs) < 1 || out.ScopeOnly && (len(out.AccountIDs) != 0 || out.ProctorID != nil) || !positive(out.ProctorID) || !positive(out.GroupID) || (out.ProctorID == nil) != (out.GroupID == nil) || !validText(out.OperationKey, 100, true) || len(out.OperationKey) < 8 || strings.TrimSpace(out.OperationKey) != out.OperationKey || strings.ContainsAny(out.OperationKey, "\r\n\t") {
		return bad("invalid_request")
	}
	ids := map[int64]bool{}
	for _, id := range out.AccountIDs {
		if id < 1 || ids[id] {
			return bad("invalid_request")
		}
		ids[id] = true
	}
	return nil
}

// One bounded operation replaces division lookup, member/group lookup and history
// guard RPCs. Division locking serializes with membership/group revocation; the
// permanent marker is returned only after the same transaction commits.
func (s *Server) attendanceCells(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	var in AttendanceCellsRequest
	if err := decodeAttendanceCells(r, &in); err != nil {
		fail(w, err)
		return
	}
	sort.Slice(in.AccountIDs, func(i, j int) bool { return in.AccountIDs[i] < in.AccountIDs[j] })
	members := []AttendanceCellMember{}
	err := s.transact(r.Context(), func(tx *sql.Tx) error {
		var id int64
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 LOCK IN SHARE MODE", in.OrganizationID).Scan(&id); err != nil {
			return err
		}
		if err := lockDivision(r.Context(), tx, Actor{OrganizationID: in.OrganizationID}, in.DivisionID); err != nil {
			return err
		}
		if in.ScopeOnly {
			return nil
		}
		if in.ProctorID != nil {
			// A fresh Auth group grant alone is insufficient: the live Core assignment
			// and active account/membership must still agree inside this transaction.
			err := tx.QueryRowContext(r.Context(), `SELECT p.user_id FROM core_proctor_groups p
 JOIN auth_users a ON a.id=p.user_id AND a.role='proctor'
 JOIN auth_account_state ast ON ast.user_id=a.id AND ast.is_active=1
 JOIN auth_organization_access oa ON oa.user_id=a.id AND oa.organization_id=?
 JOIN auth_division_access m ON m.user_id=a.id AND m.organization_id=oa.organization_id AND m.division_id=p.division_id AND m.group_id=p.group_id
 JOIN core_group_scope g ON g.group_id=p.group_id AND g.organization_id=m.organization_id AND g.division_id=m.division_id AND g.is_active=1
 WHERE p.division_id=? AND p.user_id=? AND p.group_id=? FOR UPDATE`, in.OrganizationID, in.DivisionID, *in.ProctorID, *in.GroupID).Scan(&id)
			if err == sql.ErrNoRows {
				return denied
			}
			if err != nil {
				return err
			}
		}
		// Lock and project sets, not one SQL round trip per cell/student. The
		// number of database queries stays bounded for a 100-cell stroke.
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(in.AccountIDs)), ",")
		accounts := []any{}
		for _, account := range in.AccountIDs {
			accounts = append(accounts, account)
		}
		users, err := list(r.Context(), tx, "SELECT id,role FROM auth_users WHERE id IN ("+placeholders+") ORDER BY id FOR UPDATE", accounts...)
		if err != nil {
			return err
		}
		if len(users) != len(in.AccountIDs) {
			return missing
		}
		for _, u := range users {
			if u["role"] != "student" {
				return missing
			}
		}
		states, err := list(r.Context(), tx, "SELECT user_id,is_active FROM auth_account_state WHERE user_id IN ("+placeholders+") ORDER BY user_id FOR UPDATE", accounts...)
		if err != nil {
			return err
		}
		if len(states) != len(in.AccountIDs) {
			return missing
		}
		for _, state := range states {
			if num(state["is_active"]) != 1 {
				return missing
			}
		}
		scopeArgs := append([]any{in.OrganizationID, in.DivisionID}, accounts...)
		grants, err := list(r.Context(), tx, "SELECT user_id FROM auth_division_access WHERE organization_id=? AND division_id=? AND user_id IN ("+placeholders+") ORDER BY user_id FOR UPDATE", scopeArgs...)
		if err != nil {
			return err
		}
		if len(grants) != len(in.AccountIDs) {
			return missing
		}
		rows, err := list(r.Context(), tx, `SELECT a.id account_id,p.full_name,g.group_id FROM auth_users a
 JOIN auth_organization_access oa ON oa.user_id=a.id AND oa.organization_id=?
 JOIN auth_division_access m ON m.user_id=a.id AND m.organization_id=oa.organization_id AND m.division_id=?
 JOIN students p ON p.id=a.ref_id
 LEFT JOIN core_group_scope g ON g.group_id=m.group_id AND g.organization_id=m.organization_id AND g.division_id=m.division_id AND g.is_active=1
 WHERE a.id IN (`+placeholders+`) ORDER BY a.id`, scopeArgs...)
		if err != nil {
			return err
		}
		if len(rows) != len(in.AccountIDs) {
			return missing
		}
		reservations, err := list(r.Context(), tx, "SELECT user_id,reservation_id FROM core_attendance_history_reservations WHERE organization_id=? AND division_id=? AND user_id IN ("+placeholders+")", scopeArgs...)
		if err != nil {
			return err
		}
		reserved := map[int64]string{}
		for _, reservation := range reservations {
			value, _ := reservation["reservation_id"].(string)
			if value == "" {
				return unavailable
			}
			reserved[num(reservation["user_id"])] = value
		}
		inserts := []string{}
		insertArgs := []any{}
		for _, row := range rows {
			account := num(row["account_id"])
			name, _ := row["full_name"].(string)
			m := AttendanceCellMember{AccountID: account, Role: "student", Name: name, GroupID: pointer(row["group_id"]), Permanent: true, ReservationID: reserved[account]}
			if in.ProctorID != nil && (m.GroupID == nil || *m.GroupID != *in.GroupID) {
				return denied
			}
			if m.ReservationID == "" {
				var random [32]byte
				if _, err = rand.Read(random[:]); err != nil {
					return err
				}
				m.ReservationID = hex.EncodeToString(random[:])
				inserts = append(inserts, "(?,?,?,?,?)")
				insertArgs = append(insertArgs, in.OrganizationID, in.DivisionID, account, m.ReservationID, in.OperationKey)
			}
			members = append(members, m)
		}
		if len(inserts) > 0 {
			if _, err = tx.ExecContext(r.Context(), "INSERT INTO core_attendance_history_reservations(organization_id,division_id,user_id,reservation_id,operation_key) VALUES"+strings.Join(inserts, ","), insertArgs...); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"status": true, "data": map[string]any{"organization_id": in.OrganizationID, "division_id": in.DivisionID, "members": members}})
}
