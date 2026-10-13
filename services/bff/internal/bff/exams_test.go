package bff

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestExamOpenAPIMatchesPublicBoundary(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name, In string
				Required bool
			}
		}
		Components struct{ Schemas map[string]json.RawMessage }
	}
	if err := json.Unmarshal(openAPI, &spec); err != nil {
		t.Fatal(err)
	}
	placeholders := regexp.MustCompile(`\{([^}]+)\}`)
	count := 0
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			if !oneHTTPMethod(method) {
				continue
			}
			names := map[string]bool{}
			for _, p := range operation.Parameters {
				if p.In == "path" && p.Required {
					names[p.Name] = true
				}
			}
			matches := placeholders.FindAllStringSubmatch(path, -1)
			if len(names) != len(matches) {
				t.Errorf("%s %s mismatched path parameter count", method, path)
			}
			concrete := placeholders.ReplaceAllStringFunc(path, func(v string) string {
				name := v[1 : len(v)-1]
				if !names[name] {
					t.Errorf("%s %s missing path parameter %s", method, path, name)
				}
				if name == "session" {
					return "12345678-abcd-4321-abcd-123456789abc"
				}
				if name == "kind" {
					return "questions"
				}
				return "1"
			})
			if !strings.HasPrefix(path, "/bff/exams") || path == "/bff/exams/capabilities" {
				continue
			}
			count++
			if examsRoute(strings.ToUpper(method), strings.TrimPrefix(strings.TrimPrefix(concrete, "/bff/exams"), "/")) == "" {
				t.Errorf("documented route not permitted: %s %s", method, path)
			}
		}
	}
	if count != 62 {
		t.Fatalf("unexpected public owner operations: %d", count)
	}
	refs := regexp.MustCompile(`"\$ref"\s*:\s*"#/components/schemas/([^"]+)"`)
	for _, match := range refs.FindAllSubmatch(openAPI, -1) {
		if _, ok := spec.Components.Schemas[string(match[1])]; !ok {
			t.Errorf("dangling schema reference %s", match[1])
		}
	}
	if strings.Contains(string(openAPI), "/internal/exams") {
		t.Fatal("internal API exposed")
	}
}
func oneHTTPMethod(method string) bool {
	return method == "get" || method == "post" || method == "put" || method == "patch" || method == "delete"
}

func TestExamAllowlist(t *testing.T) {
	for _, request := range []struct{ method, tail string }{
		{"GET", ""}, {"POST", ""}, {"POST", "12/attempts/34/next-question"}, {"POST", "12/attempts/34/replace-question"},
		{"GET", "12/history/45/delete-preview"}, {"DELETE", "12"}, {"DELETE", "12/connections/23"},
		{"GET", "12/imports/questions/template"},
		{"POST", "12/imports/outside"}, {"PUT", "12/imports/questions/12345678-abcd-4321-abcd-123456789abc"},
		{"POST", "12/imports/assignments/12345678-abcd-4321-abcd-123456789abc/commit"}, {"PUT", "12/training/progress/23"},
	} {
		if got := examsRoute(request.method, request.tail); got == "" {
			t.Errorf("public request denied: %+v", request)
		}
	}
	for _, request := range []struct{ method, tail string }{
		{"GET", "internal/rating-snapshot"}, {"GET", "12/receipts"}, {"GET", "0"}, {"GET", "01"}, {"GET", "12/attempts/0"},
		{"GET", "12/attempts/34/vote"}, {"PATCH", "12/config"}, {"POST", "12/imports/unknown"},
		{"GET", "12/imports/outside/123"}, {"PUT", "12/imports/outside/../config"}, {"GET", "12//config"}, {"GET", "12/config/"},
	} {
		if got := examsRoute(request.method, request.tail); got != "" {
			t.Errorf("private/malformed request allowed: %+v", request)
		}
	}
}

func TestExamImportPermissionIsIndependent(t *testing.T) {
	for _, tc := range []struct {
		actor string
		want  bool
	}{
		{`{"role":"staff_admin","permissions":{"exams":{"view":true,"edit":true},"upload":{"view":true,"edit":false}}}`, false},
		{`{"role":"staff_admin","permissions":{"exams":{"view":true,"edit":true},"upload":{"edit":true}}}`, true},
		{`{"role":"staff_admin","permissions":{"exams":{"view":true,"edit":false},"upload":{"edit":true}}}`, false},
		{`{"role":"admin","organization_admin":true}`, true},
	} {
		if got := examsCapabilities(json.RawMessage(tc.actor))["canImportExams"]; got != tc.want {
			t.Fatalf("independent import permission: %s got %v", tc.actor, got)
		}
	}
}

