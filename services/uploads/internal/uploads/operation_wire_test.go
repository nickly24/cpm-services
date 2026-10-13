package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperationsConfigFailClosed(t *testing.T) {
	for _, name := range []string{"UPLOADS_OPERATIONS_ENABLED", "AUTH_OPERATIONS_ISSUER_KEY", "CORE_OPERATION_DELIVERY_KEY", "CARDS_OPERATION_DELIVERY_KEY", "TESTS_OPERATION_DELIVERY_KEY", "EXAMS_OPERATIONS_DELIVERY_KEY"} {
		t.Setenv(name, "")
	}
	c := Config{InternalKey: strings.Repeat("i", 32)}
	if err := loadOperationConfig(&c); err != nil || c.OperationsEnabled {
		t.Fatal("disabled config must remain disabled")
	}
	t.Setenv("UPLOADS_OPERATIONS_ENABLED", "1")
	if err := loadOperationConfig(&c); err == nil {
		t.Fatal("enabled incomplete credentials accepted")
	}
	t.Setenv("AUTH_OPERATIONS_ISSUER_KEY", strings.Repeat("a", 32))
	for owner, char := range map[string]string{"CORE": "c", "CARDS": "d", "TESTS": "t"} {
		t.Setenv(owner+"_OPERATION_DELIVERY_KEY", strings.Repeat(char, 32))
	}
	t.Setenv("EXAMS_OPERATIONS_DELIVERY_KEY", strings.Repeat("e", 32))
	if err := loadOperationConfig(&c); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORE_OPERATION_DELIVERY_KEY", c.OperationsIssuerKey)
	if err := loadOperationConfig(&c); err == nil {
		t.Fatal("issuer reused for native delivery")
	}
	t.Setenv("CORE_OPERATION_DELIVERY_KEY", c.InternalKey)
	if err := loadOperationConfig(&c); err == nil {
		t.Fatal("old shared key reused for delivery")
	}
}

func TestGrantIssuerExactWireAndSanitizedErrors(t *testing.T) {
	key := ownerCommand("grant", strings.Repeat("a", 32), "")
	body := []byte(`{"contract_version":1,"target":{"kind":"exam","exam_id":"9007199254740993"}}`)
	var requests int
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		actual, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/internal/auth/operation-grants" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("X-Service-Key") != strings.Repeat("a", 32) || r.Header.Get("Idempotency-Key") != key || !bytes.Equal(actual, body) {
			t.Error("issuer mutated command/identity")
		}
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"private-password-synthetic"}`))
	}))
	defer auth.Close()
	s := &Server{HTTP: newHTTP(), Config: Config{AuthURL: auth.URL, OperationsEnabled: true, OperationsIssuerKey: strings.Repeat("a", 32)}}
	_, err := s.requestOperationGrant(context.Background(), "Bearer synthetic", key, "/internal/auth/operation-grants", body)
	if err == nil || strings.Contains(err.Error(), "private-password") || requests != 1 {
		t.Fatal("unsafe error/request handling")
	}
	_, err = s.requestOperationGrant(context.Background(), "Bearer synthetic", key, "/internal/auth/operation-grants/../authorize", body)
	if err == nil || requests != 1 {
		t.Fatal("issuer accepted arbitrary Auth path")
	}
	_, err = s.requestOperationGrant(context.Background(), "", key, "/internal/auth/operation-grants", body)
	if err == nil || requests != 1 {
		t.Fatal("issuer accepted missing live bearer")
	}
}

func TestIssuedGrantCannotChangeFrozenScopeBytesOrExpiry(t *testing.T) {
	now := time.Date(2026, 10, 9, 19, 0, 0, 0, time.UTC)
	sc := Scope{1001, 10235, 3}
	b := operationBinding{ContractVersion: 1, Type: "cards.xlsx", Owner: "cards", JobID: strings.Repeat("a", 32), Target: json.RawMessage(`{"kind":"existing_theme","direction_id":"9","theme_id":"17"}`), UploadExpiresAt: now.Add(time.Hour), ApplyWireBodySHA256: digest([]byte(`{"expected_revision":2}`))}
	d := sc.DivisionID
	env := operationEnvelope{Status: true, ContractVersion: 1, GrantID: strings.Repeat("b", 32), Mode: "apply", ExpiresAt: now.Add(time.Minute), Binding: b, Actor: operationActor{ID: 3, Actor: Actor{AccountID: 3, OrganizationID: 1001, DivisionID: &d, Role: "staff_admin", Permissions: map[string]Permission{"upload": {true, true}, "train": {true, true}}}}}
	if err := verifyIssuedOperation(env, b, sc, "apply", now); err != nil {
		t.Fatal(err)
	}
	env.Actor.ID = 999 // Profile/entity ID is distinct from auth AccountID.
	if err := verifyIssuedOperation(env, b, sc, "apply", now); err != nil {
		t.Fatal("valid distinct profile/account IDs rejected", err)
	}
	for _, mutate := range []func(*operationEnvelope){
		func(e *operationEnvelope) { e.Actor.AccountID = 4 }, func(e *operationEnvelope) { e.Actor.OrganizationID = 1002 },
		func(e *operationEnvelope) { e.Actor.DivisionID = nil }, func(e *operationEnvelope) { e.Mode = "receipt" },
		func(e *operationEnvelope) { e.ExpiresAt = now.Add(2 * time.Hour) }, func(e *operationEnvelope) { e.ExpiresAt = now },
		func(e *operationEnvelope) { e.Binding.ApplyWireBodySHA256 = digest([]byte("{}")) },
		func(e *operationEnvelope) {
			e.Binding.Target = json.RawMessage(`{"kind":"existing_theme","direction_id":"9","theme_id":"18"}`)
		},
	} {
		changed := env
		mutate(&changed)
		if err := verifyIssuedOperation(changed, b, sc, "apply", now); err == nil {
			t.Fatal("Auth response changed immutable operation")
		}
	}
	// An actual admin flag and role are both required; an asserted role alone
	// must not replace the current explicit permission map.
	env.Actor.Role = "admin"
	env.Actor.OrganizationAdmin = false
	if err := verifyIssuedOperation(env, b, sc, "apply", now); err == nil {
		t.Fatal("synthetic admin accepted")
	}
}
