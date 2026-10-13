package bff

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadsBoundaryAndTransport(t *testing.T) {
	id := strings.Repeat("a", 32)
	division, calls, status := 1001, 0, 200
	lastPath, lastContentType, lastKey, lastBody := "", "", "", ""
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			if r.URL.Path == "/v1/auth/context" {
				division = 1002
			}
			identity := map[string]any{"user": map[string]any{"account_id": 9, "organization_id": 7, "division_id": division, "role": "admin", "organization_admin": true}, "organizations": []any{}}
			if r.URL.Path == "/v1/auth/login" || r.URL.Path == "/v1/auth/context" {
				identity["access_token"], identity["refresh_token"], identity["expires_in"] = "access", "refresh", 900
			}
			json.NewEncoder(w).Encode(identity)
			return
		}
		calls++
		lastPath, lastContentType, lastKey = r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key")
		body, _ := io.ReadAll(r.Body)
		lastBody = string(body)
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-Request-ID") == "" || r.Header.Get("X-CPM-Operation-Grant") != "" || r.Header.Get("X-CPM-Operation-Stage") != "" || r.Header.Get("X-CPM-Operation-Job") != "" {
			t.Error("lost session identity or leaked browser service key")
		}
		if status != 200 {
			w.WriteHeader(status)
			w.Write([]byte(`{"status":false,"error":"owner_access_denied"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/download") {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="untrusted-name"`)
			w.Write([]byte("source"))
			return
		}
		w.Write([]byte(`{"status":true,"data":{}}`))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Uploads: owner.URL, Origin: "http://127.0.0.1:3001"})
	var cookie *http.Cookie
	revision := ""
	request := func(method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", revision)
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Idempotency-Key", "one-immutable-command")
		r.Header.Set("X-Service-Key", "untrusted-key")
		r.Header.Set("X-CPM-Operation-Grant", "untrusted-native-credential")
		r.Header.Set("X-CPM-Operation-Stage", "apply")
		r.Header.Set("X-CPM-Operation-Job", id)
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
		var v struct {
			Revision string `json:"context_revision"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		revision = v.Revision
	}
	w := request("POST", "/bff/auth/login", "application/json", strings.NewReader(`{}`))
	update(w)
	cookie = w.Result().Cookies()[0]
	if w = request("GET", "/bff/uploads/jobs?limit=20", "", nil); w.Code != 200 || lastPath != "/v1/uploads/divisions/1001/jobs?limit=20" {
		t.Fatal("scope not injected", w.Code, lastPath)
	}
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	form.WriteField("type", "cards.xlsx")
	file, _ := form.CreateFormFile("file", "source.xlsx")
	file.Write([]byte("xlsx-fixture"))
	form.Close()
	if w = request("POST", "/bff/uploads/sessions", form.FormDataContentType(), bytes.NewReader(buf.Bytes())); w.Code != 200 || lastBody != buf.String() || lastContentType != form.FormDataContentType() || lastKey != "one-immutable-command" {
		t.Fatal("bounded multipart or command identity changed", w.Code, lastContentType, lastKey)
	}
	before := calls
	for _, path := range []string{"/bff/uploads/jobs?division_id=2", "/bff/uploads/catalog?actor_account_id=10", "/bff/uploads/sessions?limit=1&limit=2", "/bff/uploads/jobs/" + id + "?url=http://evil", "/bff/uploads/files/" + id + "/download?organization_id=8"} {
		if request("GET", path, "", nil).Code != 400 {
			t.Fatalf("invalid selector accepted %s", path)
		}
	}
	if calls != before {
		t.Fatal("invalid scope reached uploads")
	}
	for _, path := range []string{"/bff/uploads/internal/jobs", "/bff/uploads/divisions/1002/jobs", "/bff/uploads/sessions/01", "/bff/uploads/homework/9/create"} {
		if request("GET", path, "", nil).Code != 501 {
			t.Fatalf("private/malformed route accepted %s", path)
		}
	}
	if request("POST", "/bff/uploads/jobs/"+id+"/retry", form.FormDataContentType(), bytes.NewReader(buf.Bytes())).Code != 400 {
		t.Fatal("multipart outside session create accepted")
	}
	old := revision
	update(request("POST", "/bff/auth/context", "application/json", strings.NewReader(`{}`)))
	fresh := revision
	revision = old
	if request("POST", "/bff/uploads/sessions/"+id+"/commit", "application/json", strings.NewReader(`{"expected_revision":1}`)).Code != 409 {
		t.Fatal("stale cabinet commit accepted")
	}
	revision = fresh
	if w = request("GET", "/bff/uploads/files/"+id+"/download", "", nil); w.Code != 200 || w.Body.String() != "source" || lastPath != "/v1/uploads/divisions/1002/files/"+id+"/download" || w.Header().Get("Content-Disposition") != `attachment; filename="upload"` {
		t.Fatal("scoped download changed or owner filename reflected", w.Code, lastPath)
	}
	for _, action := range []string{"recover", "revoke"} {
		before = calls
		body := `{"confirmed":true}`
		if w = request("POST", "/bff/uploads/jobs/"+id+"/"+action, "application/json", strings.NewReader(body)); w.Code != 200 || calls != before+1 || lastPath != "/v1/uploads/divisions/1002/jobs/"+id+"/"+action || lastBody != body || lastKey != "one-immutable-command" {
			t.Fatalf("scoped %s command changed or repeated: %d %s", action, w.Code, lastPath)
		}
		if request("POST", "/bff/uploads/jobs/"+id+"/"+action+"?division_id=1001", "application/json", strings.NewReader(body)).Code != 400 || calls != before+1 {
			t.Fatalf("%s accepted a scope override", action)
		}
		if request("GET", "/bff/uploads/jobs/"+id+"/"+action, "", nil).Code != 501 || calls != before+1 {
			t.Fatalf("%s accepted the wrong method", action)
		}
	}
	before = calls
	for _, path := range []string{"/bff/uploads/jobs/01/recover", "/bff/uploads/jobs/" + id + "/grant", "/bff/uploads/internal/operation-grants", "/bff/auth/operation-grants"} {
		if request("POST", path, "application/json", strings.NewReader(`{"confirmed":true}`)).Code != 501 || calls != before {
			t.Fatalf("private/malformed authorization proxy accepted: %s", path)
		}
	}
	status = 403
	before = calls
	if w = request("POST", "/bff/uploads/jobs/"+id+"/retry", "application/json", strings.NewReader(`{}`)); w.Code != 403 || calls != before+1 {
		t.Fatal("owner denial changed or retried", w.Code, calls-before)
	}
}
