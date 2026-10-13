package auth

import (
	"errors"
	"github.com/go-sql-driver/mysql"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Operations                OperationConfig
	Listen                    string
	InternalKey               string
	DSN                       string
	KeyFile, Issuer, Audience string
	AccessTTL, SessionTTL     time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{InternalKey: os.Getenv("AUTH_INTERNAL_KEY"), Listen: env("AUTH_LISTEN_ADDR", "127.0.0.1:8002"),
		KeyFile: os.Getenv("AUTH_PRIVATE_KEY_FILE"), Issuer: env("AUTH_ISSUER", "cpm-auth"), Audience: env("AUTH_AUDIENCE", "cpm-services"), AccessTTL: 15 * time.Minute, SessionTTL: 14 * 24 * time.Hour}
	for _, key := range []string{"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD", "AUTH_PRIVATE_KEY_FILE"} {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return c, errors.New("required environment variable: " + key)
		}
	}
	port, e := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if e != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid MYSQL_PORT")
	}
	mc := mysql.NewConfig()
	mc.User = os.Getenv("MYSQL_USER")
	mc.Passwd = os.Getenv("MYSQL_PASSWORD")
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(os.Getenv("MYSQL_HOST"), strconv.Itoa(port))
	mc.DBName = os.Getenv("MYSQL_DATABASE")
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = 5 * time.Second
	mc.WriteTimeout = 5 * time.Second
	mc.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	if mode := os.Getenv("MYSQL_TLS"); mode != "" {
		mc.TLSConfig = mode
	}
	mc.ClientFoundRows = true
	c.DSN = mc.FormatDSN()
	var operationErr error
	c.Operations, operationErr = loadOperationConfig(c.InternalKey)
	if operationErr != nil {
		return c, operationErr
	}
	return c, nil
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
