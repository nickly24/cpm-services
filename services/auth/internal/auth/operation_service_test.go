package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryOperationStore struct {
	mu       sync.Mutex
	grants   map[string]OperationGrant
	issuance map[string]string
}

func cloneTestGrant(g OperationGrant) OperationGrant {
	raw, _ := json.Marshal(g.Binding)
	// Decode into an empty value: unmarshalling onto a shallow struct copy
	// reuses its map and pointers and would mutate other concurrent requests.
	var binding OperationBinding
	_ = json.Unmarshal(raw, &binding)
	g.Binding = binding
	return g
}
func (s *memoryOperationStore) Issue(_ context.Context, g OperationGrant) (OperationGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := operationScopeHash(g)
	if id := s.issuance[k]; id != "" {
		old := s.grants[id]
		if old.InputHash != g.InputHash {
			return old, opError(409, "operation_issuance_conflict")
		}
		return cloneTestGrant(old), nil
	}
	if !g.ExpiresAt.After(g.IssuedAt) {
		return g, opError(410, "operation_expired")
	}
	s.grants[g.ID] = cloneTestGrant(g)
	s.issuance[k] = g.ID
	return cloneTestGrant(g), nil
}
func (s *memoryOperationStore) Get(_ context.Context, id string) (OperationGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[id]
	if !ok {
		return g, opError(404, "operation_not_found")
	}
	return cloneTestGrant(g), nil
}
func (s *memoryOperationStore) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[id]
	if !ok {
		return opError(404, "operation_not_found")
	}
	g.Revoked = true
	s.grants[id] = g
	return nil
}

type operationTestAccounts struct {
	account *Account
	err     error
}

func (s *operationTestAccounts) LoadAccount(_ context.Context, id int64) (*Account, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.account == nil || s.account.UserID != id {
		return nil, ErrCredentials
	}
	return s.account, nil
}

func opString(s string) *string { return &s }
func operationFixture(t *testing.T) (*OperationService, *operationTestAccounts, UserDTO, OperationBinding) {
	t.Helper()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	permissions := map[string]Permission{"upload": {true, true}, "users": {true, true}, "train": {true, true}, "tests": {true, true}, "test-results": {true, true}, "exams": {true, true}}
	a := &Account{UserID: 23, EntityID: 73, Role: "staff_admin", AuthVersion: 1, Organizations: []Organization{{ID: 11, Divisions: []Division{{ID: 17, Permissions: permissions}}}}}
	source := &operationTestAccounts{account: a}
	d := int64(17)
	u, e := a.User(ContextDTO{11, &d})
	if e != nil {
		t.Fatal(e)
	}
	store := &memoryOperationStore{grants: map[string]OperationGrant{}, issuance: map[string]string{}}
	svc := &OperationService{Store: store, Accounts: source, TTL: 6 * time.Hour, Now: func() time.Time { return now }}
	hash := strings.Repeat("a", 64)
	deadline := now.Add(24 * time.Hour).Format(time.RFC3339)
	binding := OperationBinding{ContractVersion: 1, UploadID: strings.Repeat("1", 32), UploadSessionID: strings.Repeat("2", 32), JobID: strings.Repeat("3", 32), CoordinatorCommandKey: "original_http_key", Type: "users.legacy", Owner: "core", NativeCommandKey: "11111111-1111-4111-8111-111111111111", OwnerSessionID: opString("123"), UploadPreviewRevision: "3", OwnerPreviewRevision: opString("2"), Target: map[string]string{"kind": "division_users", "division_id": "17"}, SourceSHA256: hash, OwnerProof: OperationProof{Scheme: "core.legacy.snapshot.v1", ValidationSHA256: hash, InputSHA256: &hash}, OwnerRequestSHA256: hash, ApplyWireBodySHA256: operationDigest([]byte(`{"expected_revision":2}`)), UploadExpiresAt: deadline, OwnerPreviewExpiresAt: &deadline, RequiredDivisionIDs: []string{"17"}}
	return svc, source, u, binding
}

