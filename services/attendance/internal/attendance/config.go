package attendance

import (
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen, DSN, AuthURL, CoreURL, Key string
	QRKey                              []byte
}

func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("ATTENDANCE_LISTEN_ADDR"), AuthURL: os.Getenv("ATTENDANCE_AUTH_URL"), CoreURL: os.Getenv("ATTENDANCE_CORE_URL"), Key: os.Getenv("AUTH_INTERNAL_KEY"), QRKey: []byte(os.Getenv("ATTENDANCE_QR_KEY"))}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8013"
	}
	if len(c.Key) < 32 || len(c.QRKey) < 32 || c.Key == string(c.QRKey) {
		return c, errors.New("distinct service and QR keys required")
	}
	if os.Getenv("APP_ENV") != "production" && os.Getenv("MYSQL_DATABASE") != "cpm_dev" {
		return c, errors.New("only cpm_dev is enabled before migration")
	}
	for _, raw := range []string{c.AuthURL, c.CoreURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "auth" || u.Hostname() == "core" || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return c, errors.New("invalid dependency URL")
		}
	}
	port, e := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if e != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid mysql port")
	}
	host := os.Getenv("MYSQL_HOST")
	if os.Getenv("APP_ENV") != "production" && host != "mysql" && host != "127.0.0.1" && host != "localhost" {
		return c, errors.New("only dev mysql host is allowed")
	}
	cfg := mysql.NewConfig()
	cfg.User = os.Getenv("MYSQL_USER")
	cfg.Passwd = os.Getenv("MYSQL_PASSWORD")
	if cfg.User == "" || cfg.Passwd == "" {
		return c, errors.New("mysql credentials required")
	}
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.DBName = os.Getenv("MYSQL_DATABASE")
	if host == "" || cfg.DBName == "" {
		return c, errors.New("mysql host and database required")
	}
	cfg.TLSConfig = os.Getenv("MYSQL_TLS")
	cfg.Loc = time.UTC
	cfg.ParseTime = true
	cfg.Timeout = 3 * time.Second
	cfg.ReadTimeout = 20 * time.Second
	cfg.WriteTimeout = 20 * time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	c.DSN = cfg.FormatDSN()
	return c, nil
}
