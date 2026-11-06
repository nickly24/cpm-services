package core

import (
	"database/sql/driver"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestExaminerCreationDivisionsAreRestrictedAndDeterministic(t *testing.T) {
	a := Actor{OrganizationAdmin: true}
	got, err := creationDivisions(a, 10, UserDTO{Role: "examinator", DivisionIDs: []int64{20, 10, 20, 5}})
	if err != nil || !reflect.DeepEqual(got, []int64{5, 10, 20}) {
		t.Fatal(got, err)
	}
	for _, in := range []UserDTO{{Role: "student", DivisionIDs: []int64{20}}, {Role: "examinator", DivisionIDs: []int64{0}}, {Role: "examinator", DivisionIDs: make([]int64, 101)}} {
		if _, err = creationDivisions(a, 10, in); err == nil {
			t.Fatal("accepted invalid division selection")
		}
	}
	if _, err = creationDivisions(Actor{Role: "staff_admin"}, 10, UserDTO{Role: "examinator", DivisionIDs: []int64{20}}); err == nil {
		t.Fatal("delegated actor granted staff access")
	}
}

func TestExamParticipantsPermissionsAndBoundedQueries(t *testing.T) {
	s := &Server{}
	for _, a := range []Actor{{Role: "student"}, {Role: "examinator"}, {Role: "staff_admin", Permissions: map[string]Permission{"exams": {Edit: true}}}} {
		if _, _, err := s.examParticipants(httptest.NewRequest("GET", "/", nil), a, 10); err == nil {
			t.Fatal("participant data exposed")
		}
	}
	a := Actor{Role: "staff_admin", Permissions: map[string]Permission{"exams": {View: true}}}
	for _, query := range []string{"limit=201", "offset=-1", "after_id=-1", "after_id=5&offset=1", "role=admin", "after_id=oops"} {
		if _, _, err := s.examParticipants(httptest.NewRequest("GET", "/?"+query, nil), a, 10); err == nil {
			t.Fatal("invalid query accepted", query)
		}
	}
}

func TestExamParticipantsSQLScopeAndSafeProjection(t *testing.T) {
	fragments := []string{"s.is_active=1", "o.organization_id=x.organization_id", "d.is_active=1", "x.organization_id=? AND x.division_id=?", "a.role=?"}
	db, c := scopeGuardDB(t, []scopeGuardStep{
		{contains: append([]string{"a.id account_id,a.ref_id profile_id", "a.id>?", "ORDER BY a.id LIMIT ? OFFSET ?"}, fragments...), notContain: []string{"password", "tg_name", "school_id"}, args: []any{int64(1), int64(10), "examinator", int64(42), int64(2), int64(0)}, columns: []string{"account_id", "profile_id"}, rows: [][]driver.Value{{int64(50), int64(99)}}},
	})
	body, code, err := (&Server{DB: db}).examParticipants(httptest.NewRequest("GET", "/?role=examinator&limit=2&after_id=42", nil), Actor{OrganizationID: 1, OrganizationAdmin: true}, 10)
	if err != nil || code != 200 || !c.committed {
		t.Fatal(code, err)
	}
	page := body.(map[string]any)
	if _, exists := page["total"]; exists || len(page["items"].([]map[string]any)) != 1 {
		t.Fatal(page)
	}
}
