package ratings

import (
	"context"
	"strings"
	"testing"
)

func TestRequiredExternalOwnerNeedsScopedReadinessNotJustEmptyHash(t *testing.T) {
	j := fixtureJob()
	for _, test := range []struct {
		name   string
		domain *ExternalDomain
		code   int
		reason string
	}{
		{"old unimplemented owner", nil, 409, "external_tests_not_connected"},
		{"unavailable owner", &ExternalDomain{1, "unavailable", j.OrganizationID, j.DivisionID}, 409, "external_tests_not_connected"},
		{"unsupported domain", &ExternalDomain{2, "ready", j.OrganizationID, j.DivisionID}, 503, "dependency_unavailable"},
		{"foreign organization", &ExternalDomain{1, "ready", j.OrganizationID + 1, j.DivisionID}, 503, "dependency_unavailable"},
		{"foreign division", &ExternalDomain{1, "ready", j.OrganizationID, j.DivisionID + 1}, 503, "dependency_unavailable"},
		{"healthy empty catalog", &ExternalDomain{1, "ready", j.OrganizationID, j.DivisionID}, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
				s := fixtureSnapshot(sourceJob(request))
				s.Tests.ExternalTests = []Test{}
				s.Tests.ExternalSourceVersion = strings.Repeat("a", 64)
				s.Tests.ExternalDomain = test.domain
				return s
			}, nil)
			defer server.Close()
			deps := sourceDependencies(server.URL, "required", server.URL)
			deps.Config.ExternalTestsMode = "required"
			_, err := deps.Collect(context.Background(), j)
			if test.code == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertAPIError(t, err, test.code, test.reason)
			}
		})
	}
}
