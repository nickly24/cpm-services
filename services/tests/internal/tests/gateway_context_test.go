package tests

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signedGatewayRequest(t *testing.T, key, body string, change func(*gatewayClaims)) *http.Request {
	t.Helper()
	now := time.Now().Unix()
	hash := sha256.Sum256([]byte(body))
	claims := gatewayClaims{Version: 1, Issuer: "cpm-bff", Audience: "tests", IssuedAt: now, ExpiresAt: now + 30, Method: "POST", URI: "/v1/tests/attempts/0123456789abcdef01234567/commits?x=1", BodyHash: hex.EncodeToString(hash[:]), User: json.RawMessage(`{"account_id":12,"organization_id":7,"division_id":10,"role":"student","division_ids":[10]}`)}
	if change != nil {
		change(&claims)
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(encoded))
	r := httptest.NewRequest("POST", claims.URI, strings.NewReader(body))
	r.Header.Set("X-CPM-Gateway-Context", encoded)
	r.Header.Set("X-CPM-Gateway-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return r
}

func TestGatewayIdentityChecksAuthOnceAtEntryAndPreservesBody(t *testing.T) {
	key := strings.Repeat("gateway-test-key", 3)
	dependencies := &Dependencies{GatewayKey: key, HTTP: &http.Client{}, AuthURL: "http://127.0.0.1:1"}
	body := `{"commits":[{"value":"ответ"}]}`
	r := signedGatewayRequest(t, key, body, nil)
	actor, err := dependencies.Identify(r)
	if err != nil || actor.AccountID != 12 || actor.DivisionID == nil || *actor.DivisionID != 10 {
		t.Fatalf("signed identity rejected: %+v %v", actor, err)
	}
	raw, _ := io.ReadAll(r.Body)
	if string(raw) != body {
		t.Fatal("gateway validation consumed owner request body")
	}
	if err := gatewaySelectedDivision(r, actor, 11); err == nil {
		t.Fatal("signed request escaped selected cabinet")
	}
	actor.Role = "admin"
	actor.OrganizationAdmin = true
	if err := gatewaySelectedDivision(r, actor, 11); err == nil {
		t.Fatal("organisation admin bypassed selected cabinet")
	}
}

func TestGatewayContextRejectsForgedExpiredAndReboundRequests(t *testing.T) {
	key := strings.Repeat("k", 40)
	dependencies := &Dependencies{GatewayKey: key}
	cases := []struct {
		name    string
		claims  func(*gatewayClaims)
		request func(*http.Request)
	}{
		{"expired", func(c *gatewayClaims) { c.IssuedAt -= 60; c.ExpiresAt -= 60 }, nil},
		{"future", func(c *gatewayClaims) { c.IssuedAt += 60; c.ExpiresAt += 60 }, nil},
		{"long lifetime", func(c *gatewayClaims) { c.ExpiresAt += 1 }, nil},
		{"audience", func(c *gatewayClaims) { c.Audience = "core" }, nil},
		{"issuer", func(c *gatewayClaims) { c.Issuer = "browser" }, nil},
		{"invalid actor", func(c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":0,"organization_id":7,"role":"student"}`)
		}, nil},
		{"signature", nil, func(r *http.Request) { r.Header.Set("X-CPM-Gateway-Signature", "bad") }},
		{"empty assertion", nil, func(r *http.Request) {
			r.Header.Set("X-CPM-Gateway-Context", "")
			r.Header.Set("X-CPM-Gateway-Signature", "")
		}},
		{"duplicate headers", nil, func(r *http.Request) { r.Header.Add("X-CPM-Gateway-Context", r.Header.Get("X-CPM-Gateway-Context")) }},
		{"method", nil, func(r *http.Request) { r.Method = "GET" }},
		{"path", nil, func(r *http.Request) { r.URL.Path += "/other" }},
		{"query", nil, func(r *http.Request) { r.URL.RawQuery = "x=2" }},
		{"body", nil, func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"commits":[]}`)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := signedGatewayRequest(t, key, `{"commits":[1]}`, tc.claims)
			if tc.request != nil {
				tc.request(r)
			}
			_, present, err := dependencies.gatewayActor(r)
			if !present || err == nil {
				t.Fatal("unsafe gateway assertion accepted or fell back to bearer")
			}
		})
	}
	_, present, err := (&Dependencies{}).gatewayActor(signedGatewayRequest(t, key, `{}`, nil))
	if !present || err == nil {
		t.Fatal("unconfigured owner trusted assertion")
	}
}

func TestDirectBearerRequestsStillIntrospect(t *testing.T) {
	calls := 0
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/auth/introspect" || r.Header.Get("Authorization") != "Bearer direct-token" {
			t.Error("invalid direct authorisation")
		}
		io.WriteString(w, `{"status":true,"active":true,"user":{"account_id":12,"organization_id":7,"role":"student","division_id":10}}`)
	}))
	defer auth.Close()
	d := &Dependencies{AuthURL: auth.URL, HTTP: auth.Client(), GatewayKey: strings.Repeat("k", 40)}
	r := httptest.NewRequest("GET", "/v1/tests/attempts", nil)
	r.Header.Set("Authorization", "Bearer direct-token")
	if _, err := d.Identify(r); err != nil || calls != 1 {
		t.Fatalf("direct caller bypassed or repeated Auth: calls=%d err=%v", calls, err)
	}
}
