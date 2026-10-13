package core

import (
	"database/sql/driver"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func schoolOrganizationStep(org int64) scopeGuardStep {
	return scopeGuardStep{contains: []string{"organizations WHERE id=? AND is_active=1 FOR UPDATE"}, args: []any{org}, columns: []string{"id"}, rows: [][]driver.Value{{org}}}
}
func TestSchoolCreateScopesNameAndDatabaseOwnershipToOrganization(t *testing.T) {
	for _, org := range []int64{1, 3} {
		t.Run(fmt.Sprintf("organization_%d", org), func(t *testing.T) {
			db, c := scopeGuardDB(t, []scopeGuardStep{
				schoolOrganizationStep(org),
				countStep("x.organization_id=? AND s.name=? AND s.id<>?", 0, org, "DEV Same school", int64(0)),
				{exec: true, contains: []string{"INSERT INTO schools(organization_id,name,short_name,notes,is_active) VALUES(?,?,?,?,?)"}, args: []any{org, "DEV Same school", "DS", "", true}, id: 5},
				{exec: true, contains: []string{"INSERT INTO core_school_scope(school_id,organization_id)"}, args: []any{int64(5), org}},
				{exec: true, contains: []string{"INSERT INTO core_audit"}, args: []any{int64(9), org, nil, "school.save", int64(5)}},
			})
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":" DEV Same school ","short_name":"DS","notes":"","is_active":true}`))
			r.Header.Set("Content-Type", "application/json")
			v, code, e := (&Server{DB: db}).schoolSave(r, Actor{AccountID: 9, OrganizationID: org, OrganizationAdmin: true}, 0)
			if e != nil || code != 201 || !c.committed || v.(map[string]any)["id"] != int64(5) {
				t.Fatal("school organization lost", code, e)
			}
		})
	}
}
func TestSchoolDuplicateInSameOrganizationDoesNotCreateRecord(t *testing.T) {
	db, c := scopeGuardDB(t, []scopeGuardStep{schoolOrganizationStep(1), countStep("x.organization_id=? AND s.name=? AND s.id<>?", 1, int64(1), "DEV Same school", int64(0))})
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"DEV Same school","is_active":true}`))
	r.Header.Set("Content-Type", "application/json")
	_, _, e := (&Server{DB: db}).schoolSave(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 0)
	api, ok := e.(*APIError)
	if !ok || api.Code != 409 || api.Message != "school_name_exists" || c.committed {
		t.Fatal("duplicate school written", e)
	}
}
func TestSchoolUpdateCannotTargetForeignSchoolEvenWithSameName(t *testing.T) {
	db, c := scopeGuardDB(t, []scopeGuardStep{schoolOrganizationStep(1), {contains: []string{"s.id=? AND x.organization_id=?"}, args: []any{int64(5), int64(1)}, columns: []string{"id"}}})
	r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"name":"DEV Same school","is_active":true}`))
	r.SetPathValue("school", "5")
	r.Header.Set("Content-Type", "application/json")
	if _, _, e := (&Server{DB: db}).schoolSave(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 0); e != missing || c.committed {
		t.Fatal("foreign school changed", e)
	}
}
func TestSchoolWritesStillRequireFullOrganizationAdmin(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	if _, _, e := (&Server{}).schoolSave(r, Actor{Role: "staff_admin", Permissions: map[string]Permission{"schools": {View: true, Edit: true}}}, 0); e != denied {
		t.Fatal("delegated school permission bypassed owner policy", e)
	}
}
