package cards

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func operationWireFixture(t *testing.T, stage string) (*http.Request, operationRequest, operationResponse) {
	t.Helper()
	r := operationTestRequest(stage)
	a, ss, _, _ := operationScopeFixture()
	ss.ID = strings.Repeat("b", 32)
	op, e := parseOperationRequest(r, operationTestConfig())
	if e != nil {
		t.Fatal(e)
	}
	proof := importProof(a, ss)
	revision := strconv.FormatInt(ss.Revision, 10)
	expires := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Microsecond)
	w := operationBindingWire{ContractVersion: 1, UploadID: strings.Repeat("1", 32), UploadSessionID: strings.Repeat("2", 32), JobID: op.JobID, CoordinatorCommandKey: "uploads_confirm_1234", Type: "cards.xlsx", Owner: "cards", NativeCommandKey: op.NativeKey, OwnerSessionID: &ss.ID, UploadPreviewRevision: "3", OwnerPreviewRevision: &revision, Target: proof.Target, SourceSHA256: proof.SourceSHA256,
		OwnerProof: operationProofWire{Scheme: proof.Scheme, ValidationSHA256: proof.SHA256}, OwnerRequestSHA256: proof.OwnerRequestSHA256, ApplyWireBodySHA256: op.BodyHash, UploadExpiresAt: expires, OwnerPreviewExpiresAt: &expires, RequiredDivisionIDs: []string{strconv.FormatInt(*a.DivisionID, 10)}}
	out := operationResponse{Status: true, ContractVersion: 1, GrantID: op.GrantID, Mode: "apply", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), Actor: operationActorWire{ID: 999, AccountID: a.AccountID, Role: a.Role, OrganizationID: a.OrganizationID, DivisionID: a.DivisionID, DivisionIDs: []int64{*a.DivisionID}, Permissions: a.Permissions}, DivisionPermissions: []operationDivisionWire{{*a.DivisionID, a.Permissions}}, Binding: w}
	return r, *op, out
}

func TestOperationWireRoundTripAndProfileAccountSeparation(t *testing.T) {
	_, op, out := operationWireFixture(t, "apply")
	raw, _ := json.Marshal(out)
	var decoded operationResponse
	if e := strictOperationJSON(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	a, b, e := decoded.ownerBinding(op)
	if e != nil || a.AccountID != 7 || b.AccountID != 7 || a.AccountID == decoded.Actor.ID {
		t.Fatal("profile ID replaced account", e)
	}
}

func TestOperationWireStrictJSON(t *testing.T) {
	_, _, out := operationWireFixture(t, "apply")
	raw, _ := json.Marshal(out)
	original := string(raw)
	var invalid operationResponse
	if strictOperationJSON(append(append([]byte{}, raw...), 0xff), &invalid) == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	for name, body := range map[string]string{
		"duplicate":         strings.Replace(original, `"status":true`, `"status":false,"status":true`, 1),
		"escaped duplicate": strings.Replace(original, `"status":true`, `"status":true,"\u0073tatus":true`, 1),
		"wrong case":        strings.Replace(original, `"account_id":7`, `"ACCOUNT_ID":7`, 1),
		"unknown nested":    strings.Replace(original, `"account_id":7`, `"account_id":7,"super_admin":true`, 1),
		"missing nullable":  strings.Replace(original, `"target_revision":null,`, "", 1),
		"numeric metadata":  strings.Replace(original, `"owner_preview_revision":"2"`, `"owner_preview_revision":2`, 1),
		"null permissions":  strings.Replace(original, `"permissions":{"train":{"view":true,"edit":true},"upload":{"view":true,"edit":true}}`, `"permissions":null`, 1),
		"trailing":          original + " {}",
		"irrelevant target": strings.Replace(original, `"kind":"existing_theme"`, `"kind":"existing_theme","name":""`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var got operationResponse
			if strictOperationJSON([]byte(body), &got) == nil {
				t.Fatal("malformed Auth envelope accepted")
			}
		})
	}
}

