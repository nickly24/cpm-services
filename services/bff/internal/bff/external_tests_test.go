package bff

import "testing"

func TestExternalTestsRoutesUseSelectedDivision(t *testing.T) {
	id := "0123456789abcdef01234567"
	for _, tc := range []struct{ method, tail string }{
		{"GET", "external-tests"}, {"POST", "external-tests"},
		{"GET", "external-tests/" + id}, {"PUT", "external-tests/" + id}, {"DELETE", "external-tests/" + id},
		{"GET", "external-tests/" + id + "/results"}, {"GET", "external-tests/" + id + "/history"},
		{"GET", "external-tests/" + id + "/delete-preview"}, {"GET", "external-results"},
		{"PUT", "external-tests/" + id + "/results/1234"}, {"DELETE", "external-tests/" + id + "/results/1234"},
	} {
		route, ownerRead := testsRoute(tc.method, tc.tail)
		if route != "/v1/tests/divisions/%d/"+tc.tail || ownerRead != "" {
			t.Fatalf("%s %s lost selected division: %q %q", tc.method, tc.tail, route, ownerRead)
		}
	}
	for _, tc := range []struct{ method, tail string }{
		{"GET", "external-tests/0"}, {"GET", "external-tests/external_123"},
		{"GET", "external-tests/divisions/1002"}, {"POST", "external-results"},
		{"GET", "external-tests/" + id + "/internal"}, {"POST", "external-tests/" + id + "/history"},
		{"GET", "external-imports/commit"}, {"POST", "external-tests/" + id + "/results"},
	} {
		if route, _ := testsRoute(tc.method, tc.tail); route != "" {
			t.Fatalf("unexpected external route %s %s: %s", tc.method, tc.tail, route)
		}
	}
}
