package core

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func credentialProofInput() ImportDTO {
	return ImportDTO{Users: []UserDTO{{Role: "student", FullName: "Тест <&>", Class: 7, Login: "synthetic", Password: "pbkdf2:sha256:600000$fixed-synthetic-salt$fixed-synthetic-hash"}}}
}

func TestCredentialImportGetPublishesStoredScopeWithoutPrivateInput(t *testing.T) {
	for _, name := range []string{"own_proof", "organization_admin_other_creator", "unknown_old_source"} {
		t.Run(name, func(t *testing.T) {
			a := legacyAdmin()
			actorID := a.AccountID
			if name == "organization_admin_other_creator" {
				actorID = 91
			}
			in := credentialProofInput()
			payload, _ := json.Marshal(in)
			rows := [][]driver.Value{{strings.Repeat("a", 64), "core-file-v1"}}
			if name == "unknown_old_source" {
				rows = nil
			}
			connector := &legacySQLConnector{steps: []legacySQLStep{
				{kind: "query", contains: "SELECT id,organization_id,division_id,actor_id,status,payload,report,created_at FROM core_imports WHERE id=? AND organization_id=? AND division_id=?", args: []any{int64(7), int64(1), int64(2)}, cols: []string{"id", "organization_id", "division_id", "actor_id", "status", "payload", "report", "created_at"}, rows: [][]driver.Value{{int64(7), int64(1), int64(2), actorID, "preview", string(payload), nil, time.Now().UTC()}}},
				{kind: "query", contains: "core_import_operation_sources", args: []any{"users.credentials", int64(7), int64(1), int64(2), actorID}, cols: []string{"source_sha256", "provenance"}, rows: rows},
			}}
			db := sql.OpenDB(connector)
			defer db.Close()
			r := httptest.NewRequest("GET", "/v1/core/divisions/2/imports/7", nil)
			r.SetPathValue("import", "7")
			out, code, e := (&Server{DB: db}).importGet(r, a, 2)
			if e != nil || code != 200 || len(connector.steps) != 0 {
				t.Fatal("scoped native GET failed", e)
			}
			encoded, _ := json.Marshal(out)
			var published CredentialImportSessionView
			if json.Unmarshal(encoded, &published) != nil || published.ID != 7 || published.OrganizationID != 1 || published.DivisionID != 2 || published.ActorID != actorID {
				t.Fatal("stored origin scope or creator missing from native response")
			}
			if len(published.Users) != 1 || published.Users[0].Password != "" || bytes.Contains(encoded, []byte(in.Users[0].Password)) || bytes.Contains(encoded, []byte(`"payload"`)) {
				t.Fatal("private input escaped native GET")
			}
			if name == "unknown_old_source" {
				if published.AuthorizationProof != nil || published.SourceSHA256 != nil || published.OwnerRequestSHA256 != nil {
					t.Fatal("unknown old source was fabricated")
				}
			} else {
				owner := a
				owner.AccountID = actorID
				proof := credentialSnapshotProof(owner, 2, 7, in, &importOperationSource{strings.Repeat("a", 64), "core-file-v1"})
				if published.AuthorizationProof == nil || *published.AuthorizationProof != *proof || published.OwnerRequestSHA256 == nil || *published.OwnerRequestSHA256 != credentialCommitHash(7) {
					t.Fatal("native original proof/command metadata changed")
				}
			}
		})
	}
}

