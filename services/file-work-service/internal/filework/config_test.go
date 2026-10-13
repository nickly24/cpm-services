package filework

import (
	"strings"
	"testing"
)

func configFixture(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{"RABBITMQ_URL": "amqp://worker:fixture@127.0.0.1:5672/test", "HOMEWORK_URL": "http://127.0.0.1:8006", "HOMEWORK_SERVICE_KEY": strings.Repeat("x", 32), "S3_ENDPOINT_URL": "http://127.0.0.1:19000", "S3_BUCKET": "test", "S3_ACCESS_KEY_ID": "fixture", "S3_SECRET_ACCESS_KEY": "fixture", "PDF_PROCESS_TIMEOUT_SECONDS": "120", "LISTEN_ADDR": ":8007"} {
		t.Setenv(key, value)
	}
}
func TestConfigDependencyAndResourceValidation(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"no_broker", "RABBITMQ_URL", ""}, {"wrong_broker_scheme", "RABBITMQ_URL", "https://localhost"}, {"short_service_key", "HOMEWORK_SERVICE_KEY", "short"}, {"no_bucket", "S3_BUCKET", ""}, {"no_access_key", "S3_ACCESS_KEY_ID", ""}, {"no_secret", "S3_SECRET_ACCESS_KEY", ""}, {"s3_credentials_in_url", "S3_ENDPOINT_URL", "http://secret:secret@localhost"}, {"s3_path", "S3_ENDPOINT_URL", "http://localhost/proxy"}, {"s3_query", "S3_ENDPOINT_URL", "http://localhost?key=secret"}, {"s3_scheme", "S3_ENDPOINT_URL", "file:///tmp"}, {"homework_credentials_in_url", "HOMEWORK_URL", "http://secret:secret@localhost"}, {"timeout_low", "PDF_PROCESS_TIMEOUT_SECONDS", "4"}, {"timeout_high", "PDF_PROCESS_TIMEOUT_SECONDS", "241"}, {"timeout_malformed", "PDF_PROCESS_TIMEOUT_SECONDS", "one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configFixture(t)
			t.Setenv(tc.key, tc.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	t.Run("valid", func(t *testing.T) {
		configFixture(t)
		if _, err := LoadConfig(); err != nil {
			t.Fatal(err)
		}
	})
}