func operationRequest(b OperationBinding, stage string) OperationAuthorizeRequest {
	r := OperationAuthorizeRequest{ContractVersion: 1, JobID: b.JobID, Stage: stage, NativeCommandKey: b.NativeCommandKey}
	if stage == "apply" {
		r.Method = "POST"
		r.Path = "/v1/core/divisions/17/legacy-imports/123/commit"
		r.WireBodySHA256 = b.ApplyWireBodySHA256
	} else {
		r.Method = "GET"
		r.Path = "/v1/core/divisions/17/legacy-imports/123/receipts/" + b.NativeCommandKey
		r.WireBodySHA256 = operationDigest(nil)
	}
	return r
}
func expectOperationError(t *testing.T, e error, status int) {
	t.Helper()
	var oe *operationError
	if !errors.As(e, &oe) || oe.status != status {
		t.Fatalf("expected %d, got %v", status, e)
	}
}

func TestOperationIssuanceConcurrencyAndImmutableExpiry(t *testing.T) {
	svc, source, u, b := operationFixture(t)
	key := "22222222-2222-4222-8222-222222222222"
	var wg sync.WaitGroup
	responses := make(chan OperationResponse, 24)
	errs := make(chan error, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Issue(context.Background(), source.account, u, key, b)
			responses <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(responses)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id, expiry := "", ""
	for r := range responses {
		if id == "" {
			id, expiry = r.GrantID, r.ExpiresAt
		}
		if r.GrantID != id || r.ExpiresAt != expiry {
			t.Fatal("issuance generated another grant/expiry")
		}
		if r.Actor.ID != 73 || r.Actor.AccountID != 23 {
			t.Fatal("profile identity collapsed into account")
		}
	}
	if len(svc.Store.(*memoryOperationStore).grants) != 1 {
		t.Fatal("duplicate grant records")
	}
	changed := b
	changed.SourceSHA256 = strings.Repeat("b", 64)
	_, e := svc.Issue(context.Background(), source.account, u, key, changed)
	expectOperationError(t, e, 409)
	svc.Now = func() time.Time { return time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC) }
	r, e := svc.Issue(context.Background(), source.account, u, key, b)
	if e != nil || r.GrantID != id || r.ExpiresAt != expiry {
		t.Fatal("lost issue after source expiry cannot recover original identity")
	}
	_, e = svc.Issue(context.Background(), source.account, u, "33333333-3333-4333-8333-333333333333", b)
	expectOperationError(t, e, 410)
}

func TestOperationLiveAccessEpochAndReceiptOnlyRecovery(t *testing.T) {
	svc, source, u, b := operationFixture(t)
	ctx := context.Background()
	original, e := svc.Issue(ctx, source.account, u, "22222222-2222-4222-8222-222222222222", b)
	if e != nil {
		t.Fatal(e)
	}
	apply := operationRequest(b, "apply")
	if _, e = svc.Authorize(ctx, original.GrantID, "core", apply); e != nil {
		t.Fatal(e)
	}
	source.account.Organizations[0].Divisions[0].Permissions["users"] = Permission{true, false}
	_, e = svc.Authorize(ctx, original.GrantID, "core", apply)
	expectOperationError(t, e, 403)
	source.account.Organizations[0].Divisions[0].Permissions["users"] = Permission{true, true}
	source.account.AuthVersion++
	_, e = svc.Authorize(ctx, original.GrantID, "core", apply)
	expectOperationError(t, e, 410)
	svc.Now = func() time.Time { return time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC) }
	recovery, e := svc.Recover(ctx, original.GrantID, "44444444-4444-4444-8444-444444444444", source.account, u)
	if e != nil {
		t.Fatal(e)
	}
	if recovery.Mode != "receipt" || recovery.Binding.NativeCommandKey != b.NativeCommandKey || recovery.Binding.ApplyWireBodySHA256 != b.ApplyWireBodySHA256 {
		t.Fatal("recovery changed original command")
	}
	_, e = svc.Authorize(ctx, recovery.GrantID, "core", apply)
	expectOperationError(t, e, 403)
	if _, e = svc.Authorize(ctx, recovery.GrantID, "core", operationRequest(b, "receipt")); e != nil {
		t.Fatal(e)
	}
	if e = svc.Revoke(ctx, recovery.GrantID, source.account, u); e != nil {
		t.Fatal(e)
	}
	if e = svc.Revoke(ctx, recovery.GrantID, source.account, u); e != nil {
		t.Fatal(e)
	}
	_, e = svc.Authorize(ctx, recovery.GrantID, "core", operationRequest(b, "receipt"))
	expectOperationError(t, e, 410)
	source.err = errors.New("database failure")
	_, e = svc.Authorize(ctx, original.GrantID, "core", apply)
	expectOperationError(t, e, 410)
	source.err = nil
	other := u
	other.AccountID = 24
	_, e = svc.Recover(ctx, original.GrantID, "55555555-5555-4555-8555-555555555555", source.account, other)
	expectOperationError(t, e, 404)
}

