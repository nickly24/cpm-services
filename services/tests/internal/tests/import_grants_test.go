package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const importTestUUID = "11111111-1111-4111-8111-111111111111"

func importTestActor() Actor {
	d := int64(17)
	return Actor{AccountID: 23, OrganizationID: 11, DivisionID: &d, DivisionIDs: []int64{17}, Role: "staff_admin", Permissions: map[string]Permission{"upload": {true, true}, "tests": {true, true}, "test-results": {true, true}}}
}
func importTestConfig() Config {
	return Config{OperationDeliveryKey: strings.Repeat("d", 32), OperationAuthKey: strings.Repeat("a", 32), ServiceKey: strings.Repeat("k", 32)}
}
func importTestJSON(t *testing.T) (JSONImportCommitDTO, []byte, operationAuthorization) {
	t.Helper()
	in := JSONImportCommitDTO{JSONImportPreviewDTO: jsonImportFixture(), CommandID: importTestUUID}
	p := buildJSONImportPreview(in.JSONImportPreviewDTO, "Математика")
	in.PreviewHash = p.PreviewHash
	raw, _ := json.Marshal(in)
	// Whitespace is part of the bound wire representation.
	raw = append(raw, '\n')
	_, hash, err := jsonImportOperation(jsonImportRequest(in), importTestActor(), in)
	if err != nil {
		t.Fatal(err)
	}
	return in, raw, importTestAuthorization(raw, p.OwnerProof, hash)
}
func importTestAuthorization(raw []byte, p *ImportOwnerProof, hash string) operationAuthorization {
	direction := "31"
	actor := importTestActor()
	wire := sha256.Sum256(raw)
	wireSHA := hex.EncodeToString(wire[:])
	return operationAuthorization{Status: true, ContractVersion: 1, GrantID: strings.Repeat("1", 32), Mode: "apply", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		Actor:               operationActor{ID: 1049, Actor: actor}, // Deliberately different from account_id.
		DivisionPermissions: []operationDivisionPermissions{{17, actor.Permissions}},
		Binding:             operationBinding{ContractVersion: 1, UploadID: strings.Repeat("2", 32), UploadSessionID: strings.Repeat("3", 32), JobID: strings.Repeat("4", 32), CoordinatorCommandKey: "uploads-command:original", Type: "tests.json", Owner: "tests", NativeCommandKey: importTestUUID, UploadPreviewRevision: "3", Target: operationTarget{Kind: "create_tests", DirectionID: &direction}, SourceSHA256: strings.Repeat("5", 64), OwnerProof: operationProof{Scheme: p.Scheme, ValidationSHA256: p.ValidationSHA256, InputSHA256: &p.InputSHA256, MappingSHA256: p.MappingSHA256}, OwnerRequestSHA256: hash, ApplyWireBodySHA256: wireSHA, PreviewWireBodySHA256: &wireSHA, UploadExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), RequiredDivisionIDs: []string{"17"}}}
}
func importTestRequest(path, stage string, raw []byte) *http.Request {
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CPM-Operation-Grant", strings.Repeat("1", 32))
	r.Header.Set("X-CPM-Operation-Job", strings.Repeat("4", 32))
	r.Header.Set("X-CPM-Operation-Stage", stage)
	r.Header.Set("X-CPM-Operation-Service-Key", importTestConfig().OperationDeliveryKey)
	r.Header.Set("Idempotency-Key", importTestUUID)
	return r
}
func importTestAuth(t *testing.T, expected *http.Request, raw []byte, status int, response []byte, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Service-Key") != importTestConfig().OperationAuthKey || r.Header.Get("Authorization") != "" || r.URL.Path != "/internal/auth/operation-grants/"+strings.Repeat("1", 32)+"/authorize" {
			t.Error("owner did not authorize directly with its own credential")
		}
		var in map[string]any
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			t.Error("invalid authorize body")
		}
		sum := sha256.Sum256(raw)
		want := map[string]any{"contract_version": float64(1), "job_id": strings.Repeat("4", 32), "stage": expected.Header.Get("X-CPM-Operation-Stage"), "method": "POST", "path": expected.URL.EscapedPath(), "raw_query": "", "force_query": false, "wire_body_sha256": hex.EncodeToString(sum[:]), "native_command_key": importTestUUID}
		if !reflect.DeepEqual(in, want) {
			t.Error("authorize changed exact bytes/path/stage/native key")
		}
		w.WriteHeader(status)
		_, _ = w.Write(response)
	}))
}
func importTestMux(s *Server) http.Handler {
	m := http.NewServeMux()
	s.externalInternalRoutes(m)
	return m
}
func assertImportResponse(t *testing.T, w *httptest.ResponseRecorder, code int, name string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	if name != "" {
		var out ErrorDTO
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Status || out.Error != name {
			t.Fatal("wrong failure envelope", w.Body.String())
		}
	}
}
func assertImportReadOnly(t *testing.T, commands []bson.Raw) {
	t.Helper()
	for _, c := range commands {
		for _, name := range []string{"insert", "update", "delete", "findAndModify"} {
			if c.Lookup(name).Type != 0 {
				t.Fatal("readonly/rejected command mutated Mongo", name)
			}
		}
	}
}

