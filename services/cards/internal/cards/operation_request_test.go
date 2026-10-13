package cards

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func operationTestConfig() Config {
	return Config{Key: strings.Repeat("n", 32), OperationAuthKey: strings.Repeat("a", 32), OperationDeliveryKey: strings.Repeat("d", 32), OperationCoreKey: strings.Repeat("c", 32)}
}

func operationTestRequest(stage string) *http.Request {
	id := strings.Repeat("b", 32)
	key := "01234567-89ab-cdef-0123-456789abcdef"
	method, path, body := "GET", "/v1/cards/sessions/"+id, ""
	if stage == "apply" {
		method, path, body = "POST", path+"/commit", "{\"expected_revision\":2}\n"
	}
	if stage == "receipt" {
		path = "/v1/cards/receipts/" + key
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(operationGrantHeader, strings.Repeat("e", 32))
	r.Header.Set(operationStageHeader, stage)
	r.Header.Set(operationJobHeader, strings.Repeat("f", 32))
	r.Header.Set(operationKeyHeader, operationTestConfig().OperationDeliveryKey)
	r.Header.Set("Idempotency-Key", key)
	if stage == "apply" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestOperationRevisionRejectsDuplicateAndUnboundDTO(t *testing.T) {
	for _, body := range []string{
		`{"expected_revision":2,"expected_revision":2}`, `{"expected_revision":2,"organization_id":1}`,
		`{"expected_revision":null}`, `{"expected_revision":"2"}`, `{"expected_revision":0}`,
		`{"expected_revision":2.0}`, `{"expected_revision":9223372036854775808}`, `{}`, `null`,
		`{"expected_revision":2} {}`, `{"expected_revision":2,}`,
	} {
		if _, err := operationRevision([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, body := range []string{`{"expected_revision":2}`, "  { \"expected_revision\" : 2 }\n"} {
		if got, err := operationRevision([]byte(body)); err != nil || got != 2 {
			t.Fatal("valid exact bytes rejected", err)
		}
	}
}

func TestOperationRequestPinsActualBytesAndRestoresStream(t *testing.T) {
	for _, stage := range []string{"preview", "receipt", "apply"} {
		r := operationTestRequest(stage)
		op, err := parseOperationRequest(r, operationTestConfig())
		if err != nil || op == nil {
			t.Fatal(stage, err)
		}
		raw, _ := io.ReadAll(r.Body)
		digest := sha256.Sum256(raw)
		if string(raw) != string(op.Body) || op.BodyHash != hex.EncodeToString(digest[:]) {
			t.Fatal("wire bytes not preserved")
		}
		if stage == "apply" && !strings.HasSuffix(string(raw), "\n") {
			t.Fatal("encoder newline was lost")
		}
	}
	plain := httptest.NewRequest("GET", "/v1/cards/tree", nil)
	plain.Header.Set("Authorization", "Bearer ordinary")
	if op, err := parseOperationRequest(plain, Config{}); err != nil || op != nil {
		t.Fatal("native bearer affected")
	}
}

func TestOperationEnvelopeRejectsAmbiguityAndScopeWidening(t *testing.T) {
	cases := map[string]func(*http.Request){
		"mixed bearer":         func(r *http.Request) { r.Header.Set("Authorization", "Bearer user") },
		"scope selector":       func(r *http.Request) { r.Header.Set("X-Organization-ID", "99") },
		"shared service key":   func(r *http.Request) { r.Header.Set("X-Service-Key", operationTestConfig().Key) },
		"wrong delivery key":   func(r *http.Request) { r.Header.Set(operationKeyHeader, operationTestConfig().OperationAuthKey) },
		"missing grant":        func(r *http.Request) { r.Header.Del(operationGrantHeader) },
		"duplicate grant":      func(r *http.Request) { r.Header.Add(operationGrantHeader, strings.Repeat("e", 32)) },
		"duplicate native key": func(r *http.Request) { r.Header.Add("Idempotency-Key", r.Header.Get("Idempotency-Key")) },
		"empty stage":          func(r *http.Request) { r.Header.Set(operationStageHeader, "") },
		"foreign stage":        func(r *http.Request) { r.Header.Set(operationStageHeader, "core-direction") },
		"uppercase job":        func(r *http.Request) { r.Header.Set(operationJobHeader, strings.Repeat("A", 32)) },
		"invalid command":      func(r *http.Request) { r.Header.Set("Idempotency-Key", "bad/key") },
		"old carrier":          func(r *http.Request) { r.Header.Set("X-Operation-Grant", "old") },
		"unknown carrier":      func(r *http.Request) { r.Header.Set("X-CPM-Operation-Actor", "1") },
		"query":                func(r *http.Request) { r.URL.RawQuery = "organization_id=2" },
		"bare query":           func(r *http.Request) { r.URL.ForceQuery = true },
		"malformed query":      func(r *http.Request) { r.URL.RawQuery = "%" },
		"encoded path":         func(r *http.Request) { r.URL.RawPath = "/v1/cards/sessions/%62" + strings.Repeat("b", 31) },
		"sibling source":       func(r *http.Request) { r.URL.Path += "/source" },
		"transform route":      func(r *http.Request) { r.URL.Path = "/v1/cards/transforms"; r.Method = "POST" },
		"wrong method":         func(r *http.Request) { r.Method = "DELETE" },
		"get body":             func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := operationTestRequest("preview")
			change(r)
			if _, err := parseOperationRequest(r, operationTestConfig()); err == nil {
				t.Fatal("accepted invalid carrier")
			}
		})
	}
	r := operationTestRequest("receipt")
	r.URL.Path += "different"
	if _, err := parseOperationRequest(r, operationTestConfig()); err == nil {
		t.Fatal("foreign receipt path accepted")
	}
	r = operationTestRequest("apply")
	r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", (16<<10)+1)))
	if _, err := parseOperationRequest(r, operationTestConfig()); err == nil {
		t.Fatal("oversized operation accepted")
	}
}

func TestOperationCredentialsAllOrNoneAndDistinct(t *testing.T) {
	if err := (Config{}).validateOperationKeys(); err != nil {
		t.Fatal("disabled feature must retain native config")
	}
	if err := operationTestConfig().validateOperationKeys(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.OperationCoreKey = "" },
		func(c *Config) { c.OperationAuthKey = c.Key },
		func(c *Config) { c.OperationDeliveryKey = c.OperationAuthKey },
		func(c *Config) { c.OperationCoreKey = "short" },
	} {
		c := operationTestConfig()
		mutate(&c)
		if c.validateOperationKeys() == nil {
			t.Fatal("partial or reused credential accepted")
		}
	}
	if _, err := parseOperationRequest(operationTestRequest("apply"), Config{}); err == nil {
		t.Fatal("unconfigured grant lane accepted")
	}
}
