package exams

import (
	"strings"
	"testing"
)

func TestOperationConfigurationDisabledOrDistinctPair(t *testing.T) {
	for key, value := range map[string]string{"MYSQL_HOST": "127.0.0.1", "MYSQL_PORT": "13306", "MYSQL_DATABASE": "cpm_dev", "MYSQL_USER": "synthetic", "MYSQL_PASSWORD": "synthetic", "AUTH_INTERNAL_KEY": strings.Repeat("s", 32), "EXAMS_AUTH_URL": "http://127.0.0.1:8002", "EXAMS_CORE_URL": "http://127.0.0.1:8003", "RABBITMQ_URL": "", "AUTH_OPERATIONS_EXAMS_KEY": "", "EXAMS_OPERATIONS_DELIVERY_KEY": ""} {
		t.Setenv(key, value)
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatal("normal bearer configuration requires operations keys", err)
	}
	for _, pair := range [][2]string{{strings.Repeat("o", 32), ""}, {"", strings.Repeat("d", 32)}, {strings.Repeat("o", 32), strings.Repeat("o", 32)}, {strings.Repeat("s", 32), strings.Repeat("d", 32)}, {strings.Repeat("o", 32), strings.Repeat("s", 32)}, {"short", strings.Repeat("d", 32)}} {
		t.Setenv("AUTH_OPERATIONS_EXAMS_KEY", pair[0])
		t.Setenv("EXAMS_OPERATIONS_DELIVERY_KEY", pair[1])
		if _, err := LoadConfig(); err == nil {
			t.Fatal("partial/reused operations credential accepted")
		}
	}
	t.Setenv("AUTH_OPERATIONS_EXAMS_KEY", strings.Repeat("o", 32))
	t.Setenv("EXAMS_OPERATIONS_DELIVERY_KEY", strings.Repeat("d", 32))
	config, err := LoadConfig()
	if err != nil || config.OperationsAuthKey != strings.Repeat("o", 32) || config.OperationsDeliveryKey != strings.Repeat("d", 32) {
		t.Fatal("distinct pair rejected", err)
	}
}
