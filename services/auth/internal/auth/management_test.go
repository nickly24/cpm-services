package auth

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHashEndpointServiceAuthorization(t *testing.T) {
	s := &Server{Config: Config{InternalKey: strings.Repeat("s", 32)}}
	for _, key := range []string{"", strings.Repeat("x", 32)} {
		r := httptest.NewRequest("POST", "/internal/auth/password-hash", strings.NewReader(`{"password":"password123"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Service-Key", key)
		w := httptest.NewRecorder()
		s.hashPassword(w, r)
		if w.Code != 401 {
			t.Fatal("untrusted service authorized")
		}
	}
	r := httptest.NewRequest("POST", "/internal/auth/password-hash", strings.NewReader(`{"password":"password123"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Service-Key", s.Config.InternalKey)
	w := httptest.NewRecorder()
	s.hashPassword(w, r)
	var out struct {
		Hash string `json:"hash"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || !CheckPassword(out.Hash, "password123") || CheckPassword(out.Hash, "other") {
		t.Fatal("invalid generated password hash")
	}
}
