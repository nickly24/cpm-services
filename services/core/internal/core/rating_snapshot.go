package core

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// RatingSnapshotRequest is an internal service DTO. The caller has already
// authorized the public request; this endpoint trusts only service credentials.
type RatingSnapshotRequest struct {
	OrganizationID int64  `json:"organization_id"`
	DivisionID     int64  `json:"division_id"`
	DateFrom       string `json:"date_from"`
	DateTo         string `json:"date_to"`
	AsOf           string `json:"as_of"`
}

func (in RatingSnapshotRequest) validate() error {
	if in.OrganizationID < 1 || in.DivisionID < 1 {
		return bad("invalid_scope")
	}
	if e := period(in.DateFrom, in.DateTo); e != nil {
		return e
	}
	start, _ := time.Parse("2006-01-02", in.DateFrom)
	if start.Year() < 1000 {
		return bad("invalid_date_range")
	}
	at, e := time.Parse(time.RFC3339Nano, in.AsOf)
	if e != nil {
		return bad("invalid_as_of")
	}
	_, offset := at.Zone()
	if offset != 0 || at.Year() < 1000 {
		return bad("invalid_as_of")
	}
	return nil
}

type RatingAccount struct {
	AccountID       int64   `json:"account_id"`
	ProfileID       int64   `json:"profile_id"`
	FullName        string  `json:"full_name"`
	Class           *int64  `json:"class"`
	GroupID         *int64  `json:"group_id"`
	GroupName       *string `json:"group_name"`
	SchoolID        *int64  `json:"school_id"`
	SchoolName      *string `json:"school_name"`
	SchoolShortName *string `json:"school_short_name"`
	IsActive        bool    `json:"is_active"`
}

type RatingDirection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type RatingCoreSnapshot struct {
	Source         string            `json:"source"`
	OrganizationID int64             `json:"organization_id"`
	DivisionID     int64             `json:"division_id"`
	Accounts       []RatingAccount   `json:"accounts"`
	Directions     []RatingDirection `json:"directions"`
	InputHash      string            `json:"input_hash"`
}

func (out RatingCoreSnapshot) fingerprint() (string, error) {
	// The fingerprint contains no capture timestamp or derived hash. Struct field
	// order and database ID order make this canonical for this schema version.
	body, e := json.Marshal(struct {
		Source         string            `json:"source"`
		OrganizationID int64             `json:"organization_id"`
		DivisionID     int64             `json:"division_id"`
		Accounts       []RatingAccount   `json:"accounts"`
		Directions     []RatingDirection `json:"directions"`
	}{out.Source, out.OrganizationID, out.DivisionID, out.Accounts, out.Directions})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:]), nil
}

func (s *Server) ratingSnapshot(w http.ResponseWriter, r *http.Request) {
	if len(s.InternalKey) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Service-Key")), []byte(s.InternalKey)) != 1 {
		fail(w, &APIError{http.StatusUnauthorized, "invalid_service_key"})
		return
	}
	var in RatingSnapshotRequest
	if e := decode(r, &in); e != nil {
		fail(w, e)
		return
	}
	if e := in.validate(); e != nil {
		fail(w, e)
		return
	}
	tx, e := s.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	var division int64
	e = tx.QueryRowContext(r.Context(), `SELECT d.id FROM divisions d JOIN organizations o ON o.id=d.organization_id
 WHERE d.organization_id=? AND d.id=? AND d.is_active=1 AND o.is_active=1`, in.OrganizationID, in.DivisionID).Scan(&division)
	if e != nil {
		fail(w, e)
		return
	}
	out := RatingCoreSnapshot{Source: "core", OrganizationID: in.OrganizationID, DivisionID: in.DivisionID,
		Accounts: []RatingAccount{}, Directions: []RatingDirection{}}
	rows, e := tx.QueryContext(r.Context(), `SELECT a.id,a.ref_id,p.full_name,p.`+"`class`"+`,g.group_id,gn.name,
 ss.school_id,sc.name,sc.short_name,COALESCE(ast.is_active,0)
 FROM auth_division_access m JOIN auth_users a ON a.id=m.user_id AND a.role='student'
 JOIN students p ON p.id=a.ref_id LEFT JOIN auth_account_state ast ON ast.user_id=a.id
 LEFT JOIN core_group_scope g ON g.group_id=m.group_id AND g.organization_id=m.organization_id
  AND g.division_id=m.division_id AND g.is_active=1
 LEFT JOIN `+"`groups`"+` gn ON gn.id=g.group_id
 LEFT JOIN core_school_scope ss ON ss.school_id=p.school_id AND ss.organization_id=m.organization_id
 LEFT JOIN schools sc ON sc.id=ss.school_id
 WHERE m.organization_id=? AND m.division_id=? ORDER BY a.id LIMIT 2001`, in.OrganizationID, in.DivisionID)
	if e != nil {
		fail(w, e)
		return
	}
	for rows.Next() {
		var a RatingAccount
		if e = rows.Scan(&a.AccountID, &a.ProfileID, &a.FullName, &a.Class, &a.GroupID, &a.GroupName,
			&a.SchoolID, &a.SchoolName, &a.SchoolShortName, &a.IsActive); e != nil {
			break
		}
		out.Accounts = append(out.Accounts, a)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	if len(out.Accounts) > 2000 {
		fail(w, bad("rating_snapshot_too_large"))
		return
	}
	// Archived directions can still label historical tests, so retain their names.
	rows, e = tx.QueryContext(r.Context(), `SELECT d.id,d.name FROM directions d JOIN core_direction_scope x
 ON x.direction_id=d.id WHERE x.organization_id=? ORDER BY d.id LIMIT 2001`, in.OrganizationID)
	if e != nil {
		fail(w, e)
		return
	}
	for rows.Next() {
		var d RatingDirection
		if e = rows.Scan(&d.ID, &d.Name); e != nil {
			break
		}
		out.Directions = append(out.Directions, d)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		fail(w, e)
		return
	}
	if len(out.Directions) > 2000 {
		fail(w, bad("rating_snapshot_too_large"))
		return
	}
	if out.InputHash, e = out.fingerprint(); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, e)
		return
	}
	reply(w, http.StatusOK, map[string]any{"status": true, "data": out})
}