func TestOperationAuthorizeBindsActualRequestAndFailsClosed(t *testing.T) {
	svc, source, u, b := operationFixture(t)
	ctx := context.Background()
	g, e := svc.Issue(ctx, source.account, u, "22222222-2222-4222-8222-222222222222", b)
	if e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*OperationAuthorizeRequest){"job": func(r *OperationAuthorizeRequest) { r.JobID = strings.Repeat("0", 32) }, "key": func(r *OperationAuthorizeRequest) { r.NativeCommandKey = "33333333-3333-4333-8333-333333333333" }, "scope query": func(r *OperationAuthorizeRequest) { r.RawQuery = "organization_id=12" }, "bare query": func(r *OperationAuthorizeRequest) { r.ForceQuery = true }, "encoded path": func(r *OperationAuthorizeRequest) { r.Path = strings.Replace(r.Path, "123", "%31%32%33", 1) }, "different method": func(r *OperationAuthorizeRequest) { r.Method = "GET" }, "LF bytes": func(r *OperationAuthorizeRequest) {
		r.WireBodySHA256 = operationDigest([]byte("{\"expected_revision\":2}\n"))
	}, "stage": func(r *OperationAuthorizeRequest) { r.Stage = "anything" }} {
		t.Run(name, func(t *testing.T) {
			r := operationRequest(b, "apply")
			change(&r)
			_, e := svc.Authorize(ctx, g.GrantID, "core", r)
			expectOperationError(t, e, 409)
		})
	}
	_, e = svc.Authorize(ctx, g.GrantID, "tests", operationRequest(b, "apply"))
	expectOperationError(t, e, 404)
	source.err = errors.New("offline")
	_, e = svc.Authorize(ctx, g.GrantID, "core", operationRequest(b, "apply"))
	expectOperationError(t, e, 503)
	source.err = ErrCredentials
	_, e = svc.Authorize(ctx, g.GrantID, "core", operationRequest(b, "apply"))
	expectOperationError(t, e, 403)
}