func TestOperationGrantAuthorizesExactWireAndRetainsDistinctProfileAccount(t *testing.T) {
	_, raw, a := importTestJSON(t)
	r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
	response, _ := json.Marshal(a)
	var calls atomic.Int32
	auth := importTestAuth(t, r, raw, 200, response, &calls)
	defer auth.Close()
	c := importTestConfig()
	c.AuthURL = auth.URL
	s := &Server{Config: c, Dependencies: NewDependencies(c)}
	actor, err := s.identifyImport(r)
	if err != nil || !reflect.DeepEqual(actor, importTestActor()) || actor.OrganizationAdmin || calls.Load() != 1 {
		t.Fatal("live actor changed", actor, err)
	}
	restored, _ := io.ReadAll(r.Body)
	if !bytes.Equal(restored, raw) || importOperationContext(r).Authorization.Actor.ID != 1049 {
		t.Fatal("native wire/profile lost")
	}
}

func TestOperationGrantRejectsAmbiguousDeliveryBeforeAuthOrMongo(t *testing.T) {
	_, raw, _ := importTestJSON(t)
	cases := map[string]func(*http.Request){
		"bearer":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer browser-secret") },
		"legacy-key": func(r *http.Request) { r.Header.Set("X-Service-Key", importTestConfig().ServiceKey) },
		"wrong-credential": func(r *http.Request) {
			r.Header.Set("X-CPM-Operation-Service-Key", importTestConfig().OperationAuthKey)
		},
		"duplicate-key":   func(r *http.Request) { r.Header.Add("Idempotency-Key", importTestUUID) },
		"partial":         func(r *http.Request) { r.Header.Del("X-CPM-Operation-Job") },
		"scope":           func(r *http.Request) { r.Header.Set("X-Organization-ID", "12") },
		"unknown-carrier": func(r *http.Request) { r.Header.Set("X-CPM-Operation-Actor", "admin") },
		"prepare-carrier": func(r *http.Request) {
			r.Header.Set("X-CPM-Import-Prepare-Key", importTestConfig().OperationDeliveryKey)
		},
		"stage":      func(r *http.Request) { r.Header.Set("X-CPM-Operation-Stage", "receipt") },
		"bare-query": func(r *http.Request) { r.URL.ForceQuery = true },
		"query":      func(r *http.Request) { r.URL.RawQuery = "organization_id=11" },
		"wrong-body-key": func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), importTestUUID, "22222222-2222-4222-8222-222222222222", 1)))
		},
		"uppercase-key": func(r *http.Request) { r.Header.Set("Idempotency-Key", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA") },
		"encoded-path":  func(r *http.Request) { r.URL.RawPath = "/internal/tests/imports/json/%63ommit" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
			mutate(r)
			// Nil Store and unreachable Auth prove rejection precedes both dependencies.
			s := &Server{Config: importTestConfig(), Dependencies: NewDependencies(Config{AuthURL: "http://127.0.0.1:1"})}
			_, err := s.identifyImport(r)
			if err == nil {
				t.Fatal("invalid carrier accepted")
			}
			var api *APIError
			if !asImportError(err, &api) || api.Code == 503 {
				t.Fatal("request reached Auth", err)
			}
		})
	}
}

func asImportError(err error, out **APIError) bool {
	value, ok := err.(*APIError)
	*out = value
	return ok
}

