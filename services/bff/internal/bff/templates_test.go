package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtectedTemplatesTransportAndLiveFence(t *testing.T) {
	ownerStatus, contentType, payload := 200, xlsxType, "PK-template"
	calls := 0
	lastPath := ""
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			value := map[string]any{"user": map[string]any{"account_id": 9, "organization_id": 7, "division_id": 10}, "organizations": []any{}}
			if r.URL.Path == "/v1/auth/login" {
				value["access_token"], value["refresh_token"], value["expires_in"] = "server-access", "refresh", 900
			}
			json.NewEncoder(w).Encode(value)
			return
		}
		calls++
		lastPath = r.URL.RequestURI()
		if r.Header.Get("Authorization") != "Bearer server-access" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-Operation-Grant") != "" || r.Header.Get("X-Request-ID") == "" {
			t.Error("template transport leaked browser credentials or lost server identity")
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="injected-name"`)
		if ownerStatus == 302 {
			w.Header().Set("Location", "/redirect-must-not-follow")
		}
		w.WriteHeader(ownerStatus)
		w.Write([]byte(payload))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Uploads: owner.URL, Exams: owner.URL, Origin: "http://127.0.0.1:3001"})
	var cookie *http.Cookie
	revision := ""
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("X-Service-Key", "browser-key")
		r.Header.Set("X-Operation-Grant", "browser-grant")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/bff/auth/login")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(w.Body.Bytes(), &identity)
	revision = identity.Revision
	for _, tc := range []struct{ path, upstream string }{
		{"/bff/exams/12/imports/questions/template", "/v1/exams/12/imports/questions/template"},
		{"/bff/uploads/templates/cards.xlsx", "/v1/uploads/divisions/10/templates/cards.xlsx"},
		{"/bff/uploads/templates/exams.outside?target_id=12", "/v1/uploads/divisions/10/templates/exams.outside?target_id=12"},
	} {
		w = request("GET", tc.path)
		if w.Code != 200 || w.Body.String() != payload || lastPath != tc.upstream || w.Header().Get("Content-Type") != xlsxType || w.Header().Get("Content-Disposition") != `attachment; filename="import-template.xlsx"` {
			t.Fatal("protected binary template changed", tc.path, w.Code, lastPath, w.Body.String())
		}
	}
	contentType, payload = "text/csv; charset=utf-8", "login,password\r\n"
	w = request("GET", "/bff/uploads/templates/users.credentials")
	if w.Code != 200 || w.Body.String() != payload || w.Header().Get("Content-Disposition") != `attachment; filename="import-template.csv"` {
		t.Fatal("CSV template changed", w.Code)
	}
	contentType, payload = "application/json; charset=utf-8", `{"title":"sample","questions":[{"type":"text"}]}`
	w = request("GET", "/bff/uploads/templates/tests.json?direction_id=12&format=cpm_json")
	if w.Code != 200 || w.Body.String() != payload || lastPath != "/v1/uploads/divisions/10/templates/tests.json?direction_id=12&format=cpm_json" || w.Header().Get("Content-Disposition") != `attachment; filename="import-template.json"` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("protected JSON template changed", w.Code, lastPath)
	}
	if request("GET", "/bff/uploads/templates/cards.xlsx").Code != 502 {
		t.Fatal("JSON allowed outside the registered Tests template")
	}
	contentType, payload = "text/javascript; charset=utf-8", `var test = {"Name":"sample","Questions":[{"AnswerType":420}]};`
	w = request("GET", "/bff/uploads/templates/tests.json?direction_id=12&format=online_test_pad")
	if w.Code != 200 || w.Body.String() != payload || w.Header().Get("Content-Disposition") != `attachment; filename="import-template.js"` {
		t.Fatal("protected OTP data download changed", w.Code)
	}
	if request("GET", "/bff/uploads/templates/tests.json?direction_id=12").Code != 502 {
		t.Fatal("OTP MIME accepted for JSON format")
	}
	contentType, payload = xlsxType, "PK-template"
	w = request("GET", "/bff/uploads/templates/tests.external-results?target_id="+strings.Repeat("a", 24))
	if w.Code != 200 || !strings.Contains(lastPath, "target_id="+strings.Repeat("a", 24)) {
		t.Fatal("scoped external template target changed", w.Code, lastPath)
	}
	before := calls
	for _, path := range []string{
		"/bff/exams/12/imports/questions/template?target_id=13", "/bff/exams/12/imports/questions/template?organization_id=8",
		"/bff/uploads/templates/cards.xlsx?target_id=12", "/bff/uploads/templates/exams.outside?target_id=01",
		"/bff/uploads/templates/exams.outside?target_id=12&target_id=13", "/bff/uploads/templates/exams.outside?division_id=11",
		"/bff/uploads/templates/tests.external-results?target_id=12", "/bff/uploads/templates/tests.external-results?target_id=" + strings.Repeat("a", 24) + "&direction_id=12",
		"/bff/uploads/templates/tests.json?format=javascript", "/bff/uploads/templates/tests.json?direction_id=01",
		"/bff/uploads/templates/tests.json?direction_id=12&direction_id=13", "/bff/uploads/templates/tests.json?target_id=12",
	} {
		if w = request("GET", path); w.Code != 400 {
			t.Fatal("template selector accepted", path, w.Code)
		}
	}
	if calls != before {
		t.Fatal("rejected template query reached owner")
	}
	for _, path := range []string{"/bff/uploads/templates/homework.pdf", "/bff/uploads/templates/unknown", "/bff/exams/12/imports/unknown/template"} {
		if request("GET", path).Code != 501 {
			t.Fatal("unsupported template route exposed", path)
		}
	}
	old := revision
	revision = "obsolete-context"
	if request("GET", "/bff/exams/12/imports/outside/template").Code != 409 || calls != before {
		t.Fatal("stale template read reached owner")
	}
	revision = old
	ownerStatus, contentType, payload = 403, "application/json", `{"error":"access_denied"}`
	w = request("GET", "/bff/exams/12/imports/questions/template")
	if w.Code != 403 || calls != before+1 {
		t.Fatal("live template denial altered or retried", w.Code)
	}
	ownerStatus, contentType, payload = 200, "text/html", "<html>bad</html>"
	if request("GET", "/bff/exams/12/imports/questions/template").Code != 502 {
		t.Fatal("non-template MIME accepted")
	}
	ownerStatus = 302
	before = calls
	if request("GET", "/bff/exams/12/imports/questions/template").Code != 502 || calls != before+1 {
		t.Fatal("template redirect followed")
	}
}

func TestTestsTemplateDataEnvelope(t *testing.T) {
	for _, tc := range []struct {
		body       string
		otp, valid bool
	}{
		{`{"title":"sample","questions":[{}]}`, false, true},
		{`var test = {"Name":"sample","Questions":[{}]};`, true, true},
		{`{"error":"unavailable"}`, false, false},
		{`{"title":"sample","questions":[]}`, false, false},
		{`null`, false, false},
		{`var test = {"Name":"sample","Questions":[{}]}; alert(1);`, true, false},
		{`var different = {"Name":"sample","Questions":[{}]};`, true, false},
		{`{"Name":"sample","Questions":[{}]}`, true, false},
	} {
		if got := validTestTemplate([]byte(tc.body), tc.otp); got != tc.valid {
			t.Errorf("template envelope otp=%v got=%v want=%v", tc.otp, got, tc.valid)
		}
	}
}
