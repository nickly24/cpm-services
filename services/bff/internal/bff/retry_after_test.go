package bff

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHandlerPreservesOnlySafeRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name, raw, expected, body              string
		status, expectedStatus                 int
		authFailure, refreshFailure, preflight bool
	}{
		{name: "rate limited delivery", status: 429, expectedStatus: 429, raw: "120", expected: "120"},
		{name: "unavailable delivery", status: 503, expectedStatus: 503, raw: "Tue, 13 Oct 2026 12:02:00 GMT", expected: "Tue, 13 Oct 2026 12:02:00 GMT"},
		{name: "canonical numeric delay", status: 429, expectedStatus: 429, raw: "000012", expected: "12"},
		{name: "canonical legacy date", status: 503, expectedStatus: 503, raw: "Tuesday, 13-Oct-26 12:02:00 GMT", expected: "Tue, 13 Oct 2026 12:02:00 GMT"},
		{name: "negative delay rejected", status: 429, expectedStatus: 429, raw: "-3"},
		{name: "fractional delay rejected", status: 429, expectedStatus: 429, raw: "1.5"},
		{name: "overflow rejected", status: 429, expectedStatus: 429, raw: "999999999999999999999999999999999999999999"},
		{name: "oversized delay rejected", status: 503, expectedStatus: 503, raw: strings.Repeat("0", 129) + "120"},
		{name: "invalid date rejected", status: 503, expectedStatus: 503, raw: "tomorrow"},
		{name: "nonretryable error drops hint", status: 409, expectedStatus: 409, raw: "120"},
		{name: "success drops hint", status: 200, expectedStatus: 200, raw: "120", body: `{"status":true,"data":{}}`},
		{name: "invalid upstream response drops hint", status: 503, expectedStatus: 502, raw: "120", body: `invalid-json`},
		{name: "entry auth overload", status: 503, expectedStatus: 503, raw: "30", expected: "30", authFailure: true},
		{name: "automatic auth refresh overload", status: 503, expectedStatus: 503, raw: "60", expected: "60", refreshFailure: true},
		{name: "owner preflight overload", status: 503, expectedStatus: 503, raw: "45", expected: "45", preflight: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ownerCalls atomic.Int64
			writeError := func(w http.ResponseWriter) {
				w.Header().Set("Retry-After", tt.raw)
				w.Header().Set("Set-Cookie", "upstream_secret=must-not-leak")
				w.Header().Set("Authorization", "Bearer must-not-leak")
				w.WriteHeader(tt.status)
				body := tt.body
				if body == "" {
					body = `{"status":false,"error":"upstream_busy"}`
				}
				io.WriteString(w, body)
			}
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/auth/me" && tt.authFailure || r.URL.Path == "/v1/auth/refresh" && tt.refreshFailure {
					writeError(w)
					return
				}
				identity := map[string]any{"status": true, "user": map[string]any{"account_id": 12, "organization_id": 7, "division_id": 10, "division_ids": []int{10}, "role": "student"}, "organizations": []any{}}
				if r.URL.Path == "/v1/auth/login" {
					identity["access_token"], identity["refresh_token"], identity["expires_in"] = "access", "refresh", 900
					if tt.refreshFailure {
						identity["expires_in"] = 14
					}
				}
				json.NewEncoder(w).Encode(identity)
			}))
			defer auth.Close()
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ownerCalls.Add(1)
				if tt.preflight && r.Method != "GET" {
					t.Error("replayed mutation after a failed owner preflight")
				}
				writeError(w)
			}))
			defer owner.Close()
			gatewayKey := strings.Repeat("test-gateway-key", 3)
			if tt.preflight {
				gatewayKey = ""
			}
			s := New(Config{Auth: auth.URL, Tests: owner.URL, GatewayKey: gatewayKey, Origin: "http://127.0.0.1:3001"})
			defer s.Close()
			login := redisBrowserRequest(s, "POST", "/bff/auth/login", "", nil)
			revision := redisRevision(t, login)
			response := redisBrowserRequest(s, "POST", "/bff/tests/attempts/0123456789abcdef01234567/finalize", revision, login.Result().Cookies()[0])
			if response.Code != tt.expectedStatus || response.Header().Get("Retry-After") != tt.expected {
				t.Fatalf("status=%d Retry-After=%q body=%s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
			}
			if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Authorization") != "" || strings.Contains(response.Body.String(), "must-not-leak") {
				t.Fatal("forwarded an unrelated upstream secret/header")
			}
			expectedCalls := 1
			if tt.authFailure || tt.refreshFailure {
				expectedCalls = 0
			}
			if ownerCalls.Load() != int64(expectedCalls) {
				t.Fatalf("owner calls=%d expected=%d", ownerCalls.Load(), expectedCalls)
			}
		})
	}
}
