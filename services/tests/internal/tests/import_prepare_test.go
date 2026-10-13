package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func importTestCore(t *testing.T, profile int64, active bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			OrganizationID  int64   `json:"organization_id"`
			DivisionIDs     []int64 `json:"division_ids"`
			DirectionID     *int64  `json:"direction_id"`
			IncludeStudents bool    `json:"include_students"`
		}
		if r.URL.Path != "/internal/core/test-references" || json.NewDecoder(r.Body).Decode(&input) != nil || input.OrganizationID != 11 || len(input.DivisionIDs) != 1 || input.DivisionIDs[0] != 17 {
			t.Error("unexpected Core scope")
			w.WriteHeader(404)
			return
		}
		var direction *Reference
		if input.DirectionID != nil {
			direction = &Reference{ID: *input.DirectionID, Name: "Математика", Active: active}
		}
		reply(w, 200, Envelope{true, map[string]any{"organization": Reference{ID: 11, Active: true}, "divisions": []Reference{{ID: 17, Active: true}}, "direction": direction, "accounts_complete": true, "account_projection_version": 1, "accounts": []any{map[string]any{"account_id": 71, "profile_id": profile, "full_name": "Примеров Ученик", "is_active": true}}}})
	}))
}

func importTestExternal(t *testing.T) (ExternalImportCommitDTO, ExternalTestDocument, []byte, operationAuthorization) {
	t.Helper()
	account, profile, zero := int64(71), int64(101), 0.0
	doc := ExternalTestDocument{ID: bson.NewObjectID(), OrganizationID: 11, DivisionID: 17, DirectionID: 31, Revision: 2, Name: "Внешний", Date: "2026-10-09", Results: []ExternalResult{}}
	in := ExternalImportCommitDTO{ExternalImportValidateDTO: ExternalImportValidateDTO{OrganizationID: 11, DivisionID: 17, TestID: doc.ID.Hex(), ExpectedTestRevision: 2, Rows: []ExternalImportRowDTO{{RowID: "r2", Row: 2, FullName: "Примеров Ученик", AccountID: &account, LegacyStudentID: &profile, Percent: &zero}}}, CommandID: importTestUUID, UploadID: strings.Repeat("2", 32), UploadSessionID: strings.Repeat("3", 32), UploadPreviewRevision: 3}
	p := externalImportBuildPreview(doc, in.Rows, []ExternalPerson{{account, profile, "Примеров Ученик", true}})
	if !p.CanCommit {
		t.Fatal("invalid external fixture", p)
	}
	in.PreviewHash = p.PreviewHash
	_, _, hash, err := externalImportCommitRequest(in, 23)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(in)
	a := importTestAuthorization(raw, externalImportOwnerProof(in.ExternalImportValidateDTO, p), hash)
	target, revision := doc.ID.Hex(), "2"
	a.Binding.Type = "tests.external-results"
	a.Binding.Target = operationTarget{Kind: "external_test", TestID: &target}
	a.Binding.TargetRevision = &revision
	return in, doc, raw, a
}

func importTestPrepareAuth(t *testing.T, actor Actor, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/auth/introspect" || r.Header.Get("Authorization") != "Bearer current-user" || r.Header.Get("X-Service-Key") != "" {
			t.Error("prepare did not use current bearer only")
		}
		reply(w, 200, map[string]any{"status": true, "active": true, "user": actor})
	}))
}
func importTestPrepareRequest(path string, raw []byte) *http.Request {
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer current-user")
	r.Header.Set("X-CPM-Import-Prepare-Key", importTestConfig().OperationDeliveryKey)
	r.Header.Set("Idempotency-Key", importTestUUID)
	return r
}

