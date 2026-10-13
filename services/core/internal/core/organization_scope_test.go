package core

import (
	"database/sql/driver"
	"net/http/httptest"
	"strings"
	"testing"
)

func countStep(fragment string, count int64, args ...any) scopeGuardStep {
	return scopeGuardStep{contains: []string{fragment}, args: args, columns: []string{"count"}, rows: [][]driver.Value{{count}}}
}
func lockScopeStep() scopeGuardStep {
	return scopeGuardStep{contains: []string{"id=? AND organization_id=? AND is_active=1 FOR UPDATE"}, args: []any{int64(2), int64(1)}, columns: []string{"id"}, rows: [][]driver.Value{{int64(2)}}}
}
func revokeScopeSteps() []scopeGuardStep {
	return []scopeGuardStep{
		lockScopeStep(),
		{contains: []string{"SELECT role FROM auth_users WHERE id=? FOR UPDATE"}, args: []any{int64(10)}, columns: []string{"role"}, rows: [][]driver.Value{{"student"}}},
		{contains: []string{"WHERE a.id=? AND x.organization_id=? AND x.division_id=?"}, args: []any{int64(10), int64(1), int64(2)}, columns: []string{"account_id"}, rows: [][]driver.Value{{int64(10)}}},
		countStep("auth_division_access WHERE user_id=?", 2, int64(10)),
		countStep("core_attendance_history_reservations WHERE organization_id=? AND division_id=? AND user_id=?", 0, int64(1), int64(2), int64(10)),
		countStep("core_rating_locks WHERE user_id=? AND division_id=?", 0, int64(10), int64(2)),
		countStep("core_legacy_rating_scope WHERE organization_id=? AND division_id=? AND user_id=?", 0, int64(1), int64(2), int64(10)),
	}
}
func TestMembershipWithoutRatingInSelectedDivisionCanBeRemoved(t *testing.T) {
	steps := append(revokeScopeSteps(),
		countStep("core_attendance WHERE user_id=? AND division_id=?", 0, int64(10), int64(2), int64(10), int64(2)),
		scopeGuardStep{exec: true, contains: []string{"DELETE FROM core_proctor_groups WHERE user_id=? AND division_id=?"}, args: []any{int64(10), int64(2)}},
		scopeGuardStep{exec: true, contains: []string{"DELETE FROM auth_division_access WHERE user_id=? AND division_id=?"}, args: []any{int64(10), int64(2)}},
		scopeGuardStep{exec: true, contains: []string{"DELETE FROM auth_organization_access WHERE user_id=? AND organization_id=?", "NOT EXISTS"}, args: []any{int64(10), int64(1), int64(10), int64(1)}},
		scopeGuardStep{exec: true, contains: []string{"INSERT INTO core_audit"}, args: []any{int64(9), int64(1), int64(2), "membership.revoke", int64(10)}},
	)
	db, c := scopeGuardDB(t, steps)
	r := httptest.NewRequest("DELETE", "/", nil)
	r.SetPathValue("user", "10")
	_, code, e := (&Server{DB: db}).revokeGrant(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 2)
	if e != nil || code != 200 || !c.committed {
		t.Fatal("unrelated legacy rating blocked new participation", code, e)
	}
}
func TestMembershipLegacyRatingRetainsOnlyItsMappedDivision(t *testing.T) {
	steps := revokeScopeSteps()
	steps[len(steps)-1].rows = [][]driver.Value{{int64(1)}}
	db, c := scopeGuardDB(t, steps)
	r := httptest.NewRequest("DELETE", "/", nil)
	r.SetPathValue("user", "10")
	_, _, e := (&Server{DB: db}).revokeGrant(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 2)
	api, ok := e.(*APIError)
	if !ok || api.Code != 409 || api.Message != "rating_exists" || c.committed {
		t.Fatal("mapped rating history could be removed", e)
	}
}
func TestMembershipAttendanceReservationStillPreventsRemoval(t *testing.T) {
	steps := revokeScopeSteps()[:5]
	steps[4].rows = [][]driver.Value{{int64(1)}}
	db, c := scopeGuardDB(t, steps)
	r := httptest.NewRequest("DELETE", "/", nil)
	r.SetPathValue("user", "10")
	_, _, e := (&Server{DB: db}).revokeGrant(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 2)
	api, ok := e.(*APIError)
	if !ok || api.Message != "membership_has_history" || c.committed {
		t.Fatal("attendance history guard removed", e)
	}
}