func TestCredentialImportGetOpenAPIPublishesScopeAndMaskedResponse(t *testing.T) {
	op := openAPI()["paths"].(map[string]any)["/v1/core/divisions/{division}/imports/{import}"].(map[string]any)["get"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	envelope := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	data := envelope["properties"].(map[string]any)["data"].(map[string]any)
	props := data["properties"].(map[string]any)
	for _, name := range []string{"id", "organization_id", "division_id", "actor_id"} {
		property := props[name].(map[string]any)
		if property["type"] != "integer" || property["format"] != "int64" || property["minimum"] != 1 {
			t.Fatal("scope/identity contract incomplete")
		}
	}
	for _, name := range []string{"authorization_proof", "source_sha256", "source_provenance", "owner_request_sha256", "owner_preview_expires_at"} {
		if _, present := props[name]; !present {
			t.Fatal("native proof contract missing")
		}
	}
	userProps := props["users"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if userProps["password"].(map[string]any)["const"] != "" || props["owner_preview_expires_at"].(map[string]any)["type"] != "null" {
		t.Fatal("credential GET advertised an unmasked password or fabricated expiry")
	}
}
func TestImportProofPinsPrivateInputOriginScopeAndVersion(t *testing.T) {
	a := Actor{ID: 19, AccountID: 9007199254740993, OrganizationID: 1}
	source := &importOperationSource{strings.Repeat("a", 64), "core-file-v1"}
	in := credentialProofInput()
	proof := credentialSnapshotProof(a, 2, 9007199254740995, in, source)
	masked := in
	masked.Users = append([]UserDTO(nil), in.Users...)
	masked.Users[0].Password = ""
	if proof.InputSHA256 == importJSONHash(masked) || proof.InputSHA256 == legacyCommandHash("credential_preview", 0, in) || proof.MappingSHA256 != nil {
		t.Fatal("masked or creation digest substituted for private input")
	}
	b, _ := json.Marshal(proof)
	if bytes.Contains(b, []byte(in.Users[0].Password)) || bytes.Contains(b, []byte(in.Users[0].Login)) || bytes.Contains(b, []byte(in.Users[0].FullName)) {
		t.Fatal("proof leaked private input")
	}
	for _, change := range []func(*Actor, *ImportDTO, *importOperationSource){func(a *Actor, _ *ImportDTO, _ *importOperationSource) { a.AccountID++ }, func(a *Actor, _ *ImportDTO, _ *importOperationSource) { a.OrganizationID++ }, func(_ *Actor, in *ImportDTO, _ *importOperationSource) { in.Users[0].Password += "changed" }, func(_ *Actor, _ *ImportDTO, s *importOperationSource) { s.SHA256 = strings.Repeat("b", 64) }, func(_ *Actor, _ *ImportDTO, s *importOperationSource) { s.Provenance = "uploads-file-v1" }} {
		actor, input, src := a, credentialProofInput(), *source
		change(&actor, &input, &src)
		if credentialSnapshotProof(actor, 2, 9007199254740995, input, &src).ValidationSHA256 == proof.ValidationSHA256 {
			t.Fatal("changed origin/input/source not bound")
		}
	}
	if credentialSnapshotProof(a, 3, 9007199254740995, in, source).ValidationSHA256 == proof.ValidationSHA256 || credentialSnapshotProof(a, 2, 9007199254740996, in, source).ValidationSHA256 == proof.ValidationSHA256 {
		t.Fatal("scope/session not bound")
	}
	if credentialSnapshotProof(a, 2, 1, in, nil) != nil {
		t.Fatal("invented old preview source")
	}
	var reordered ImportDTO
	if json.Unmarshal([]byte(`{"users":[{"password":"pbkdf2:sha256:600000$fixed-synthetic-salt$fixed-synthetic-hash","login":"synthetic","role_id":null,"group_id":null,"school_id":null,"tg_name":"","class":7,"full_name":"Тест <&>","role":"student"}]}`), &reordered) != nil || importJSONHash(reordered) != proof.InputSHA256 {
		t.Fatal("persisted JSON key order changed proof")
	}
}
func TestLegacyProofPreservesRowsNullsAndMicrosecondDeadline(t *testing.T) {
	a := legacyAdmin()
	rows := []LegacyImportRow{legacyRow(2, "А Б"), legacyRow(3, "В Г")}
	stamp := time.Date(2031, 4, 5, 6, 7, 8, 123456789, time.FixedZone("MSK", 3*3600))
	out := LegacyImportSession{ID: 7, DivisionID: 2, Revision: 2, ExpiresAt: stamp, Preview: legacyBuildPreview(rows, legacyEmptyIndex(), a)}
	source := &importOperationSource{strings.Repeat("a", 64), "uploads-file-v1"}
	proof := legacySnapshotProof(a, out, rows, source)
	out.ExpiresAt = importMicrosecond(stamp)
	if legacySnapshotProof(a, out, rows, source).ValidationSHA256 != proof.ValidationSHA256 || importProofTime(stamp) != "2031-04-05T03:07:08.123456Z" {
		t.Fatal("persisted microsecond/UTC boundary unstable")
	}
	out.Revision++
	if legacySnapshotProof(a, out, rows, source).ValidationSHA256 == proof.ValidationSHA256 {
		t.Fatal("revision not bound")
	}
	out.Revision--
	rows[0], rows[1] = rows[1], rows[0]
	if legacySnapshotProof(a, out, rows, source).InputSHA256 == proof.InputSHA256 {
		t.Fatal("source row order sorted or ignored")
	}
	if credentialCommitHash(7) != importSHA256([]byte(`{"Operation":"credential_commit","Session":7,"Payload":{}}`)) || credentialCommitHash(7) == importSHA256([]byte(`{}`)) {
		t.Fatal("native/wire command hashes conflated")
	}
	if legacyCommandHash("commit", 7, LegacyImportCommitDTO{2}) != importSHA256([]byte(`{"Operation":"commit","Session":7,"Payload":{"expected_revision":2}}`)) {
		t.Fatal("existing native commit hash changed")
	}
}
func TestCredentialSourceDigestIsOriginalFileNotParsedRows(t *testing.T) {
	content := []byte("full_name,login,password,class\nA B,synthetic,not-logged-password,7\n")
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	f, _ := writer.CreateFormFile("file", "synthetic.csv")
	f.Write(content)
	writer.Close()
	r := httptest.NewRequest("POST", "/", &b)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	in, source, e := readImportSource(r)
	if e != nil || source == nil || source.SHA256 != importSHA256(content) || source.Provenance != "core-file-v1" || source.SHA256 == importJSONHash(in) {
		t.Fatal("file digest not computed from actual original bytes", e)
	}
}
func TestLegacySourceAttestationRequiresDistinctCredentialAndExactSHA(t *testing.T) {
	key := strings.Repeat("k", 32)
	for _, kind := range []string{"valid", "wrongkey", "missingkey", "duplicate", "uppercase", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.Header.Set("X-CPM-Import-Source-Key", key)
			r.Header.Set("X-CPM-Import-Source-SHA256", strings.Repeat("a", 64))
			configured := key
			switch kind {
			case "wrongkey":
				r.Header.Set("X-CPM-Import-Source-Key", strings.Repeat("x", 32))
			case "missingkey":
				r.Header.Del("X-CPM-Import-Source-Key")
			case "duplicate":
				r.Header.Add("X-CPM-Import-Source-Key", key)
			case "uppercase":
				r.Header.Set("X-CPM-Import-Source-SHA256", strings.Repeat("A", 64))
			case "disabled":
				configured = ""
			}
			source, e := importSourceClaim(r, configured)
			if (e == nil) != (kind == "valid") {
				t.Fatal("source claim accepted invalid credential", e)
			}
			if source != nil && source.Provenance != "uploads-file-v1" {
				t.Fatal("claimed independent source hashing")
			}
		})
	}
}
func TestSourceReplayCannotBackfillOrChangeOriginalFile(t *testing.T) {
	for _, kind := range []string{"same", "changed", "oldunknown"} {
		t.Run(kind, func(t *testing.T) {
			rows := [][]driver.Value{{strings.Repeat("a", 64), "uploads-file-v1"}}
			if kind == "oldunknown" {
				rows = nil
			}
			c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "core_import_operation_sources", args: []any{"users.legacy", int64(7), int64(1), int64(2), int64(3)}, cols: []string{"source", "provenance"}, rows: rows}}}
			db := sql.OpenDB(c)
			defer db.Close()
			source := &importOperationSource{strings.Repeat("a", 64), "uploads-file-v1"}
			if kind == "changed" {
				source.SHA256 = strings.Repeat("b", 64)
			}
			e := matchImportCreationSource(httptest.NewRequest("POST", "/", nil), db, legacyAdmin(), 2, 7, "users.legacy", source)
			if (e == nil) != (kind == "same") || len(c.steps) != 0 {
				t.Fatal("source replay altered/backfilled source", e)
			}
		})
	}
}
func TestOldCredentialMultipartCreationReceiptStillRecoversWithoutSourceBackfill(t *testing.T) {
	content := []byte("full_name,login,password,class\nA B,synthetic,not-logged-password,7\n")
	in, _ := importRows([][]string{{"full_name", "login", "password", "class"}, {"A B", "synthetic", "not-logged-password", "7"}})
	key := "01234567-89ab-4def-8123-456789abcdef"
	hash := legacyCommandHash("credential_preview", 0, in)
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "core_import_receipts", cols: []string{"hash", "session"}, rows: [][]driver.Value{{hash, int64(37)}}}, {kind: "query", contains: "core_import_operation_sources", cols: []string{"source", "provenance"}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	f, _ := writer.CreateFormFile("file", "synthetic.csv")
	f.Write(content)
	writer.Close()
	r := httptest.NewRequest("POST", "/", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Idempotency-Key", key)
	out, code, e := s.importPreview(r, legacyAdmin(), 2)
	if e != nil || code != 201 || out.(map[string]any)["id"] != int64(37) || len(c.steps) != 0 || c.committed || c.rolledBack {
		t.Fatal("old bearer lost-preview recovery regressed or backfilled source", e)
	}
}
func operationReceiptStep(kind string, session int64, key string, proof ImportAuthorizationProof, source, hash string, response any) legacySQLStep {
	p, _ := json.Marshal(proof)
	b, _ := json.Marshal(response)
	return legacySQLStep{kind: "query", contains: "core_import_operation_receipts WHERE organization_id=? AND division_id=? AND actor_id=? AND command_key=?", args: []any{int64(1), int64(2), int64(3), key}, cols: []string{"kind", "session", "hash", "source", "proof", "response", "status"}, rows: [][]driver.Value{{kind, session, hash, source, p, b, int64(200)}}}
}
func TestApplicationReceiptReadNeverFallsIntoApply(t *testing.T) {
	key := "01234567-89ab-4def-8123-456789abcdef"
	proof := ImportAuthorizationProof{legacySnapshotScheme, strings.Repeat("a", 64), strings.Repeat("b", 64), nil}
	for _, scenario := range []string{"found", "missing", "wrongsession", "wrongkind", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			step := operationReceiptStep("users.legacy", 7, key, proof, strings.Repeat("c", 64), strings.Repeat("d", 64), map[string]any{"status": "completed", "revision": 3, "account_id": 9007199254740993})
			if scenario == "missing" {
				step.rows = nil
			}
			if scenario == "wrongsession" {
				step.rows[0][1] = int64(8)
			}
			if scenario == "wrongkind" {
				step.rows[0][0] = "users.credentials"
			}
			c := &legacySQLConnector{steps: []legacySQLStep{step}}
			db := sql.OpenDB(c)
			defer db.Close()
			s := &Server{DB: db}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Pattern = "GET /v1/core/divisions/{division}/legacy-imports/{import}/receipts/{receipt}"
			r.SetPathValue("import", "7")
			r.SetPathValue("receipt", key)
			a := legacyAdmin()
			if scenario == "denied" {
				a.OrganizationAdmin = false
			}
			out, code, e := s.importApplicationReceiptGet(r, a, 2)
			if (e == nil) != (scenario == "found") {
				t.Fatal("receipt access did not fail closed", e)
			}
			if scenario == "found" && (code != 200 || out.(map[string]any)["account_id"].(json.Number).String() != "9007199254740993") {
				t.Fatal("historical response identity changed")
			}
			remaining := 0
			if scenario == "denied" {
				remaining = 1
			}
			if len(c.steps) != remaining || c.committed || c.rolledBack {
				t.Fatal("readonly recovery attempted other SQL/apply")
			}
		})
	}
}
func TestAccountSecurityVersionMutationIsAtomic(t *testing.T) {
	for _, mode := range []string{"password", "block", "reactivate"} {
		for _, failure := range []string{"none", "epoch", "audit"} {
			t.Run(mode+failure, func(t *testing.T) {
				steps := []legacySQLStep{{kind: "exec", contains: "UPDATE auth_users"}, {kind: "exec", contains: "UPDATE auth_account_state"}, {kind: "exec", contains: "INSERT INTO core_audit"}}
				if failure == "epoch" {
					steps[1].err = errors.New("synthetic epoch failure")
					steps = steps[:2]
				}
				if failure == "audit" {
					steps[2].err = errors.New("synthetic audit failure")
				}
				c := &legacySQLConnector{steps: steps}
				db := sql.OpenDB(c)
				defer db.Close()
				s := &Server{DB: db}
				e := s.transact(t.Context(), func(tx *sql.Tx) error {
					if _, e := tx.ExecContext(t.Context(), "UPDATE auth_users SET password=? WHERE id=?", "synthetic-hash", int64(91)); e != nil {
						return e
					}
					var e error
					if mode == "password" {
						e = bumpAccountAuthVersion(t.Context(), tx, 91)
					} else {
						e = setAccountActiveAndBumpAuthVersion(t.Context(), tx, 91, mode == "reactivate")
					}
					if e != nil {
						return e
					}
					return audit(t.Context(), tx, Actor{AccountID: 31, OrganizationID: 1}, legacyInt(2), "synthetic.security", 91)
				})
				if (e == nil) != (failure == "none") || c.committed != (failure == "none") || c.rolledBack != (failure != "none") || len(c.steps) != 0 {
					t.Fatal("security/epoch/audit did not share atomic transaction", e)
				}
			})
		}
	}
}
