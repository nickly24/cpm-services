package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestCardsCoreOpenAPIPublicBoundary(t *testing.T) {
	var document struct {
		Paths map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(openAPI, &document); err != nil {
		t.Fatal(err)
	}
	params := regexp.MustCompile(`\{([^}]+)\}`)
	cardsCount, coreCount := 0, 0
	for path, methods := range document.Paths {
		if !strings.HasPrefix(path, "/bff/cards/") && !strings.HasPrefix(path, "/bff/core/") {
			continue
		}
		concrete := params.ReplaceAllStringFunc(path, func(v string) string {
			switch v {
			case "{kind}":
				return "manual"
			case "{session}":
				return "1234567890abcdef1234567890abcdef"
			default:
				return "1"
			}
		})
		for method := range methods {
			if !oneHTTPMethod(method) {
				continue
			}
			if strings.HasPrefix(path, "/bff/cards/") {
				cardsCount++
				if cardsRoute(strings.ToUpper(method), strings.TrimPrefix(concrete, "/bff/cards/")) == "" {
					t.Errorf("unroutable cards contract %s %s", method, path)
				}
			} else {
				coreCount++
				if coreRoute(strings.ToUpper(method), strings.TrimPrefix(concrete, "/bff/core/")) == "" {
					t.Errorf("unroutable core contract %s %s", method, path)
				}
			}
		}
	}
	if cardsCount != 19 || coreCount < 30 {
		t.Fatalf("incomplete public cards/core contract: cards=%d core=%d", cardsCount, coreCount)
	}
	for _, path := range []string{"/bff/cards/imports/parse", "/bff/cards/imports/targets", "/bff/core/divisions/{division}/imports", "/bff/core/schedule"} {
		if _, ok := document.Paths[path]; ok {
			t.Errorf("excluded route documented %s", path)
		}
	}
	for _, path := range []string{"/bff/core/dashboard", "/bff/core/students", "/bff/core/reports/tests", "/bff/core/reports/exams", "/bff/core/reports/attendance"} {
		if _, ok := document.Paths[path]["get"]; !ok {
			t.Errorf("missing read contract %s", path)
		}
	}
	for _, route := range []struct{ method, path string }{
		{"get", "/bff/core/organization/users"}, {"get", "/bff/core/organization/users/{user}/memberships"}, {"get", "/bff/core/audit"},
		{"put", "/bff/core/organization"}, {"post", "/bff/core/divisions"}, {"put", "/bff/core/divisions/{division}"}, {"delete", "/bff/core/divisions/{division}"},
		{"post", "/bff/core/directions"}, {"put", "/bff/core/directions/{direction}"}, {"delete", "/bff/core/directions/{direction}"},
		{"get", "/bff/core/divisions/{division}/users"}, {"put", "/bff/core/divisions/{division}/memberships/{user}"}, {"delete", "/bff/core/divisions/{division}/memberships/{user}"},
	} {
		if _, ok := document.Paths[route.path][route.method]; !ok {
			t.Errorf("missing organization contract %s %s", route.method, route.path)
		}
	}
}

func TestCardsCoreRouteAllowlist(t *testing.T) {
	for _, c := range []struct{ method, tail string }{
		{"GET", "admin/catalog"}, {"GET", "tree"}, {"GET", "directions/9/sections"},
		{"GET", "sections/manual/9"}, {"GET", "sections/test/1234567890abcdef12345678/batches/0"},
		{"PUT", "sections/manual/9/settings"}, {"POST", "sections/test/1234567890abcdef12345678/learned"},
		{"DELETE", "sections/manual/9/learned"}, {"POST", "themes"}, {"PUT", "themes/9"},
		{"DELETE", "themes/9"}, {"GET", "themes/9/cards"}, {"POST", "themes/9/cards"},
		{"PUT", "themes/9/cards/3"}, {"DELETE", "themes/9/cards/3"}, {"POST", "transforms"},
		{"GET", "sessions/1234567890abcdef1234567890abcdef"}, {"PUT", "sessions/1234567890abcdef1234567890abcdef"},
		{"POST", "sessions/1234567890abcdef1234567890abcdef/commit"},
	} {
		if cardsRoute(c.method, c.tail) != "/v1/cards/"+c.tail {
			t.Fatalf("missing cards public route %+v", c)
		}
	}
	for _, c := range []struct{ method, tail string }{
		{"POST", "imports/parse"}, {"GET", "internal/cards"}, {"GET", "sections/exam/9"},
		{"GET", "sections/manual/0"}, {"GET", "sections/manual/01"}, {"GET", "sections/test/not-an-id"},
		{"GET", "sessions/9"}, {"GET", "divisions/2/tree"}, {"GET", "themes/9/cards/3"},
		{"PUT", "themes"}, {"GET", "tree/"}, {"GET", "sections/manual/9/batches/-1"},
	} {
		if cardsRoute(c.method, c.tail) != "" {
			t.Fatalf("private or malformed cards route permitted %+v", c)
		}
	}
	for _, c := range []struct{ method, tail, expected string }{
		{"GET", "organization/users", "/v1/core/organization/users"}, {"GET", "organization/users/9/memberships", "/v1/core/organization/users/9/memberships"}, {"GET", "audit", "/v1/core/audit"},
		{"POST", "divisions", "/v1/core/divisions"}, {"PUT", "organization", "/v1/core/organization"},
		{"PUT", "divisions/2", "/v1/core/divisions/2"}, {"DELETE", "directions/2", "/v1/core/directions/2"},
		{"GET", "divisions/2/users", "/v1/core/divisions/2/users"}, {"PUT", "divisions/2/memberships/9", "/v1/core/divisions/2/memberships/9"}, {"DELETE", "divisions/2/memberships/9", "/v1/core/divisions/2/memberships/9"},
		{"GET", "schools/9", "/v1/core/schools/9"}, {"POST", "schools", "/v1/core/schools"},
		{"PUT", "schools/9", "/v1/core/schools/9"}, {"GET", "roles", "/v1/core/roles"},
		{"POST", "roles", "/v1/core/roles"}, {"PUT", "roles/9", "/v1/core/roles/9"},
		{"DELETE", "roles/9", "/v1/core/roles/9"},
		{"GET", "students", "/v1/core/divisions/%d/students"}, {"GET", "dashboard", "/v1/core/divisions/%d/dashboard"},
		{"GET", "reports/tests", "/v1/core/divisions/%d/reports/tests"}, {"GET", "reports/exams", "/v1/core/divisions/%d/reports/exams"},
		{"GET", "reports/attendance", "/v1/core/divisions/%d/reports/attendance"},
		{"PUT", "users/9/credentials", "/v1/core/divisions/%d/users/9/credentials"},
		{"PUT", "users/9/active", "/v1/core/divisions/%d/users/9/active"},
		{"PUT", "memberships/9", "/v1/core/divisions/%d/memberships/9"},
		{"DELETE", "memberships/9", "/v1/core/divisions/%d/memberships/9"},
		{"POST", "groups/9/members", "/v1/core/divisions/%d/groups/9/members"},
		{"DELETE", "groups/9/members/3", "/v1/core/divisions/%d/groups/9/members/3"},
	} {
		if coreRoute(c.method, c.tail) != c.expected {
			t.Fatalf("wrong core public route %+v", c)
		}
	}
	for _, c := range []struct{ method, tail string }{
		{"GET", "users/9/credentials"}, {"DELETE", "users/9"}, {"GET", "roles/9"},
		{"PUT", "roles"}, {"POST", "roles/9"}, {"DELETE", "roles/01"},
		{"POST", "imports"}, {"POST", "divisions/2/users"}, {"PUT", "divisions/2/users/9"},
		{"GET", "schedule"}, {"POST", "class-days"}, {"POST", "zaps"}, {"DELETE", "organization"},
		{"DELETE", "schools/9"}, {"GET", "users/01"}, {"GET", "divisions/0/users"}, {"GET", "organization/users/01/memberships"},
	} {
		if coreRoute(c.method, c.tail) != "" {
			t.Fatalf("out-of-scope core route permitted %+v", c)
		}
	}
}

func TestCardsCoreLiveScopeAndTransport(t *testing.T) {
	division, allowed, calls := 1001, true, 0
	lastPath, lastKey, lastBody := "", "", ""
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
			identity := map[string]any{"user": map[string]any{"role": "staff_admin", "account_id": 9, "organization_id": 7, "division_id": selected}, "organizations": []any{}}
			if r.URL.Path == "/v1/auth/login" || r.URL.Path == "/v1/auth/context" {
				identity["access_token"], identity["refresh_token"], identity["expires_in"] = "access", "refresh", 900
			}
			json.NewEncoder(w).Encode(identity)
			return
		}
		calls++
		lastPath, lastKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		var body json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		lastBody = string(body)
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-Request-ID") == "" {
			t.Error("owner transport leaked or lost authorization")
		}
		if !allowed {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":"access_denied"}`))
			return
		}
		w.Write([]byte(`{"status":true,"data":[]}`))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Core: owner.URL, Cards: owner.URL, Origin: "http://127.0.0.1:3001"})
	rev := ""
	var cookie *http.Cookie
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", rev)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("Idempotency-Key", "12345678-abcd-4321-abcd-123456789abc")
		r.Header.Set("X-Service-Key", "browser-key-must-be-dropped")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	update := func(w *httptest.ResponseRecorder) {
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var identity struct {
			Revision string `json:"context_revision"`
		}
		json.Unmarshal(w.Body.Bytes(), &identity)
		rev = identity.Revision
	}
	w := request("POST", "/bff/auth/login", `{}`)
	update(w)
	cookie = w.Result().Cookies()[0]
	if w = request("PUT", "/bff/core/users/9", `{"full_name":"Имя"}`); w.Code != 200 || lastPath != "/v1/core/divisions/1001/users/9" || lastBody != `{"full_name":"Имя"}` {
		t.Fatal("scoped user DTO changed", w.Code, lastPath, lastBody)
	}
	if w = request("POST", "/bff/cards/themes", `{"name":"Тема","direction_id":9}`); w.Code != 200 || lastPath != "/v1/cards/themes" || lastKey == "" {
		t.Fatal("organization-wide command changed", w.Code, lastPath, lastKey)
	}
	if w = request("PUT", "/bff/core/divisions/1002/memberships/9", `{"role_id":4}`); w.Code != 200 || lastPath != "/v1/core/divisions/1002/memberships/9" {
		t.Fatal("explicit owner-authorized target changed", w.Code, lastPath)
	}
	for _, c := range []struct{ method, tail string }{{"POST", "roles"}, {"PUT", "roles/9"}, {"DELETE", "roles/9"}} {
		if w = request(c.method, "/bff/core/"+c.tail, `{"name":"Доступ","permissions":{}}`); w.Code != 200 || lastPath != "/v1/core/"+c.tail {
			t.Fatal("role command invented division scope", w.Code, lastPath)
		}
	}
	before := calls
	for _, path := range []string{"/bff/cards/tree?account_id=10", "/bff/cards/tree?organization_id=8", "/bff/cards/tree?division_id=1002", "/bff/cards/tree?url=http://evil", "/bff/cards/directions/9/sections?page=1&page=2", "/bff/core/users?division_id=2", "/bff/core/users?role=student&role=admin", "/bff/core/reports/tests?organization_id=8", "/bff/core/dashboard?account_id=10", "/bff/core/roles?division_id=2", "/bff/core/divisions/1002/users?organization_id=8", "/bff/core/organization/users?unassigned=1"} {
		if got := request("GET", path, "").Code; got != 400 {
			t.Fatalf("scope/duplicate query accepted %s: %d", path, got)
		}
	}
	if calls != before {
		t.Fatal("rejected query reached business owner")
	}
	if request("PUT", "/bff/core/roles/9?organization_id=8", `{}`).Code != 400 || calls != before {
		t.Fatal("role write scope query reached business owner")
	}
	old := rev
	update(request("POST", "/bff/auth/context", `{"organization_id":7,"division_id":1002}`))
	newRevision := rev
	rev = old
	if request("POST", "/bff/cards/themes", `{}`).Code != 409 || request("GET", "/bff/core/users", "").Code != 409 {
		t.Fatal("stale scope accepted")
	}
	rev = newRevision
	if w = request("GET", "/bff/core/dashboard", ""); w.Code != 200 || lastPath != "/v1/core/divisions/1002/dashboard" {
		t.Fatal("new scope not injected", w.Code, lastPath)
	}
	allowed = false
	before = calls
	if request("POST", "/bff/cards/themes", `{}`).Code != 403 || calls != before+1 {
		t.Fatal("owner denial altered or retried")
	}
	allowed = true
	division = 0
	if request("GET", "/bff/core/users", "").Code != 409 {
		t.Fatal("missing division accepted")
	}
	if w = request("GET", "/bff/cards/tree", ""); w.Code != 200 || lastPath != "/v1/cards/tree" {
		t.Fatal("cards invented a division scope", w.Code, lastPath)
	}
}