func TestImportPrepareReturnsNativeQuoteWithNoOwnerWrites(t *testing.T) {
	for _, kind := range []string{"json", "external-results"} {
		t.Run(kind, func(t *testing.T) {
			core := importTestCore(t, 101, true)
			defer core.Close()
			_, raw, expected := importTestJSON(t)
			path := "/internal/tests/imports/json/prepare"
			responses := []bson.D{}
			if kind == "external-results" {
				_, doc, externalRaw, a := importTestExternal(t)
				raw, expected = externalRaw, a
				path = "/internal/tests/external-results/prepare"
				responses = []bson.D{externalRetryCursor(t, doc)}
			}
			commands := []bson.Raw{}
			s := jsonImportMongoServer(t, core.URL, responses, &commands)
			var calls atomic.Int32
			auth := importTestPrepareAuth(t, importTestActor(), &calls)
			defer auth.Close()
			s.Config = importTestConfig()
			s.Dependencies.AuthURL = auth.URL
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, importTestPrepareRequest(path, raw))
			assertImportResponse(t, w, 200, "")
			var out struct {
				Status bool                  `json:"status"`
				Data   ImportPreparedCommand `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Status || out.Data.NativeCommandKey != importTestUUID || out.Data.OwnerRequestSHA256 != expected.Binding.OwnerRequestSHA256 || out.Data.OwnerProof == nil || out.Data.OwnerProof.InputSHA256 != *expected.Binding.OwnerProof.InputSHA256 || out.Data.OwnerProof.ValidationSHA256 != expected.Binding.OwnerProof.ValidationSHA256 || out.Data.OwnerProof.SchemaVersion != 1 || calls.Load() != 1 {
				t.Fatal("wrong native quote", w.Body.String())
			}
			assertImportReadOnly(t, commands)
		})
	}
}

func TestImportPrepareRejectsCredentialOrImmutableCommandConflicts(t *testing.T) {
	_, raw, _ := importTestJSON(t)
	for _, name := range []string{"grant", "legacy", "wrong-key", "duplicate-bearer", "duplicate-key", "query", "command", "nested-case", "scope", "no-bearer"} {
		t.Run(name, func(t *testing.T) {
			r := importTestPrepareRequest("/internal/tests/imports/json/prepare", raw)
			switch name {
			case "grant":
				r.Header.Set("X-CPM-Operation-Grant", strings.Repeat("1", 32))
			case "legacy":
				r.Header.Set("X-Service-Key", importTestConfig().ServiceKey)
			case "wrong-key":
				r.Header.Set("X-CPM-Import-Prepare-Key", importTestConfig().OperationAuthKey)
			case "duplicate-bearer":
				r.Header.Add("Authorization", "Bearer second-user")
			case "duplicate-key":
				r.Header.Add("Idempotency-Key", importTestUUID)
			case "query":
				r.URL.ForceQuery = true
			case "command":
				r.Header.Set("Idempotency-Key", "22222222-2222-4222-8222-222222222222")
			case "nested-case":
				r.Body = http.NoBody
			case "scope":
				r.Header.Set("X-Division-ID", "18")
			case "no-bearer":
				r.Header.Del("Authorization")
			}
			var calls atomic.Int32
			auth := importTestPrepareAuth(t, importTestActor(), &calls)
			defer auth.Close()
			c := importTestConfig()
			c.AuthURL = auth.URL
			s := &Server{Config: c, Dependencies: NewDependencies(c)}
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatal("invalid preparation accepted", w.Body.String())
			}
		})
	}
}

func TestImportPrepareRejectsStalePreviewTargetMappingAndCurrentRights(t *testing.T) {
	for _, name := range []string{"json-preview", "json-direction", "external-revision", "external-mapping", "denied-rights"} {
		t.Run(name, func(t *testing.T) {
			profile := int64(101)
			active := name != "json-direction"
			if name == "external-mapping" {
				profile = 102
			}
			core := importTestCore(t, profile, active)
			defer core.Close()
			_, raw, _ := importTestJSON(t)
			path := "/internal/tests/imports/json/prepare"
			responses := []bson.D{}
			if name == "json-preview" {
				var in JSONImportCommitDTO
				_ = json.Unmarshal(raw, &in)
				in.PreviewHash = strings.Repeat("9", 64)
				raw, _ = json.Marshal(in)
			}
			if strings.HasPrefix(name, "external-") {
				_, doc, data, _ := importTestExternal(t)
				raw = data
				path = "/internal/tests/external-results/prepare"
				if name == "external-revision" {
					doc.Revision++
				}
				responses = []bson.D{externalRetryCursor(t, doc)}
			}
			actor := importTestActor()
			if name == "denied-rights" {
				actor.Permissions["upload"] = Permission{true, false}
			}
			commands := []bson.Raw{}
			s := jsonImportMongoServer(t, core.URL, responses, &commands)
			var calls atomic.Int32
			auth := importTestPrepareAuth(t, actor, &calls)
			defer auth.Close()
			s.Config = importTestConfig()
			s.Dependencies.AuthURL = auth.URL
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, importTestPrepareRequest(path, raw))
			if w.Code < 400 {
				t.Fatal("stale preparation accepted")
			}
			assertImportReadOnly(t, commands)
		})
	}
}

func TestOperationGrantExternalApplyPreviewAndRecoveryReceipt(t *testing.T) {
	for _, stage := range []string{"preview", "apply", "receipt", "missing", "mapping-changed", "revision-changed", "upload-changed", "canonical-deny"} {
		t.Run(stage, func(t *testing.T) {
			in, doc, raw, a := importTestExternal(t)
			profile := int64(101)
			if stage == "mapping-changed" {
				profile = 102
			}
			core := importTestCore(t, profile, true)
			defer core.Close()
			path, carrier := "/internal/tests/external-results/commit", "apply"
			if stage == "preview" {
				raw, _ = json.Marshal(in.ExternalImportValidateDTO)
				path = "/internal/tests/external-results/validate"
				carrier = "preview"
				sum := sha256.Sum256(raw)
				sha := hex.EncodeToString(sum[:])
				a.Binding.PreviewWireBodySHA256 = &sha
			}
			if stage == "revision-changed" {
				doc.Revision++
			}
			if stage == "upload-changed" {
				a.Binding.UploadID = strings.Repeat("9", 32)
			}
			if stage == "canonical-deny" {
				a.Actor.Permissions["test-results"] = Permission{true, false}
				a.Actor.Permissions["test_results"] = Permission{true, true}
			}
			if stage == "receipt" || stage == "missing" {
				path = "/internal/tests/external-results/receipt"
				carrier = "receipt"
				a.Mode = "receipt"
				doc.Deleted = true
				doc.Revision = 99
				a.Binding.UploadExpiresAt = "2020-01-01T00:00:00Z"
				if stage == "receipt" {
					doc.Commands = []ExternalCommandReceipt{{CommandID: in.CommandID, InputHash: a.Binding.OwnerRequestSHA256, ActorID: 23, SourceRevision: 3}}
				}
			}
			responses := []bson.D{externalRetryCursor(t, doc), {{Key: "ok", Value: 1}, {Key: "n", Value: 1}}, {{Key: "ok", Value: 1}, {Key: "n", Value: 1}, {Key: "nModified", Value: 1}}}
			commands := []bson.Raw{}
			s := jsonImportMongoServer(t, core.URL, responses, &commands)
			r := importTestRequest(path, carrier, raw)
			data, _ := json.Marshal(a)
			var calls atomic.Int32
			auth := importTestAuth(t, r, raw, 200, data, &calls)
			defer auth.Close()
			s.Config = importTestConfig()
			s.Dependencies.AuthURL = auth.URL
			w := httptest.NewRecorder()
			importTestMux(s).ServeHTTP(w, r)
			switch stage {
			case "apply":
				assertImportResponse(t, w, 201, "")
				inserts, updates := 0, 0
				for _, c := range commands {
					if c.Lookup("insert").Type != 0 {
						inserts++
					}
					if c.Lookup("update").Type != 0 {
						updates++
						fields := c.Lookup("updates").Array().Index(0).Document().Lookup("u").Document().Lookup("$set").Document()
						result := fields.Lookup("results").Array().Index(0).Document()
						if result.Lookup("account_id").Int64() != 71 || result.Lookup("student_id").Int64() != 101 || result.Lookup("score").Double() != 0 {
							t.Fatal("result/account/profile/zero score not preserved")
						}
					}
				}
				if inserts != 1 || updates != 1 {
					t.Fatal("expected one audit and one CAS", inserts, updates)
				}
			case "preview", "receipt":
				assertImportResponse(t, w, 200, "")
				assertImportReadOnly(t, commands)
			case "missing":
				assertImportResponse(t, w, 404, "import_receipt_not_found")
				assertImportReadOnly(t, commands)
			default:
				if w.Code < 400 {
					t.Fatal("invalid external grant accepted", stage)
				}
				assertImportReadOnly(t, commands)
			}
		})
	}
}

func TestOperationCredentialConfigDisabledOrFullyDistinct(t *testing.T) {
	for name, value := range map[string]string{"MONGO_HOST": "localhost", "MONGO_PORT": "27017", "MONGO_DATABASE": "unit", "MONGO_USER": "unit", "MONGO_PASSWORD": "unit", "TESTS_AUTH_URL": "http://auth:8002", "TESTS_CORE_URL": "http://core:8003", "TESTS_CALCULATE_URL": "http://calculate:8005", "AUTH_INTERNAL_KEY": strings.Repeat("k", 32), "RABBITMQ_URL": "amqp://localhost/", "TESTS_OPERATION_DELIVERY_KEY": "", "AUTH_OPERATIONS_TESTS_KEY": ""} {
		t.Setenv(name, value)
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatal("absent feature must retain bearer flow", err)
	}
	for _, tc := range []struct {
		delivery, owner string
		valid           bool
	}{{strings.Repeat("d", 32), "", false}, {"", strings.Repeat("a", 32), false}, {strings.Repeat("d", 32), strings.Repeat("d", 32), false}, {strings.Repeat("k", 32), strings.Repeat("a", 32), false}, {strings.Repeat("d", 32), strings.Repeat("k", 32), false}, {"short", strings.Repeat("a", 32), false}, {strings.Repeat("d", 32), strings.Repeat("a", 32), true}} {
		t.Setenv("TESTS_OPERATION_DELIVERY_KEY", tc.delivery)
		t.Setenv("AUTH_OPERATIONS_TESTS_KEY", tc.owner)
		_, err := LoadConfig()
		if (err == nil) != tc.valid {
			t.Fatal("partial/equal credential config accepted")
		}
	}
}

func TestImportOpenAPIDocumentsPrepareSeparatelyFromGrantStages(t *testing.T) {
	api := openAPI()
	paths := api["paths"].(map[string]any)
	for _, prefix := range []string{"/internal/tests/imports/json", "/internal/tests/external-results"} {
		prepare := paths[prefix+"/prepare"].(map[string]any)["post"].(map[string]any)
		security := prepare["security"].([]any)[0].(map[string]any)
		if len(security) != 3 || security["bearerAuth"] == nil || security["importPrepareKey"] == nil || security["nativeCommandKey"] == nil {
			t.Fatal("prepare credentials mixed with grant lane")
		}
		for _, stage := range []string{"validate", "commit", "receipt"} {
			op := paths[prefix+"/"+stage].(map[string]any)["post"].(map[string]any)
			if len(op["security"].([]any)) != 2 {
				t.Fatal("legacy/grant alternative missing")
			}
		}
	}
}
