package uploads

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) create(r *http.Request, a Actor, sc Scope) (any, int, error) {
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	var t Type
	var ext, target string
	var content, body []byte
	var ct string
	var hash string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var fields map[string]string
		content, ext, fields, e = readFile(r)
		if e != nil {
			return nil, 0, e
		}
		t, e = findType(fields["type"])
		if e != nil {
			return nil, 0, e
		}
		target = fields["target_id"]
		found := false
		for _, f := range t.Formats {
			if f == ext {
				found = true
			}
		}
		if !found || t.ID == "cards.transform" {
			return nil, 0, api(415, "unsupported_file_format")
		}
		if e = permission(a, t, true); e != nil {
			return nil, 0, e
		}
		if !t.Create || t.Mode != "owner_session" && t.Mode != "validated_input" {
			return nil, 0, api(501, "owner_adapter_pending")
		}
		rows := t.MaxRows
		table := t.ID == "tests.json" && ext == "xlsx"
		if table {
			rows = 0
		}
		if e = validFile(content, ext, rows, table); e != nil {
			return nil, 0, e
		}
		if t.ID == "users.legacy" || stagedType(t.ID) {
			body, e = parseOwnerInput(t.ID, content, ext, fields)
			ct = "application/json"
			if e == nil && t.ID == "tests.json" {
				direction, er := parseID(fields["direction_id"])
				if er != nil {
					return nil, 0, api(400, "direction_required")
				}
				body, e = json.Marshal(stagedJSONInput{DirectionID: direction, Payload: body})
			}
		} else {
			body, ct, e = ownerMultipart(content, ext, fields)
		}
		if e != nil {
			return nil, 0, e
		}
		meta, _ := json.Marshal(fields)
		hash = digest(append(append([]byte(ext), meta...), content...))
	} else {
		var in CreateDTO
		if e = decode(r, &in); e != nil {
			return nil, 0, e
		}
		t, e = findType(in.Type)
		if e != nil {
			return nil, 0, e
		}
		if e = permission(a, t, true); e != nil {
			return nil, 0, e
		}
		if in.Type != "cards.transform" && !stagedType(in.Type) {
			return nil, 0, api(501, "owner_adapter_pending")
		}
		if !t.Create {
			return nil, 0, api(501, "owner_adapter_pending")
		}
		if !json.Valid(in.OwnerPayload) {
			return nil, 0, api(400, "owner_payload_required")
		}
		body = in.OwnerPayload
		ct = "application/json"
		target = in.TargetID
		ext = "json"
		content = body
		hash = digest(append([]byte(in.Type+":"+target), body...))
	}
	if t.MaxBytes > 0 && int64(len(content)) > t.MaxBytes {
		return nil, 0, api(413, "type_file_too_large")
	}
	if strings.HasPrefix(t.ID, "exams.") {
		if _, e = parseID(target); e != nil {
			return nil, 0, api(400, "exam_target_required")
		}
	} else if t.ID == "tests.external-results" {
		target, e = stagedTarget(target)
		if e != nil {
			return nil, 0, e
		}
	} else if target != "" {
		return nil, 0, api(400, "unexpected_target")
	}
	id, fresh, e := s.reserve(r.Context(), sc, key, "session.create", hash, uuid())
	if e != nil {
		return nil, 0, e
	}
	ss := Session{ID: id, Scope: sc, Type: t.ID, Owner: t.Owner, TargetID: target, Revision: 1, OwnerRevision: 1, State: "staging", FileID: uuid(), SourceHash: digest(content), Expires: time.Now().UTC().Add(72 * time.Hour)}
	if !fresh {
		ss, e = loadSession(r.Context(), s.DB, sc, id, false)
		if e != nil {
			return nil, 0, api(409, "command_outcome_unknown")
		}
		if !ss.Expires.After(time.Now()) {
			return nil, 0, api(410, "session_expired")
		}
		if ss.State == "rejected" {
			return ss, 200, nil
		}
		if ss.State != "staging" && ss.State != "outcome_unknown" {
			return s.sessionView(r, ss)
		}
		// Replaying requires a verified native idempotency contract. The same
		// uploaded bytes and fields are already checked by our request receipt.
		if ss.Type == "users.credentials" {
			return s.recoverCredentialPreview(r, ss, key, t)
		}
		if !creationReplaySafe(ss.Type) {
			return ss, 200, nil
		}
	}
	// Credential files are processed only in memory; core stores password hashes.
	stored := content
	if t.ID == "users.credentials" {
		stored = nil
	}
	if fresh {
		e = s.tx(r.Context(), func(tx *sql.Tx) error {
			if e := insertSession(r.Context(), tx, ss); e != nil {
				return e
			}
			_, e := tx.ExecContext(r.Context(), "INSERT INTO uploads_files_v1(id,organization_id,division_id,actor_account_id,type,owner,source_hash,size_bytes,extension,content,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)", ss.FileID, sc.OrganizationID, sc.DivisionID, sc.ActorID, t.ID, t.Owner, ss.SourceHash, len(content), ext, stored, ss.Expires)
			if e != nil {
				return e
			}
			return event(r.Context(), tx, sc, t, id, "session.staging", 1)
		})
		if e != nil {
			return nil, 0, e
		}
	}
	raw, ownerErr := s.ownerCreate(r.Context(), ss, r.Header.Get("Authorization"), ownerCommand("create", ss.ID, ""), ct, body)
	if ownerErr != nil {
		state := "rejected"
		if ae, ok := ownerErr.(*APIError); ok && ae.Code >= 500 {
			state = "outcome_unknown"
		}
		if _, e = s.persistCreationOutcome(r, ss, key, t, state, "session."+state, false); e != nil {
			return nil, 0, e
		}
		return nil, 0, ownerErr
	}
	ss.OwnerSessionID, ss.OwnerRevision, e = creationReference(ss.Type, raw)
	if e != nil {
		if _, persistErr := s.persistCreationOutcome(r, ss, key, t, "outcome_unknown", "session.outcome_unknown", false); persistErr != nil {
			return nil, 0, persistErr
		}
		return nil, 0, e
	}
	ss, e = s.persistCreationOutcome(r, ss, key, t, "preview", "session.preview", false)
	if e != nil {
		return nil, 0, e
	}
	view, _, e := s.sessionView(r, ss)
	if !fresh {
		return view, 200, e
	}
	return view, 201, e
}
func (s *Server) sessionView(r *http.Request, ss Session) (any, int, error) {
	if ss.State != "completed" && time.Now().After(ss.Expires) {
		return nil, 0, api(410, "session_expired")
	}
	if ss.OwnerSessionID == "" {
		return ss, 200, nil
	}
	raw, e := s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
	if e != nil {
		return nil, 0, e
	}
	ss.Preview = masked(raw)
	return ss, 200, nil
}
func (s *Server) sessionGet(r *http.Request, a Actor, sc Scope) (any, int, error) {
	ss, e := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if e != nil {
		return nil, 0, e
	}
	t, e := findType(ss.Type)
	if e != nil {
		return nil, 0, e
	}
	if e = permission(a, t, false); e != nil {
		return nil, 0, e
	}
	if ss.Type == "users.credentials" && (ss.State == "staging" || ss.State == "outcome_unknown") {
		return s.recoverCredentialPreview(r, ss, "", t)
	}
	return s.sessionView(r, ss)
}
func (s *Server) edit(r *http.Request, a Actor, sc Scope) (any, int, error) {
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	var in EditDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	ss, e := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if e != nil {
		return nil, 0, e
	}
	t, e := findType(ss.Type)
	if e != nil {
		return nil, 0, e
	}
	if e = permission(a, t, true); e != nil {
		return nil, 0, e
	}
	if !t.Edit {
		return nil, 0, api(405, "owner_preview_not_editable")
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(in.OwnerPayload, &payload) != nil || payload == nil {
		return nil, 0, api(400, "owner_payload_required")
	}
	// Revision is owned by the coordinator, supplied from its stable owner reference.
	if _, ok := payload["expected_revision"]; ok {
		return nil, 0, api(400, "use_coordinator_revision")
	}
	payload["expected_revision"], _ = json.Marshal(ss.OwnerRevision)
	body, _ := json.Marshal(payload)
	encoded, _ := json.Marshal(in)
	_, fresh, e := s.reserve(r.Context(), sc, key, "session.edit:"+ss.ID, digest(encoded), ss.ID)
	if e != nil {
		return nil, 0, e
	}
	if !fresh {
		return s.sessionView(r, ss)
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		x, e := loadSession(r.Context(), tx, sc, ss.ID, true)
		if e != nil {
			return e
		}
		if x.State != "preview" || x.Revision != in.ExpectedRevision || !x.Expires.After(time.Now()) {
			return api(409, "preview_not_editable_or_modified")
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state='editing' WHERE id=?", ss.ID)
		return e
	})
	if e != nil {
		return nil, 0, e
	}
	var raw json.RawMessage
	var er error
	if stagedType(ss.Type) {
		raw, er = s.editStagedPreview(r.Context(), ss, r.Header.Get("Authorization"), body)
	} else {
		base, path, e := s.ownerPath(ss)
		if e != nil {
			return nil, 0, e
		}
		raw, er = s.call(r.Context(), "PUT", base, path, r.Header.Get("Authorization"), ownerCommand("edit", ss.ID, key), "application/json", body)
	}
	state := "preview"
	rev := ss.OwnerRevision
	if er == nil {
		if stagedType(ss.Type) {
			rev, _ = ownerState(raw)
		} else {
			rev, er = nativeEditRevision(raw, ss)
		}
	}
	if ae, ok := er.(*APIError); ok && ae.Code >= 500 {
		state = "edit_unknown"
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		inc := 0
		if er == nil {
			inc = 1
		}
		result, e := tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state=?,owner_revision=?,revision=revision+? WHERE id=? AND state='editing' AND revision=? AND owner_revision=?", state, rev, inc, ss.ID, ss.Revision, ss.OwnerRevision)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return api(409, "preview_modified")
		}
		if e = receipt(r.Context(), tx, sc, key, state); e != nil {
			return e
		}
		return event(r.Context(), tx, sc, t, ss.ID, "session.edit."+state, ss.Revision+int64(inc))
	})
	if e != nil {
		return nil, 0, e
	}
	if er != nil {
		return nil, 0, er
	}
	ss, e = loadSession(r.Context(), s.DB, sc, ss.ID, false)
	if e != nil {
		return nil, 0, e
	}
	// Native mutation responses may contain only the first preview page. Read
	// the accepted session through the complete protected owner adapter.
	raw, e = s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
	if e != nil {
		return nil, 0, e
	}
	id, currentRevision, e := ref(raw)
	if e != nil {
		return nil, 0, e
	}
	if id != ss.OwnerSessionID || currentRevision != ss.OwnerRevision {
		return nil, 0, api(409, "owner_preview_changed_after_edit")
	}
	ss.Preview = masked(raw)
	return ss, 200, nil
}

