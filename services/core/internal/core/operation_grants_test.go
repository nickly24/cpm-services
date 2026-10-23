package core

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func coreGrantEnvelope(kind string, proof *ImportAuthorizationProof, source string) operationEnvelope {
	div, session, revision := int64(2), "7", "2"
	perms := map[string]Permission{"upload": {true, true}, "users": {true, true}, "groups": {true, true}, "train": {true, true}}
	target := json.RawMessage(`{"kind":"division_users","division_id":"2"}`)
	now := importMicrosecond(time.Now())
	expiry := now.Add(time.Hour)
	b := operationBinding{ContractVersion: 1, UploadID: strings.Repeat("a", 32), UploadSessionID: strings.Repeat("b", 32), JobID: strings.Repeat("c", 32), CoordinatorCommandKey: "native-http-command_1", Type: kind, Owner: "core", NativeCommandKey: "01234567-89ab-4def-8123-456789abcdef", OwnerSessionID: &session, UploadPreviewRevision: "3", Target: target, SourceSHA256: source, OwnerProof: operationOwnerProof{proof.Scheme, proof.ValidationSHA256, &proof.InputSHA256, nil}, OwnerRequestSHA256: credentialCommitHash(7), ApplyWireBodySHA256: importSHA256([]byte(`{}`)), UploadExpiresAt: now.Add(2 * time.Hour), RequiredDivisionIDs: []string{"2"}}
	if kind == "users.legacy" {
		b.OwnerPreviewRevision = &revision
		b.OwnerPreviewExpiresAt = &expiry
		b.OwnerRequestSHA256 = legacyCommandHash("commit", 7, LegacyImportCommitDTO{2})
		b.ApplyWireBodySHA256 = importSHA256([]byte(`{"expected_revision":2}`))
	}
	return operationEnvelope{Status: true, ContractVersion: 1, GrantID: strings.Repeat("d", 32), Mode: "apply", ExpiresAt: now.Add(30 * time.Minute), Actor: operationActor{ID: 19, AccountID: 3, Role: "staff_admin", OrganizationID: 1, DivisionID: div, DivisionIDs: []int64{div}, Permissions: perms}, DivisionPermissions: []operationDivisionPermissions{{div, perms}}, Binding: b}
}
func TestCredentialGrantFirstApplyChecksProofUnderSessionLockAndStoresReceiptAtomically(t *testing.T) {
	for _, scenario := range []string{"success", "sourcechanged", "inputchanged", "oldsource", "receiptwritefails"} {
		t.Run(scenario, func(t *testing.T) {
			in := credentialProofInput()
			source := strings.Repeat("a", 64)
			proof := credentialSnapshotProof(legacyAdmin(), 2, 7, in, &importOperationSource{source, "core-file-v1"})
			env := coreGrantEnvelope("users.credentials", proof, source)
			stored := credentialProofInput()
			if scenario == "inputchanged" {
				stored.Users[0].Password += "changed"
			}
			payload, _ := json.Marshal(stored)
			steps := []legacySQLStep{{kind: "query", contains: "core_import_operation_receipts", cols: []string{"kind", "session", "hash", "source", "proof", "response", "status"}}, {kind: "query", contains: "divisions WHERE id=? AND organization_id=? AND is_active=1 FOR UPDATE", args: []any{int64(2), int64(1)}, cols: []string{"id"}, rows: [][]driver.Value{{int64(2)}}}, {kind: "query", contains: "core_imports WHERE id=? AND organization_id=? AND division_id=? FOR UPDATE", args: []any{int64(7), int64(1), int64(2)}, cols: []string{"actor", "status", "payload", "report"}, rows: [][]driver.Value{{int64(3), "preview", payload, nil}}}, {kind: "query", contains: "core_import_operation_sources", args: []any{"users.credentials", int64(7), int64(1), int64(2), int64(3)}, cols: []string{"source", "provenance"}, rows: [][]driver.Value{{source, "core-file-v1"}}}}
			if scenario == "sourcechanged" {
				steps[3].rows[0][0] = strings.Repeat("b", 64)
			}
			if scenario == "oldsource" {
				steps[3].rows = nil
			}
			if scenario == "success" || scenario == "receiptwritefails" {
				steps = append(steps, legacySQLStep{kind: "query", contains: "COUNT(*) FROM auth_users WHERE username=?", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, legacySQLStep{kind: "exec", contains: "INSERT INTO students", inserted: 19}, legacySQLStep{kind: "query", contains: "COUNT(*) FROM auth_users WHERE role='student' AND ref_id=?", cols: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, legacySQLStep{kind: "exec", contains: "INSERT INTO auth_users", inserted: 91})
				for _, table := range []string{"auth_account_state", "core_user_owners", "auth_organization_access", "auth_division_access", "core_audit"} {
					steps = append(steps, legacySQLStep{kind: "exec", contains: "INSERT INTO " + table})
				}
				steps = append(steps, legacySQLStep{kind: "exec", contains: "UPDATE core_imports SET status='completed'"}, legacySQLStep{kind: "exec", contains: "INSERT INTO core_import_operation_receipts"})
				if scenario == "receiptwritefails" {
					steps[len(steps)-1].err = errors.New("synthetic durable receipt write failure")
				} else {
					steps = append(steps, legacySQLStep{kind: "exec", contains: "INSERT INTO core_audit"})
				}
			}
			c := &legacySQLConnector{steps: steps}
			db := sql.OpenDB(c)
			defer db.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(env) }))
			defer server.Close()
			s := &Server{DB: db, Auth: coreGrantClient(server)}
			r := coreGrantRequest("POST", "/v1/core/divisions/2/imports/7/commit", "apply", []byte(`{}`))
			r.SetPathValue("import", "7")
			a, e := s.Auth.Identify(r)
			if e != nil {
				t.Fatal(e)
			}
			_, _, e = s.importCommit(r, a, 2)
			if (e == nil) != (scenario == "success") || c.committed != (scenario == "success") || c.rolledBack != (scenario != "success") || len(c.steps) != 0 {
				t.Fatal("first apply proof/receipt not atomic under locks", e)
			}
			if scenario == "sourcechanged" || scenario == "inputchanged" || scenario == "oldsource" {
				var api *APIError
				if !errors.As(e, &api) || api.Message != "operation_request_mismatch" {
					t.Fatal("failed to reject changed/unproved snapshot before subject writes", e)
				}
			}
		})
	}
}
func TestLegacyGrantChecksSourceProofAfterScopeAndSessionLocks(t *testing.T) {
	rows := []LegacyImportRow{legacyRow(2, "Synthetic Student")}
	out := LegacyImportSession{ID: 7, OrganizationID: 1, DivisionID: 2, ActorID: 3, Revision: 2, Status: "preview", Preview: legacyBuildPreview(rows, legacyEmptyIndex(), legacyAdmin()), CreatedAt: importMicrosecond(time.Now()), ExpiresAt: importMicrosecond(time.Now().Add(time.Hour))}
	source := strings.Repeat("a", 64)
	proof := legacySnapshotProof(legacyAdmin(), out, rows, &importOperationSource{source, "uploads-file-v1"})
	env := coreGrantEnvelope("users.legacy", proof, source)
	env.Binding.OwnerPreviewExpiresAt = &out.ExpiresAt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/auth/password-hash" {
			io.WriteString(w, `{"status":true,"hash":"pbkdf2:sha256:600000$synthetic"}`)
			return
		}
		json.NewEncoder(w).Encode(env)
	}))
	defer server.Close()
	payload, _ := json.Marshal(LegacyImportDTO{rows})
	preview, _ := json.Marshal(out.Preview)
	session := func(locked bool) legacySQLStep {
		query := "core_legacy_imports WHERE id=?"
		if locked {
			query = "actor_id=? FOR UPDATE"
		}
		return legacySQLStep{kind: "query", contains: query, cols: []string{"id", "org", "div", "actor", "rev", "status", "payload", "preview", "report", "created", "expires"}, rows: [][]driver.Value{{int64(7), int64(1), int64(2), int64(3), int64(2), "preview", payload, preview, nil, out.CreatedAt, out.ExpiresAt}}}
	}
	steps := []legacySQLStep{{kind: "query", contains: "core_import_operation_receipts", cols: []string{"kind", "session", "hash", "source", "proof", "response", "status"}}, {kind: "query", contains: "core_legacy_import_receipts", cols: []string{"hash", "response", "status"}}, session(false), {kind: "query", contains: "divisions WHERE id=? AND organization_id=? AND is_active=1 FOR UPDATE", cols: []string{"id"}, rows: [][]driver.Value{{int64(2)}}}, {kind: "query", contains: "organizations WHERE id=? AND is_active=1 FOR UPDATE", cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, {kind: "query", contains: "core_legacy_import_receipts", cols: []string{"hash", "response", "status"}}, session(true)}
	for _, query := range []string{"WHERE o.organization_id=? AND u.role=?", "WHERE o.organization_id=? AND u.role=?", "schools s JOIN core_school_scope", "JOIN core_group_scope"} {
		steps = append(steps, legacySQLStep{kind: "query", contains: query, cols: []string{"unused"}})
	}
	steps = append(steps, legacySQLStep{kind: "query", contains: "core_import_operation_sources", cols: []string{"source", "provenance"}, rows: [][]driver.Value{{strings.Repeat("b", 64), "uploads-file-v1"}}})
	c := &legacySQLConnector{steps: steps}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, Auth: coreGrantClient(server)}
	r := coreGrantRequest("POST", "/v1/core/divisions/2/legacy-imports/7/commit", "apply", []byte(`{"expected_revision":2}`))
	r.SetPathValue("import", "7")
	a, e := s.Auth.Identify(r)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.legacyImportCommit(r, a, 2)
	var api *APIError
	if !errors.As(e, &api) || api.Message != "operation_request_mismatch" || len(c.steps) != 0 || !c.rolledBack || c.committed {
		t.Fatal("changed source did not fail under all native locks before apply", e)
	}
}
func coreGrantRequest(method, path, stage string, body []byte) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CPM-Operation-Grant", strings.Repeat("d", 32))
	r.Header.Set("X-CPM-Operation-Job", strings.Repeat("c", 32))
	r.Header.Set("X-CPM-Operation-Stage", stage)
	r.Header.Set("X-CPM-Operation-Service-Key", strings.Repeat("u", 32))
	r.Header.Set("Idempotency-Key", "01234567-89ab-4def-8123-456789abcdef")
	return r
}
func coreGrantClient(server *httptest.Server) *AuthClient {
	return NewAuthClient(Config{AuthURL: server.URL, InternalKey: strings.Repeat("s", 32), OperationOwnerKey: strings.Repeat("o", 32), OperationDeliveryKey: strings.Repeat("u", 32), CardsOperationKey: strings.Repeat("k", 32)})
}
func TestOperationCarrierStrictTransportRejectsBeforeAuthOrOwnerSQL(t *testing.T) {
	for _, name := range []string{"mixed", "duplicate", "partial", "wronguuid", "scope", "encoded", "dot", "query", "barequery", "wrongstage", "foreignroute", "unknownheader", "browser"} {
		t.Run(name, func(t *testing.T) {
			r := coreGrantRequest("GET", "/v1/core/divisions/2/imports/7", "preview", nil)
			switch name {
			case "mixed":
				r.Header.Set("Authorization", "Bearer synthetic")
			case "duplicate":
				r.Header.Add("X-CPM-Operation-Grant", strings.Repeat("d", 32))
			case "partial":
				r.Header.Del("X-CPM-Operation-Job")
			case "wronguuid":
				r.Header.Set("Idempotency-Key", "native-command-not-uuid")
			case "scope":
				r.Header.Set("X-Division-ID", "999")
			case "encoded":
				r.URL.RawPath = "/v1/core/divisions/2/imports/%37"
			case "dot":
				r.URL.Path = "/v1/core/divisions/2/imports/../7"
			case "query":
				r.URL.RawQuery = "organization_id=999"
			case "barequery":
				r.URL.ForceQuery = true
			case "wrongstage":
				r.Header.Set("X-CPM-Operation-Stage", "apply")
			case "foreignroute":
				r.URL.Path = "/v1/core/divisions/2/users"
			case "unknownheader":
				r.Header.Set("X-CPM-Operation-Actor", "admin")
			case "browser":
				r.Header.Set("Cookie", "synthetic-cookie")
			}
			if e := validateOperationTransport(r); e == nil {
				t.Fatal("unsafe transport accepted")
			}
		})
	}
}
func TestOperationAuthorizationUsesExactBytesSeparateOwnerKeyAndProfileID(t *testing.T) {
	source := strings.Repeat("a", 64)
	proof := credentialSnapshotProof(legacyAdmin(), 2, 7, credentialProofInput(), &importOperationSource{source, "core-file-v1"})
	env := coreGrantEnvelope("users.credentials", proof, source)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/internal/auth/operation-grants/"+strings.Repeat("d", 32)+"/authorize" || r.Header.Get("X-Service-Key") != strings.Repeat("o", 32) || r.Header.Get("Authorization") != "" || r.Header.Get("X-CPM-Operation-Service-Key") != "" {
			t.Error("delivery identity forwarded or wrong Auth owner key")
		}
		var actual operationAuthorizeRequest
		body, _ := io.ReadAll(r.Body)
		if decodeOperationJSON(body, &actual) != nil || actual.WireBodySHA256 != importSHA256([]byte(`{}`)) || actual.RawQuery != "" || actual.ForceQuery || actual.NativeCommandKey != env.Binding.NativeCommandKey {
			t.Error("wrong byte/path/key binding")
		}
		json.NewEncoder(w).Encode(env)
	}))
	defer server.Close()
	r := coreGrantRequest("POST", "/v1/core/divisions/2/imports/7/commit", "apply", []byte(`{}`))
	a, e := coreGrantClient(server).Identify(r)
	if e != nil || a.ID != 19 || a.AccountID != 3 || a.OrganizationAdmin || !a.Can("users", true) || calls != 1 {
		t.Fatal("identity or native permission context changed", e)
	}
	b, _ := io.ReadAll(r.Body)
	if string(b) != "{}" {
		t.Fatal("raw owner body lost/reencoded")
	}
}
func TestOperationAuthorizationRejectsMalformedOrChangedContext(t *testing.T) {
	source := strings.Repeat("a", 64)
	proof := credentialSnapshotProof(legacyAdmin(), 2, 7, credentialProofInput(), &importOperationSource{source, "core-file-v1"})
	for _, name := range []string{"wrongcase", "duplicate", "escapedduplicate", "missingnull", "falseadmin", "foreignactor", "foreignjob", "foreigntarget", "wronghash", "expired", "revokedpermission", "receiptmodeapply", "malformed"} {
		t.Run(name, func(t *testing.T) {
			env := coreGrantEnvelope("users.credentials", proof, source)
			switch name {
			case "falseadmin":
				env.Actor.OrganizationAdmin = true
			case "foreignactor":
				env.Actor.DivisionID = 3
			case "foreignjob":
				env.Binding.JobID = strings.Repeat("e", 32)
			case "foreigntarget":
				env.Binding.Target = json.RawMessage(`{"kind":"division_users","division_id":"3"}`)
			case "wronghash":
				env.Binding.ApplyWireBodySHA256 = strings.Repeat("e", 64)
			case "expired":
				env.ExpiresAt = time.Now().Add(-time.Hour)
			case "revokedpermission":
				env.Actor.Permissions["users"] = Permission{true, false}
			case "receiptmodeapply":
				env.Mode = "receipt"
			}
			body, _ := json.Marshal(env)
			switch name {
			case "wrongcase":
				body = bytes.Replace(body, []byte(`"account_id"`), []byte(`"Account_ID"`), 1)
			case "duplicate":
				body = bytes.Replace(body, []byte(`"status":true`), []byte(`"status":true,"status":true`), 1)
			case "escapedduplicate":
				body = bytes.Replace(body, []byte(`"status":true`), []byte(`"status":true,"\u0073tatus":true`), 1)
			case "missingnull":
				body = bytes.Replace(body, []byte(`"target_revision":null,`), nil, 1)
			case "malformed":
				body = []byte(`{"status":true}`)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
			defer server.Close()
			r := coreGrantRequest("POST", "/v1/core/divisions/2/imports/7/commit", "apply", []byte(`{}`))
			if _, e := coreGrantClient(server).Identify(r); e == nil {
				t.Fatal("invalid Auth response authorized")
			}
		})
	}
}
func TestOperationDependencyUsesCardsKeyAndOnlyOnePinnedDirectionInReceiptMode(t *testing.T) {
	proof := &ImportAuthorizationProof{Scheme: "cards.xlsx.v1", ValidationSHA256: strings.Repeat("a", 64)}
	env := coreGrantEnvelope("users.credentials", proof, strings.Repeat("b", 64))
	session, rev := "0123456789abcdef0123456789abcdef", "2"
	env.Mode = "receipt"
	env.Binding.Type = "cards.xlsx"
	env.Binding.Owner = "cards"
	env.Binding.OwnerSessionID = &session
	env.Binding.OwnerPreviewRevision = &rev
	env.Binding.OwnerProof.InputSHA256 = nil
	deadline := importMicrosecond(time.Now().Add(-24 * time.Hour))
	env.Binding.OwnerPreviewExpiresAt = &deadline
	env.Binding.Target = json.RawMessage(`{"kind":"existing_theme","direction_id":"11","theme_id":"12"}`)
	env.Binding.UploadExpiresAt = importMicrosecond(time.Now().Add(-24 * time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(env) }))
	defer server.Close()
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "JOIN divisions d", args: []any{int64(2), int64(11), int64(1)}, cols: []string{"id"}, rows: [][]driver.Value{{int64(11)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, Auth: coreGrantClient(server)}
	r := coreGrantRequest("GET", "/internal/core/import-directions/11", "core-direction", nil)
	r.Header.Set("X-CPM-Operation-Service-Key", strings.Repeat("k", 32))
	r.SetPathValue("direction", "11")
	w := httptest.NewRecorder()
	s.importDirection(w, r)
	if w.Code != 200 || len(c.steps) != 0 || strings.Contains(w.Body.String(), "directions") || strings.Contains(w.Body.String(), "name") || !strings.Contains(w.Body.String(), `"account_id":3`) {
		t.Fatal("dependency exposed catalog or failed historical recovery", w.Code)
	}
	r = coreGrantRequest("GET", "/internal/core/import-directions/11", "core-direction", nil)
	if _, e := s.Auth.Identify(r); e == nil {
		t.Fatal("Uploads delivery key authorized Cards dependency")
	}
}
func TestReceiptGrantChecksHistoricalProofWithoutCurrentPreviewReads(t *testing.T) {
	proof := ImportAuthorizationProof{legacySnapshotScheme, strings.Repeat("a", 64), strings.Repeat("b", 64), nil}
	source := strings.Repeat("c", 64)
	env := coreGrantEnvelope("users.legacy", &proof, source)
	env.Mode = "receipt"
	expired := importMicrosecond(time.Now().Add(-24 * time.Hour))
	env.Binding.OwnerPreviewExpiresAt = &expired
	env.Binding.UploadExpiresAt = expired
	for _, name := range []string{"match", "mismatch", "missing"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(env) }))
			defer server.Close()
			step := operationReceiptStep("users.legacy", 7, env.Binding.NativeCommandKey, proof, source, env.Binding.OwnerRequestSHA256, map[string]any{"status": "completed", "revision": 3, "report": map[string]any{"created_students": 1}})
			if name == "mismatch" {
				step.rows[0][2] = strings.Repeat("e", 64)
			}
			if name == "missing" {
				step.rows = nil
			}
			c := &legacySQLConnector{steps: []legacySQLStep{step}}
			db := sql.OpenDB(c)
			defer db.Close()
			s := &Server{DB: db, Auth: coreGrantClient(server)}
			r := coreGrantRequest("GET", "/v1/core/divisions/2/legacy-imports/7/receipts/"+env.Binding.NativeCommandKey, "receipt", nil)
			r.Pattern = "GET /v1/core/divisions/{division}/legacy-imports/{import}/receipts/{receipt}"
			r.SetPathValue("import", "7")
			r.SetPathValue("receipt", env.Binding.NativeCommandKey)
			a, e := s.Auth.Identify(r)
			if e != nil {
				t.Fatal(e)
			}
			out, code, e := s.importApplicationReceiptGet(r, a, 2)
			if (e == nil) != (name == "match") || len(c.steps) != 0 || c.committed || c.rolledBack {
				t.Fatal("receipt mismatched or attempted apply/current reads", e)
			}
			if name == "match" && (code != 200 || out.(map[string]any)["revision"].(json.Number).String() != "3") {
				t.Fatal("original response replaced")
			}
		})
	}
}
func TestLiveOperationAuthorizationRepeatsAfterSlowHashingAndStopsBeforeWrites(t *testing.T) {
	rows := []LegacyImportRow{legacyRow(2, "Synthetic Student")}
	out := LegacyImportSession{ID: 7, OrganizationID: 1, DivisionID: 2, ActorID: 3, Revision: 2, Status: "preview", Preview: legacyBuildPreview(rows, legacyEmptyIndex(), legacyAdmin()), CreatedAt: importMicrosecond(time.Now()), ExpiresAt: importMicrosecond(time.Now().Add(time.Hour))}
	source := strings.Repeat("a", 64)
	proof := legacySnapshotProof(legacyAdmin(), out, rows, &importOperationSource{source, "uploads-file-v1"})
	env := coreGrantEnvelope("users.legacy", proof, source)
	env.Binding.OwnerPreviewExpiresAt = &out.ExpiresAt
	var denied atomic.Bool
	var authorizations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/auth/password-hash" {
			denied.Store(true)
			io.WriteString(w, `{"status":true,"hash":"pbkdf2:sha256:600000$synthetic"}`)
			return
		}
		authorizations.Add(1)
		if denied.Load() {
			w.WriteHeader(403)
			io.WriteString(w, `{"status":false,"error":"operation_access_denied"}`)
			return
		}
		json.NewEncoder(w).Encode(env)
	}))
	defer server.Close()
	payload, _ := json.Marshal(LegacyImportDTO{rows})
	preview, _ := json.Marshal(out.Preview)
	c := &legacySQLConnector{steps: []legacySQLStep{{kind: "query", contains: "core_import_operation_receipts", cols: []string{"kind", "session", "hash", "source", "proof", "response", "status"}}, {kind: "query", contains: "core_legacy_import_receipts", cols: []string{"hash", "response", "status"}}, {kind: "query", contains: "core_legacy_imports WHERE id=?", cols: []string{"id", "org", "div", "actor", "rev", "status", "payload", "preview", "report", "created", "expires"}, rows: [][]driver.Value{{int64(7), int64(1), int64(2), int64(3), int64(2), "preview", payload, preview, nil, out.CreatedAt, out.ExpiresAt}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, Auth: coreGrantClient(server)}
	r := coreGrantRequest("POST", "/v1/core/divisions/2/legacy-imports/7/commit", "apply", []byte(`{"expected_revision":2}`))
	r.SetPathValue("import", "7")
	a, e := s.Auth.Identify(r)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.legacyImportCommit(r, a, 2)
	if e == nil || authorizations.Load() != 2 || len(c.steps) != 0 || c.committed || c.rolledBack {
		t.Fatal("permission withdrawn during hashing did not stop before TX", e)
	}
}
func TestOperationConfigurationDisabledOrFullyDistinct(t *testing.T) {
	if validateCoreOperationKeys(Config{}) != nil {
		t.Fatal("absent feature enabled")
	}
	valid := Config{InternalKey: strings.Repeat("s", 32), OperationOwnerKey: strings.Repeat("o", 32), OperationDeliveryKey: strings.Repeat("u", 32), CardsOperationKey: strings.Repeat("k", 32)}
	if validateCoreOperationKeys(valid) != nil {
		t.Fatal("distinct config rejected")
	}
	for _, change := range []func(*Config){func(c *Config) { c.OperationOwnerKey = "" }, func(c *Config) { c.CardsOperationKey = c.OperationDeliveryKey }, func(c *Config) { c.OperationOwnerKey = c.InternalKey }, func(c *Config) { c.OperationDeliveryKey = "short" }} {
		c := valid
		change(&c)
		if validateCoreOperationKeys(c) == nil {
			t.Fatal("partial/non-distinct config accepted")
		}
	}
}
func TestLegacyGrantRejectsInconsistentNativeQuoteBeforeReceiptReplay(t *testing.T) {
	proof := &ImportAuthorizationProof{legacySnapshotScheme, strings.Repeat("a", 64), strings.Repeat("b", 64), nil}
	env := coreGrantEnvelope("users.legacy", proof, strings.Repeat("c", 64))
	// Auth cannot reproduce Core's native hash. Even a quote pinning these exact
	// bytes must not replay the old revision-2 receipt as revision-3 success.
	env.Binding.ApplyWireBodySHA256 = importSHA256([]byte(`{"expected_revision":3}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(env) }))
	defer server.Close()
	c := &legacySQLConnector{}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db, Auth: coreGrantClient(server)}
	r := coreGrantRequest("POST", "/v1/core/divisions/2/legacy-imports/7/commit", "apply", []byte(`{"expected_revision":3}`))
	r.SetPathValue("import", "7")
	a, e := s.Auth.Identify(r)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = s.legacyImportCommit(r, a, 2)
	var api *APIError
	if !errors.As(e, &api) || api.Message != "operation_request_mismatch" || c.committed || c.rolledBack {
		t.Fatal("inconsistent native quote reached historical receipt/apply", e)
	}
}
func TestLegacyGrantCurrentNativeRevisionAndDeadlineMustMatchBinding(t *testing.T) {
	proof := &ImportAuthorizationProof{legacySnapshotScheme, strings.Repeat("a", 64), strings.Repeat("b", 64), nil}
	env := coreGrantEnvelope("users.legacy", proof, strings.Repeat("c", 64))
	out := LegacyImportSession{Revision: 2, ExpiresAt: *env.Binding.OwnerPreviewExpiresAt}
	for _, scenario := range []string{"match", "revision", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(env) }))
			defer server.Close()
			r := coreGrantRequest("GET", "/v1/core/divisions/2/legacy-imports/7", "preview", nil)
			if _, e := coreGrantClient(server).Identify(r); e != nil {
				t.Fatal(e)
			}
			native := out
			if scenario == "revision" {
				native.Revision = 3
			}
			if scenario == "deadline" {
				native.ExpiresAt = native.ExpiresAt.Add(time.Minute)
			}
			if e := verifyLegacyGrantSession(r, native); (e == nil) != (scenario == "match") {
				t.Fatal("native revision/deadline accepted mismatched binding", e)
			}
		})
	}
}
