package bff

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
	"sync/atomic"
	"testing"
)

func TestTestsRequestUsesOneEntryAuthAndSignedOwnerScope(t *testing.T) {
	key := strings.Repeat("gateway-test-key", 3)
	var authCalls, ownerCalls atomic.Int64
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		user := map[string]any{"account_id": 12, "organization_id": 7, "division_id": 10, "division_ids": []int64{10}, "role": "student"}
		if r.URL.Path == "/v1/auth/login" {
			json.NewEncoder(w).Encode(map[string]any{"status": true, "user": user, "organizations": []any{}, "access_token": "access", "refresh_token": "refresh", "expires_in": 900})
			return
		}
		if r.URL.Path != "/v1/auth/me" || r.Header.Get("Authorization") != "Bearer access" {
			t.Error("unexpected authorisation request", r.URL.Path)
		}
		authCalls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"status": true, "user": user, "organizations": []any{}})
	}))
	defer auth.Close()
	body := `{"commits":[]}`
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerCalls.Add(1)
		if r.Method != "POST" || r.URL.RequestURI() != "/v1/tests/attempts/0123456789abcdef01234567/commits" {
			t.Error("redundant preflight or incorrect route", r.Method, r.URL.RequestURI())
		}
		encoded := r.Header.Get("X-CPM-Gateway-Context")
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var claims gatewayClaims
		if json.Unmarshal(raw, &claims) != nil {
			t.Error("invalid context envelope")
		}
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(encoded))
		sig, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-CPM-Gateway-Signature"))
		hash := sha256.Sum256([]byte(body))
		var user struct {
			AccountID  int64 `json:"account_id"`
			DivisionID int64 `json:"division_id"`
		}
		json.Unmarshal(claims.User, &user)
		if !hmac.Equal(sig, mac.Sum(nil)) || claims.Method != r.Method || claims.URI != r.URL.RequestURI() || claims.BodyHash != hex.EncodeToString(hash[:]) || user.AccountID != 12 || user.DivisionID != 10 {
			t.Error("gateway assertion lost request binding or accepted browser identity")
		}
		requestBody, _ := io.ReadAll(r.Body)
		if string(requestBody) != body {
			t.Error("owner body changed")
		}
		io.WriteString(w, `{"status":true,"data":{"acknowledged_commit_ids":[],"server_answer_count":0,"server_time":"2026-10-13T00:00:00Z"}}`)
	}))
	defer owner.Close()
	s := New(Config{Auth: auth.URL, Tests: owner.URL, GatewayKey: key, Origin: "http://127.0.0.1:3001"})
	request := func(path, body, rev string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", rev)
		r.Header.Set("X-CPM-Gateway-Context", "forged-browser-admin")
		r.Header.Set("X-CPM-Gateway-Signature", "forged")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request("/bff/auth/login", `{}`, "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(w.Body.Bytes(), &identity)
	w = request("/bff/tests/attempts/0123456789abcdef01234567/commits", body, identity.Revision, w.Result().Cookies()[0])
	if w.Code != 200 || authCalls.Load() != 1 || ownerCalls.Load() != 1 {
		t.Fatalf("one browser write multiplied calls: status=%d auth=%d owner=%d body=%s", w.Code, authCalls.Load(), ownerCalls.Load(), w.Body.String())
	}
}

func TestHomeworkAudienceAndIndependentCredential(t *testing.T) {
	homeworkKey := strings.Repeat("homework-synthetic", 3)
	testsKey := strings.Repeat("tests-synthetic", 3)
	s := New(Config{Tests: "http://tests.local", Homework: "http://homework.local", GatewayKey: testsKey, HomeworkGatewayKey: homeworkKey})
	user := json.RawMessage(`{"account_id":7,"organization_id":1,"division_id":2,"role":"student"}`)
	for _, c := range []struct{ base, audience, key string }{{s.config.Tests, "tests", testsKey}, {s.config.Homework, "homework", homeworkKey}} {
		r := httptest.NewRequest("POST", c.base+"/route?x=1", strings.NewReader(`{}`))
		r = r.WithContext(withGatewayIdentity(r.Context(), user))
		if err := s.attachGatewayContext(r, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		encoded := r.Header.Get("X-CPM-Gateway-Context")
		raw, _ := base64.RawURLEncoding.DecodeString(encoded)
		var claims gatewayClaims
		json.Unmarshal(raw, &claims)
		sig, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-CPM-Gateway-Signature"))
		mac := hmac.New(sha256.New, []byte(c.key))
		mac.Write([]byte(encoded))
		if claims.Audience != c.audience || !hmac.Equal(sig, mac.Sum(nil)) {
			t.Fatal("audience/key crossed service boundary")
		}
	}
	r := httptest.NewRequest("GET", s.config.Homework+"/v1/homework/divisions/2/assignments", nil)
	if err := s.attachGatewayContext(r, nil); err == nil {
		t.Fatal("Homework signed without this request's live identity")
	}
	s.config.HomeworkGatewayKey = ""
	if err := s.attachGatewayContext(r, nil); err != nil || r.Header.Get("X-CPM-Gateway-Context") != "" {
		t.Fatal("optional-key native path regressed")
	}
}

func TestHomeworkGatewayConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("BFF_TESTS_CONTEXT_KEY", strings.Repeat("t", 32))
	for _, c := range []struct {
		key   string
		valid bool
	}{{"", true}, {"short", false}, {strings.Repeat("t", 32), false}, {strings.Repeat("h", 32), true}} {
		t.Setenv("BFF_HOMEWORK_CONTEXT_KEY", c.key)
		_, _, err := LoadConfig()
		if (err == nil) != c.valid {
			t.Fatal("Homework key validation", err)
		}
	}
}