func TestDivisionArchiveRetainsMappedLegacyHistoryAndAttendanceReservations(t *testing.T) {
	for _, source := range []string{"attendance", "legacy_rating"} {
		t.Run(source, func(t *testing.T) {
			steps := []scopeGuardStep{lockScopeStep(), countStep("auth_division_access WHERE division_id=?", 0, int64(2)), countStep("core_attendance_history_reservations WHERE organization_id=? AND division_id=?", 0, int64(1), int64(2))}
			if source == "attendance" {
				steps[2].rows = [][]driver.Value{{int64(1)}}
			} else {
				steps = append(steps, countStep("core_legacy_rating_scope WHERE organization_id=? AND division_id=?", 1, int64(1), int64(2)))
			}
			db, c := scopeGuardDB(t, steps)
			_, _, e := (&Server{DB: db}).divisionArchive(httptest.NewRequest("DELETE", "/", nil), Actor{OrganizationID: 1, OrganizationAdmin: true}, 2)
			api, ok := e.(*APIError)
			if !ok || api.Message != "division_has_history" || c.committed {
				t.Fatal("historical division archived", e)
			}
		})
	}
}
func TestMembershipNewParticipationCanBeGrantedWithUnrelatedHistory(t *testing.T) {
	db, c := scopeGuardDB(t, []scopeGuardStep{
		lockScopeStep(),
		{contains: []string{"a.role,o.organization_id", "a.id=? FOR UPDATE"}, args: []any{int64(10)}, columns: []string{"role", "organization_id"}, rows: [][]driver.Value{{"student", int64(1)}}},
		{exec: true, contains: []string{"INSERT IGNORE INTO auth_organization_access"}, args: []any{int64(10), int64(1)}},
		{exec: true, contains: []string{"INSERT INTO auth_division_access(user_id,organization_id,division_id,role_id)", "ON DUPLICATE KEY UPDATE role_id=VALUES(role_id)"}, args: []any{int64(10), int64(1), int64(2), nil}},
		{exec: true, contains: []string{"INSERT INTO core_audit"}, args: []any{int64(9), int64(1), int64(2), "membership.grant", int64(10)}},
	})
	r := httptest.NewRequest("PUT", "/", strings.NewReader(`{}`))
	r.SetPathValue("user", "10")
	r.Header.Set("Content-Type", "application/json")
	if _, code, e := (&Server{DB: db}).grant(r, Actor{AccountID: 9, OrganizationID: 1, OrganizationAdmin: true}, 2); e != nil || code != 200 || !c.committed {
		t.Fatal("adding participation should not consult other historical ratings", e)
	}
}
func TestOrganizationMembershipsAreOwnOrgAndReadOnlyForForeignOwner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owner  any
		role   string
		assign bool
	}{{"owned", int64(1), "student", true}, {"shared_foreign", int64(3), "student", false}, {"unknown_owner", nil, "student", false}, {"admin", int64(1), "admin", false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, c := scopeGuardDB(t, []scopeGuardStep{
				{contains: []string{"a.id=? AND (o.organization_id=? OR EXISTS", "x.organization_id=?"}, args: []any{int64(10), int64(1), int64(1)}, columns: []string{"account_id", "role", "owner_organization_id"}, rows: [][]driver.Value{{int64(10), tc.role, tc.owner}}},
				{contains: []string{"d.organization_id=m.organization_id", "role.organization_id=m.organization_id", "g.organization_id=m.organization_id AND g.division_id=m.division_id", "WHERE m.organization_id=? AND m.user_id=?"}, args: []any{int64(1), int64(10)}, columns: []string{"division_id", "division_name", "role_id", "role_name", "group_id"}, rows: [][]driver.Value{{int64(2), "DEV Selected", nil, nil, nil}}},
			})
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("user", "10")
			v, code, e := (&Server{DB: db}).organizationUserMemberships(r, Actor{OrganizationAdmin: true, OrganizationID: 1}, 0)
			if e != nil || code != 200 || !c.committed {
				t.Fatal(code, e)
			}
			out := v.(map[string]any)
			if out["can_assign"] != tc.assign || out["owner_organization_id"] != tc.owner || len(out["memberships"].([]map[string]any)) != 1 {
				t.Fatal(out)
			}
			if out["can_revoke"] != (tc.role != "admin") || out["memberships"].([]map[string]any)[0]["can_revoke"] != (tc.role != "admin") || len(out) != 5 {
				t.Fatal("revoke capability or minimal account projection incorrect", out)
			}
		})
	}
}
func TestOrganizationMembershipsRejectDelegatedAndUnrelatedAccounts(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("user", "10")
	if _, _, e := s.organizationUserMemberships(r, Actor{Role: "staff_admin", Permissions: map[string]Permission{"users": {View: true, Edit: true}}}, 0); e != denied {
		t.Fatal("staff bypassed organization-only guard", e)
	}
	db, c := scopeGuardDB(t, []scopeGuardStep{{contains: []string{"a.id=? AND (o.organization_id=? OR EXISTS"}, args: []any{int64(10), int64(1), int64(1)}, columns: []string{"account_id", "role", "owner_organization_id"}}})
	if _, _, e := (&Server{DB: db}).organizationUserMemberships(r, Actor{OrganizationAdmin: true, OrganizationID: 1}, 0); e != missing || c.committed {
		t.Fatal("unrelated account is visible", e)
	}
}
