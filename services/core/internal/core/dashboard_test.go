package core

import (
	"database/sql"
	"database/sql/driver"
	"net/http/httptest"
	"slices"
	"testing"
)

func dashboardNameQuery() ratingSnapshotQuery {
	return ratingSnapshotQuery{contains: []string{"id=? AND organization_id=? AND is_active=1"}, args: []int64{2, 1}, columns: []string{"name"}, rows: [][]driver.Value{{"Division"}}}
}
func TestDashboardHiddenTotalsRemainNull(t *testing.T) {
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{dashboardNameQuery()}}
	db := sql.OpenDB(c)
	defer db.Close()
	s := &Server{DB: db}
	v, code, e := s.dashboard(httptest.NewRequest("GET", "/", nil), Actor{Role: "supervisor", OrganizationID: 1}, 2)
	if e != nil || code != 200 {
		t.Fatal(code, e)
	}
	out := v.(Dashboard)
	if out.Totals.Students != nil || out.Totals.Groups != nil || out.Totals.Staff != nil || out.Totals.Schools != nil || len(c.queries) != 0 {
		t.Fatal("hidden total read or invented zero", out)
	}
}
func TestDashboardProctorChecksLiveScopedGroupAssignment(t *testing.T) {
	g := int64(4)
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{dashboardNameQuery(), {contains: []string{"x.organization_id=? AND x.division_id=? AND x.group_id=? AND p.user_id=?", "g.is_active=1"}, args: []int64{1, 2, 4, 10}, columns: []string{"count"}, rows: [][]driver.Value{{int64(2)}}}, {contains: []string{"g.organization_id=? AND g.division_id=? AND g.group_id=? AND p.user_id=?", "g.is_active=1"}, args: []int64{1, 2, 4, 10}, columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	v, _, e := (&Server{DB: db}).dashboard(httptest.NewRequest("GET", "/", nil), Actor{Role: "proctor", OrganizationID: 1, AccountID: 10, GroupID: &g}, 2)
	if e != nil {
		t.Fatal(e)
	}
	out := v.(Dashboard)
	if *out.Totals.Students != 2 || *out.Totals.Groups != 1 || out.Totals.Staff != nil || out.Totals.Schools != nil || len(c.queries) != 0 {
		t.Fatal(out)
	}
}
func TestDashboardTestResultCapabilityMatchesCanonicalPermission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions map[string]Permission
		allowed     bool
	}{
		{"canonical read only", map[string]Permission{"test-results": {View: true}}, true},
		{"canonical deny overrides alias", map[string]Permission{"test-results": {View: false}, "test_results": {View: true, Edit: true}}, false},
		{"edit alone grants no read", map[string]Permission{"test-results": {Edit: true}}, false},
		{"legacy alias without canonical", map[string]Permission{"test_results": {View: true}}, true},
		{"no result permission", map[string]Permission{"tests": {View: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Actor{Role: "staff_admin", Permissions: tc.permissions}
			if got := slices.Contains(dashboardCapabilities(a), "test_results"); got != tc.allowed {
				t.Fatal("dashboard advertised unavailable result access", got, tc.allowed)
			}
			if reportAllowed(a, "tests") != tc.allowed {
				t.Fatal("dashboard and report access disagree")
			}
		})
	}
}
func TestStudentsRejectRoleEscalationAndStaleProctorGroup(t *testing.T) {
	s := &Server{}
	if _, _, e := s.students(httptest.NewRequest("GET", "/?role=admin", nil), Actor{}, 2); e == nil {
		t.Fatal("student endpoint accepted staff filter")
	}
	v, code, e := s.students(httptest.NewRequest("GET", "/", nil), Actor{Role: "proctor"}, 2)
	if e != nil || code != 200 || len(v.([]map[string]any)) != 0 {
		t.Fatal("unassigned proctor roster not empty")
	}
	g := int64(4)
	c := &ratingSnapshotConnector{queries: []ratingSnapshotQuery{{contains: []string{"g.organization_id=? AND g.division_id=? AND g.group_id=? AND p.user_id=?"}, args: []int64{1, 2, 4, 10}, columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}}}
	db := sql.OpenDB(c)
	defer db.Close()
	v, code, e = (&Server{DB: db}).students(httptest.NewRequest("GET", "/", nil), Actor{Role: "proctor", OrganizationID: 1, AccountID: 10, GroupID: &g}, 2)
	if e != nil || code != 200 || len(v.([]map[string]any)) != 0 || len(c.queries) != 0 {
		t.Fatal("stale proctor assignment disclosed roster", code, e)
	}
}
func TestCoreReportOpenAPIDeclaresNullScoresAndStrictQueries(t *testing.T) {
	paths := openAPI()["paths"].(map[string]any)
	op := paths["/v1/core/divisions/{division}/reports/tests"].(map[string]any)["get"].(map[string]any)
	params := op["parameters"].([]any)
	if len(params) != 3 {
		t.Fatal("reports expose unrelated filters", params)
	}
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	if response["content"] == nil {
		t.Fatal("report response missing schema")
	}
}
