package homework

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticGatewayKey = "synthetic-homework-gateway-key-32-bytes"

func gatewayTestClaims(method, uri, body string, user any) gatewayClaims {
	raw, _ := json.Marshal(user)
	hash := sha256.Sum256([]byte(body))
	now := time.Now().Unix()
	return gatewayClaims{1, "cpm-bff", "homework", now, now + 30, method, uri, hex.EncodeToString(hash[:]), raw}
}
func gatewayTestSign(r *http.Request, raw []byte) {
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(syntheticGatewayKey))
	mac.Write([]byte(encoded))
	r.Header.Set("X-CPM-Gateway-Context", encoded)
	r.Header.Set("X-CPM-Gateway-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}
func gatewayTestRequest(method, uri, body string, claims gatewayClaims) *http.Request {
	r := httptest.NewRequest(method, uri, strings.NewReader(body))
	raw, _ := json.Marshal(claims)
	gatewayTestSign(r, raw)
	r.Header.Set("Authorization", "Bearer synthetic-direct-token")
	return r
}
func TestGatewayRejectsInvalidWithoutBearerFallback(t *testing.T) {
	var calls atomic.Int64
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"status":true,"active":true,"user":{"account_id":7,"organization_id":1,"role":"student"}}`)
	}))
	defer auth.Close()
	d := &Dependencies{AuthURL: auth.URL, HTTP: auth.Client(), GatewayKey: syntheticGatewayKey}
	actor := Actor{AccountID: 7, OrganizationID: 1, Role: "student"}
	base := gatewayTestClaims("POST", "/v1/homework/divisions/2/assignments/9/submit?x=1", `{}`, actor)
	tests := []struct {
		name   string
		mutate func(*http.Request, *gatewayClaims)
	}{
		{"signature", func(r *http.Request, c *gatewayClaims) { r.Header.Set("X-CPM-Gateway-Signature", "forged") }},
		{"partial context", func(r *http.Request, c *gatewayClaims) { r.Header.Del("X-CPM-Gateway-Signature") }},
		{"partial signature", func(r *http.Request, c *gatewayClaims) { r.Header.Del("X-CPM-Gateway-Context") }},
		{"duplicate context", func(r *http.Request, c *gatewayClaims) {
			r.Header.Add("X-CPM-Gateway-Context", r.Header.Get("X-CPM-Gateway-Context"))
		}},
		{"duplicate signature", func(r *http.Request, c *gatewayClaims) {
			r.Header.Add("X-CPM-Gateway-Signature", r.Header.Get("X-CPM-Gateway-Signature"))
		}},
		{"expired", func(r *http.Request, c *gatewayClaims) { c.IssuedAt -= 40; c.ExpiresAt -= 40 }},
		{"future", func(r *http.Request, c *gatewayClaims) { c.IssuedAt += 10; c.ExpiresAt += 10 }},
		{"timestamp overflow", func(r *http.Request, c *gatewayClaims) { c.IssuedAt = math.MinInt64; c.ExpiresAt = math.MaxInt64 }},
		{"TTL", func(r *http.Request, c *gatewayClaims) { c.ExpiresAt++ }},
		{"reversed expiry", func(r *http.Request, c *gatewayClaims) { c.ExpiresAt = c.IssuedAt }},
		{"audience", func(r *http.Request, c *gatewayClaims) { c.Audience = "tests" }},
		{"issuer", func(r *http.Request, c *gatewayClaims) { c.Issuer = "client" }},
		{"version", func(r *http.Request, c *gatewayClaims) { c.Version = 2 }},
		{"method", func(r *http.Request, c *gatewayClaims) { c.Method = "GET" }},
		{"query", func(r *http.Request, c *gatewayClaims) { c.URI += "&y=2" }},
		{"object", func(r *http.Request, c *gatewayClaims) { c.URI = "/v1/homework/divisions/2/assignments/8/submit?x=1" }},
		{"body", func(r *http.Request, c *gatewayClaims) { c.BodyHash = strings.Repeat("0", 64) }},
		{"zero account", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":0,"organization_id":1,"role":"student"}`)
		}},
		{"zero org", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":0,"role":"student"}`)
		}},
		{"division", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"student","division_id":0}`)
		}},
		{"group", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"proctor","group_id":0}`)
		}},
		{"role", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"worker"}`)
		}},
		{"role flag", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"student","organization_admin":true}`)
		}},
		{"unknown user field", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"student","trusted":true}`)
		}},
		{"duplicate role", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"student","role":"admin"}`)
		}},
		{"duplicate role case", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"student","Role":"admin"}`)
		}},
		{"unknown permission field", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"staff_admin","permissions":{"assignments":{"view":true,"grant":true}}}`)
		}},
		{"duplicate permission", func(r *http.Request, c *gatewayClaims) {
			c.User = json.RawMessage(`{"account_id":7,"organization_id":1,"role":"staff_admin","permissions":{"assignments":{"view":true,"view":false}}}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			r := gatewayTestRequest(c.Method, c.URI, `{}`, c)
			tc.mutate(r, &c)
			if !reflect.DeepEqual(c, base) {
				raw, _ := json.Marshal(c)
				gatewayTestSign(r, raw)
			}
			if _, err := d.Identify(r); err == nil || asError(err).Code != 401 {
				t.Fatalf("invalid assertion accepted: %v", err)
			}
		})
	}
	for _, raw := range []string{`{"version":1,"version":1}`, `{"extra":true}`, `{} {}`, `null`} {
		r := gatewayTestRequest(base.Method, base.URI, `{}`, base)
		gatewayTestSign(r, []byte(raw))
		if _, err := d.Identify(r); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	r := gatewayTestRequest(base.Method, base.URI, `{}`, base)
	d.GatewayKey = ""
	if _, err := d.Identify(r); err == nil {
		t.Fatal("unconfigured assertion accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid context fell back to Auth", calls.Load())
	}
	d.GatewayKey = syntheticGatewayKey
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer synthetic-direct-token")
	if _, err := d.Identify(r); err != nil || calls.Load() != 1 {
		t.Fatal("direct bearer regression", err, calls.Load())
	}
}

func TestGatewayActorsPreservePermissionsAndBody(t *testing.T) {
	div, group := int64(2), int64(3)
	for _, role := range []string{"student", "proctor", "admin", "staff_admin", "supervisor", "examinator"} {
		t.Run(role, func(t *testing.T) {
			actor := Actor{AccountID: 7, ProfileID: 900, OrganizationID: 1, DivisionID: &div, GroupID: &group, Role: role, OrganizationAdmin: role == "admin", Permissions: map[string]Permission{"assignments": {View: true}, "review-queue": {View: true, Edit: true}}}
			c := gatewayTestClaims("POST", "/path?x=1", `{"name":"synthetic"}`, actor)
			r := gatewayTestRequest(c.Method, c.URI, `{"name":"synthetic"}`, c)
			r.Header.Del("Authorization")
			got, err := (&Dependencies{GatewayKey: syntheticGatewayKey}).Identify(r)
			body, _ := io.ReadAll(r.Body)
			if err != nil || !reflect.DeepEqual(got, actor) || string(body) != `{"name":"synthetic"}` {
				t.Fatal("identity/body changed", err)
			}
			for _, section := range []string{"assignments", "review-queue", "homework-archive", "monitoring"} {
				for _, edit := range []bool{false, true} {
					if got.Can(section, edit) != actor.Can(section, edit) {
						t.Fatal("permissions changed")
					}
				}
			}
			for _, member := range []Member{{AccountID: 7, Role: "student", GroupID: &group}, {AccountID: 8, Role: "student"}, {AccountID: 8, Role: "proctor", GroupID: &group}} {
				if got.Access(member) != actor.Access(member) {
					t.Fatal("member access changed")
				}
			}
		})
	}
	for _, n := range []int{1 << 20, (1 << 20) + 1} {
		body := strings.Repeat("x", n)
		c := gatewayTestClaims("POST", "/path", body, Actor{AccountID: 7, OrganizationID: 1, Role: "student"})
		r := gatewayTestRequest(c.Method, c.URI, body, c)
		_, err := (&Dependencies{GatewayKey: syntheticGatewayKey}).Identify(r)
		if (err == nil) != (n == 1<<20) {
			t.Fatal("body bound changed", n, err)
		}
	}
}

func TestGatewayDoesNotPromoteAdminRole(t *testing.T) {
	actor := Actor{AccountID: 7, OrganizationID: 1, Role: "admin"}
	c := gatewayTestClaims("GET", "/path", "", actor)
	got, err := (&Dependencies{GatewayKey: syntheticGatewayKey}).Identify(gatewayTestRequest("GET", "/path", "", c))
	if err != nil || got.OrganizationAdmin || got.Can("assignments", false) || got.Access(Member{AccountID: 8, Role: "student"}) {
		t.Fatal("admin role promoted without organization_admin", err)
	}
}
