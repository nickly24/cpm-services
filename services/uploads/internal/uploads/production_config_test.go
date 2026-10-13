package uploads

import (
	"github.com/go-sql-driver/mysql"
	"strings"
	"testing"
)

func TestExplicitProductionDatabaseAndVhost(t *testing.T) {
	for k, v := range map[string]string{"APP_ENV": "production", "MYSQL_HOST": "db.example.test", "MYSQL_DATABASE": "production_fixture", "MYSQL_PORT": "3306", "MYSQL_USER": "fixture", "MYSQL_PASSWORD": "fixture", "MYSQL_TLS": "true", "UPLOADS_INTERNAL_KEY": strings.Repeat("u", 32), "UPLOADS_OPERATIONS_ENABLED": "0", "RABBITMQ_VHOST": "cpm_prod", "RABBITMQ_URL": "amqp://fixture:fixture@rabbitmq:5672/cpm_prod"} {
		t.Setenv(k, v)
	}
	c, e := LoadConfig()
	if e != nil {
		t.Fatal(e)
	}
	d, e := mysql.ParseDSN(c.DSN)
	if e != nil || d.DBName != "production_fixture" || d.TLSConfig != "true" {
		t.Fatal("production DSN not applied")
	}
	t.Setenv("RABBITMQ_VHOST", "other")
	if _, e = LoadConfig(); e == nil {
		t.Fatal("foreign vhost allowed")
	}
	t.Setenv("APP_ENV", "")
	if _, e = LoadConfig(); e == nil {
		t.Fatal("dev guard weakened")
	}
}