func TestExamTransportBoundary(t *testing.T) {
	type captured struct {
		path, auth, key, confirmation, contentType, serviceKey string
		body                                                   []byte
	}
	var calls []captured
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			user := map[string]any{"account_id": 9, "id": 999, "role": "staff_admin", "organization_id": 1001, "division_id": 1002, "permissions": map[string]any{"exams": map[string]bool{"view": true, "edit": false}}}
			body := map[string]any{"status": true, "user": user, "organizations": []any{}}
			if r.URL.Path == "/v1/auth/login" {
				body["access_token"] = "server-access"
				body["refresh_token"] = "server-refresh"
				body["expires_in"] = 900
			}
			json.NewEncoder(w).Encode(body)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, captured{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("X-Exam-Confirmation"), r.Header.Get("Content-Type"), r.Header.Get("X-Service-Key"), raw})
		w.Write([]byte(`{"status":true,"data":{"items":[]}}`))
	}))
	defer owner.Close()
	b := New(Config{Auth: owner.URL, Exams: owner.URL, Origin: "http://127.0.0.1:3001"})
	var cookie *http.Cookie
	var rev string
	request := func(method, path, contentType string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", rev)
		r.Header.Set("Origin", b.config.Origin)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Authorization", "Bearer client-injected")
		r.Header.Set("X-Service-Key", "must-not-forward")
		r.Header.Set("Idempotency-Key", "12345678-abcd-4321-abcd-123456789abc")
		r.Header.Set("X-Exam-Confirmation", "fresh-preview")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	login := request("POST", "/bff/auth/login", "application/json", []byte(`{}`))
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body.String())
	}
	cookie = login.Result().Cookies()[0]
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(login.Body.Bytes(), &identity)
	rev = identity.Revision
	cap := request("GET", "/bff/exams/capabilities", "", nil)
	if cap.Code != 200 || !strings.Contains(cap.Body.String(), `"canReadAdminExams":true`) || !strings.Contains(cap.Body.String(), `"canCreateClassic":false`) {
		t.Fatal("permission projection", cap.Body.String())
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, _ := writer.CreateFormFile("file", "questions.xlsx")
	part.Write(bytes.Repeat([]byte("x"), (1<<20)+1024))
	writer.Close()
	w := request("POST", "/bff/exams/12/imports/questions", writer.FormDataContentType(), multipartBody.Bytes())
	if w.Code != 200 {
		t.Fatal("bounded XLSX multipart", w.Code, w.Body.String())
	}
	c := calls[len(calls)-1]
	if c.path != "/v1/exams/12/imports/questions" || c.auth != "Bearer server-access" || c.serviceKey != "" || c.key == "" || c.contentType != writer.FormDataContentType() || !bytes.Equal(c.body, multipartBody.Bytes()) {
		t.Fatal("multipart not preserved or credentials leaked")
	}
	w = request("DELETE", "/bff/exams/12", "application/json", []byte(`{"expected_revision":3}`))
	if w.Code != 200 || calls[len(calls)-1].confirmation != "fresh-preview" {
		t.Fatal("delete confirmation dropped", w.Code)
	}
	before := len(calls)
	for _, path := range []string{"/bff/exams?division_id=999", "/bff/exams?organization_id=999", "/bff/exams?url=http://example.com"} {
		if request("GET", path, "", nil).Code != 400 {
			t.Fatal("context injection", path)
		}
	}
	if request("POST", "/bff/exams/12/config", writer.FormDataContentType(), []byte("small multipart")).Code != 400 {
		t.Fatal("multipart admitted on wrong route")
	}
	if request("POST", "/bff/exams/12/imports/questions", "multipart/form-data", multipartBody.Bytes()).Code != 400 {
		t.Fatal("invalid boundary accepted")
	}
	if request("POST", "/bff/exams/12/imports/questions", writer.FormDataContentType(), bytes.Repeat([]byte("x"), (18<<20)+1)).Code != 413 {
		t.Fatal("unbounded upload")
	}
	if request("GET", "/bff/exams/internal/rating-snapshot", "", nil).Code != 501 {
		t.Fatal("internal endpoint exposed")
	}
	if len(calls) != before {
		t.Fatal("rejected request reached owner")
	}
}
