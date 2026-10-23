package core

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CredentialImportSessionView describes the native GET projection for OpenAPI.
// Identity fields come from the scoped stored row; the handler retains its
// existing map response and masks stored password hashes before serialization.
type CredentialImportSessionView struct {
	ID                    int64                     `json:"id"`
	OrganizationID        int64                     `json:"organization_id"`
	DivisionID            int64                     `json:"division_id"`
	ActorID               int64                     `json:"actor_id"`
	Status                string                    `json:"status"`
	Users                 []UserDTO                 `json:"users"`
	Report                any                       `json:"report"`
	CreatedAt             string                    `json:"created_at"`
	AuthorizationProof    *ImportAuthorizationProof `json:"authorization_proof"`
	SourceSHA256          *string                   `json:"source_sha256"`
	SourceProvenance      *string                   `json:"source_provenance"`
	OwnerRequestSHA256    *string                   `json:"owner_request_sha256"`
	OwnerPreviewExpiresAt *string                   `json:"owner_preview_expires_at"`
}

func parsePositive(raw string) (int64, error) {
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n < 1 {
		return 0, bad("invalid_id")
	}
	return n, nil
}
func importRows(rows [][]string) (ImportDTO, error) {
	out := ImportDTO{Users: []UserDTO{}}
	if len(rows) < 2 || len(rows) > 101 {
		return out, bad("import_requires_1_to_100_rows")
	}
	headers := rows[0]
	seen := map[string]bool{}
	allowed := map[string]bool{"profile_id": true, "role": true, "full_name": true, "class": true, "tg_name": true, "school_id": true, "group_id": true, "role_id": true, "login": true, "password": true}
	for _, h := range headers {
		if !allowed[h] || seen[h] {
			return out, bad("invalid_import_columns")
		}
		seen[h] = true
	}
	for _, h := range []string{"full_name", "login", "password"} {
		if !seen[h] {
			return out, bad("missing_import_column")
		}
	}
	for _, row := range rows[1:] {
		if len(row) > len(headers) {
			return out, bad("invalid_import_row")
		}
		m := map[string]string{}
		for i, v := range row {
			if !utf8.ValidString(v) {
				return out, bad("invalid_import_row")
			}
			m[headers[i]] = strings.TrimSpace(v)
		}
		u := UserDTO{Role: m["role"], FullName: m["full_name"], TGName: m["tg_name"], Login: m["login"], Password: m["password"]}
		if u.Role == "" {
			u.Role = "student"
		}
		if m["class"] != "" {
			n, e := strconv.Atoi(m["class"])
			if e != nil {
				return out, bad("invalid_class")
			}
			if n < 1 || n > 99 {
				return out, bad("invalid_class")
			}
			u.Class = n
		}
		for k, p := range map[string]**int64{"profile_id": &u.ProfileID, "school_id": &u.SchoolID, "group_id": &u.GroupID, "role_id": &u.RoleID} {
			if raw := m[k]; raw != "" {
				n, e := parsePositive(raw)
				if e != nil {
					return out, e
				}
				*p = &n
			}
		}
		out.Users = append(out.Users, u)
	}
	return out, nil
}
func readImport(r *http.Request) (ImportDTO, error) {
	in, _, e := readImportSource(r)
	return in, e
}
func readImportSource(r *http.Request) (ImportDTO, *importOperationSource, error) {
	var in ImportDTO
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if e := r.ParseMultipartForm(8 << 20); e != nil {
			return in, nil, bad("invalid_upload")
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		f, h, e := r.FormFile("file")
		if e != nil {
			return in, nil, bad("file_required")
		}
		defer f.Close()
		content, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		if e != nil || len(content) > 8<<20 {
			return in, nil, bad("invalid_upload")
		}
		source := &importOperationSource{importSHA256(content), "core-file-v1"}
		var rows [][]string
		switch strings.ToLower(filepath.Ext(h.Filename)) {
		case ".csv":
			reader := csv.NewReader(bytes.NewReader(content))
			for {
				row, e := reader.Read()
				if e == io.EOF {
					break
				}
				if e != nil {
					return in, nil, bad("invalid_csv")
				}
				rows = append(rows, row)
				if len(rows) > 101 {
					return in, nil, bad("import_requires_1_to_100_rows")
				}
			}
		case ".xlsx":
			book, e := excelize.OpenReader(bytes.NewReader(content), excelize.Options{UnzipSizeLimit: 32 << 20, UnzipXMLSizeLimit: 8 << 20})
			if e != nil {
				return in, nil, bad("invalid_xlsx")
			}
			defer book.Close()
			sheets := book.GetSheetList()
			if len(sheets) == 0 {
				return in, nil, bad("empty_workbook")
			}
			iterator, e := book.Rows(sheets[0])
			if e != nil {
				return in, nil, bad("invalid_xlsx")
			}
			defer iterator.Close()
			for iterator.Next() {
				row, e := iterator.Columns()
				if e != nil {
					return in, nil, bad("invalid_xlsx")
				}
				rows = append(rows, row)
				if len(rows) > 101 {
					return in, nil, bad("import_requires_1_to_100_rows")
				}
			}
			if iterator.Error() != nil {
				return in, nil, bad("invalid_xlsx")
			}
		default:
			return in, nil, bad("csv_or_xlsx_required")
		}
		in, e = importRows(rows)
		return in, source, e
	}
	e := decode(r, &in)
	return in, nil, e
}
func (s *Server) importPreview(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("upload", true) || !a.Can("users", true) {
		return nil, 0, denied
	}
	key, e := legacyCommandKey(r, false)
	if e != nil {
		return nil, 0, e
	}
	in, source, e := readImportSource(r)
	if e != nil {
		return nil, 0, e
	}
	if len(in.Users) < 1 || len(in.Users) > 100 {
		return nil, 0, bad("import_requires_1_to_100_rows")
	}
	// Digest is computed before replacing plaintext with Auth-produced hashes.
	// Repeated preview resolves before validation against accounts already applied.
	digest := legacyCommandHash("credential_preview", 0, in)
	if key != "" {
		if n, found, e := credentialImportReceipt(r, s.DB, a, d, key, digest); e != nil || found {
			if e == nil {
				e = matchImportCreationSource(r, s.DB, a, d, n, "users.credentials", source)
			}
			return map[string]any{"id": n, "rows": len(in.Users)}, 201, e
		}
	}
	logins := map[string]bool{}
	for i, u := range in.Users {
		if e = s.validUser(r, s.DB, a, d, u); e != nil {
			return nil, 0, &APIError{400, fmt.Sprintf("invalid_import_row_%d", i+1)}
		}
		login := strings.ToLower(u.Login)
		if logins[login] {
			return nil, 0, bad("duplicate_import_login")
		}
		logins[login] = true
	}
	for i, u := range in.Users {
		hash, e := s.Auth.Hash(r.Context(), u.Password)
		if e != nil {
			return nil, 0, e
		}
		in.Users[i].Password = hash
	}
	payload, e := json.Marshal(in)
	if e != nil {
		return nil, 0, e
	}
	var n int64
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if key != "" {
			if e := lockDivision(r.Context(), tx, a, d); e != nil {
				return e
			}
			stored, found, e := credentialImportReceipt(r, tx, a, d, key, digest)
			if e != nil {
				return e
			}
			if found {
				n = stored
				return matchImportCreationSource(r, tx, a, d, n, "users.credentials", source)
			}
		}
		res, e := tx.ExecContext(r.Context(), "INSERT INTO core_imports(organization_id,division_id,actor_id,payload) VALUES(?,?,?,?)", a.OrganizationID, d, a.AccountID, payload)
		if e != nil {
			return e
		}
		n, e = res.LastInsertId()
		if e != nil {
			return e
		}
		if e := saveImportSource(r, tx, a, d, n, "users.credentials", source); e != nil {
			return e
		}
		if key != "" {
			if _, e = tx.ExecContext(r.Context(), "INSERT INTO core_import_receipts(organization_id,division_id,actor_id,command_key,command_hash,session_id) VALUES(?,?,?,?,?,?)", a.OrganizationID, d, a.AccountID, key, digest, n); e != nil {
				return e
			}
		}
		return audit(r.Context(), tx, a, &d, "import.preview", n)
	})
	return map[string]any{"id": n, "rows": len(in.Users)}, 201, e
}
func (s *Server) imports(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("upload", false) || !a.Can("users", false) {
		return nil, 0, denied
	}
	limit, offset, e := page(r)
	if e != nil {
		return nil, 0, e
	}
	q := "SELECT id,actor_id,status,created_at FROM core_imports WHERE organization_id=? AND division_id=?"
	args := []any{a.OrganizationID, d}
	if !a.OrganizationAdmin {
		q += " AND actor_id=?"
		args = append(args, a.AccountID)
	}
	args = append(args, limit, offset)
	rows, e := list(r.Context(), s.DB, q+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	return rows, 200, e
}
func (s *Server) importGet(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("upload", false) || !a.Can("users", false) {
		return nil, 0, denied
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	row, e := one(r.Context(), s.DB, "SELECT id,organization_id,division_id,actor_id,status,payload,report,created_at FROM core_imports WHERE id=? AND organization_id=? AND division_id=?", n, a.OrganizationID, d)
	if e != nil {
		return nil, 0, e
	}
	if !a.OrganizationAdmin && num(row["actor_id"]) != a.AccountID {
		return nil, 0, denied
	}
	if requestOperation(r) != nil && num(row["actor_id"]) != a.AccountID {
		return nil, 0, missing
	}
	var in ImportDTO
	if json.Unmarshal([]byte(row["payload"].(string)), &in) != nil {
		return nil, 0, unavailable
	}
	owner := a
	owner.AccountID = num(row["actor_id"])
	source, e := loadImportSource(r, s.DB, owner, d, n, "users.credentials")
	if e != nil {
		return nil, 0, e
	}
	row["authorization_proof"], row["source_sha256"], row["source_provenance"], row["owner_request_sha256"], row["owner_preview_expires_at"] = nil, nil, nil, nil, nil
	if source != nil && row["status"] == "preview" {
		row["authorization_proof"] = credentialSnapshotProof(owner, d, n, in, source)
		row["source_sha256"], row["source_provenance"], row["owner_request_sha256"] = source.SHA256, source.Provenance, credentialCommitHash(n)
	}
	var currentProof *ImportAuthorizationProof
	if row["status"] == "preview" {
		currentProof = credentialSnapshotProof(owner, d, n, in, source)
	}
	if e := verifyImportGrantProof(r, "users.credentials", n, source, currentProof, credentialCommitHash(n)); e != nil {
		return nil, 0, e
	}
	for i := range in.Users {
		in.Users[i].Password = ""
	}
	row["users"] = in.Users
	delete(row, "payload")
	if row["report"] != nil {
		var report any
		if json.Unmarshal([]byte(row["report"].(string)), &report) != nil {
			return nil, 0, unavailable
		}
		row["report"] = report
	}
	return ok(row)
}
func (s *Server) importCommit(r *http.Request, a Actor, d int64) (any, int, error) {
	if !a.Can("upload", true) || !a.Can("users", true) {
		return nil, 0, denied
	}
	n, e := id(r, "import")
	if e != nil {
		return nil, 0, e
	}
	key, e := legacyCommandKey(r, false)
	if e != nil {
		return nil, 0, e
	}
	requestHash := credentialCommitHash(n)
	if requestOperation(r) != nil {
		if ctx := requestOperation(r); !bytes.Equal(ctx.Body, []byte(`{}`)) {
			return nil, 0, &APIError{409, "operation_request_mismatch"}
		}
		out, e := importApplicationReceipt(r, s.DB, a, d, key)
		if e != nil {
			return nil, 0, e
		}
		if out != nil {
			if e := verifyImportGrantReceipt(r, out); e != nil {
				return nil, 0, e
			}
			result, e := decodeImportReceipt(out)
			return result, 200, e
		}
	}
	var report any
	e = s.transact(r.Context(), func(tx *sql.Tx) error {
		if e := lockDivision(r.Context(), tx, a, d); e != nil {
			return e
		}
		var actor int64
		var status string
		var payload, stored []byte
		if e := tx.QueryRowContext(r.Context(), "SELECT actor_id,status,payload,report FROM core_imports WHERE id=? AND organization_id=? AND division_id=? FOR UPDATE", n, a.OrganizationID, d).Scan(&actor, &status, &payload, &stored); e != nil {
			return e
		}
		if !a.OrganizationAdmin && actor != a.AccountID {
			return denied
		}
		if requestOperation(r) != nil && actor != a.AccountID {
			return missing
		}
		if status == "completed" {
			if requestOperation(r) != nil {
				out, e := importApplicationReceipt(r, tx, a, d, key)
				if e != nil {
					return e
				}
				if out == nil {
					return &APIError{409, "import_completed_by_another_command"}
				}
				if e := verifyImportGrantReceipt(r, out); e != nil {
					return e
				}
				report, e = decodeImportReceipt(out)
				return e
			}
			return json.Unmarshal(stored, &report)
		}
		var in ImportDTO
		if e := json.Unmarshal(payload, &in); e != nil {
			return e
		}
		owner := a
		owner.AccountID = actor
		source, e := loadImportSource(r, tx, owner, d, n, "users.credentials")
		if e != nil {
			return e
		}
		proof := credentialSnapshotProof(owner, d, n, in, source)
		if e := verifyImportGrantProof(r, "users.credentials", n, source, proof, requestHash); e != nil {
			return e
		}
		ids := []int64{}
		for _, u := range in.Users {
			if !strings.HasPrefix(u.Password, "pbkdf2:sha256:600000$") {
				return unavailable
			}
			id, e := s.insertUser(r, tx, a, d, u, u.Password)
			if e != nil {
				return e
			}
			ids = append(ids, id)
		}
		report = map[string]any{"created": ids, "count": len(ids)}
		data, e := json.Marshal(report)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(r.Context(), "UPDATE core_imports SET status='completed',report=? WHERE id=?", data, n); e != nil {
			return e
		}
		if e := saveImportApplicationReceipt(r, tx, a, d, n, "users.credentials", key, requestHash, source, proof, report); e != nil {
			return e
		}
		return audit(r.Context(), tx, a, &d, "import.commit", n)
	})
	return report, 200, e
}
