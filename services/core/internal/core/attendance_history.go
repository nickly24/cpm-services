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
	"strings"
	"unicode/utf8"
)

type AttendanceHistoryRequest struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	AccountID      int64  `json:"account_id"`
	OperationKey   string `json:"operation_key"`
}
type AttendanceHistoryReservation struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	AccountID      int64  `json:"account_id"`
	Permanent      bool   `json:"permanent"`
	ReservationID  string `json:"reservation_id"`
}

// This small internal DTO rejects duplicate keys as well as unknown fields.
// It deliberately does not change parsing of existing public core endpoints.
func decodeAttendanceHistory(r *http.Request, out *AttendanceHistoryRequest) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return &APIError{415, "json_required"}
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(b) > 4096 || !utf8.Valid(b) {
		return bad("invalid_request")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return bad("invalid_request")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return bad("invalid_request")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return bad("invalid_request")
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return bad("invalid_request")
		}
	}
	if closing, err := d.Token(); err != nil || closing != json.Delim('}') {
		return bad("invalid_request")
	}
	if _, err := d.Token(); err != io.EOF {
		return bad("invalid_request")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return bad("invalid_request")
	}
	if out.OrganizationID < 1 || out.DivisionID < 1 || out.AccountID < 1 || !validText(out.OperationKey, 100, true) || len(out.OperationKey) < 8 || strings.TrimSpace(out.OperationKey) != out.OperationKey || strings.ContainsAny(out.OperationKey, "\r\n\t") {
		return bad("invalid_request")
	}
	return nil
}

func (s *Server) attendanceHistory(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{401, "invalid_service_key"})
		return
	}
	var in AttendanceHistoryRequest
	if err := decodeAttendanceHistory(r, &in); err != nil {
		fail(w, err)
		return
	}
	out := AttendanceHistoryReservation{OrganizationID: in.OrganizationID, DivisionID: in.DivisionID, AccountID: in.AccountID, Permanent: true}
	err := s.transact(r.Context(), func(tx *sql.Tx) error {
		// Shared organization lock is compatible with membership-grant FK reads.
		// Exclusive lock order matches revokeGrant: division, then account.
		var id int64
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM organizations WHERE id=? AND is_active=1 LOCK IN SHARE MODE", in.OrganizationID).Scan(&id); err != nil {
			return err
		}
		if err := lockDivision(r.Context(), tx, Actor{OrganizationID: in.OrganizationID}, in.DivisionID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRowContext(r.Context(), "SELECT role FROM auth_users WHERE id=? FOR UPDATE", in.AccountID).Scan(&role); err != nil {
			return err
		}
		if role != "student" {
			return missing
		}
		var active bool
		if err := tx.QueryRowContext(r.Context(), "SELECT is_active FROM auth_account_state WHERE user_id=? FOR UPDATE", in.AccountID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return missing
		}
		if err := tx.QueryRowContext(r.Context(), "SELECT user_id FROM auth_division_access WHERE organization_id=? AND division_id=? AND user_id=? FOR UPDATE", in.OrganizationID, in.DivisionID, in.AccountID).Scan(&id); err != nil {
			return err
		}
		err := tx.QueryRowContext(r.Context(), "SELECT reservation_id FROM core_attendance_history_reservations WHERE organization_id=? AND division_id=? AND user_id=?", in.OrganizationID, in.DivisionID, in.AccountID).Scan(&out.ReservationID)
		if err == nil {
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return err
		}
		out.ReservationID = hex.EncodeToString(random[:])
		_, err = tx.ExecContext(r.Context(), "INSERT INTO core_attendance_history_reservations(organization_id,division_id,user_id,reservation_id,operation_key) VALUES(?,?,?,?,?)", in.OrganizationID, in.DivisionID, in.AccountID, out.ReservationID, in.OperationKey)
		return err
	})
	if err != nil {
		if translate(err) == missing {
			fail(w, missing)
		} else {
			fail(w, unavailable)
		}
		return
	}
	// No release endpoint: cancelled/deleted attendance retains this marker.
	reply(w, 200, map[string]any{"status": true, "data": out})
}
