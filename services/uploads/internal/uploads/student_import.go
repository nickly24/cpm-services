package uploads

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Passwords never enter this coordinator; the native owner derives each child
// from its encrypted manifest. Applying it uses the existing grant and receipt.
func (s *Server) studentBatch(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !a.OrganizationAdmin || !s.Config.OperationsEnabled {
		return nil, 0, api(403, "student_import_full_admin_required")
	}
	t, _ := findType("users.credentials")
	if e := permission(a, t, true); e != nil {
		return nil, 0, e
	}
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	draft, index := r.PathValue("draft"), r.PathValue("batch")
	if !lowerHexReference(draft, 32) {
		return nil, 0, api(404, "not_found")
	}
	n, e := strconv.Atoi(index)
	if e != nil || n < 0 || strconv.Itoa(n) != index {
		return nil, 0, api(400, "invalid_batch")
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	encoded, _ := json.Marshal(in)
	id, _, e := s.reserve(r.Context(), sc, key, "student.batch:"+draft+":"+index, digest(encoded), uuid())
	if e != nil {
		return nil, 0, e
	}
	ss := Session{ID: id, Scope: sc, Type: t.ID, Owner: t.Owner, Revision: 1, OwnerRevision: 1, State: "staging", FileID: uuid(), Expires: time.Now().UTC().Add(24 * time.Hour)}
	ss, e = s.studentBatchSession(r, sc, key, ss)
	if e != nil {
		return nil, 0, e
	}
	if ss.OwnerSessionID != "" {
		return s.sessionView(r, ss)
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": in.ExpectedRevision, "upload_session_id": ss.ID})
	raw, e := s.call(r.Context(), "POST", s.Config.CoreURL, "/v1/core/divisions/"+strconv.FormatInt(sc.DivisionID, 10)+"/student-imports/"+draft+"/batches/"+index+"/prepare", r.Header.Get("Authorization"), ownerCommand("create", ss.ID, ""), "application/json", body)
	if e != nil {
		return nil, 0, e
	}
	var native struct {
		ID     int64  `json:"id"`
		Source string `json:"source_sha256"`
	}
	if json.Unmarshal(raw, &native) != nil || native.ID < 1 || !lowerHexReference(native.Source, 64) {
		return nil, 0, api(503, "owner_outcome_unknown")
	}
	ss.OwnerSessionID = strconv.FormatInt(native.ID, 10)
	ss.SourceHash = native.Source
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET source_hash=? WHERE id=? AND state='staging'", ss.SourceHash, ss.ID); e != nil {
			return e
		}
		_, e := tx.ExecContext(r.Context(), "UPDATE uploads_files_v1 SET source_hash=? WHERE id=?", ss.SourceHash, ss.FileID)
		return e
	})
	if e != nil {
		return nil, 0, e
	}
	ss, e = s.persistCreationOutcome(r, ss, key, t, "preview", "student.batch.preview", false)
	if e != nil {
		return nil, 0, e
	}
	return s.sessionView(r, ss)
}

// Reservation and session creation can be separated by process death. The
// receipt owns the stable session ID; locking it serializes recovery with the
// original caller. No native owner request is sent until this transaction commits.
func (s *Server) studentBatchSession(r *http.Request, sc Scope, key string, proposed Session) (Session, error) {
	var session Session
	e := s.tx(r.Context(), func(tx *sql.Tx) error {
		var id string
		if e := tx.QueryRowContext(r.Context(), "SELECT resource_id FROM uploads_receipts_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=? FOR UPDATE", sc.OrganizationID, sc.DivisionID, sc.ActorID, key).Scan(&id); e != nil {
			return e
		}
		if id != proposed.ID {
			return api(409, "command_payload_conflict")
		}
		stored, e := loadSession(r.Context(), tx, sc, id, true)
		if e == nil {
			session = stored
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e := insertSession(r.Context(), tx, proposed); e != nil {
			return e
		}
		_, e = tx.ExecContext(r.Context(), "INSERT INTO uploads_files_v1(id,organization_id,division_id,actor_account_id,type,owner,size_bytes,extension,expires_at) VALUES(?,?,?,?,?,?,0,'xlsx',?)", proposed.FileID, sc.OrganizationID, sc.DivisionID, sc.ActorID, proposed.Type, proposed.Owner, proposed.Expires)
		session = proposed
		return e
	})
	return session, e
}

func (s *Server) studentSessionJob(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !a.OrganizationAdmin {
		return nil, 0, api(403, "access_denied")
	}
	ss, e := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if e != nil {
		return nil, 0, e
	}
	if ss.Type != "users.credentials" {
		return nil, 0, api(404, "not_found")
	}
	var id string
	e = s.DB.QueryRowContext(r.Context(), "SELECT id FROM uploads_jobs_v1 WHERE session_id=? AND organization_id=? AND division_id=? AND actor_account_id=?", ss.ID, sc.OrganizationID, sc.DivisionID, sc.ActorID).Scan(&id)
	if e != nil {
		return nil, 0, e
	}
	j, e := s.jobWithCapabilities(r.Context(), a, sc, id)
	return j, 200, e
}

type studentValidationIssue struct {
	Row    int    `json:"row"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type studentValidationError struct{ Issues []studentValidationIssue }

func (e *studentValidationError) Error() string { return "student_import_validation" }
func studentValidation(raw []byte) *studentValidationError {
	var v struct {
		Error  string                   `json:"error"`
		Issues []studentValidationIssue `json:"issues"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Error != "student_import_validation" || len(v.Issues) == 0 || len(v.Issues) > 512 {
		return nil
	}
	fields := map[string]bool{"full_name": true, "profile_id": true, "school": true, "class": true, "login": true}
	reasons := map[string]bool{"name_required_or_too_long": true, "invalid_login": true, "invalid_class": true, "school_not_found": true, "school_ambiguous": true, "duplicate_login": true, "login_exists": true, "invalid_profile_id": true, "duplicate_profile_id": true, "profile_id_exists": true, "duplicate_row": true}
	for _, x := range v.Issues {
		if x.Row < 2 || x.Row > 10000000 || !fields[x.Field] || !reasons[x.Reason] {
			return nil
		}
	}
	return &studentValidationError{v.Issues}
}
