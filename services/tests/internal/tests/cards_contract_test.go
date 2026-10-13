package tests

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCardsMembershipUnion(t *testing.T) {
	d := int64(11)
	a := Actor{Role: "student", DivisionID: &d, DivisionIDs: []int64{11, 12}}
	if len(cardsDivisions(a)) != 2 {
		t.Fatal("student catalog follows selected division")
	}
	a.Role = "staff_admin"
	a.Permissions = map[string]Permission{"train": {View: true}}
	if len(cardsDivisions(a)) != 1 || !cardsActor(a) {
		t.Fatal("staff rights crossed divisions")
	}
	a.Permissions = nil
	if cardsActor(a) {
		t.Fatal("staff membership promoted")
	}
}
func TestCardsInternalRequiresBothCredentials(t *testing.T) {
	s := &Server{Config: Config{ServiceKey: strings.Repeat("k", 32)}, Dependencies: NewDependencies(Config{AuthURL: "http://127.0.0.1:1"})}
	mux := http.NewServeMux()
	s.cardsRoutes(mux)
	r := httptest.NewRequest("GET", "/internal/tests/cards/catalog", nil)
	r.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/internal/tests/cards/catalog", nil)
	r.Header.Set("X-Service-Key", s.Config.ServiceKey)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("service key alone bypassed user auth", w.Code)
	}
}
