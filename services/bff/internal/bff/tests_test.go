package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTestsRoutesStayInSelectedCabinet(t *testing.T) {
	id := "0123456789abcdef01234567"
	var seenPath, seenKey, seenTarget string
	var seenQuery url.Values
	calls := 0
	persistedDivision := 1001
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			v := map[string]any{"user": map[string]any{"account_id": 9, "division_id": 1001}, "organizations": []any{}}
			if strings.HasSuffix(r.URL.Path, "/login") {
				v["access_token"] = "owner-access"
				v["refresh_token"] = "owner-refresh"
				v["expires_in"] = 900
			}
			json.NewEncoder(w).Encode(v)
			return
		}
		if r.Header.Get("Authorization") != "Bearer owner-access" || r.Header.Get("X-Service-Key") != "" {
			t.Error("bad owner authorization")
		}
		calls++
		seenPath = r.URL.Path
		seenKey = r.Header.Get("Idempotency-Key")
		seenTarget = r.URL.Query().Get("target_division_id")
		seenQuery = r.URL.Query()
		if r.Method == "GET" && (strings.Contains(r.URL.Path, "/attempts/") || strings.Contains(r.URL.Path, "/submissions/")) {
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"division_id": persistedDivision}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/finalize") {
			w.WriteHeader(202)
			w.Write([]byte(`{"status":true,"data":{"submission_status":"accepted","calculation_status":"queued","result":null}}`))
			return
		}
		w.Write([]byte(`{"status":true,"data":{"items":[],"total":0}}`))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Tests: owner.URL, Core: owner.URL, Origin: "http://127.0.0.1:3001"})
	var cookie *http.Cookie
	revision := ""
	request := func(method, path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Service-Key", "do-not-forward")
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
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(w.Body.Bytes(), &identity)
	revision = identity.Revision
	for _, tc := range []struct{ method, path, up string }{
		{"GET", "catalog", "/v1/tests/divisions/1001/tests"},
		{"GET", "catalog?view=admin&limit=6&offset=12&q=право&status=hidden&date_from=2026-10-01&date_to=2026-10-20&include_external=true", "/v1/tests/divisions/1001/tests"},
		{"GET", "bank?view=admin&limit=6&offset=12&is_archived=all&include_external=false", "/v1/tests/bank"},
		{"GET", "catalog-with-access", "/v1/tests/divisions/1001/catalog-with-access"},
		{"GET", "bank", "/v1/tests/bank"},
		{"POST", id + "/copy", "/v1/tests/divisions/1001/tests/" + id + "/copy"},
		{"POST", "drafts", "/v1/tests/divisions/1001/drafts"},
		{"POST", "drafts/" + id + "/lock", "/v1/tests/divisions/1001/drafts/" + id + "/lock"},
		{"GET", "submissions/stats", "/v1/tests/divisions/1001/submissions/stats"},
		{"GET", "external-tests", "/v1/tests/divisions/1001/external-tests"},
		{"POST", "external-tests", "/v1/tests/divisions/1001/external-tests"},
		{"GET", "external-tests/" + id + "/delete-preview", "/v1/tests/divisions/1001/external-tests/" + id + "/delete-preview"},
		{"GET", "external-results", "/v1/tests/divisions/1001/external-results"},
	} {
		w = request(tc.method, "/bff/tests/"+tc.path, "unique-create-key-123")
		if w.Code != 200 || seenPath != tc.up {
			t.Fatalf("route %s: %d %s", tc.path, w.Code, seenPath)
		}
		if strings.Contains(tc.path, "view=admin") {
			original, _ := url.Parse(tc.path)
			for key, values := range original.Query() {
				if seenQuery.Get(key) != values[0] {
					t.Fatalf("catalogue query lost %s: %s", key, seenQuery.Get(key))
				}
			}
		}
		if seenKey != "unique-create-key-123" {
			t.Fatal("idempotency key lost")
		}
		if strings.HasPrefix(tc.path, "bank") && seenTarget != "1001" {
			t.Fatal("bank actions lacked selected division")
		}
	}
	before := calls
	for _, path := range []string{"bank?target_division_id=1002", "catalog?division_id=1002", "divisions/1002/tests", "internal/result", "attempts/invalid-id", "external-tests?division_id=1002", "external-results?organization_id=1002", "external-imports/commit"} {
		w = request("GET", "/bff/tests/"+path, "")
		if w.Code != 400 && w.Code != 501 {
			t.Fatalf("scope/route bypass %s: %d", path, w.Code)
		}
	}
	if calls != before {
		t.Fatal("rejected scope reached owner")
	}
	w = request("POST", "/bff/tests/attempts/"+id+"/finalize", "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"result":null`) {
		t.Fatal("async acceptance changed", w.Code, w.Body.String())
	}
	persistedDivision = 1002
	before = calls
	w = request("POST", "/bff/tests/attempts/"+id+"/force-submit", "")
	if w.Code != 404 || calls != before+1 {
		t.Fatal("mutation escaped selected division")
	}
	w = request("GET", "/bff/tests/submissions/"+id+"/review", "")
	if w.Code != 404 {
		t.Fatal("review escaped selected division")
	}
}

func TestSubmissionLinksUseBrowserBoundary(t *testing.T) {
	raw := []byte(`{"status":true,"data":{"items":[{"status_url":"/v1/tests/submissions/0123456789abcdef01234567"}],"text":"/v1/tests/untouched","account_id":9007199254740993}}`)
	got := string(testsBrowserLinks(raw))
	if !strings.Contains(got, `"status_url":"/bff/tests/submissions/0123456789abcdef01234567"`) || !strings.Contains(got, `"text":"/v1/tests/untouched"`) {
		t.Fatal(got)
	}
	if !strings.Contains(got, `"account_id":9007199254740993`) {
		t.Fatal("link adaptation rounded the owner's numeric ID", got)
	}
}