func TestOperationBindingRejectsInventedNativeMetadata(t *testing.T) {
	_, _, _, b := operationFixture(t)
	for name, change := range map[string]func(*OperationBinding){"leading zero": func(b *OperationBinding) { b.OwnerSessionID = opString("0123") }, "overflow": func(b *OperationBinding) { b.OwnerPreviewRevision = opString("9223372036854775808") }, "extra target": func(b *OperationBinding) { b.Target["organization_id"] = "12" }, "foreign division": func(b *OperationBinding) { b.Target["division_id"] = "18" }, "scope set": func(b *OperationBinding) { b.RequiredDivisionIDs = []string{"17", "18"} }, "unknown type": func(b *OperationBinding) { b.Type = "cards.transform" }, "scheme": func(b *OperationBinding) { b.OwnerProof.Scheme = "wrapper.v1" }, "upper hash": func(b *OperationBinding) { b.SourceSHA256 = strings.Repeat("A", 64) }, "credentials revision": func(b *OperationBinding) {
		b.Type = "users.credentials"
		b.OwnerProof.Scheme = "core.credentials.snapshot.v1"
	}, "stateless fake session": func(b *OperationBinding) {
		b.Type = "tests.json"
		b.Owner = "tests"
		b.OwnerProof.Scheme = "tests.json.v1"
		b.Target = map[string]string{"kind": "create_tests", "direction_id": "31"}
		b.PreviewWireBodySHA256 = opString(strings.Repeat("a", 64))
	}} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(b)
			var value OperationBinding
			_ = json.Unmarshal(raw, &value)
			change(&value)
			expectOperationError(t, value.validate(17), 400)
		})
	}
}

func TestOperationHTTPSeparatesCredentialsAndRejectsAliases(t *testing.T) {
	svc, source, u, b := operationFixture(t)
	g, e := svc.Issue(context.Background(), source.account, u, "22222222-2222-4222-8222-222222222222", b)
	if e != nil {
		t.Fatal(e)
	}
	keys := map[string]string{}
	for i, owner := range []string{"core", "cards", "tests", "exams"} {
		keys[owner] = strings.Repeat(strconvTest(i+1), 64)
	}
	cfg := OperationConfig{Enabled: true, IssuerKey: strings.Repeat("i", 64), OwnerKeys: keys, TTL: 6 * time.Hour}
	handler := (&Server{Config: Config{InternalKey: strings.Repeat("x", 64), Operations: cfg}, Operations: svc}).Handler()
	raw, _ := json.Marshal(operationRequest(b, "apply"))
	for _, tc := range []struct {
		name, key, body string
		headers         func(*http.Request)
		status          int
	}{{"allowed", keys["core"], string(raw), nil, 200}, {"issuer cannot authorize", cfg.IssuerKey, string(raw), nil, 401}, {"foreign owner", keys["tests"], string(raw), nil, 404}, {"mixed bearer", keys["core"], string(raw), func(r *http.Request) { r.Header.Set("Authorization", "Bearer forbidden") }, 401}, {"duplicate credential", keys["core"], string(raw), func(r *http.Request) { r.Header.Add("X-Service-Key", keys["core"]) }, 401}, {"native carrier", keys["core"], string(raw), func(r *http.Request) { r.Header.Set("X-CPM-Operation-Grant", g.GrantID) }, 401}, {"wrong-case JSON", keys["core"], strings.Replace(string(raw), `"job_id"`, `"Job_ID"`, 1), nil, 400}, {"escaped duplicate", keys["core"], strings.Replace(string(raw), `"contract_version":1`, `"contract_version":1,"contract_\u0076ersion":1`, 1), nil, 400}, {"force missing", keys["core"], strings.Replace(string(raw), `,"force_query":false`, "", 1), nil, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/internal/auth/operation-grants/"+g.GrantID+"/authorize", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Service-Key", tc.key)
			if tc.headers != nil {
				tc.headers(r)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status%d expected%d: %s", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing private response headers")
			}
			if strings.Contains(w.Body.String(), "forbidden") || strings.Contains(w.Body.String(), cfg.IssuerKey) {
				t.Fatal("credential leak")
			}
		})
	}
	for _, suffix := range []string{"?", "?x=%zz", "?job=other"} {
		r := httptest.NewRequest("POST", "/internal/auth/operation-grants/"+g.GrantID+"/authorize"+suffix, strings.NewReader(string(raw)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("ambiguous actual Auth URL accepted")
		}
	}
}
func strconvTest(n int) string { return fmt.Sprintf("%d", n) }
