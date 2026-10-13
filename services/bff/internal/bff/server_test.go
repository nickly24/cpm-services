package bff

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBrowserBoundary(t *testing.T) {
	var mu sync.Mutex
	access, refresh := "a1", "r1"
	division := 1001
	calls := 0
	rotations := 0
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		ident := func(tokens bool) {
			v := map[string]any{"user": map[string]any{"account_id": 9, "id": 1001, "role": "student", "division_id": division, "organization_id": 1001}, "organizations": []any{}, "status": true}
			if tokens {
				v["access_token"] = access
				v["refresh_token"] = refresh
				v["expires_in"] = 900
			}
			json.NewEncoder(w).Encode(v)
		}
		switch r.URL.Path {
		case "/v1/auth/login":
			ident(true)
		case "/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer "+access {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"invalid_token"}`))
				return
			}
			ident(false)
		case "/v1/auth/context":
			access = "a2"
			division = 1002
			json.NewEncoder(w).Encode(map[string]any{"access_token": access, "expires_in": 900, "user": map[string]any{"division_id": division}, "organizations": []any{}})
		case "/v1/auth/refresh":
			var v map[string]string
			json.NewDecoder(r.Body).Decode(&v)
			if v["refresh_token"] != refresh {
				t.Error("lost or replayed refresh")
				w.WriteHeader(401)
				return
			}
			rotations++
			refresh = fmt.Sprint("r", rotations+1)
			ident(true)
		case "/v1/auth/logout":
			w.Write([]byte(`{"status":true}`))
		default:
			calls++
			if r.URL.Path != "/v1/homework/divisions/1002/assignments" {
				t.Errorf("unexpected upstream route %s", r.URL.Path)
			}
			if r.Header.Get("X-Service-Key") != "" {
				t.Error("forwarded service key")
			}
			w.Write([]byte(`{"status":true,"data":{"items":[]}}`))
		}
	}))
	defer owner.Close()
	b := New(Config{Auth: owner.URL, Core: owner.URL, Homework: owner.URL, Origin: "http://127.0.0.1:3001"})
	request := func(method, path, body, rev string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CPM-Context", rev)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	origin := b.config.Origin
	w := request("POST", "/bff/auth/login", `{}`, "", nil, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "access_token") || strings.Contains(w.Body.String(), "refresh_token") {
		t.Fatal("token leak")
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/bff" {
		t.Fatal("unsafe cookie")
	}
	var id struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(w.Body.Bytes(), &id)
	old := id.Revision
	for _, badOrigin := range []string{"", "https://evil.example"} {
		if request("POST", "/bff/auth/context", `{}`, old, cookie, badOrigin).Code != 403 {
			t.Fatal("CSRF accepted")
		}
	}
	w = request("POST", "/bff/auth/context", `{"organization_id":1001,"division_id":1002}`, old, cookie, origin)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &id)
	if id.Revision == old {
		t.Fatal("context fence unchanged")
	}
	if request("GET", "/bff/homework/assignments", "", old, cookie, origin).Code != 409 {
		t.Fatal("stale request accepted")
	}
	if request("GET", "/bff/homework/assignments", "", id.Revision, cookie, origin).Code != 200 {
		t.Fatal("scoped request failed")
	}
	for _, path := range []string{"/bff/internal/auth/password-hash", "/bff/homework/anything", "/bff/core/users/9/credentials"} {
		if request("GET", path, "", id.Revision, cookie, origin).Code != 501 {
			t.Fatal("allowlist bypass")
		}
	}
	if request("GET", "/bff/homework/assignments?division_id=2001", "", id.Revision, cookie, origin).Code != 400 {
		t.Fatal("scope injection")
	}
	if request("POST", "/bff/auth/refresh", strings.Repeat("x", (1<<20)+1), id.Revision, cookie, origin).Code != 413 {
		t.Fatal("body unbounded")
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if request("POST", "/bff/auth/refresh", `{}`, id.Revision, cookie, origin).Code != 200 {
				t.Error("concurrent refresh failed")
			}
		}()
	}
	wg.Wait()
	if rotations != 8 || calls != 1 {
		t.Fatal("unexpected retries", rotations, calls)
	}
	if request("POST", "/bff/auth/logout", `{}`, id.Revision, cookie, origin).Code != 200 {
		t.Fatal("logout failed")
	}
	if request("GET", "/bff/auth/me", "", "", cookie, origin).Code != 401 {
		t.Fatal("closed cookie accepted")
	}
}
func TestUnavailableAndRedirect(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write([]byte(`{}`)) }))
	defer target.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	b := New(Config{Auth: up.URL, Origin: "http://127.0.0.1:3001"})
	call := func() int {
		r := httptest.NewRequest("POST", "/bff/auth/login", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("Origin", b.config.Origin)
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		data, _ := io.ReadAll(w.Result().Body)
		if strings.Contains(string(data), up.URL) {
			t.Error("address leaked")
		}
		return w.Code
	}
	if call() != 502 || hits != 0 {
		t.Fatal("redirect followed")
	}
	up.Close()
	if call() != 503 {
		t.Fatal("outage hidden")
	}
}
