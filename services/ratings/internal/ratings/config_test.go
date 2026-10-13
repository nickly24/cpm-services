package ratings

import (
	"context"
	"strings"
	"testing"
)

func ratingConfigEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"RATINGS_LISTEN_ADDR": "", "RATINGS_AUTH_URL": "http://127.0.0.1:8002/", "RATINGS_CORE_URL": "http://127.0.0.1:8003/",
		"RATINGS_HOMEWORK_URL": "http://127.0.0.1:8006/", "RATINGS_TESTS_URL": "http://127.0.0.1:8004/", "RATINGS_CALCULATE_URL": "http://127.0.0.1:8005/",
		"RATINGS_EXAMS_URL": "", "RATINGS_EXAMS_MODE": "", "RATINGS_EXTERNAL_TESTS_MODE": "", "AUTH_INTERNAL_KEY": strings.Repeat("k", 32),
		"RABBITMQ_URL": "amqp://synthetic:synthetic@127.0.0.1:5672/", "MYSQL_HOST": "127.0.0.1", "MYSQL_PORT": "13306", "MYSQL_DATABASE": "cpm_dev",
		"MYSQL_USER": "synthetic", "MYSQL_PASSWORD": "synthetic", "MYSQL_TLS": "",
	} {
		t.Setenv(key, value)
	}
}

func TestDefaultConfigurationRequiresBothExamsAndExternalTests(t *testing.T) {
	ratingConfigEnvironment(t)
	c, e := LoadConfig()
	if e != nil || c.ExamsMode != "required" || c.ExternalTestsMode != "required" || c.Listen != "127.0.0.1:8012" || strings.HasSuffix(c.CoreURL, "/") {
		t.Fatal("required defaults or URL normalization changed", e)
	}
}

func TestLoopbackExamOwnerThroughDockerHostIsDevOnly(t *testing.T) {
	ratingConfigEnvironment(t)
	t.Setenv("RATINGS_EXAMS_URL", "http://host.docker.internal:8010")
	if _, e := LoadConfig(); e != nil {
		t.Fatal("local dev exam owner cannot be reached", e)
	}
	t.Setenv("MYSQL_DATABASE", "synthetic_non_dev")
	if _, e := LoadConfig(); e == nil {
		t.Fatal("plain HTTP Docker host alias escaped dev")
	}
}

func TestEmptyOrPartialSourceModesAreExplicitAndLimitedToDev(t *testing.T) {
	for _, mode := range []struct{ key, value string }{{"RATINGS_EXAMS_MODE", "dev_empty"}, {"RATINGS_EXTERNAL_TESTS_MODE", "dev_internal_only"}} {
		t.Run(mode.value, func(t *testing.T) {
			ratingConfigEnvironment(t)
			t.Setenv(mode.key, mode.value)
			if _, e := LoadConfig(); e != nil {
				t.Fatal("explicit dev source mode rejected", e)
			}
			t.Setenv("MYSQL_DATABASE", "synthetic_non_dev")
			if _, e := LoadConfig(); e == nil {
				t.Fatal("incomplete source mode escaped dev-only configuration")
			}
		})
	}
}

func TestMissingExternalOwnerNeverBecomesImplicitZeroComponent(t *testing.T) {
	server := sourceFixtureServer(t, func(request SnapshotRequest) Snapshot {
		s := fixtureSnapshot(sourceJob(request))
		s.Tests.ExternalTests = nil
		s.Tests.ExternalSourceVersion = ""
		return s
	}, nil)
	defer server.Close()
	deps := sourceDependencies(server.URL, "dev_empty", "")
	deps.Config.ExternalTestsMode = "required"
	_, e := deps.Collect(context.Background(), fixtureJob())
	assertAPIError(t, e, 409, "external_tests_not_connected")
	deps.Config.ExternalTestsMode = "dev_internal_only"
	s, e := deps.Collect(context.Background(), fixtureJob())
	if e != nil || s.ExternalTestsMode != "dev_internal_only" {
		t.Fatal("explicit partial dev snapshot was not labelled", e)
	}
	other := s
	other.ExternalTestsMode = "required"
	if fingerprint(other) == fingerprint(s) {
		t.Fatal("source mode was omitted from snapshot identity")
	}
}