func TestOperationGrantValidatesLiveBindingAndStrictAuthEnvelope(t *testing.T) {
	_, raw, original := importTestJSON(t)
	tests := map[string]func(map[string]any){
		"missing-null":    func(v map[string]any) { delete(v["binding"].(map[string]any), "owner_session_id") },
		"wrong-case":      func(v map[string]any) { v["Actor"] = v["actor"]; delete(v, "actor") },
		"unknown":         func(v map[string]any) { v["admin"] = true },
		"null-permission": func(v map[string]any) { v["actor"].(map[string]any)["permissions"] = nil },
		"null-edit": func(v map[string]any) {
			v["actor"].(map[string]any)["permissions"].(map[string]any)["tests"].(map[string]any)["edit"] = nil
		},
		"denied-rights": func(v map[string]any) {
			v["actor"].(map[string]any)["permissions"].(map[string]any)["tests"].(map[string]any)["edit"] = false
			v["division_permissions"].([]any)[0].(map[string]any)["permissions"] = v["actor"].(map[string]any)["permissions"]
		},
		"missing-membership":   func(v map[string]any) { v["actor"].(map[string]any)["division_ids"] = []int64{18} },
		"missing-context":      func(v map[string]any) { v["division_permissions"] = []any{} },
		"context-mismatch":     func(v map[string]any) { v["division_permissions"].([]any)[0].(map[string]any)["division_id"] = 18 },
		"duplicate-membership": func(v map[string]any) { v["actor"].(map[string]any)["division_ids"] = []int64{17, 17} },
		"expired":              func(v map[string]any) { v["expires_at"] = "2020-01-01T00:00:00Z" },
		"nonutc":               func(v map[string]any) { v["expires_at"] = "2030-01-01T00:00:00+03:00" },
		"receipt-cannot-apply": func(v map[string]any) { v["mode"] = "receipt" },
		"audience":             func(v map[string]any) { v["binding"].(map[string]any)["owner"] = "cards" },
		"job":                  func(v map[string]any) { v["binding"].(map[string]any)["job_id"] = strings.Repeat("9", 32) },
		"native-key": func(v map[string]any) {
			v["binding"].(map[string]any)["native_command_key"] = "22222222-2222-4222-8222-222222222222"
		},
		"wire": func(v map[string]any) {
			v["binding"].(map[string]any)["apply_wire_body_sha256"] = strings.Repeat("9", 64)
		},
		"revision-leading-zero":  func(v map[string]any) { v["binding"].(map[string]any)["upload_preview_revision"] = "03" },
		"target-irrelevant-null": func(v map[string]any) { v["binding"].(map[string]any)["target"].(map[string]any)["test_id"] = nil },
		"fake-session":           func(v map[string]any) { v["binding"].(map[string]any)["owner_session_id"] = "3" },
		"fake-target-revision":   func(v map[string]any) { v["binding"].(map[string]any)["target_revision"] = "1" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(original)
			var v map[string]any
			_ = json.Unmarshal(data, &v)
			change(v)
			data, _ = json.Marshal(v)
			r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
			var calls atomic.Int32
			auth := importTestAuth(t, r, raw, 200, data, &calls)
			defer auth.Close()
			c := importTestConfig()
			c.AuthURL = auth.URL
			s := &Server{Config: c, Dependencies: NewDependencies(c)}
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, r)
			if w.Code < 400 || calls.Load() != 1 {
				t.Fatal("invalid authorization reached native apply", w.Code)
			}
		})
	}
	for _, response := range []string{`{"status":true,"status":true}`, `{"status":true,"\u0073tatus":true}`, `{"status":true} {}`} {
		r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
		var calls atomic.Int32
		auth := importTestAuth(t, r, raw, 200, []byte(response), &calls)
		c := importTestConfig()
		c.AuthURL = auth.URL
		s := &Server{Config: c, Dependencies: NewDependencies(c)}
		_, err := s.identifyImport(r)
		assertRatingAPIError(t, err, 503, "authorization_unavailable")
		auth.Close()
	}
}

func TestOperationGrantPreservesAuthFailuresAndDoesNotLeakUnknownErrors(t *testing.T) {
	_, raw, _ := importTestJSON(t)
	for _, tc := range []struct {
		code int
		name string
		want int
	}{{401, "invalid_operation_credential", 401}, {403, "operation_access_denied", 403}, {404, "operation_not_found", 404}, {409, "operation_request_mismatch", 409}, {410, "operation_revoked", 410}, {410, "operation_expired", 410}, {500, "secret_from_auth", 503}, {403, "uncontracted_error", 503}} {
		r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
		response, _ := json.Marshal(ErrorDTO{false, tc.name})
		var calls atomic.Int32
		auth := importTestAuth(t, r, raw, tc.code, response, &calls)
		c := importTestConfig()
		c.AuthURL = auth.URL
		s := &Server{Config: c, Dependencies: NewDependencies(c)}
		w := httptest.NewRecorder()
		importTestMux(s).ServeHTTP(w, r)
		name := tc.name
		if tc.want == 503 {
			name = "authorization_unavailable"
		}
		assertImportResponse(t, w, tc.want, name)
		auth.Close()
	}
}

