package attendance

import (
	"bytes"
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

func signedAttendanceRequest(t *testing.T, s *Server, body []byte, change func(*gatewayClaims)) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/attendance/divisions/2/attendance-cells/batch", bytes.NewReader(body))
	a := fixtureIdentity(1, "proctor")
	a.Status = true
	a.Active = true
	raw, _ := json.Marshal(a)
	hash := sha256.Sum256(body)
	now := time.Now().Unix()
	claims := gatewayClaims{Version: 1, Issuer: "cpm-bff", Audience: "attendance", IssuedAt: now, ExpiresAt: now + 30, Method: r.Method, URI: r.URL.RequestURI(), BodyHash: hex.EncodeToString(hash[:]), User: raw}
	if change != nil {
		change(&claims)
	}
	data, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, []byte(s.GatewayKey))
	mac.Write([]byte(encoded))
	r.Header.Set("X-CPM-Gateway-Context", encoded)
	r.Header.Set("X-CPM-Gateway-Signature", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return r
}
func TestAttendanceGatewayRequestBindingAndScopedGrants(t *testing.T) {
	s := Server{GatewayKey: strings.Repeat("attendance-key", 3)}
	body := []byte(`{"expected_revision":1,"data":{"cells":[]}}`)
	r := signedAttendanceRequest(t, &s, body, nil)
	a, present, err := s.gatewayIdentity(r)
	if err != nil || !present || a.User.AccountID != 8 || !a.journal(2, true) {
		t.Fatal(a, present, err)
	}
	restored, _ := io.ReadAll(r.Body)
	if !bytes.Equal(restored, body) {
		t.Fatal("body changed")
	}
	for _, change := range []func(*gatewayClaims){func(c *gatewayClaims) { c.Audience = "homework" }, func(c *gatewayClaims) { c.Issuer = "browser" }, func(c *gatewayClaims) { c.ExpiresAt = time.Now().Unix() - 1 }, func(c *gatewayClaims) { c.IssuedAt = time.Now().Unix() + 10 }, func(c *gatewayClaims) { c.ExpiresAt = c.IssuedAt + 31 }, func(c *gatewayClaims) { c.Method = "DELETE" }, func(c *gatewayClaims) { c.URI += "?organization_id=2" }, func(c *gatewayClaims) { c.BodyHash = strings.Repeat("0", 64) }, func(c *gatewayClaims) {
		c.User = json.RawMessage(`{"status":true,"active":true,"user":{"account_id":6,"organization_id":1,"role":"student","organization_admin":true},"organizations":[{"id":1,"divisions":[]}]}`)
	}} {
		r = signedAttendanceRequest(t, &s, body, change)
		if _, present, err = s.gatewayIdentity(r); !present || errorCode(err) != "invalid_gateway_context" {
			t.Fatal("tampered assertion accepted", present, err)
		}
	}
	r = signedAttendanceRequest(t, &s, body, nil)
	r.Header.Add("X-CPM-Gateway-Signature", r.Header.Get("X-CPM-Gateway-Signature"))
	if _, _, err = s.gatewayIdentity(r); err == nil {
		t.Fatal("duplicate signature accepted")
	}
	r = signedAttendanceRequest(t, &s, body, nil)
	r.Header.Set("X-CPM-Gateway-Signature", strings.Repeat("x", 43))
	if _, _, err = s.gatewayIdentity(r); err == nil {
		t.Fatal("forged signature accepted")
	}
	r = httptest.NewRequest("GET", "/v1/attendance/calendars", nil)
	if _, present, err = s.gatewayIdentity(r); present || err != nil {
		t.Fatal("direct bearer fallback lost")
	}
	r.Header.Set("X-CPM-Gateway-Context", "partial")
	if _, present, err = s.gatewayIdentity(r); !present || err == nil {
		t.Fatal("partial assertion fell back")
	}
}

func TestAttendanceGatewayDistinctRuntimeCredential(t *testing.T) {
	for key, value := range map[string]string{"APP_ENV": "production", "MYSQL_HOST": "db.example.test", "MYSQL_DATABASE": "production_fixture", "MYSQL_PORT": "3306", "MYSQL_USER": "fixture", "MYSQL_PASSWORD": "fixture", "MYSQL_TLS": "true", "ATTENDANCE_AUTH_URL": "http://auth:8002", "ATTENDANCE_CORE_URL": "http://core:8003", "AUTH_INTERNAL_KEY": strings.Repeat("s", 32), "ATTENDANCE_QR_KEY": strings.Repeat("q", 32), "BFF_TESTS_CONTEXT_KEY": strings.Repeat("t", 32), "BFF_HOMEWORK_CONTEXT_KEY": strings.Repeat("h", 32)} {
		t.Setenv(key, value)
	}
	for _, test := range []struct {
		key   string
		valid bool
	}{{"", true}, {"short", false}, {strings.Repeat("s", 32), false}, {strings.Repeat("q", 32), false}, {strings.Repeat("t", 32), false}, {strings.Repeat("h", 32), false}, {strings.Repeat("a", 32), true}} {
		t.Setenv("BFF_ATTENDANCE_CONTEXT_KEY", test.key)
		_, err := LoadConfig()
		if (err == nil) != test.valid {
			t.Fatal("runtime credential validation", err)
		}
	}
}