func TestOperationWireBindingGuards(t *testing.T) {
	changes := map[string]func(*operationResponse){
		"wrong owner":              func(o *operationResponse) { o.Binding.Owner = "tests" },
		"transform":                func(o *operationResponse) { o.Binding.Type = "cards.transform" },
		"session missing":          func(o *operationResponse) { o.Binding.OwnerSessionID = nil },
		"leading zero revision":    func(o *operationResponse) { v := "02"; o.Binding.OwnerPreviewRevision = &v },
		"foreign direction shape":  func(o *operationResponse) { o.Binding.Target.DirectionID = "013" },
		"request digest":           func(o *operationResponse) { o.Binding.OwnerRequestSHA256 = strings.Repeat("0", 64) },
		"wire digest":              func(o *operationResponse) { o.Binding.ApplyWireBodySHA256 = strings.Repeat("0", 64) },
		"foreign job":              func(o *operationResponse) { o.Binding.JobID = strings.Repeat("a", 32) },
		"wrong native key":         func(o *operationResponse) { o.Binding.NativeCommandKey = "11234567-89ab-cdef-0123-456789abcdef" },
		"no origin membership":     func(o *operationResponse) { o.Actor.DivisionIDs = []int64{99} },
		"missing current contexts": func(o *operationResponse) { o.DivisionPermissions = []operationDivisionWire{} },
		"upload revoked":           func(o *operationResponse) { o.Actor.Permissions["upload"] = Permission{View: true} },
		"train revoked":            func(o *operationResponse) { o.Actor.Permissions["train"] = Permission{View: true} },
		"foreign origin required":  func(o *operationResponse) { o.Binding.RequiredDivisionIDs = []string{"99"} },
		"receipt cannot apply":     func(o *operationResponse) { o.Mode = "receipt" },
		"expired":                  func(o *operationResponse) { o.ExpiresAt = time.Now().Add(-time.Minute) },
		"outlives preview":         func(o *operationResponse) { v := time.Now().Add(time.Minute); o.Binding.OwnerPreviewExpiresAt = &v },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			_, op, out := operationWireFixture(t, "apply")
			change(&out)
			if _, _, e := out.ownerBinding(op); e == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	_, op, out := operationWireFixture(t, "receipt")
	out.Mode = "receipt"
	old := time.Now().UTC().Add(-time.Hour)
	out.Binding.UploadExpiresAt = old
	out.Binding.OwnerPreviewExpiresAt = &old
	if _, _, e := out.ownerBinding(op); e != nil {
		t.Fatal("fresh receipt-only grant should permit historical source", e)
	}
}

func TestOperationNetworkDistinctKeysActualBytesAndBoundedCore(t *testing.T) {
	r, op, out := operationWireFixture(t, "apply")
	config := operationTestConfig()
	authCalls, coreCalls := 0, 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
		if r.Method != "POST" || r.URL.Path != "/internal/auth/operation-grants/"+op.GrantID+"/authorize" || r.Header.Get("X-Service-Key") != config.OperationAuthKey || r.Header.Get("Authorization") != "" || r.Header.Get(operationKeyHeader) != "" {
			t.Error("Auth transport identity leaked or changed")
		}
		raw, _ := io.ReadAll(r.Body)
		var request operationAuthorizeRequest
		if e := strictOperationJSON(raw, &request); e != nil || request.Path != "/v1/cards/sessions/"+strings.Repeat("b", 32)+"/commit" || request.WireBodySHA256 != op.BodyHash || request.NativeCommandKey != op.NativeKey || request.RawQuery != "" || request.ForceQuery {
			t.Error("actual request tuple lost", e)
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer auth.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coreCalls++
		if r.Method != "GET" || r.URL.Path != "/internal/core/import-directions/13" || r.URL.RawQuery != "" || r.Header.Get(operationKeyHeader) != config.OperationCoreKey || r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Key") != "" || r.Header.Get(operationStageHeader) != "core-direction" || r.Header.Get(operationGrantHeader) != op.GrantID || r.Header.Get(operationJobHeader) != op.JobID || r.Header.Get("Idempotency-Key") != op.NativeKey {
			t.Error("Core scope/credential widened")
		}
		json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"account_id": 7, "organization_id": 11, "division_id": 19, "job_id": op.JobID, "owner_session_id": strings.Repeat("b", 32), "direction_id": "13", "active": true}})
	}))
	defer core.Close()
	config.AuthURL, config.CoreURL = auth.URL, core.URL
	d := NewDependencies(config)
	a, scope, b, e := d.authorizeOperation(r, op)
	if e != nil || a.AccountID != 7 || b.Revision != 2 || scope.OrganizationID != 11 || len(scope.Directions) != 1 || !scope.Allows(13) || authCalls != 1 || coreCalls != 1 {
		t.Fatal("scoped authorization failed", e)
	}
}