func TestOperationGrantMalformedAuthErrorsRemainUnavailable(t *testing.T) {
	_, raw, _ := importTestJSON(t)
	for _, response := range []string{`{"error":"operation_revoked"}`, `{"Status":false,"error":"operation_revoked"}`, `{"status":null,"error":"operation_revoked"}`, `{"status":false,"error":"operation_revoked","binding":{}}`} {
		r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
		var calls atomic.Int32
		auth := importTestAuth(t, r, raw, 410, []byte(response), &calls)
		c := importTestConfig()
		c.AuthURL = auth.URL
		s := &Server{Config: c, Dependencies: NewDependencies(c)}
		w := httptest.NewRecorder()
		importTestMux(s).ServeHTTP(w, r)
		assertImportResponse(t, w, 503, "authorization_unavailable")
		auth.Close()
	}
}

func TestOperationGrantJSONApplyAndHistoricalReceiptHaveExpectedOwnerWrites(t *testing.T) {
	in, raw, a := importTestJSON(t)
	id, hash, _ := jsonImportOperation(jsonImportRequest(in), importTestActor(), in)
	for _, stage := range []string{"apply", "replay", "receipt", "missing"} {
		t.Run(stage, func(t *testing.T) {
			core := importTestCore(t, 101, stage == "apply")
			defer core.Close()
			old := Document{ID: id, OrganizationID: 11, OwnerDivisionID: 17, DirectionID: 31, Revision: 99, Title: "Сохранённый", Archived: true, CreatedBy: 23, OperationHash: hash, Placements: []Placement{{DivisionID: 17}}}
			responses := []bson.D{ratingMongoBatch(CollectionName), {{Key: "ok", Value: 1}, {Key: "n", Value: 1}}, {{Key: "ok", Value: 1}, {Key: "n", Value: 1}}}
			path, carrier := "/internal/tests/imports/json/commit", "apply"
			authz := a
			if stage != "apply" {
				responses = []bson.D{ratingMongoBatch(CollectionName, old), ratingMongoBatch(CollectionName, old)}
			}
			if stage == "receipt" || stage == "missing" {
				path = "/internal/tests/imports/json/receipt"
				carrier = "receipt"
				authz.Mode = "receipt"
				authz.Binding.UploadExpiresAt = "2020-01-01T00:00:00Z"
			}
			if stage == "missing" {
				responses = []bson.D{ratingMongoBatch(CollectionName)}
			}
			commands := []bson.Raw{}
			s := jsonImportMongoServer(t, core.URL, responses, &commands)
			r := importTestRequest(path, carrier, raw)
			data, _ := json.Marshal(authz)
			var calls atomic.Int32
			auth := importTestAuth(t, r, raw, 200, data, &calls)
			defer auth.Close()
			s.Config = importTestConfig()
			s.Dependencies.AuthURL = auth.URL
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, r)
			if stage == "apply" {
				assertImportResponse(t, w, 201, "")
				writes := 0
				for _, c := range commands {
					if c.Lookup("insert").Type != 0 {
						writes++
					}
				}
				if writes != 2 {
					t.Fatal("expected one version and one test", writes)
				}
			} else {
				code := 200
				name := ""
				if stage == "missing" {
					code = 404
					name = "import_receipt_not_found"
				}
				assertImportResponse(t, w, code, name)
				assertImportReadOnly(t, commands)
			}
		})
	}
}

