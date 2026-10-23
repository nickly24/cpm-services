package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const studentImportBatchSize = 25
const studentImportLifetime = 24 * time.Hour

var studentImportLogin = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,49}$`)

type StudentImportRow struct {
	Row       int    `json:"row"`
	FullName  string `json:"full_name"`
	ProfileID string `json:"profile_id"`
	School    string `json:"school"`
	Class     string `json:"class"`
	Login     string `json:"login"`
	Password  string `json:"password,omitempty"`
	SchoolID  *int64 `json:"resolved_school_id,omitempty"`
}
type StudentImportIssue struct {
	Row    int    `json:"row"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type studentDraftPayload struct {
	Rows []StudentImportRow `json:"rows"`
}
type studentDraft struct {
	ID                             string
	Org, Division, Actor, Revision int64
	State                          string
	Expires                        time.Time
	Payload                        studentDraftPayload
}
type studentRevisionDTO struct {
	ExpectedRevision int64 `json:"expected_revision"`
}
type studentEditDTO struct {
	ExpectedRevision int64              `json:"expected_revision"`
	Rows             []StudentImportRow `json:"rows"`
}
type studentPrepareDTO struct {
	ExpectedRevision int64  `json:"expected_revision"`
	UploadSessionID  string `json:"upload_session_id"`
}

