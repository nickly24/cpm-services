package attendance

import (
	"github.com/go-sql-driver/mysql"
	"strings"
	"testing"
)

func TestExplicitProductionDatabase(t *testing.T) {
	for k, v := range map[string]string{"APP_ENV": "production", "MYSQL_HOST": "db.example.test", "MYSQL_DATABASE": "production_fixture", "MYSQL_PORT": "3306", "MYSQL_USER": "fixture", "MYSQL_PASSWORD": "fixture", "MYSQL_TLS": "true", "ATTENDANCE_AUTH_URL": "http://auth:8002", "ATTENDANCE_CORE_URL": "http://core:8003", "AUTH_INTERNAL_KEY": strings.Repeat("a", 32), "ATTENDANCE_QR_KEY": strings.Repeat("q", 32)} {
		t.Setenv(k, v)
	}
	c, e := LoadConfig()
	if e != nil {
		t.Fatal(e)
	}
	d, e := mysql.ParseDSN(c.DSN)
	if e != nil || d.DBName != "production_fixture" || d.Addr != "db.example.test:3306" || d.TLSConfig != "true" {
		t.Fatal("production DSN not applied")
	}
	t.Setenv("APP_ENV", "")
	if _, e = LoadConfig(); e == nil {
		t.Fatal("dev guard weakened")
	}
}
