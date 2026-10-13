package exams

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type boundaryIdentity struct {
	actor Actor
	err   error
}

func (b boundaryIdentity) Identify(*http.Request) (Actor, error) { return b.actor, b.err }
func (b boundaryIdentity) References(*http.Request, int64, []int64, int64, []int64) (map[int64]Member, error) {
	return nil, nil
}
func (b boundaryIdentity) Ready(*http.Request) error { return nil }

func TestHTTPRejectsClaimsAndMalformedRequestsBeforeStorage(t *testing.T) {
	a := Actor{Role: "admin", OrganizationAdmin: true, OrganizationID: 1, AccountID: 2, DivisionID: ptr(3)}
	for _, tc := range []struct {
		method, path, raw string
		code              int
	}{
		{"GET", "/v1/exams?division_id=999", "", 400},
		{"GET", "/v1/exams?organization_id=999", "", 400},
		{"GET", "/v1/exams?limit=1&limit=2", "", 400},
		{"GET", "/v1/exams/0", "", 400},
		{"POST", "/v1/exams/1/parts", `{"code":"A","code":"B"}`, 400},
		{"POST", "/v1/exams/1/parts", `null`, 400},
		{"POST", "/v1/exams/1/parts", `{} {}`, 400},
		{"POST", "/v1/exams/1/parts", "{}", 415},
		{"GET", "/internal/exams/rating-snapshot?organization_id=1&division_ids=3", "", 401},
	} {
		t.Run(tc.method+tc.path+tc.raw, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.raw))
			if tc.raw != "{}" {
				r.Header.Set("Content-Type", "application/json")
			}
			r.Header.Set("X-Organization-ID", "999")
			r.Header.Set("X-Role", "admin")
			w := httptest.NewRecorder()
			(&Server{Deps: boundaryIdentity{actor: a}, InternalKey: strings.Repeat("k", 32)}).Handler().ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
			var out map[string]any
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out["status"] != false {
				t.Fatal("non-JSON error")
			}
		})
	}
	r := httptest.NewRequest("GET", "/v1/exams", nil)
	r.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()
	(&Server{Deps: boundaryIdentity{err: &APIError{401, "invalid_token"}}}).Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("headers authorized an anonymous request")
	}
}

func TestHTTPReceiptHashIncludesTrainingPartAndDivision(t *testing.T) {
	a := Actor{DivisionID: ptr(4)}
	r := httptest.NewRequest("PUT", "/v1/exams/1/training/settings?part_id=2", nil)
	h := requestHash(r, a, []byte(`{"batch_size":10}`))
	r2 := httptest.NewRequest("PUT", "/v1/exams/1/training/settings?part_id=3", nil)
	if h == requestHash(r2, a, []byte(`{"batch_size":10}`)) {
		t.Fatal("different training parts share receipt")
	}
	a.DivisionID = ptr(5)
	if h == requestHash(r, a, []byte(`{"batch_size":10}`)) {
		t.Fatal("different divisions share receipt")
	}
}

func TestHTTPSharedContentScopeAndIndependentBankCopy(t *testing.T) {
	e := &Exam{OrganizationID: 1, OwnerDivisionID: 3, InBank: true, Divisions: map[int64]*Division{3: {ID: 3}, 4: {ID: 4}}}
	a := Actor{Role: "staff_admin", OrganizationID: 1, DivisionID: ptr(3), DivisionIDs: []int64{3}, Permissions: map[string]Permission{"exams": {View: true, Edit: true}}}
	p := Params{DivisionID: 3}
	if scope(e, a, p, true, true) == nil {
		t.Fatal("staff edited a shared exam without access to other division")
	}
	if scope(e, a, p, true, false) != nil {
		t.Fatal("own division operations denied")
	}
	a.DivisionID = ptr(5)
	p.DivisionID = 5
	if operationScope(e, a, p, "copy", true) != nil {
		t.Fatal("bank copy into selected division denied")
	}
	if operationScope(e, a, p, "assignment-create", true) == nil {
		t.Fatal("bank access grants operational rights")
	}
	e.OrganizationID = 2
	if operationScope(e, a, p, "copy", true) == nil {
		t.Fatal("foreign organization bank copy")
	}
}