func studentRandom(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(b)
}
func studentAccess(a Actor) error {
	if !a.OrganizationAdmin || !a.Can("upload", true) || !a.Can("users", true) {
		return denied
	}
	return nil
}
func (s *Server) studentCipher() (cipher.AEAD, error) {
	if len(s.InternalKey) < 32 {
		return nil, unavailable
	}
	key := sha256.Sum256([]byte("cpm.student-import.credentials.v1\x00" + s.InternalKey))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func studentAAD(d studentDraft) []byte {
	return []byte(fmt.Sprintf("student-import-v1:%s:%d:%d:%d:%d", d.ID, d.Org, d.Division, d.Actor, d.Revision))
}
func (s *Server) studentSeal(d studentDraft) ([]byte, error) {
	a, e := s.studentCipher()
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(d.Payload)
	if e != nil {
		return nil, e
	}
	if len(b) > 6<<20 {
		return nil, &APIError{413, "student_import_memory_limit"}
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return a.Seal(nonce, nonce, b, studentAAD(d)), nil
}
func (s *Server) studentLoad(r *http.Request, db DBTX, a Actor, div int64, lock bool) (studentDraft, error) {
	d := studentDraft{ID: r.PathValue("draft")}
	if e := studentAccess(a); e != nil {
		return d, e
	}
	if !lowerHex(d.ID, 32) {
		return d, missing
	}
	var blob []byte
	q := "SELECT organization_id,division_id,actor_id,revision,state,encrypted_payload,expires_at FROM core_student_import_drafts WHERE id=? AND organization_id=? AND division_id=? AND actor_id=?"
	if lock {
		q += " FOR UPDATE"
	}
	e := db.QueryRowContext(r.Context(), q, d.ID, a.OrganizationID, div, a.AccountID).Scan(&d.Org, &d.Division, &d.Actor, &d.Revision, &d.State, &blob, &d.Expires)
	if e != nil {
		return d, e
	}
	if !d.Expires.After(time.Now()) || len(blob) == 0 {
		return d, &APIError{410, "student_import_expired"}
	}
	c, e := s.studentCipher()
	if e != nil {
		return d, e
	}
	if len(blob) < c.NonceSize() {
		return d, unavailable
	}
	plain, e := c.Open(nil, blob[:c.NonceSize()], blob[c.NonceSize():], studentAAD(d))
	if e != nil {
		return d, unavailable
	}
	if json.Unmarshal(plain, &d.Payload) != nil {
		return d, unavailable
	}
	return d, nil
}
func (s *Server) studentSave(r *http.Request, tx DBTX, d studentDraft) error {
	blob, e := s.studentSeal(d)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(r.Context(), "UPDATE core_student_import_drafts SET revision=?,state=?,encrypted_payload=? WHERE id=?", d.Revision, d.State, blob, d.ID)
	return e
}
func (s *Server) studentExpire(r *http.Request) {
	_, _ = s.DB.ExecContext(r.Context(), "UPDATE core_student_import_drafts SET encrypted_payload=NULL WHERE expires_at<UTC_TIMESTAMP(6) AND encrypted_payload IS NOT NULL LIMIT 100")
}

func (s *Server) studentIssues(r *http.Request, a Actor, div int64, rows []StudentImportRow) ([]StudentImportIssue, error) {
	issues := []StudentImportIssue{}
	add := func(row int, field, reason string) { issues = append(issues, StudentImportIssue{row, field, reason}) }
	schools, e := list(r.Context(), s.DB, "SELECT s.id,s.name,s.short_name FROM schools s JOIN core_school_scope x ON x.school_id=s.id WHERE x.organization_id=? AND s.is_active=1", a.OrganizationID)
	if e != nil {
		return nil, e
	}
	names := map[string][]int64{}
	for _, school := range schools {
		for _, field := range []string{"name", "short_name"} {
			v, ok := school[field].(string)
			if !ok || strings.TrimSpace(v) == "" {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(v))
			id := num(school["id"])
			found := false
			for _, old := range names[k] {
				if old == id {
					found = true
				}
			}
			if !found {
				names[k] = append(names[k], id)
			}
		}
	}
	occupiedLogins, occupiedIDs, e := s.studentOccupied(r, rows)
	if e != nil {
		return nil, e
	}

	seenLogin, seenID, seenRows := map[string]int{}, map[string]int{}, map[string]int{}
	for i := range rows {
		u := &rows[i]
		u.FullName = strings.TrimSpace(u.FullName)
		u.Login = strings.TrimSpace(u.Login)
		u.ProfileID = strings.TrimSpace(u.ProfileID)
		u.Class = strings.TrimSpace(u.Class)
		u.School = strings.TrimSpace(u.School)
		u.SchoolID = nil
		if !validText(u.FullName, 50, true) {
			add(u.Row, "full_name", "name_required_or_too_long")
		}
		if !studentImportLogin.MatchString(u.Login) {
			add(u.Row, "login", "invalid_login")
		}
		if len(u.Password) < 16 || len(u.Password) > 128 {
			return nil, unavailable
		}
		if u.Class != "" {
			n, e := strconv.Atoi(u.Class)
			if e != nil || n < 1 || n > 99 {
				add(u.Row, "class", "invalid_class")
			}
		}
		if u.School != "" {
			ids := names[strings.ToLower(u.School)]
			if len(ids) == 0 {
				add(u.Row, "school", "school_not_found")
			}
			if len(ids) > 1 {
				add(u.Row, "school", "school_ambiguous")
			}
			if len(ids) == 1 {
				n := ids[0]
				u.SchoolID = &n
			}
		}
		login := strings.ToLower(u.Login)
		if old, ok := seenLogin[login]; ok {
			add(old, "login", "duplicate_login")
			add(u.Row, "login", "duplicate_login")
		}
		seenLogin[login] = u.Row
		if occupiedLogins[strings.ToLower(u.Login)] {
			add(u.Row, "login", "login_exists")
		}
		if u.ProfileID != "" {
			n, e := strconv.ParseInt(u.ProfileID, 10, 32)
			if e != nil || n < 1 || strconv.FormatInt(n, 10) != u.ProfileID {
				add(u.Row, "profile_id", "invalid_profile_id")
			} else {
				if old, ok := seenID[u.ProfileID]; ok {
					add(old, "profile_id", "duplicate_profile_id")
					add(u.Row, "profile_id", "duplicate_profile_id")
				}
				seenID[u.ProfileID] = u.Row
				if occupiedIDs[u.ProfileID] {
					add(u.Row, "profile_id", "profile_id_exists")
				}
			}
		}
		k := strings.ToLower(u.FullName) + "\x00" + u.ProfileID + "\x00" + u.Class + "\x00" + strings.ToLower(u.School)
		if old, ok := seenRows[k]; ok {
			add(old, "full_name", "duplicate_row")
			add(u.Row, "full_name", "duplicate_row")
		}
		seenRows[k] = u.Row
	}
	return issues, nil
}
func (s *Server) studentView(r *http.Request, a Actor, div int64, d studentDraft, sensitive bool) (any, int, error) {
	rows := append([]StudentImportRow(nil), d.Payload.Rows...)
	issues := []StudentImportIssue{}
	var e error
	if d.State == "preview" {
		issues, e = s.studentIssues(r, a, div, rows)
		if e != nil {
			return nil, 0, e
		}
	}
	if !sensitive {
		for i := range rows {
			rows[i].Password = ""
		}
	}
	batches, e := list(r.Context(), s.DB, "SELECT b.batch_index,b.upload_session_id,b.native_session_id,i.status FROM core_student_import_batches b LEFT JOIN core_imports i ON i.id=b.native_session_id WHERE b.draft_id=? ORDER BY b.batch_index", d.ID)
	if e != nil {
		return nil, 0, e
	}
	return ok(map[string]any{"id": d.ID, "revision": d.Revision, "state": d.State, "division_id": d.Division, "expires_at": d.Expires.UTC(), "rows": rows, "issues": issues, "batch_size": studentImportBatchSize, "batch_count": (len(rows) + studentImportBatchSize - 1) / studentImportBatchSize, "batches": batches})
}
func (s *Server) studentCreate(r *http.Request, a Actor, div int64) (any, int, error) {
	if e := studentAccess(a); e != nil {
		return nil, 0, e
	}
	s.studentExpire(r)
	key, e := legacyCommandKey(r, true)
	if e != nil {
		return nil, 0, e
	}
	var in struct {
		Workbook string `json:"workbook_base64"`
	}
	if e = decode(r, &in); e != nil {
		return nil, 0, e
	}
	raw, e := base64.StdEncoding.DecodeString(in.Workbook)
	if e != nil {
		return nil, 0, bad("invalid_xlsx")
	}
	if len(raw) > 6<<20 {
		return nil, 0, &APIError{413, "student_import_file_too_large"}
	}
	source := importSHA256(raw)
	var existing, oldhash string
	e = s.DB.QueryRowContext(r.Context(), "SELECT id,source_sha256 FROM core_student_import_drafts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", a.OrganizationID, div, a.AccountID, key).Scan(&existing, &oldhash)
	if e == nil {
		if oldhash != source {
			return nil, 0, &APIError{409, "idempotency_key_reused"}
		}
		r.SetPathValue("draft", existing)
		return s.studentGet(r, a, div)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, 0, e
	}
	rows, e := parseStudentWorkbook(raw)
	if e != nil {
		return nil, 0, e
	}
	for i := range rows {
		rows[i].Login = "student_" + studentRandom(8)
		rows[i].Password = studentRandom(12)
	}
	d := studentDraft{ID: studentRandom(16), Org: a.OrganizationID, Division: div, Actor: a.AccountID, Revision: 1, State: "preview", Expires: time.Now().UTC().Add(studentImportLifetime), Payload: studentDraftPayload{Rows: rows}}
	blob, e := s.studentSeal(d)
	if e != nil {
		return nil, 0, e
	}
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, div); e != nil {
			return e
		}
		_, e := tx.ExecContext(r.Context(), "INSERT INTO core_student_import_drafts(id,organization_id,division_id,actor_id,command_key,source_sha256,encrypted_payload,expires_at) VALUES(?,?,?,?,?,?,?,?)", d.ID, d.Org, d.Division, d.Actor, key, source, blob, d.Expires)
		return e
	})
	if e != nil {
		return nil, 0, e
	}
	return s.studentView(r, a, div, d, false)
}
func (s *Server) studentGet(r *http.Request, a Actor, div int64) (any, int, error) {
	s.studentExpire(r)
	d, e := s.studentLoad(r, s.DB, a, div, false)
	if e != nil {
		return nil, 0, e
	}
	return s.studentView(r, a, div, d, false)
}
func (s *Server) studentReview(r *http.Request, a Actor, div int64) (any, int, error) {
	s.studentExpire(r)
	d, e := s.studentLoad(r, s.DB, a, div, false)
	if e != nil {
		return nil, 0, e
	}
	return s.studentView(r, a, div, d, true)
}
func (s *Server) studentEdit(r *http.Request, a Actor, div int64) (any, int, error) {
	var in studentEditDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	var d studentDraft
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		var e error
		d, e = s.studentLoad(r, tx, a, div, true)
		if e != nil {
			return e
		}
		if d.State != "preview" || d.Revision != in.ExpectedRevision {
			return &APIError{409, "preview_modified"}
		}
		if len(in.Rows) == 0 || len(in.Rows) > len(d.Payload.Rows) {
			return bad("invalid_student_rows")
		}
		old := map[int]StudentImportRow{}
		for _, u := range d.Payload.Rows {
			old[u.Row] = u
		}
		for i := range in.Rows {
			u := &in.Rows[i]
			v, ok := old[u.Row]
			if !ok || u.Password != "" {
				return bad("invalid_student_rows")
			}
			delete(old, u.Row)
			u.Password = v.Password
		}
		d.Payload.Rows = in.Rows
		d.Revision++
		return s.studentSave(r, tx, d)
	})
	if e != nil {
		return nil, 0, e
	}
	return s.studentView(r, a, div, d, false)
}
func (s *Server) studentFreeze(r *http.Request, a Actor, div int64) (any, int, error) {
	var in studentRevisionDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	var d studentDraft
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, div); e != nil {
			return e
		}
		var e error
		d, e = s.studentLoad(r, tx, a, div, true)
		if e != nil {
			return e
		}
		if d.Revision != in.ExpectedRevision {
			return &APIError{409, "preview_modified"}
		}
		if d.State == "frozen" {
			return nil
		}
		if d.State != "preview" {
			return &APIError{409, "preview_not_confirmable"}
		}
		issues, e := s.studentIssues(r, a, div, d.Payload.Rows)
		if e != nil {
			return e
		}
		if len(issues) > 0 {
			return bad("student_import_has_errors")
		}
		d.State = "frozen"
		return s.studentSave(r, tx, d)
	})
	if e != nil {
		return nil, 0, e
	}
	return s.studentView(r, a, div, d, false)
}
func (s *Server) studentCancel(r *http.Request, a Actor, div int64) (any, int, error) {
	var d studentDraft
	e := s.transact(r.Context(), func(tx *sql.Tx) error {
		var e error
		d, e = s.studentLoad(r, tx, a, div, true)
		if e != nil {
			return e
		}
		if d.State != "preview" {
			return &APIError{409, "student_import_already_confirmed"}
		}
		_, e = tx.ExecContext(r.Context(), "UPDATE core_student_import_drafts SET state='cancelled',encrypted_payload=NULL WHERE id=?", d.ID)
		return e
	})
	return map[string]any{"cancelled": e == nil}, 200, e
}
func (s *Server) studentList(r *http.Request, a Actor, div int64) (any, int, error) {
	if e := studentAccess(a); e != nil {
		return nil, 0, e
	}
	s.studentExpire(r)
	v, e := list(r.Context(), s.DB, "SELECT id,revision,state,expires_at,created_at FROM core_student_import_drafts WHERE organization_id=? AND division_id=? AND actor_id=? AND expires_at>UTC_TIMESTAMP(6) AND encrypted_payload IS NOT NULL ORDER BY created_at DESC LIMIT 30", a.OrganizationID, div, a.AccountID)
	return v, 200, e
}