func TestOperationGrantRejectsForeignScopeOrChangedNativeProofBeforeWrite(t *testing.T) {
	_, raw, base := importTestJSON(t)
	for _, name := range []string{"organization", "division", "direction", "input", "request", "validation"} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(base)
			var a operationAuthorization
			_ = json.Unmarshal(encoded, &a)
			switch name {
			case "organization":
				a.Actor.OrganizationID = 12
			case "division":
				*a.Actor.DivisionID = 18
				a.Actor.DivisionIDs = []int64{18}
				a.DivisionPermissions[0].DivisionID = 18
				a.Binding.RequiredDivisionIDs = []string{"18"}
			case "direction":
				*a.Binding.Target.DirectionID = "32"
			case "input":
				*a.Binding.OwnerProof.InputSHA256 = strings.Repeat("9", 64)
			case "request":
				a.Binding.OwnerRequestSHA256 = strings.Repeat("9", 64)
			case "validation":
				a.Binding.OwnerProof.ValidationSHA256 = strings.Repeat("9", 64)
			}
			core := jsonImportCoreServer(t)
			defer core.Close()
			commands := []bson.Raw{}
			s := jsonImportMongoServer(t, core.URL, []bson.D{ratingMongoBatch(CollectionName)}, &commands)
			r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
			data, _ := json.Marshal(a)
			var calls atomic.Int32
			auth := importTestAuth(t, r, raw, 200, data, &calls)
			defer auth.Close()
			s.Config = importTestConfig()
			s.Dependencies.AuthURL = auth.URL
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, r)
			if w.Code != 409 {
				t.Fatal("modified scope/proof accepted", w.Code, w.Body.String())
			}
			assertImportReadOnly(t, commands)
		})
	}
}

func TestOperationGrantCannotEnterBearerOrUnrelatedInternalRoutes(t *testing.T) {
	_, raw, _ := importTestJSON(t)
	r := importTestRequest("/v1/tests/divisions/17/imports/json/commit", "apply", raw)
	r.Header.Set("Authorization", "Bearer browser")
	d := NewDependencies(Config{AuthURL: "http://127.0.0.1:1"})
	_, err := d.Identify(r)
	assertRatingAPIError(t, err, 409, "operation_request_mismatch")
	s := &Server{Config: importTestConfig()}
	m := http.NewServeMux()
	s.internalRoutes(m)
	r = importTestRequest("/internal/tests/recalculations/prepare", "apply", raw)
	r.Header.Set("X-Service-Key", s.Config.ServiceKey)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	assertImportResponse(t, w, 409, "operation_request_mismatch")
}

func TestOperationGrantForeignAccountCannotReadOriginalJSONReceipt(t *testing.T) {
	in, raw, a := importTestJSON(t)
	originalID, _, _ := jsonImportOperation(jsonImportRequest(in), importTestActor(), in)
	a.Actor.AccountID = 24
	a.Mode = "receipt"
	foreignID, _, _ := jsonImportOperation(jsonImportRequest(in), a.Actor.Actor, in)
	if originalID == foreignID {
		t.Fatal("native command identity is not account-bound")
	}
	core := jsonImportCoreServer(t)
	defer core.Close()
	commands := []bson.Raw{}
	s := jsonImportMongoServer(t, core.URL, []bson.D{ratingMongoBatch(CollectionName)}, &commands)
	r := importTestRequest("/internal/tests/imports/json/receipt", "receipt", raw)
	data, _ := json.Marshal(a)
	var calls atomic.Int32
	auth := importTestAuth(t, r, raw, 200, data, &calls)
	defer auth.Close()
	s.Config = importTestConfig()
	s.Dependencies.AuthURL = auth.URL
	w := httptest.NewRecorder()
	importTestMux(s).ServeHTTP(w, r)
	assertImportResponse(t, w, 404, "import_receipt_not_found")
	assertImportReadOnly(t, commands)
	for _, c := range commands {
		if c.Lookup("find").Type != 0 && c.Lookup("filter").Document().Lookup("_id").ObjectID() != foreignID {
			t.Fatal("foreign actor looked up original account receipt")
		}
	}
}

func TestOperationGrantLargeNativeIDsRemainExactAndAuthOutageDenies(t *testing.T) {
	_, raw, a := importTestJSON(t)
	a.Actor.AccountID = 9007199254740993
	a.Actor.ID = 9007199254741001
	r := importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
	data, _ := json.Marshal(a)
	var calls atomic.Int32
	auth := importTestAuth(t, r, raw, 200, data, &calls)
	c := importTestConfig()
	c.AuthURL = auth.URL
	s := &Server{Config: c, Dependencies: NewDependencies(c)}
	actor, err := s.identifyImport(r)
	if err != nil || actor.AccountID != 9007199254740993 || importOperationContext(r).Authorization.Actor.ID != 9007199254741001 {
		t.Fatal("native int64 IDs lost precision", err)
	}
	auth.Close()
	r = importTestRequest("/internal/tests/imports/json/commit", "apply", raw)
	_, err = s.identifyImport(r)
	assertRatingAPIError(t, err, 503, "authorization_unavailable")
}
