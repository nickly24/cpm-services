package homework

import (
	"strings"
	"testing"
)

func TestWorkerCredentialRequiredAndSeparatedFromCore(t *testing.T) {
	for k, v := range map[string]string{
		"HOMEWORK_AUTH_URL": "http://auth:8002", "HOMEWORK_CORE_URL": "http://core:8003",
		"AUTH_INTERNAL_KEY": strings.Repeat("c", 32), "RABBITMQ_URL": "amqp://rabbitmq:5672/dev",
		"S3_ENDPOINT_URL": "http://s3:9000", "S3_PUBLIC_ENDPOINT_URL": "http://localhost:19000",
		"S3_BUCKET": "fixture", "S3_ACCESS_KEY_ID": "synthetic-access", "S3_SECRET_ACCESS_KEY": "synthetic-secret",
		"MYSQL_HOST": "localhost", "MYSQL_PORT": "13306", "MYSQL_DATABASE": "cpm_dev",
		"MYSQL_USER": "synthetic", "MYSQL_PASSWORD": "synthetic", "MYSQL_TLS": "",
	} {
		t.Setenv(k, v)
	}
	for _, c := range []struct {
		name, key string
		valid     bool
	}{
		{"absent worker key", "", false},
		{"short worker key", "worker", false},
		{"shared core worker key", strings.Repeat("c", 32), false},
		{"distinct worker key", strings.Repeat("w", 32), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOMEWORK_SERVICE_KEY", c.key)
			_, err := LoadConfig()
			if (err == nil) != c.valid {
				t.Fatalf("configuration validity=%v wanted=%v", err == nil, c.valid)
			}
		})
	}
}