// Preparing a child only hashes its frozen input. The existing Uploads grant,
// native apply transaction and protected receipt remain the creation authority.
func (s *Server) studentPrepare(r *http.Request, a Actor, div int64) (any, int, error) {
	var in studentPrepareDTO
	if e := decode(r, &in); e != nil {
		return nil, 0, e
	}
	if !lowerHex(in.UploadSessionID, 32) {
		return nil, 0, bad("invalid_upload_session")
	}
	index, e := strconv.Atoi(r.PathValue("batch"))
	if e != nil || index < 0 {
		return nil, 0, bad("invalid_batch")
	}
	var d studentDraft
	var native sql.NullInt64
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		var e error
		d, e = s.studentLoad(r, tx, a, div, true)
		if e != nil {
			return e
		}
		if d.State != "frozen" || d.Revision != in.ExpectedRevision {
			return &APIError{409, "preview_modified"}
		}
		if index*studentImportBatchSize >= len(d.Payload.Rows) {
			return bad("invalid_batch")
		}
		// No new batch may start before the previous native transaction completed.
		if index > 0 {
			var state string
			if e = tx.QueryRowContext(r.Context(), "SELECT i.status FROM core_student_import_batches b JOIN core_imports i ON i.id=b.native_session_id WHERE b.draft_id=? AND b.batch_index=?", d.ID, index-1).Scan(&state); e != nil {
				return &APIError{409, "previous_batch_unresolved"}
			}
			if state != "completed" {
				return &APIError{409, "previous_batch_unresolved"}
			}
		}
		var upload string
		e = tx.QueryRowContext(r.Context(), "SELECT upload_session_id,native_session_id FROM core_student_import_batches WHERE draft_id=? AND batch_index=? FOR UPDATE", d.ID, index).Scan(&upload, &native)
		if e == nil {
			if upload != in.UploadSessionID {
				return &APIError{409, "original_batch_required"}
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = tx.ExecContext(r.Context(), "INSERT INTO core_student_import_batches(draft_id,batch_index,upload_session_id) VALUES(?,?,?)", d.ID, index, in.UploadSessionID)
		return e
	})
	if e != nil {
		return nil, 0, e
	}
	if native.Valid {
		var source string
		if e = s.DB.QueryRowContext(r.Context(), "SELECT source_sha256 FROM core_import_operation_sources WHERE kind='users.credentials' AND session_id=? AND actor_id=? AND organization_id=? AND division_id=?", native.Int64, a.AccountID, a.OrganizationID, div).Scan(&source); e != nil {
			return nil, 0, e
		}
		count := studentImportBatchSize
		if (index+1)*count > len(d.Payload.Rows) {
			count = len(d.Payload.Rows) - index*studentImportBatchSize
		}
		return ok(map[string]any{"id": native.Int64, "rows": count, "source_sha256": source})
	}
	start, end := index*studentImportBatchSize, (index+1)*studentImportBatchSize
	if end > len(d.Payload.Rows) {
		end = len(d.Payload.Rows)
	}
	rows := d.Payload.Rows[start:end]
	checkRows := append([]StudentImportRow(nil), rows...)
	issues, er := s.studentIssues(r, a, div, checkRows)
	if er != nil {
		return nil, 0, er
	}
	if len(issues) > 0 {
		return nil, 0, &studentRowsError{issues}
	}
	var csvbody bytes.Buffer
	w := csv.NewWriter(&csvbody)
	_ = w.Write([]string{"full_name", "profile_id", "school_id", "class", "login", "password"})
	for _, u := range rows {
		school := ""
		if u.School != "" {
			if u.SchoolID == nil {
				return nil, 0, bad("student_import_batch_conflict")
			}
			school = strconv.FormatInt(*u.SchoolID, 10)
		}
		_ = w.Write([]string{u.FullName, u.ProfileID, school, u.Class, u.Login, u.Password})
	}
	w.Flush()
	source := importSHA256(csvbody.Bytes())
	if !native.Valid {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("file", "students.csv")
		_, _ = file.Write(csvbody.Bytes())
		_ = form.Close()
		req, _ := http.NewRequestWithContext(r.Context(), "POST", r.URL.String(), &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Idempotency-Key", r.Header.Get("Idempotency-Key"))
		value, _, er := s.importPreview(req, a, div)
		if er != nil {
			return nil, 0, er
		}
		id := value.(map[string]any)["id"].(int64)
		native = sql.NullInt64{Int64: id, Valid: true}
		_, e = s.DB.ExecContext(r.Context(), "UPDATE core_student_import_batches SET native_session_id=? WHERE draft_id=? AND batch_index=? AND upload_session_id=? AND native_session_id IS NULL", id, d.ID, index, in.UploadSessionID)
		if e != nil {
			return nil, 0, e
		}
	}
	return ok(map[string]any{"id": native.Int64, "rows": len(rows), "source_sha256": source})
}

// Bound each SQL IN list and issue serial reads instead of one query per row.
func (s *Server) studentOccupied(r *http.Request, rows []StudentImportRow) (map[string]bool, map[string]bool, error) {
	logins, ids := map[string]bool{}, map[string]bool{}
	for start := 0; start < len(rows); start += 100 {
		end := start + 100
		if end > len(rows) {
			end = len(rows)
		}
		args := []any{}
		idargs := []any{}
		for _, u := range rows[start:end] {
			args = append(args, strings.TrimSpace(u.Login))
			if n, e := strconv.ParseInt(strings.TrimSpace(u.ProfileID), 10, 32); e == nil && n > 0 {
				idargs = append(idargs, n)
			}
		}
		found, e := list(r.Context(), s.DB, "SELECT q.candidate username FROM (SELECT ? candidate"+strings.Repeat(" UNION ALL SELECT ?", len(args)-1)+") q JOIN auth_users a ON a.username=q.candidate", args...)
		if e != nil {
			return nil, nil, e
		}
		for _, v := range found {
			logins[strings.ToLower(v["username"].(string))] = true
		}
		if len(idargs) > 0 {
			marks := strings.TrimSuffix(strings.Repeat("?,", len(idargs)), ",")
			all := append(append([]any{}, idargs...), idargs...)
			found, e = list(r.Context(), s.DB, "SELECT id FROM students WHERE id IN ("+marks+") UNION SELECT ref_id id FROM auth_users WHERE role='student' AND ref_id IN ("+marks+")", all...)
			if e != nil {
				return nil, nil, e
			}
			for _, v := range found {
				ids[strconv.FormatInt(num(v["id"]), 10)] = true
			}
		}
	}
	return logins, ids, nil
}

type studentRowsError struct{ Issues []StudentImportIssue }

func (e *studentRowsError) Error() string { return "student_import_validation" }
