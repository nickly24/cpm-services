package bff

import (
	"regexp"
	"strings"
)

var externalAccountPath = regexp.MustCompile(`^[1-9][0-9]*$`)

// External test records and results belong to the selected division. Unlike
// internal test bodies, no global test route can bypass this placement scope.
func externalTestsRoute(method, tail string) string {
	const base = "/v1/tests/divisions/%d/"
	if tail == "external-tests" && (method == "GET" || method == "POST") || tail == "external-results" && method == "GET" {
		return base + tail
	}
	parts := strings.Split(tail, "/")
	if len(parts) < 2 || parts[0] != "external-tests" || !testIDPath.MatchString(parts[1]) {
		return ""
	}
	if len(parts) == 2 && (method == "GET" || method == "PUT" || method == "DELETE") {
		return base + tail
	}
	if len(parts) == 3 && method == "GET" && (parts[2] == "delete-preview" || parts[2] == "history" || parts[2] == "results") {
		return base + tail
	}
	if len(parts) == 4 && parts[2] == "results" && externalAccountPath.MatchString(parts[3]) && (method == "PUT" || method == "DELETE") {
		return base + tail
	}
	return ""
}
