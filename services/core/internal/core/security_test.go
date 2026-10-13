package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthFailsClosed(t *testing.T) {
	for _, status := range []int{401, 403, 500, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer upstream.Close()
			client := NewAuthClient(Config{AuthURL: upstream.URL})
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer any")
			req.Header.Set("X-Role", "admin")
			_, e := client.Identify(req)
			if e == nil {
				t.Fatal("dependency failure authorized request")
			}
		})
	}
}
func TestIdentityIgnoresHeadersAndFalseAdmin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Role") != "" {
			t.Error("forged identity header forwarded")
		}
		_ = json.NewEncoder(w).Encode(Identity{Status: true, Active: true, User: Actor{ID: 12, AccountID: 32, Role: "student", OrganizationID: 4, OrganizationAdmin: true}})
	}))
	defer upstream.Close()
	client := NewAuthClient(Config{AuthURL: upstream.URL})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer any")
	req.Header.Set("X-Role", "admin")
	a, e := client.Identify(req)
	if e != nil || a.OrganizationAdmin || a.Can("users", true) {
		t.Fatal("ordinary account promoted")
	}
}
func TestPasswordHashDoesNotFollowRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("secret sent to redirected endpoint") }))
	defer other.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer upstream.Close()
	client := NewAuthClient(Config{AuthURL: upstream.URL, InternalKey: strings.Repeat("x", 32)})
	if _, e := client.Hash(context.Background(), "password123"); e == nil {
		t.Fatal("redirect accepted")
	}
}
func TestImportStrictColumns(t *testing.T) {
	if _, e := importRows([][]string{{"full_name", "login", "password", "organization_id"}, {"DEV Test", "login", "password123", "1"}}); e == nil {
		t.Fatal("scope field accepted from spreadsheet")
	}
	if _, e := importRows([][]string{{"full_name", "login", "password", "login"}, {"DEV Test", "login", "password123", "other"}}); e == nil {
		t.Fatal("duplicate header accepted")
	}
}
func TestJSONRejectsUnknownScope(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"account_id":1,"organization_id":999}`))
	r.Header.Set("Content-Type", "application/json")
	if e := decode(r, &GroupMemberDTO{}); e == nil {
		t.Fatal("unknown scope override accepted")
	}
}
