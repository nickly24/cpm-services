package bff

import (
	"net/url"
	"regexp"
	"strings"
)

const coreID = `[1-9][0-9]{0,18}`

var coreOrgRoutes = regexp.MustCompile(`^(GET (me|organization|organization/users|organization/users/` + coreID + `/memberships|audit|directions|divisions|roles|schools|schools/` + coreID + `|organization/test-settings)|POST (schools|roles|directions|divisions)|PUT (organization|schools/` + coreID + `|roles/` + coreID + `|directions/` + coreID + `|divisions/` + coreID + `|organization/test-settings)|DELETE (roles/` + coreID + `|directions/` + coreID + `|divisions/` + coreID + `))$`)
var coreDivisionRoutes = regexp.MustCompile(`^(GET (users|students|users/` + coreID + `|groups|groups/` + coreID + `/members|dashboard|reports/(tests|exams|attendance)|attendance-report)|POST (users|groups|groups/` + coreID + `/members)|PUT (users/` + coreID + `(/credentials|/active)?|memberships/` + coreID + `|groups/` + coreID + `)|DELETE (memberships/` + coreID + `|groups/` + coreID + `|groups/` + coreID + `/members/` + coreID + `))$`)

// Full administrators can select a membership target within their organization.
// Core validates that path's division against the live actor organization;
// delegated actors remain confined to their currently selected division.
var coreExplicitDivisionRoutes = regexp.MustCompile(`^(GET divisions/` + coreID + `/users|(PUT|DELETE) divisions/` + coreID + `/memberships/` + coreID + `)$`)
var coreExplicitUsers = regexp.MustCompile(`^divisions/` + coreID + `/users$`)

func coreRoute(method, tail string) string {
	if coreOrgRoutes.MatchString(method+" "+tail) || coreExplicitDivisionRoutes.MatchString(method+" "+tail) {
		return "/v1/core/" + tail
	}
	if coreDivisionRoutes.MatchString(method + " " + tail) {
		return "/v1/core/divisions/%d/" + tail
	}
	return ""
}

func coreQuery(method, tail string, q url.Values) bool {
	allowed := map[string]bool{}
	if method == "GET" {
		switch tail {
		case "users", "students", "groups", "schools", "organization/users":
			allowed["limit"], allowed["offset"], allowed["q"] = true, true, true
			if tail == "users" || tail == "organization/users" {
				allowed["role"] = true
			}
			if tail == "users" {
				allowed["unassigned"] = true
			}
			if tail == "students" {
				allowed["unassigned"] = true
			}
		}
		if tail == "audit" {
			allowed["limit"], allowed["offset"] = true, true
		}
		if coreExplicitUsers.MatchString(tail) {
			allowed["limit"], allowed["offset"], allowed["q"] = true, true, true
			allowed["role"], allowed["unassigned"] = true, true
		}
		if strings.HasPrefix(tail, "reports/") || tail == "attendance-report" {
			allowed["date_from"], allowed["date_to"] = true, true
			if tail == "attendance-report" {
				allowed["account_id"] = true
			}
		}
		if strings.HasPrefix(tail, "groups/") && strings.HasSuffix(tail, "/members") {
			allowed["limit"], allowed["offset"], allowed["q"] = true, true, true
		}
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return true
}