func TestOperationHandlerRejectsBeforeAnyDependencyOrDatabase(t *testing.T) {
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.URL.ForceQuery = true }, func(r *http.Request) { r.URL.RawQuery = "%" },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer user") }, func(r *http.Request) { r.Header.Set(operationStageHeader, "core-direction") },
		func(r *http.Request) { r.Header.Set(operationKeyHeader, "bad") }, func(r *http.Request) { r.URL.Path = "/v1/cards/transforms" },
	} {
		calls := 0
		dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
		config := operationTestConfig()
		config.AuthURL, config.CoreURL, config.TestsURL = dependency.URL, dependency.URL, dependency.URL
		s := Server{Deps: NewDependencies(config)} // nil DB catches accidental entry.
		r := operationTestRequest("apply")
		change(r)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		dependency.Close()
		if w.Code < 400 || calls != 0 {
			t.Fatal("invalid operation reached dependency or handler", w.Code, calls)
		}
	}
}

func TestOperationAuthFailureNeverReachesCoreOrOwner(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 410, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.Contains(r.URL.Path, "/authorize") {
					t.Error("Core called after Auth failure")
				}
				w.WriteHeader(status)
				io.WriteString(w, `{"status":false,"error":"operation_revoked"}`)
			}))
			defer source.Close()
			config := operationTestConfig()
			config.AuthURL, config.CoreURL = source.URL, source.URL
			s := Server{Deps: NewDependencies(config)}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, operationTestRequest("apply"))
			if w.Code != status || calls != 1 {
				t.Fatal("authorization failed open", w.Code, calls)
			}
		})
	}
}

func TestOperationCoreFailureNeverReachesOwner(t *testing.T) {
	for _, failure := range []string{"denied", "unavailable", "inactive", "foreign account", "foreign org", "foreign division", "foreign job", "foreign session", "foreign direction", "extra field", "duplicate field", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			_, op, envelope := operationWireFixture(t, "apply")
			calls := 0
			dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/authorize") {
					json.NewEncoder(w).Encode(envelope)
					return
				}
				data := map[string]any{"account_id": 7, "organization_id": 11, "division_id": 19, "job_id": op.JobID, "owner_session_id": strings.Repeat("b", 32), "direction_id": "13", "active": true}
				switch failure {
				case "denied":
					w.WriteHeader(403)
					return
				case "unavailable":
					w.WriteHeader(503)
					return
				case "redirect":
					w.Header().Set("Location", "/unrelated")
					w.WriteHeader(302)
					return
				case "inactive":
					data["active"] = false
				case "foreign account":
					data["account_id"] = 8
				case "foreign org":
					data["organization_id"] = 12
				case "foreign division":
					data["division_id"] = 20
				case "foreign job":
					data["job_id"] = strings.Repeat("1", 32)
				case "foreign session":
					data["owner_session_id"] = strings.Repeat("f", 32)
				case "foreign direction":
					data["direction_id"] = "14"
				case "extra field":
					data["all_directions"] = true
				}
				raw, _ := json.Marshal(map[string]any{"status": true, "data": data})
				if failure == "duplicate field" {
					raw = []byte(strings.Replace(string(raw), `"active":true`, `"active":true,"active":true`, 1))
				}
				w.Write(raw)
			}))
			defer dependency.Close()
			config := operationTestConfig()
			config.AuthURL, config.CoreURL = dependency.URL, dependency.URL
			s := Server{Deps: NewDependencies(config)} // nil DB proves no owner query.
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, operationTestRequest("apply"))
			want := 503
			if failure == "denied" {
				want = 403
			}
			if w.Code != want || calls != 2 {
				t.Fatal("Core failed open or redirected", w.Code, calls)
			}
		})
	}
}
