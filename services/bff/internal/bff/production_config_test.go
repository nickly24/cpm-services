package bff

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionEnvironmentAndCORS(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("BFF_ALLOWED_ORIGINS", "https://cpm-lms.ru,http://localhost:3000,http://185.239.51.138")
	t.Setenv("BFF_COOKIE_SECURE", "false")
	t.Setenv("BFF_REDIS_PASSWORD", "fixture")
	t.Setenv("BFF_TESTS_CONTEXT_KEY", strings.Repeat("g", 32))
	for name, endpoint := range map[string]string{"AUTH": "auth:8002", "CORE": "core:8003", "HOMEWORK": "homework:8006", "TESTS": "tests:8004", "EXAMS": "exams:8010", "RATINGS": "ratings:8012", "ATTENDANCE": "attendance:8013", "CARDS": "cards:8008", "UPLOADS": "uploads:8014"} {
		t.Setenv("BFF_"+name+"_URL", "http://"+endpoint)
	}
	c, _, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Exams != "http://exams:8010" || c.Secure || len(c.AllowedOrigins) != 3 {
		t.Fatal("production settings not applied")
	}
	s := New(c)
	for _, origin := range c.AllowedOrigins {
		r := httptest.NewRequest("OPTIONS", "/bff/auth/login", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "Content-Type, X-CPM-Request")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatalf("preflight failed %d", w.Code)
		}
	}
	for _, item := range []struct{ origin, headers string }{{"https://foreign.example", "Content-Type"}, {c.AllowedOrigins[0], "X-Service-Key"}} {
		r := httptest.NewRequest("OPTIONS", "/bff/auth/login", nil)
		r.Header.Set("Origin", item.origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", item.headers)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("unsafe preflight allowed %d", w.Code)
		}
	}
	t.Setenv("BFF_ALLOWED_ORIGINS", "*")
	if _, _, err = LoadConfig(); err == nil {
		t.Fatal("wildcard allowed")
	}
	t.Setenv("BFF_ALLOWED_ORIGINS", c.AllowedOrigins[0])
	t.Setenv("BFF_AUTH_URL", "")
	if _, _, err = LoadConfig(); err == nil {
		t.Fatal("production silently used dev URL")
	}
}

func TestProductionCookieFlags(t *testing.T) {
	s := New(Config{Secure: true})
	w := httptest.NewRecorder()
	s.cookie(w, "opaque")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie protections missing")
	}
}
