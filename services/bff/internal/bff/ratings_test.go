package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestRatingOpenAPIPublicBoundary(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(openAPI, &spec); err != nil {
		t.Fatal(err)
	}
	count := 0
	for path, methods := range spec.Paths {
		if !strings.HasPrefix(path, "/bff/ratings/") {
			continue
		}
		concrete := strings.ReplaceAll(path, "{job}", "12345678-abcd-4321-abcd-123456789abc")
		concrete = strings.ReplaceAll(concrete, "{account}", "9")
		for method := range methods {
			count++
			if ratingsRoute(strings.ToUpper(method), strings.TrimPrefix(concrete, "/bff/ratings/")) == "" {
				t.Fatalf("unroutable documented method %s %s", method, path)
			}
		}
	}
	if count != 10 {
		t.Fatalf("unexpected public methods: %d", count)
	}
	if strings.Contains(string(openAPI), "/internal/ratings") {
		t.Fatal("internal calculation input/callback exposed")
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "accounts/9"}, {"GET", "accounts/0"}, {"GET", "accounts/01"}, {"GET", "jobs/123"},
		{"GET", "jobs/12345678-abcd-4321-abcd-123456789abc/input"}, {"POST", "jobs/12345678-abcd-4321-abcd-123456789abc/result"},
		{"GET", "divisions/1002/ratings"}, {"DELETE", "jobs/12345678-abcd-4321-abcd-123456789abc"}, {"GET", "ratings/"},
	} {
		if ratingsRoute(route.method, route.path) != "" {
			t.Fatalf("private/malformed route allowed: %+v", route)
		}
	}
}

func TestRatingTransportAndContextFence(t *testing.T) {
	division := 1001
	permission := true
	calls := 0
	lastPath, lastQuery, lastKey := "", "", ""
	const job = "12345678-abcd-4321-abcd-123456789abc"
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			if r.URL.Path == "/v1/auth/context" {
				division = 1002
			}
			var selected any = division
			if division == 0 {
				selected = nil
			}
			identity := map[string]any{"user": map[string]any{"role": "staff_admin", "account_id": 9, "organization_id": 1001, "division_id": selected}, "organizations": []any{}}
			if r.URL.Path == "/v1/auth/login" || r.URL.Path == "/v1/auth/context" {
				identity["access_token"], identity["refresh_token"], identity["expires_in"] = "owner-access", "owner-refresh", 900
			}
			json.NewEncoder(w).Encode(identity)
			return
		}
		calls++
		lastPath, lastQuery, lastKey = r.URL.Path, r.URL.RawQuery, r.Header.Get("Idempotency-Key")
		if r.Header.Get("Authorization") != "Bearer owner-access" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-Request-ID") == "" {
			t.Error("wrong owner boundary authorization")
		}
		if !strings.HasPrefix(lastPath, "/v1/ratings/divisions/") {
			t.Error("unscoped owner call", lastPath)
		}
		if !permission {
			w.WriteHeader(403)
			w.Write([]byte(`{"status":false,"error":"access_denied"}`))
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs") {
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"job": map[string]any{"id": job, "status": "preparing"}, "status_url": "/v1/ratings/divisions/1001/jobs/" + job}})
			return
		}
		w.Write([]byte(`{"status":true,"data":{"ratings":[],"total":0,"freshness":{"is_stale":true,"reason":"never_calculated"}}}`))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Ratings: owner.URL, Origin: "http://127.0.0.1:3001"})
	revision := ""
	var cookie *http.Cookie
	request := func(method, path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Service-Key", "never-forward-this")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/bff/auth/login", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	getRevision := func(raw []byte) string {
		var data struct {
			Revision string `json:"context_revision"`
		}
		json.Unmarshal(raw, &data)
		return data.Revision
	}
	revision = getRevision(w.Body.Bytes())
	w = request("POST", "/bff/ratings/jobs", "same-immutable-key")
	if w.Code != 202 || lastPath != "/v1/ratings/divisions/1001/jobs" || lastKey != "same-immutable-key" || calls != 1 {
		t.Fatal("job request was altered or retried", w.Code, lastPath, lastKey, calls)
	}
	if !strings.Contains(w.Body.String(), "/bff/ratings/jobs/"+job) || strings.Contains(w.Body.String(), "/v1/ratings/") {
		t.Fatal("owner status URL leaked", w.Body.String())
	}
	w = request("GET", "/bff/ratings/ratings?version="+job+"&q=Student&limit=20&offset=1", "")
	if w.Code != 200 || !strings.Contains(lastQuery, "version="+job) || !strings.Contains(lastQuery, "offset=1") {
		t.Fatal("published version/filter query lost", w.Code, lastQuery)
	}
	before := calls
	for _, path := range []string{"ratings?division_id=1002", "ratings?division_ids=1002", "ratings?organization_id=2", "ratings?target_division_id=2", "ratings?url=http://evil.example", "ratings?version=a&version=b", "jobs?version=" + job, "versions?limit=1"} {
		if got := request("GET", "/bff/ratings/"+path, "").Code; got != 400 {
			t.Fatalf("invalid query accepted %s: %d", path, got)
		}
	}
	if calls != before {
		t.Fatal("invalid query reached owner")
	}
	old := revision
	w = request("POST", "/bff/auth/context", "")
	revision = getRevision(w.Body.Bytes())
	if w.Code != 200 || revision == old {
		t.Fatal("context change failed")
	}
	w = request("GET", "/bff/ratings/ratings", "")
	if w.Code != 200 || lastPath != "/v1/ratings/divisions/1002/ratings" {
		t.Fatal("new selected cabinet not injected", w.Code, lastPath)
	}
	newRevision := revision
	revision = old
	before = calls
	if request("POST", "/bff/ratings/jobs", "old-cabinet-key").Code != 409 || calls != before {
		t.Fatal("stale mutation reached owner")
	}
	revision = newRevision
	permission = false
	if request("GET", "/bff/ratings/ratings", "").Code != 403 {
		t.Fatal("owner live permission rejection ignored")
	}
	division = 0
	// A missing selected division cannot be replaced by a browser path/query.
	before = calls
	if request("GET", "/bff/ratings/ratings?division_id=1001", "").Code != 400 || calls != before {
		t.Fatal("browser supplied division was accepted")
	}
	if request("GET", "/bff/ratings/ratings", "").Code != 409 || calls != before {
		t.Fatal("missing selected division reached owner")
	}
}

func TestRatingStatusLinksPreserveOtherData(t *testing.T) {
	raw := []byte(`{"status":true,"data":{"job":{"id":"12345678-abcd-4321-abcd-123456789abc"},"status_url":"/v1/ratings/divisions/1001/jobs/12345678-abcd-4321-abcd-123456789abc"}}`)
	if !regexp.MustCompile(`"status_url":"/bff/ratings/jobs/`).Match(ratingsBrowserLinks(raw)) {
		t.Fatal("status link was not adapted")
	}
	for _, raw := range []string{`{"status":true,"data":{"ratings":[]}}`, `{"status":true,"data":{"status_url":"http://evil.example"}}`} {
		if string(ratingsBrowserLinks([]byte(raw))) != raw {
			t.Fatal("unrelated data modified")
		}
	}
}
