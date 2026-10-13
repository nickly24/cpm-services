package bff

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fixed public allowlist from the local exams owner; no internal routes.
var examsRoutes = regexp.MustCompile(`^(?:GET |POST |DELETE [1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/assignments|POST [1-9][0-9]{0,18}/assignments|DELETE [1-9][0-9]{0,18}/assignments/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/assignments/[1-9][0-9]{0,18}|POST [1-9][0-9]{0,18}/assignments/[1-9][0-9]{0,18}/attempts/ensure|GET [1-9][0-9]{0,18}/attempts|GET [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/appeals|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/next\-question|GET [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/protocol|GET [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/questions/[1-9][0-9]{0,18}/rounds|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/ready|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/replace\-question|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/start|POST [1-9][0-9]{0,18}/attempts/[1-9][0-9]{0,18}/vote|PUT [1-9][0-9]{0,18}/bank|GET [1-9][0-9]{0,18}/commissions|POST [1-9][0-9]{0,18}/commissions|DELETE [1-9][0-9]{0,18}/commissions/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/commissions/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/config|PUT [1-9][0-9]{0,18}/config|POST [1-9][0-9]{0,18}/connections|DELETE [1-9][0-9]{0,18}/connections/[1-9][0-9]{0,18}|POST [1-9][0-9]{0,18}/copy|GET [1-9][0-9]{0,18}/delete\-preview|PUT [1-9][0-9]{0,18}/direction|DELETE [1-9][0-9]{0,18}/history/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/history/[1-9][0-9]{0,18}/delete\-preview|POST [1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)|GET [1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}|PUT [1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}|POST [1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}/commit|PUT [1-9][0-9]{0,18}/metadata|POST [1-9][0-9]{0,18}/outside\-results|DELETE [1-9][0-9]{0,18}/outside\-results/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/outside\-results/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/parts|POST [1-9][0-9]{0,18}/parts|DELETE [1-9][0-9]{0,18}/parts/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/parts/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/privileges|DELETE [1-9][0-9]{0,18}/privileges/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/privileges/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/questions|POST [1-9][0-9]{0,18}/questions|DELETE [1-9][0-9]{0,18}/questions/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/questions/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/readiness|GET [1-9][0-9]{0,18}/results|GET [1-9][0-9]{0,18}/results/[1-9][0-9]{0,18}|POST [1-9][0-9]{0,18}/retakes/[1-9][0-9]{0,18}|GET [1-9][0-9]{0,18}/training|PUT [1-9][0-9]{0,18}/training|DELETE [1-9][0-9]{0,18}/training/progress/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/training/progress/[1-9][0-9]{0,18}|PUT [1-9][0-9]{0,18}/training/settings)$`)
var examsUpload = regexp.MustCompile(`^[1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)$`)
var examsTemplate = regexp.MustCompile(`^[1-9][0-9]{0,18}/imports/(?:questions|assignments|outside)/template$`)

func examsRoute(method, tail string) string {
	if !examsRoutes.MatchString(method+" "+tail) && !(method == "GET" && examsTemplate.MatchString(tail)) {
		return ""
	}
	if tail == "" {
		return "/v1/exams"
	}
	return "/v1/exams/" + tail
}
func examsCapabilities(raw json.RawMessage) map[string]any {
	var actor struct {
		Role              string `json:"role"`
		OrganizationAdmin bool   `json:"organization_admin"`
		Permissions       map[string]struct {
			View bool `json:"view"`
			Edit bool `json:"edit"`
		} `json:"permissions"`
	}
	_ = json.Unmarshal(raw, &actor)
	full := actor.Role == "admin" || actor.OrganizationAdmin
	admin := actor.Role == "admin" || actor.Role == "staff_admin"
	view := admin && (full || actor.Permissions["exams"].View)
	edit := view && (full || actor.Permissions["exams"].Edit)
	return map[string]any{"apiVersion": "v2", "canReadAdminExams": view, "canManageOutside": edit, "canCreateClassic": edit, "canImportExams": edit && (full || actor.Permissions["upload"].Edit), "canConductClassic": actor.Role == "examinator", "canReadStudentResults": actor.Role == "student"}
}
func examTail(path string) (string, bool) {
	if path == "exams" {
		return "", true
	}
	return strings.CutPrefix(path, "exams/")
}
