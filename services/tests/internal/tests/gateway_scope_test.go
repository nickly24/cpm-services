package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewaySelectedActiveCabinetReusesEntryAuthScope(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		for _, active := range []bool{true, false} {
			calls := 0
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"status": true, "data": References{Organization: Reference{ID: 7}, Divisions: []Reference{{ID: 10, Active: active}}}})
			}))
			division := int64(10)
			actor := Actor{AccountID: 12, OrganizationID: 7, DivisionID: &division, Role: "student"}
			server := &Server{Dependencies: &Dependencies{CoreURL: core.URL, HTTP: core.Client()}}
			r := httptest.NewRequest("POST", "/v1/tests/attempts/0123456789abcdef01234567/commits", nil)
			if trusted {
				r = r.WithContext(context.WithValue(r.Context(), gatewayContextKey{}, true))
			}
			err := server.scope(r, actor, division)
			core.Close()
			if trusted && (err != nil || calls != 0) {
				t.Fatalf("entry Auth scope repeated: err=%v calls=%d", err, calls)
			}
			if !trusted && (calls != 1 || (err == nil) != active) {
				t.Fatalf("direct caller bypassed live Core scope: active=%v err=%v calls=%d", active, err, calls)
			}
			if err := server.scope(r, actor, 11); err == nil {
				t.Fatal("cabinet membership fence bypassed")
			}
		}
	}
}
