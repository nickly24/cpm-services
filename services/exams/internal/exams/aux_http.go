package exams

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func importRowsPublic(rows []ImportRow) []ImportRow {
	out := append([]ImportRow(nil), rows...)
	for i := range out {
		out[i].PrivilegeBaselineVersion = nil
		out[i].PrivilegeBaselineAccountID = 0
	}
	return out
}
func (s *Server) importMembers(r *http.Request, a Actor, p Params, e *Exam, rows []ImportRow) (map[int64]Member, error) {
	ids, logins, err := ExtractImportReferences(rows)
	if err != nil {
		return nil, err
	}
	d, ok := s.Deps.(*Dependencies)
	if !ok {
		return s.Deps.References(r, a.OrganizationID, []int64{p.DivisionID}, e.DirectionID, ids)
	}
	out := map[int64]Member{}
	add := func(set ReferenceSet) {
		for _, v := range set.Members {
			if contains(v.DivisionIDs, p.DivisionID) {
				out[v.AccountID] = Member{AccountID: v.AccountID, Role: v.Role, FullName: v.FullName, Active: v.Active, Login: v.Login, DivisionIDs: v.DivisionIDs}
			}
		}
	}
	fetch := func(is []int64, ls []string) error {
		set, er := d.Fetch(r, a.OrganizationID, []int64{p.DivisionID}, e.DirectionID, is, ls)
		if er == nil {
			add(set)
			return nil
		}
		api, ok := er.(*APIError)
		if !ok || api.Status != 404 {
			return er
		}
		for _, id := range is {
			set, er = d.Fetch(r, a.OrganizationID, []int64{p.DivisionID}, e.DirectionID, []int64{id}, nil)
			if er == nil {
				add(set)
			} else if v, ok := er.(*APIError); !ok || v.Status != 404 {
				return er
			}
		}
		for _, login := range ls {
			set, er = d.Fetch(r, a.OrganizationID, []int64{p.DivisionID}, e.DirectionID, nil, []string{login})
			if er == nil {
				add(set)
			} else if v, ok := er.(*APIError); !ok || v.Status != 404 {
				return er
			}
		}
		return nil
	}
	for i := 0; i < len(ids); i += 500 {
		end := i + 500
		if end > len(ids) {
			end = len(ids)
		}
		if err = fetch(ids[i:end], nil); err != nil {
			return nil, err
		}
	}
	for i := 0; i < len(logins); i += 500 {
		end := i + 500
		if end > len(logins) {
			end = len(logins)
		}
		if err = fetch(nil, logins[i:end]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type importSession struct {
	Revision   int64
	State      string
	Rows       []byte
	Result     []byte
	ExpiresAt  time.Time
	Filename   string
	SourceHash string
}

func loadImportSession(r *http.Request, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, a Actor, p Params, examID int64, kind, id string, locked bool) (importSession, error) {
	var session importSession
	query := "SELECT revision,state,rows_json,result,expires_at,source_filename,source_hash FROM exams_imports_v1 WHERE id=? AND organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND kind=?"
	if locked {
		query += " FOR UPDATE"
	}
	err := q.QueryRowContext(r.Context(), query, id, a.OrganizationID, p.DivisionID, a.AccountID, examID, kind).Scan(&session.Revision, &session.State, &session.Rows, &session.Result, &session.ExpiresAt, &session.Filename, &session.SourceHash)
	if err == sql.ErrNoRows {
		return session, Missing
	}
	if err != nil {
		return session, err
	}
	if !session.ExpiresAt.After(time.Now().UTC()) {
		return session, &APIError{410, "import_expired"}
	}
	if session.State != "editable" && session.State != "committed" {
		return session, &APIError{503, "invalid_import_state"}
	}
	return session, nil
}
func importRequestScope(e *Exam, a Actor, p Params, edit bool, kind string) error {
	if !importPermission(a, edit) {
		return Denied
	}
	if err := scope(e, a, p, edit, kind == "questions"); err != nil {
		return err
	}
	if _, err := e.Division(p.DivisionID); err != nil {
		return err
	}
	if (kind == "outside") != (e.Kind == "outside_lms") {
		return Bad("wrong_exam_type")
	}
	return nil
}
func (s *Server) importRoute(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !importMethod(r) {
		failure(w, &APIError{405, "method_not_allowed"})
		return
	}
	edit := r.Method != "GET"
	if !importPermission(a, edit) {
		failure(w, Denied)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	if p.DivisionID < 1 {
		failure(w, Bad("division_context_required"))
		return
	}
	page, err := importPage(p)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method != "GET" && page.ExpectedRevision != nil {
		failure(w, Bad("unexpected_preview_query"))
		return
	}

	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	ex, err := load(r.Context(), s.DB, a.OrganizationID, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	kind := r.PathValue("kind")
	if !importKind(kind) {
		failure(w, Missing)
		return
	}
	if err = importRequestScope(ex, a, p, edit, kind); err != nil {
		failure(w, err)
		return
	}
	if err = operationImportScope(r, ex, false); err != nil {
		failure(w, err)
		return
	}
	sessionID := r.PathValue("session")
	if sessionID == "" {
		if r.Method != "POST" {
			failure(w, &APIError{405, "method_not_allowed"})
			return
		}
		if err = r.ParseMultipartForm(11 << 20); err != nil {
			failure(w, &APIError{413, "payload_too_large"})
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, er := r.FormFile("file")
		if er != nil {
			failure(w, Bad("file_required"))
			return
		}
		defer file.Close()
		filename, er := validText(header.Filename, "filename", 255, 1024)
		if er != nil {
			failure(w, er)
			return
		}
		content, er := io.ReadAll(io.LimitReader(file, (10<<20)+1))
		if er != nil {
			failure(w, Bad("invalid_file"))
			return
		}
		rows, er := ParseXLSX(content, filename, kind)
		if er != nil {
			failure(w, er)
			return
		}
		hash := sha256.Sum256(content)
		sourceHash := hex.EncodeToString(hash[:])
		raw, _ := json.Marshal(map[string]string{"filename": filename, "hash": sourceHash})
		out, code, er := s.mutate(r, a, raw, id, func(tx *sql.Tx, e *Exam) (any, error) {
			p.Now = time.Now().UTC()
			members, er := s.importMembers(r, a, p, e, rows)
			if er != nil {
				return nil, er
			}
			p.Members = members
			validated, er := ValidateImport(e, a, kind, p, rows)
			if er != nil {
				return nil, er
			}
			sid := UUID()
			encoded, er := json.Marshal(validated)
			if er != nil {
				return nil, er
			}
			expiry := p.Now.Add(ImportEditableRetention)
			_, er = tx.ExecContext(r.Context(), "INSERT INTO exams_imports_v1(id,organization_id,division_id,account_id,exam_id,kind,rows_json,expires_at,source_filename,source_hash) VALUES(?,?,?,?,?,?,?,?,?,?)", sid, a.OrganizationID, p.DivisionID, a.AccountID, id, kind, encoded, expiry, filename, sourceHash)
			if er != nil {
				return nil, er
			}
			return importPreview(sid, 1, "editable", kind, e, a, p, validated, expiry, filename, sourceHash, nil, page)
		}, func(e *Exam) error { return importRequestScope(e, a, p, true, kind) })
		if er != nil {
			failure(w, er)
			return
		}
		respond(w, code, out)
		return
	}
	if !validateKey(sessionID) {
		failure(w, Bad("invalid_session_id"))
		return
	}
	// Read before receipt replay, so a valid old command receipt never bypasses
	// the session's owner, division or expiry after its lifecycle has ended.
	session, err := loadImportSession(r, s.DB, a, p, id, kind, sessionID, false)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method == "GET" {
		var rows []ImportRow
		if json.Unmarshal(session.Rows, &rows) != nil {
			failure(w, &APIError{503, "invalid_preview_storage"})
			return
		}
		if session.State == "editable" {
			p.Members, err = s.importMembers(r, a, p, ex, rows)
			if err != nil {
				failure(w, err)
				return
			}
			rows, err = ValidateImport(ex, a, kind, p, rows)
			if err != nil {
				failure(w, err)
				return
			}
		}
		var result any
		if len(session.Result) > 0 {
			result, err = decodeStoredValue(session.Result)
			if err != nil {
				failure(w, &APIError{503, "invalid_import_result"})
				return
			}
		}
		out, er := importPreview(sessionID, session.Revision, session.State, kind, ex, a, p, rows, session.ExpiresAt, session.Filename, session.SourceHash, result, page)
		if er != nil {
			failure(w, er)
			return
		}
		respond(w, 200, out)
		return
	}
	isCommit := r.Method == "POST"
	if isCommit && !strings.HasSuffix(r.URL.Path, "/commit") {
		failure(w, &APIError{405, "method_not_allowed"})
		return
	}
	raw, err := body(r)
	if err != nil {
		failure(w, err)
		return
	}
	var in struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		Changes          []struct {
			RowID    string          `json:"row_id"`
			Input    json.RawMessage `json:"input"`
			Excluded *bool           `json:"excluded"`
		} `json:"changes,omitempty"`
	}
	if err = DecodeStrict(raw, &in); err != nil || in.ExpectedRevision == nil || *in.ExpectedRevision < 1 {
		failure(w, Bad("invalid_preview"))
		return
	}
	if len(in.Changes) > 5000 {
		failure(w, Bad("changes_limit"))
		return
	}
	if isCommit && len(in.Changes) > 0 {
		failure(w, Bad("unexpected_changes"))
		return
	}
	out, code, err := s.mutate(r, a, raw, id, func(tx *sql.Tx, e *Exam) (any, error) {
		p.Now = time.Now().UTC()
		locked, er := loadImportSession(r, tx, a, p, id, kind, sessionID, true)
		if er != nil {
			return nil, er
		}
		if locked.State == "committed" {
			if !isCommit {
				return nil, Conflict("import_committed")
			}
			result, decodeErr := decodeStoredValue(locked.Result)
			if decodeErr != nil {
				return nil, &APIError{503, "invalid_import_result"}
			}
			if er = verifyOperationReceipt(r, result); er != nil {
				return nil, er
			}
			return result, nil
		}
		if locked.Revision != *in.ExpectedRevision {
			return nil, Conflict("preview_modified")
		}
		var rows []ImportRow
		if json.Unmarshal(locked.Rows, &rows) != nil {
			return nil, &APIError{503, "invalid_preview_storage"}
		}
		seen := map[string]bool{}
		indices := map[string]int{}
		for index, row := range rows {
			indices[row.RowID] = index
		}
		for _, change := range in.Changes {
			if seen[change.RowID] {
				return nil, Bad("duplicate_preview_row")
			}
			seen[change.RowID] = true
			index, found := indices[change.RowID]
			if !found {
				return nil, Bad("unknown_preview_row")
			}
			if len(change.Input) > 0 {
				rows[index].Input = change.Input
				rows[index].PrivilegeBaselineVersion = nil
				rows[index].PrivilegeBaselineAccountID = 0
			}
			if change.Excluded != nil {
				rows[index].Excluded = *change.Excluded
			}
		}
		p.Members, er = s.importMembers(r, a, p, e, rows)
		if er != nil {
			return nil, er
		}
		checked, er := ValidateImport(e, a, kind, p, rows)
		if er != nil {
			return nil, er
		}
		if isCommit {
			if op := requestOperation(r); op != nil {
				if er = operationImportScope(r, e, true); er != nil {
					return nil, er
				}
				if er = verifyPinnedImport(locked.Revision, locked.SourceHash, op.Binding.OwnerRevision, op.Binding.SourceHash, op.Binding.ValidationHash, checked); er != nil {
					return nil, er
				}
			}
			validationHash, er := importValidationHash(checked)
			if er != nil {
				return nil, er
			}
			targetRevision := e.Revision
			result, er := CommitImport(e, a, kind, p, checked)
			if er != nil {
				return nil, er
			}
			expiry := p.Now.Add(ImportCommittedRetention)
			receipt, ok := result.(map[string]any)
			if !ok {
				return nil, &APIError{503, "invalid_import_result"}
			}
			changed, _ := receipt["changed"].(bool)
			ratingChanged, _ := receipt["rating_changed"].(bool)
			if changed {
				e.Revision++
				if ratingChanged {
					e.Divisions[p.DivisionID].RatingRevision++
				}
				if er = save(r.Context(), tx, e); er != nil {
					return nil, er
				}
				if ratingChanged {
					if er = sourceChange(r.Context(), tx, e, []int64{p.DivisionID}, "imported"); er != nil {
						return nil, er
					}
				}
			}
			receipt["id"] = sessionID
			receipt["session_id"] = sessionID
			receipt["revision"] = locked.Revision
			receipt["state"] = "committed"
			receipt["total"] = len(checked)
			receipt["source_hash"] = locked.SourceHash
			receipt["validation_hash"] = validationHash
			receipt["exam_id"] = e.ID
			receipt["exam_revision"] = targetRevision
			receipt["kind"] = kind
			receipt["organization_id"] = a.OrganizationID
			receipt["division_id"] = p.DivisionID
			receipt["account_id"] = a.AccountID
			receipt["native_command_key"] = r.Header.Get("Idempotency-Key")
			if op := requestOperation(r); op != nil {
				receipt["operation_job_id"] = op.Request.JobID
			}
			receipt["expires_at"] = expiry
			encoded, er := json.Marshal(receipt)
			if er != nil {
				return nil, er
			}
			rowsJSON, er := json.Marshal(checked)
			if er != nil {
				return nil, er
			}
			_, er = tx.ExecContext(r.Context(), "UPDATE exams_imports_v1 SET state='committed',rows_json=?,result=?,expires_at=? WHERE id=?", rowsJSON, encoded, expiry, sessionID)
			return receipt, er
		}
		encoded, er := json.Marshal(checked)
		if er != nil {
			return nil, er
		}
		revision := locked.Revision + 1
		_, er = tx.ExecContext(r.Context(), "UPDATE exams_imports_v1 SET revision=?,rows_json=? WHERE id=?", revision, encoded, sessionID)
		if er != nil {
			return nil, er
		}
		return importPreview(sessionID, revision, "editable", kind, e, a, p, checked, locked.ExpiresAt, locked.Filename, locked.SourceHash, nil, page)
	}, func(e *Exam) error {
		if er := importRequestScope(e, a, p, true, kind); er != nil {
			return er
		}
		if er := operationImportScope(r, e, false); er != nil {
			return er
		}
		if !session.ExpiresAt.After(time.Now().UTC()) {
			return &APIError{410, "import_expired"}
		}
		return nil
	})
	if err != nil {
		failure(w, err)
		return
	}
	if isCommit {
		if err = verifyOperationReceipt(r, out); err != nil {
			failure(w, err)
			return
		}
	}
	respond(w, code, out)
}
func (s *Server) trainingRoute(w http.ResponseWriter, r *http.Request) {
	a, err := s.identity(r)
	if err != nil {
		failure(w, err)
		return
	}
	p, err := params(r, a)
	if err != nil {
		failure(w, err)
		return
	}
	id, err := examID(r)
	if err != nil {
		failure(w, err)
		return
	}
	e, err := load(r.Context(), s.DB, a.OrganizationID, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err = scope(e, a, p, false, false); err != nil {
		failure(w, err)
		return
	}
	if r.Method == "GET" {
		settings := DefaultTrainingSettings()
		var raw []byte
		er := s.DB.QueryRowContext(r.Context(), "SELECT settings FROM exams_training_settings_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND part_id=?", a.OrganizationID, p.DivisionID, a.AccountID, id, p.PartID).Scan(&raw)
		if er != nil && er != sql.ErrNoRows {
			failure(w, er)
			return
		}
		settings, er = storedTrainingSettings(raw)
		if er != nil {
			failure(w, er)
			return
		}
		if p.PartID > 0 {
			var rootRaw []byte
			er = s.DB.QueryRowContext(r.Context(), "SELECT settings FROM exams_training_settings_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND part_id=0", a.OrganizationID, p.DivisionID, a.AccountID, id).Scan(&rootRaw)
			if er != nil && er != sql.ErrNoRows {
				failure(w, er)
				return
			}
			root, er := storedTrainingSettings(rootRaw)
			if er != nil {
				failure(w, er)
				return
			}
			settings.LastPartID = root.LastPartID
		}
		rows, er := s.DB.QueryContext(r.Context(), "SELECT question_id,fingerprint FROM exams_training_progress_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=?", a.OrganizationID, p.DivisionID, a.AccountID, id)
		if er != nil {
			failure(w, er)
			return
		}
		progress := map[string]string{}
		for rows.Next() {
			var q int64
			var fp string
			if er = rows.Scan(&q, &fp); er != nil {
				rows.Close()
				failure(w, er)
				return
			}
			progress["exam:"+strconv.FormatInt(id, 10)+":"+strconv.FormatInt(q, 10)] = fp
		}
		er = rows.Err()
		rows.Close()
		if er != nil {
			failure(w, er)
			return
		}
		out, er := TrainingView(e, a, p, progress, settings)
		if er != nil {
			failure(w, er)
			return
		}
		respond(w, 200, out)
		return
	}
	raw, err := body(r)
	if err != nil {
		failure(w, err)
		return
	}
	toggle := r.URL.Path == "/v1/exams/"+strconv.FormatInt(id, 10)+"/training"
	if toggle && r.Method != "PUT" {
		failure(w, &APIError{405, "method_not_allowed"})
		return
	}
	if toggle && !a.Can(true) || !toggle && a.Role != "student" {
		failure(w, Denied)
		return
	}
	out, code, err := s.mutate(r, a, raw, id, func(tx *sql.Tx, e *Exam) (any, error) {
		if toggle {
			result, er := AuxCommand(e, a, "training-toggle", p, raw)
			if er == nil {
				er = save(r.Context(), tx, e)
			}
			return result, er
		}
		if _, er := trainingScope(e, a, p); er != nil {
			return nil, er
		}
		if r.PathValue("question") != "" {
			var in struct {
				Fingerprint string `json:"content_fingerprint"`
			}
			if er := DecodeStrict(raw, &in); er != nil {
				return nil, er
			}
			if r.Method == "DELETE" {
				if _, er := ResolveTrainingCard(e, p, p.QuestionID, ""); er != nil {
					return nil, er
				}
				_, er := tx.ExecContext(r.Context(), "DELETE FROM exams_training_progress_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND question_id=?", a.OrganizationID, p.DivisionID, a.AccountID, id, p.QuestionID)
				return map[string]any{"learned": false}, er
			}
			if len(in.Fingerprint) != 64 {
				return nil, Bad("content_fingerprint_required")
			}
			if _, er := hex.DecodeString(in.Fingerprint); er != nil {
				return nil, Bad("invalid_fingerprint")
			}
			if _, er := ResolveTrainingCard(e, p, p.QuestionID, in.Fingerprint); er != nil {
				return nil, er
			}
			_, er := tx.ExecContext(r.Context(), "INSERT INTO exams_training_progress_v1(organization_id,division_id,account_id,exam_id,question_id,fingerprint) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE fingerprint=VALUES(fingerprint)", a.OrganizationID, p.DivisionID, a.AccountID, id, p.QuestionID, in.Fingerprint)
			return map[string]any{"learned": true}, er
		}
		if r.Method != "PUT" {
			return nil, &APIError{405, "method_not_allowed"}
		}
		current := DefaultTrainingSettings()
		var b []byte
		er := tx.QueryRowContext(r.Context(), "SELECT settings FROM exams_training_settings_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND part_id=?", a.OrganizationID, p.DivisionID, a.AccountID, id, p.PartID).Scan(&b)
		if er != nil && er != sql.ErrNoRows {
			return nil, er
		}
		current, er = storedTrainingSettings(b)
		if er != nil {
			return nil, er
		}
		next, er := ValidateTrainingSettings(e, p, current, raw)
		if er != nil {
			return nil, er
		}
		b, _ = json.Marshal(next)
		_, er = tx.ExecContext(r.Context(), "INSERT INTO exams_training_settings_v1(organization_id,division_id,account_id,exam_id,part_id,settings) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE settings=VALUES(settings)", a.OrganizationID, p.DivisionID, a.AccountID, id, p.PartID, b)
		if er == nil && p.PartID > 0 {
			global := DefaultTrainingSettings()
			var encoded []byte
			loadErr := tx.QueryRowContext(r.Context(), "SELECT settings FROM exams_training_settings_v1 WHERE organization_id=? AND division_id=? AND account_id=? AND exam_id=? AND part_id=0", a.OrganizationID, p.DivisionID, a.AccountID, id).Scan(&encoded)
			if loadErr != nil && loadErr != sql.ErrNoRows {
				return nil, loadErr
			}
			global, loadErr = storedTrainingSettings(encoded)
			if loadErr != nil {
				return nil, loadErr
			}
			global.LastPartID = next.LastPartID
			encoded, _ = json.Marshal(global)
			_, er = tx.ExecContext(r.Context(), "INSERT INTO exams_training_settings_v1(organization_id,division_id,account_id,exam_id,part_id,settings) VALUES(?,?,?,?,0,?) ON DUPLICATE KEY UPDATE settings=VALUES(settings)", a.OrganizationID, p.DivisionID, a.AccountID, id, encoded)
		}
		return next, er
	}, func(e *Exam) error {
		if toggle {
			return scope(e, a, p, true, false)
		}
		if er := scope(e, a, p, false, false); er != nil {
			return er
		}
		_, er := trainingScope(e, a, p)
		return er
	})
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, code, out)
}
