package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthOwnedMutationsDoNotPreflightMe(t *testing.T) {
	for _, operation := range []string{"context", "logout"} {
		for _, status := range []int{200, 401, 403, 503} {
			t.Run(operation+"/"+http.StatusText(status), func(t *testing.T) {
				me, mutations := 0, 0
				owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/v1/auth/login":
						json.NewEncoder(w).Encode(map[string]any{"status": true, "access_token": "access", "refresh_token": "refresh", "expires_in": 900, "user": map[string]any{"account_id": 9, "id": 3, "role": "student", "organization_id": 1, "division_id": 11}, "organizations": []any{}})
					case "/v1/auth/me":
						me++
						w.Write([]byte(`{"status":true,"user":{},"organizations":[]}`))
					case "/v1/auth/" + operation:
						mutations++
						if r.Header.Get("Authorization") != "Bearer access" {
							t.Error("lost authenticated bearer")
						}
						w.WriteHeader(status)
						if status == 200 && operation == "context" {
							w.Write([]byte(`{"status":true,"access_token":"next","expires_in":900,"user":{"organization_id":1,"division_id":12},"organizations":[]}`))
						} else {
							w.Write([]byte(`{"status":true,"error":"denied"}`))
						}
					default:
						t.Error("unexpected Auth call", r.URL.Path)
					}
				}))
				defer owner.Close()
				b := New(Config{Auth: owner.URL, Origin: "http://localhost:3000"})
				req := func(path, body, revision string, cookie *http.Cookie) *httptest.ResponseRecorder {
					r := httptest.NewRequest("POST", "/bff/auth/"+path, strings.NewReader(body))
					r.Header.Set("Origin", b.config.Origin)
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("X-CPM-Request", "1")
					r.Header.Set("X-CPM-Context", revision)
					if cookie != nil {
						r.AddCookie(cookie)
					}
					w := httptest.NewRecorder()
					b.ServeHTTP(w, r)
					return w
				}
				w := req("login", `{}`, "", nil)
				if w.Code != 200 {
					t.Fatal("login failed", w.Code)
				}
				cookie := w.Result().Cookies()[0]
				var identity struct {
					Revision string `json:"context_revision"`
				}
				json.Unmarshal(w.Body.Bytes(), &identity)
				// A stale cabinet still fails before reaching Auth, even without /me.
				if req(operation, `{}`, "stale", cookie).Code != 409 || mutations != 0 {
					t.Fatal("stale cabinet reached Auth")
				}
				w = req(operation, `{"organization_id":1,"division_id":12}`, identity.Revision, cookie)
				want := status
				if operation == "logout" && status == 401 {
					want = 200
				}
				if w.Code != want || mutations != 1 || me != 0 {
					t.Fatalf("status=%d want=%d mutations=%d me=%d", w.Code, want, mutations, me)
				}
			})
		}
	}
}
