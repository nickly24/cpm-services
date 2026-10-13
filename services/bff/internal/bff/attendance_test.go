package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttendanceAllowlistAndFilters(t *testing.T) {
	for _, r := range []struct{ method, tail string }{{"GET", "me/schedule"}, {"GET", "schedule"}, {"POST", "schedule/bulk"}, {"PUT", "divisions/1002/calendar"}, {"POST", "divisions/1002/class-days/8/scan"}, {"GET", "divisions/1002/zaps/4/images/1"}, {"DELETE", "divisions/1002/zaps/4/dates/2026-10-08"}} {
		if attendanceRoute(r.method, r.tail) == "" {
			t.Fatal(r)
		}
	}
	for _, tail := range []string{"internal/history", "divisions/0/schedule", "divisions/01/schedule", "divisions/1001/memberships/9", "schedule/../audit"} {
		if attendanceRoute("GET", tail) != "" {
			t.Fatal(tail)
		}
	}
}
func TestAttendanceReceiptIsOneCanonicalRead(t *testing.T) {
	path := "command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd"
	if attendanceRoute("GET", path) != "/v1/attendance/"+path {
		t.Fatal("protected receipt read not mapped")
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if attendanceRoute(method, path) != "" {
			t.Fatal("receipt mutation exposed", method)
		}
	}
	for _, invalid := range []string{"command-receipts", path + "/replay", "command-receipts/invalid", strings.ToUpper(path)} {
		if attendanceRoute("GET", invalid) != "" {
			t.Fatal("invalid receipt route exposed", invalid)
		}
	}
}
func TestAttendanceContextAndBinaryBoundary(t *testing.T) {
	ownerCalls := 0
	lastKey := ""
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			out := map[string]any{"status": true, "user": map[string]any{"division_id": 1001, "organization_id": 1001}, "organizations": []any{}}
			if strings.HasSuffix(r.URL.Path, "login") {
				out["access_token"] = "fixture-access"
				out["refresh_token"] = "fixture-refresh"
				out["expires_in"] = 900
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		ownerCalls++
		if r.Header.Get("X-Service-Key") != "" || r.Header.Get("X-Operation-Grant") != "" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("invalid boundary")
		}
		lastKey = r.Header.Get("Idempotency-Key")
		switch r.URL.Path {
		case "/v1/attendance/me/schedule":
		case "/v1/attendance/divisions/1002/calendar":
		case "/v1/attendance/command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd":
		case "/v1/attendance/divisions/1002/zaps/8/images/1":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte{137, 80, 78, 71})
			return
		default:
			t.Error("unexpected", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":true,"data":{"revision":1,"value":[]}}`))
	}))
	defer owner.Close()
	s := New(Config{Auth: owner.URL, Attendance: owner.URL, Origin: "http://127.0.0.1:3001"})
	var cookie *http.Cookie
	rev := ""
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", s.config.Origin)
		r.Header.Set("X-CPM-Request", "1")
		r.Header.Set("X-CPM-Context", rev)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Service-Key", "untrusted-browser-service-key")
		r.Header.Set("X-Operation-Grant", "untrusted-browser-operation-grant")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/bff/auth/login", "{}", "")
	cookie = w.Result().Cookies()[0]
	var identity struct {
		Revision string `json:"context_revision"`
	}
	json.Unmarshal(w.Body.Bytes(), &identity)
	rev = identity.Revision
	if call("GET", "/bff/attendance/me/schedule?date_from=2026-10-08&date_to=2026-10-08", "", "").Code != 200 {
		t.Fatal("personal")
	}
	if call("PUT", "/bff/attendance/divisions/1002/calendar", `{"expected_revision":0,"data":{"visibility":"closed","timezone":"Europe/Moscow"}}`, "same-request-key").Code != 200 || lastKey != "same-request-key" {
		t.Fatal("delegated division changed by BFF")
	}
	w = call("GET", "/bff/attendance/divisions/1002/zaps/8/images/1", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "iVBORw==") {
		t.Fatal("protected image", w.Code, w.Body.String())
	}
	if call("GET", "/bff/attendance/command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd", "", "").Code != 200 {
		t.Fatal("protected receipt read")
	}
	before := ownerCalls
	for _, path := range []string{"/bff/attendance/me/schedule?organization_id=2001", "/bff/attendance/me/schedule?division_id=1002", "/bff/attendance/me/schedule?date_from=a&date_from=b"} {
		if call("GET", path, "", "").Code != 400 {
			t.Fatal(path)
		}
	}
	for _, query := range []string{"actor_account_id=9", "method=POST", "path=/schedule/bulk", "organization_id=2001"} {
		if call("GET", "/bff/attendance/command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd?"+query, "", "").Code != 400 {
			t.Fatal("receipt query reached owner", query)
		}
	}
	for _, query := range []string{"?", "?x=%zz", "?x=1;y=2"} {
		if call("GET", "/bff/attendance/command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd"+query, "", "").Code != 400 {
			t.Fatal("unparsed receipt query reached owner", query)
		}
	}
	if call("GET", "/bff/attendance/command-receipts/fb802334-eedb-4b1b-9668-91bb6052b6cd", "{}", "").Code != 400 || before != ownerCalls {
		t.Fatal("receipt body or scope override reached owner")
	}
	rev = "stale"
	if call("GET", "/bff/attendance/calendars", "", "").Code != 409 || before != ownerCalls {
		t.Fatal("invalid request reached owner")
	}
}
