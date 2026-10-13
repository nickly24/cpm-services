package core

import (
	"net/http"
	"sort"
)

type DashboardTotals struct {
	Students *int64 `json:"students"`
	Staff    *int64 `json:"staff"`
	Groups   *int64 `json:"groups"`
	Schools  *int64 `json:"schools"`
}
type Dashboard struct {
	OrganizationID int64           `json:"organization_id"`
	DivisionID     int64           `json:"division_id"`
	DivisionName   string          `json:"division_name"`
	Role           string          `json:"role"`
	Totals         DashboardTotals `json:"totals"`
	Capabilities   []string        `json:"capabilities"`
}

func (s *Server) students(r *http.Request, a Actor, d int64) (any, int, error) {
	q := r.URL.Query()
	for key, values := range q {
		if len(values) != 1 || (key != "limit" && key != "offset" && key != "q" && key != "unassigned" && key != "role") {
			return nil, 0, bad("invalid_students_query")
		}
	}
	if q.Get("role") != "" && q.Get("role") != "student" {
		return nil, 0, bad("student_role_required")
	}
	if a.Role == "proctor" && !a.Can("users", false) {
		if a.GroupID == nil {
			return []map[string]any{}, 200, nil
		}
		var assigned int
		e := s.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM core_group_scope g JOIN core_proctor_groups p ON p.group_id=g.group_id AND p.division_id=g.division_id WHERE g.organization_id=? AND g.division_id=? AND g.group_id=? AND p.user_id=? AND g.is_active=1", a.OrganizationID, d, *a.GroupID, a.AccountID).Scan(&assigned)
		if e != nil {
			return nil, 0, e
		}
		if assigned == 0 {
			return []map[string]any{}, 200, nil
		}
	}
	q.Set("role", "student")
	copy := r.Clone(r.Context())
	u := *r.URL
	copy.URL = &u
	copy.URL.RawQuery = q.Encode()
	return s.users(copy, a, d)
}
func dashboardCapabilities(a Actor) []string {
	out := []string{}
	for _, section := range []string{"users", "groups", "schools", "homework", "tests", "exams", "ratings", "test_results"} {
		permissionSection := section
		if section == "test_results" {
			permissionSection = "test-results"
			if _, canonical := a.Permissions[permissionSection]; !canonical {
				permissionSection = "test_results"
			}
		}
		if a.Can(permissionSection, false) {
			out = append(out, section)
		}
	}
	if a.Role == "proctor" {
		out = append(out, "students", "homework", "ratings")
	}
	if a.Role == "supervisor" {
		out = append(out, "reports_tests", "reports_exams", "reports_attendance", "homework", "ratings")
	}
	sort.Strings(out)
	return out
}
func (s *Server) dashboard(r *http.Request, a Actor, d int64) (any, int, error) {
	if len(r.URL.Query()) != 0 {
		return nil, 0, bad("invalid_dashboard_query")
	}
	out := Dashboard{OrganizationID: a.OrganizationID, DivisionID: d, Role: a.Role, Capabilities: dashboardCapabilities(a)}
	if e := s.DB.QueryRowContext(r.Context(), "SELECT name FROM divisions WHERE id=? AND organization_id=? AND is_active=1", d, a.OrganizationID).Scan(&out.DivisionName); e != nil {
		return nil, 0, e
	}
	count := func(query string, args ...any) (*int64, error) {
		var n int64
		e := s.DB.QueryRowContext(r.Context(), query, args...).Scan(&n)
		return &n, e
	}
	var e error
	if a.Can("users", false) {
		out.Totals.Students, e = count("SELECT COUNT(*) FROM auth_division_access x JOIN auth_users u ON u.id=x.user_id WHERE x.organization_id=? AND x.division_id=? AND u.role='student'", a.OrganizationID, d)
		if e != nil {
			return nil, 0, e
		}
		out.Totals.Staff, e = count("SELECT COUNT(*) FROM auth_division_access x JOIN auth_users u ON u.id=x.user_id WHERE x.organization_id=? AND x.division_id=? AND u.role<>'student'", a.OrganizationID, d)
		if e != nil {
			return nil, 0, e
		}
	} else if a.Role == "proctor" {
		zero := int64(0)
		out.Totals.Students = &zero
		out.Totals.Groups = &zero
		if a.GroupID != nil {
			out.Totals.Students, e = count("SELECT COUNT(*) FROM auth_division_access x JOIN auth_users u ON u.id=x.user_id JOIN core_group_scope g ON g.group_id=x.group_id AND g.organization_id=x.organization_id AND g.division_id=x.division_id AND g.is_active=1 JOIN core_proctor_groups p ON p.group_id=g.group_id AND p.division_id=g.division_id WHERE x.organization_id=? AND x.division_id=? AND x.group_id=? AND p.user_id=? AND u.role='student'", a.OrganizationID, d, *a.GroupID, a.AccountID)
			if e != nil {
				return nil, 0, e
			}
			out.Totals.Groups, e = count("SELECT COUNT(*) FROM core_group_scope g JOIN core_proctor_groups p ON p.group_id=g.group_id AND p.division_id=g.division_id WHERE g.organization_id=? AND g.division_id=? AND g.group_id=? AND p.user_id=? AND g.is_active=1", a.OrganizationID, d, *a.GroupID, a.AccountID)
			if e != nil {
				return nil, 0, e
			}
		}
	}
	if a.Can("groups", false) || a.Can("users", false) {
		out.Totals.Groups, e = count("SELECT COUNT(*) FROM core_group_scope WHERE organization_id=? AND division_id=? AND is_active=1", a.OrganizationID, d)
		if e != nil {
			return nil, 0, e
		}
	}
	if a.Can("schools", false) {
		out.Totals.Schools, e = count("SELECT COUNT(*) FROM core_school_scope x JOIN schools s ON s.id=x.school_id WHERE x.organization_id=? AND s.is_active=1", a.OrganizationID)
		if e != nil {
			return nil, 0, e
		}
	}
	return out, 200, nil
}
