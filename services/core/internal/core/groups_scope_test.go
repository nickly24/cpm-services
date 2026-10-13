package core

import (
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func groupScopeStep() scopeGuardStep {
	return scopeGuardStep{contains: []string{"g.id=? AND x.organization_id=? AND x.division_id=? AND x.is_active=1"}, args: []any{int64(4), int64(1), int64(2)}, columns: []string{"id", "name"}, rows: [][]driver.Value{{int64(4), "DEV Group"}}}
}
func TestGroupMembersWithoutUsersViewNeverProjectsContacts(t *testing.T) {
	for _, role := range []string{"staff_admin", "proctor", "student"} {
		t.Run(role, func(t *testing.T) {
			g := int64(4)
			a := Actor{AccountID: 10, OrganizationID: 1, Role: role, GroupID: &g}
			steps := []scopeGuardStep{groupScopeStep()}
			if role == "staff_admin" {
				a.Permissions = map[string]Permission{"groups": {View: true}}
			} else {
				step := scopeGuardStep{contains: []string{"x.user_id=? AND x.organization_id=? AND x.division_id=? AND x.group_id=? AND u.role=?", "ast.is_active=1"}, args: []any{int64(10), int64(1), int64(2), int64(4), role}, columns: []string{"group_id"}, rows: [][]driver.Value{{g}}}
				if role == "proctor" {
					step.contains = append(step.contains, "p.user_id=x.user_id AND p.division_id=x.division_id AND p.group_id=x.group_id")
				}
				steps = append(steps, step)
			}
			roster := scopeGuardStep{contains: []string{"SELECT a.id account_id,a.ref_id profile_id,a.role,p.full_name,school_scope.school_id school_id,p.`class`,x.group_id,s.is_active", "school_scope.organization_id=x.organization_id", "x.organization_id=? AND x.division_id=? AND x.group_id=?", "LIMIT ? OFFSET ?"}, notContain: []string{"a.username", "core_profile_extra", "x.role_id"}, args: []any{int64(1), int64(2), int64(4), int64(2), int64(3)}, columns: []string{"account_id", "profile_id", "role", "full_name", "school_id", "class", "group_id", "is_active"}, rows: [][]driver.Value{{int64(11), int64(21), "student", "DEV Peer", nil, int64(8), g, int64(1)}}}
			if role == "student" {
				roster.contains = append(roster.contains, "AND a.id=?")
				roster.args = []any{int64(1), int64(2), int64(4), int64(10), int64(2), int64(3)}
				roster.rows[0][0], roster.rows[0][3] = int64(10), "DEV Self"
			} else {
				roster.notContain = append(roster.notContain, "AND a.id=?")
			}
			steps = append(steps, roster)
			db, _ := scopeGuardDB(t, steps)
			r := httptest.NewRequest("GET", "/?limit=2&offset=3", nil)
			r.SetPathValue("group", "4")
			v, code, e := (&Server{DB: db}).groupMembers(r, a, 2)
			if e != nil || code != 200 {
				t.Fatal(code, e)
			}
			row := v.([]map[string]any)[0]
			if row["account_id"] == row["profile_id"] || row["class"] != int64(8) || row["school_id"] != nil || row["is_active"] != true {
				t.Fatal("identity or nullable fields lost", row)
			}
			for _, key := range []string{"login", "password", "tg_name", "role_id"} {
				if _, exists := row[key]; exists {
					t.Fatal("private field disclosed", key)
				}
			}
		})
	}
}
func TestGroupMembersWithUsersViewPreservesAuthorizedDTO(t *testing.T) {
	db, _ := scopeGuardDB(t, []scopeGuardStep{groupScopeStep(), {contains: []string{"a.username login", "tg_name", "x.role_id", "x.organization_id=? AND x.division_id=? AND x.group_id=?"}, args: []any{int64(1), int64(2), int64(4), int64(100), int64(0)}, columns: []string{"account_id", "profile_id", "login", "tg_name", "role_id"}, rows: [][]driver.Value{{int64(11), int64(21), "dev_peer", "dev_peer_contact", nil}}}})
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("group", "4")
	v, _, e := (&Server{DB: db}).groupMembers(r, Actor{Role: "staff_admin", OrganizationID: 1, Permissions: map[string]Permission{"users": {View: true}}}, 2)
	if e != nil || v.([]map[string]any)[0]["login"] != "dev_peer" {
		t.Fatal("authorized user DTO changed", e)
	}
}
func TestGroupMembersRejectStaleOwnGroupBeforeReadingRoster(t *testing.T) {
	for _, role := range []string{"proctor", "student"} {
		t.Run(role, func(t *testing.T) {
			g := int64(4)
			db, _ := scopeGuardDB(t, []scopeGuardStep{groupScopeStep(), {contains: []string{"x.organization_id=? AND x.division_id=? AND x.group_id=?"}, args: []any{int64(10), int64(1), int64(2), int64(4), role}, columns: []string{"group_id"}}})
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("group", "4")
			if _, _, e := (&Server{DB: db}).groupMembers(r, Actor{AccountID: 10, OrganizationID: 1, Role: role, GroupID: &g}, 2); e != denied {
				t.Fatal("stale own group accepted", e)
			}
		})
	}
}
func TestGroupMembersRejectForeignScopedGroupBeforeAuthorization(t *testing.T) {
	step := groupScopeStep()
	step.rows = nil
	db, _ := scopeGuardDB(t, []scopeGuardStep{step})
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("group", "4")
	if _, _, e := (&Server{DB: db}).groupMembers(r, Actor{OrganizationID: 1, OrganizationAdmin: true}, 2); e != missing {
		t.Fatal("foreign group accepted", e)
	}
}

func TestStudentOwnGroupHTTPReturnsSelfBeforeSearchAndPagination(t *testing.T) {
	for _, test := range []struct {
		name, query, search string
		offset              int64
		found               bool
	}{
		{name: "own_only_despite_claimed_peer", query: "account_id=11", found: true},
		{name: "peer_search_cannot_enumerate", query: "q=DEV+Peer", search: "DEV Peer"},
		{name: "offset_after_self_is_empty", query: "offset=1", offset: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			g, d := int64(4), int64(2)
			actor := Actor{AccountID: 10, ID: 21, OrganizationID: 1, DivisionID: &d, GroupID: &g, Role: "student"}
			steps := []scopeGuardStep{
				{contains: []string{"SELECT id FROM divisions WHERE id=? AND organization_id=? AND is_active=1"}, args: []any{d, int64(1)}, columns: []string{"id"}, rows: [][]driver.Value{{d}}},
				groupScopeStep(),
				{contains: []string{"x.user_id=? AND x.organization_id=? AND x.division_id=? AND x.group_id=? AND u.role=?", "ast.is_active=1"}, args: []any{int64(10), int64(1), d, g, "student"}, columns: []string{"group_id"}, rows: [][]driver.Value{{g}}},
			}
			roster := scopeGuardStep{contains: []string{"a.id account_id,a.ref_id profile_id", "WHERE x.organization_id=? AND x.division_id=? AND x.group_id=? AND a.id=?", "ORDER BY a.id LIMIT ? OFFSET ?"}, notContain: []string{"a.username", "p.tg_name", "core_profile_extra", "x.role_id", "password"}, args: []any{int64(1), d, g, int64(10)}, columns: []string{"account_id", "profile_id", "role", "full_name", "school_id", "class", "group_id", "is_active"}}
			if test.search != "" {
				roster.contains = append(roster.contains, "AND a.id=? AND p.full_name LIKE ?")
				roster.args = append(roster.args, "%"+test.search+"%")
			}
			roster.args = append(roster.args, int64(100), test.offset)
			if test.found {
				roster.rows = [][]driver.Value{{int64(10), int64(21), "student", "DEV Self", nil, int64(7), g, int64(1)}}
			}
			steps = append(steps, roster)
			db, _ := scopeGuardDB(t, steps)
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/auth/introspect" || r.Header.Get("X-Role") != "" {
					t.Error("group identity bypassed live native authentication")
				}
				_ = json.NewEncoder(w).Encode(Identity{Status: true, Active: true, User: actor})
			}))
			defer auth.Close()
			s := &Server{DB: db, Auth: NewAuthClient(Config{AuthURL: auth.URL})}
			r := httptest.NewRequest("GET", "/v1/core/divisions/2/groups/4/members?"+test.query, nil)
			r.Header.Set("Authorization", "Bearer synthetic-student")
			r.Header.Set("X-Role", "admin")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			var envelope struct {
				Status bool             `json:"status"`
				Data   []map[string]any `json:"data"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || !envelope.Status {
				t.Fatal("student own-group read failed", w.Code)
			}
			if test.found {
				if len(envelope.Data) != 1 || envelope.Data[0]["account_id"] != float64(10) || envelope.Data[0]["profile_id"] != float64(21) || envelope.Data[0]["full_name"] != "DEV Self" {
					t.Fatal("peer row or incorrect account/profile identity exposed", envelope.Data)
				}
			} else if len(envelope.Data) != 0 {
				t.Fatal("search or pagination exposed a peer", envelope.Data)
			}
			for _, field := range []string{"login", "password", "hash", "tg_name", "role_id", "DEV Peer"} {
				if strings.Contains(w.Body.String(), field) {
					t.Fatal("private peer/contact field in own-group HTTP response", field)
				}
			}
		})
	}
}