// A successful mutation response must identify the edited preview and its next
// revision. An invalid response leaves the outcome unknown for reconciliation.
func nativeEditRevision(raw json.RawMessage, ss Session) (int64, error) {
	id, rev, e := ref(raw)
	var v struct {
		Revision int64  `json:"revision"`
		State    string `json:"state"`
		Status   string `json:"status"`
	}
	if e != nil || json.Unmarshal(raw, &v) != nil || id != ss.OwnerSessionID || v.Revision < 1 || rev != ss.OwnerRevision+1 {
		return ss.OwnerRevision, api(503, "owner_invalid_edit_response")
	}
	state := v.State
	if state == "" {
		state = v.Status
	}
	expectedState := "preview"
	if strings.HasPrefix(ss.Type, "exams.") {
		expectedState = "editable"
	}
	if state != expectedState {
		return ss.OwnerRevision, api(503, "owner_invalid_edit_response")
	}
	return rev, nil
}

func (s *Server) reconcile(r *http.Request, a Actor, sc Scope) (any, int, error) {
	ss, e := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if e != nil {
		return nil, 0, e
	}
	t, _ := findType(ss.Type)
	if e = permission(a, t, true); e != nil {
		return nil, 0, e
	}
	if ss.Type == "users.credentials" && (ss.State == "staging" || ss.State == "outcome_unknown") {
		return s.recoverCredentialPreview(r, ss, "", t)
	}
	if ss.State != "edit_unknown" {
		return nil, 0, api(409, "nothing_to_reconcile")
	}
	// Manual reconciliation is a read of the owner revision, never a second edit.
	raw, e := s.ownerGet(r.Context(), ss, r.Header.Get("Authorization"))
	if e != nil {
		return nil, 0, e
	}
	rev, state := ownerState(raw)
	if state != "preview" && state != "editable" {
		return nil, 0, api(409, "owner_not_editable")
	}
	if rev != ss.OwnerRevision && rev != ss.OwnerRevision+1 {
		return nil, 0, api(409, "owner_changed_outside_coordinator")
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		result, e := tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state='preview',revision=revision+?,owner_revision=? WHERE id=? AND state='edit_unknown' AND revision=? AND owner_revision=?", rev-ss.OwnerRevision, rev, ss.ID, ss.Revision, ss.OwnerRevision)
		if e != nil {
			return e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return api(409, "preview_modified")
		}
		return event(r.Context(), tx, sc, t, ss.ID, "session.reconciled", ss.Revision+rev-ss.OwnerRevision)
	})
	if e != nil {
		return nil, 0, e
	}
	return s.sessionGet(r, a, sc)
}
func (s *Server) commit(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if s.Config.OperationsEnabled {
		return s.confirmedCommit(r, a, sc)
	}
	key, e := command(r)
	if e != nil {
		return nil, 0, e
	}
	var in RevisionDTO
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	ss, e := loadSession(r.Context(), s.DB, sc, r.PathValue("session"), false)
	if e != nil {
		return nil, 0, e
	}
	t, _ := findType(ss.Type)
	if e = permission(a, t, true); e != nil {
		return nil, 0, e
	}
	if !t.Commit {
		return nil, 0, api(501, "owner_adapter_pending")
	}
	if ss.Revision != in.ExpectedRevision {
		return nil, 0, api(409, "preview_modified")
	}
	if stagedType(ss.Type) && ss.State == "preview" {
		if e = s.stagedConfirmable(r.Context(), ss, r.Header.Get("Authorization")); e != nil {
			return nil, 0, e
		}
	}
	encoded, _ := json.Marshal(in)
	jid, _, e := s.reserve(r.Context(), sc, key, "session.commit:"+ss.ID, digest(encoded), uuid())
	if e != nil {
		return nil, 0, e
	}
	e = s.tx(r.Context(), func(tx *sql.Tx) error {
		x, e := loadSession(r.Context(), tx, sc, ss.ID, true)
		if e != nil {
			return e
		}
		if x.Revision != in.ExpectedRevision {
			return api(409, "preview_modified")
		}
		if x.State == "queued" || x.State == "completed" || x.State == "failed" || x.State == "awaiting_authorization" {
			if e = tx.QueryRowContext(r.Context(), "SELECT id FROM uploads_jobs_v1 WHERE session_id=?", x.ID).Scan(&jid); e != nil {
				return e
			}
			_, e = tx.ExecContext(r.Context(), "UPDATE uploads_receipts_v1 SET resource_id=?,state='completed' WHERE organization_id=? AND division_id=? AND actor_account_id=? AND command_id=?", jid, sc.OrganizationID, sc.DivisionID, sc.ActorID, key)
			return e
		}
		if x.State != "preview" || !x.Expires.After(time.Now()) {
			return api(409, "preview_not_confirmable")
		}
		_, e = tx.ExecContext(r.Context(), "INSERT INTO uploads_jobs_v1(id,organization_id,division_id,actor_account_id,session_id,type,owner,status) VALUES(?,?,?,?,?,?,?,'queued')", jid, sc.OrganizationID, sc.DivisionID, sc.ActorID, x.ID, t.ID, t.Owner)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE uploads_sessions_v1 SET state='queued' WHERE id=?", x.ID)
		if e != nil {
			return e
		}
		if e = receipt(r.Context(), tx, sc, key, "completed"); e != nil {
			return e
		}
		if e = EnqueueImport(r.Context(), tx, jid, sc); e != nil {
			return e
		}
		return event(r.Context(), tx, sc, t, jid, "job.queued", x.Revision)
	})
	if e != nil {
		return nil, 0, e
	}
	j, e := loadJob(r.Context(), s.DB, sc, jid)
	return j, 202, e
}
func (s *Server) listSessions(r *http.Request, a Actor, sc Scope) (any, int, error) {
	if !a.Can("upload", false) {
		return nil, 0, api(403, "access_denied")
	}
	limit, e := limit(r)
	if e != nil {
		return nil, 0, e
	}
	rows, e := s.DB.QueryContext(r.Context(), "SELECT id,type FROM uploads_sessions_v1 WHERE organization_id=? AND division_id=? AND actor_account_id=? ORDER BY created_at DESC LIMIT ?", sc.OrganizationID, sc.DivisionID, sc.ActorID, limit)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id, kind string
		if e = rows.Scan(&id, &kind); e != nil {
			return nil, 0, e
		}
		t, _ := findType(kind)
		if permission(a, t, false) == nil {
			ids = append(ids, id)
		}
	}
	if e = rows.Err(); e != nil {
		return nil, 0, e
	}
	out := []Session{}
	for _, id := range ids {
		x, e := loadSession(r.Context(), s.DB, sc, id, false)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, x)
	}
	return out, 200, nil
}
func limit(r *http.Request) (int, error) {
	n := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		x, e := strconv.Atoi(v)
		if e != nil {
			return 0, api(400, "invalid_limit")
		}
		n = x
	}
	if n < 1 || n > 200 {
		return 0, api(400, "invalid_limit")
	}
	return n, nil
}
func (s *Server) cleanup(ctx context.Context) error {
	// Only coordinator-owned input bytes. Domain objects, previews and S3 buckets are untouched.
	statement := "UPDATE uploads_jobs_v1 j JOIN uploads_sessions_v1 ss ON ss.id=j.session_id SET j.status='failed',j.error_code='source_expired',j.lease_token=NULL,j.lease_until=NULL WHERE ss.expires_at<UTC_TIMESTAMP(6) AND j.status IN ('queued','retry','awaiting_authorization')"
	if s.Config.OperationsEnabled {
		statement = "UPDATE uploads_jobs_v1 j JOIN uploads_sessions_v1 ss ON ss.id=j.session_id LEFT JOIN uploads_execution_plans_v1 p ON p.job_id=j.id SET j.status=IF(p.apply_started_at IS NOT NULL OR p.grant_mode='receipt','outcome_unknown','authorization_expired'),ss.state=IF(p.apply_started_at IS NOT NULL OR p.grant_mode='receipt','outcome_unknown','authorization_expired'),j.error_code='source_expired',j.lease_token=NULL,j.lease_until=NULL WHERE ss.expires_at<UTC_TIMESTAMP(6) AND j.status IN ('queued','retry','awaiting_authorization','authorizing','authorization_unknown')"
	}
	if _, e := s.DB.ExecContext(ctx, statement); e != nil {
		return e
	}
	_, e := s.DB.ExecContext(ctx, "UPDATE uploads_files_v1 f SET content=NULL WHERE f.expires_at<UTC_TIMESTAMP(6) AND f.content IS NOT NULL AND NOT EXISTS (SELECT 1 FROM uploads_sessions_v1 ss JOIN uploads_jobs_v1 j ON j.session_id=ss.id WHERE ss.file_id=f.id AND j.status IN ('queued','running','retry','awaiting_authorization')) LIMIT 100")
	if e != nil {
		return e
	}
	_, e = s.DB.ExecContext(ctx, "DELETE p FROM uploads_previews_v1 p JOIN uploads_sessions_v1 ss ON ss.id=p.session_id WHERE ss.expires_at<UTC_TIMESTAMP(6) AND NOT EXISTS (SELECT 1 FROM uploads_jobs_v1 j WHERE j.session_id=ss.id AND j.status='running')")
	return e
}
