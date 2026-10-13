package cards

import (
	"errors"
	"github.com/go-sql-driver/mysql"
	"net"
	"net/url"
	"os"
	"time"
)

type Config struct {
	Listen, DSN, AuthURL, CoreURL, TestsURL, Key             string
	OperationAuthKey, OperationDeliveryKey, OperationCoreKey string
}

func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("CARDS_LISTEN_ADDR"), AuthURL: os.Getenv("CARDS_AUTH_URL"), CoreURL: os.Getenv("CARDS_CORE_URL"), TestsURL: os.Getenv("CARDS_TESTS_URL"), Key: os.Getenv("AUTH_INTERNAL_KEY")}
	c.OperationAuthKey = os.Getenv("AUTH_OPERATIONS_CARDS_KEY")
	c.OperationDeliveryKey = os.Getenv("CARDS_OPERATION_DELIVERY_KEY")
	c.OperationCoreKey = os.Getenv("CARDS_CORE_OPERATION_KEY")
	if err := c.validateOperationKeys(); err != nil {
		return c, err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8008"
	}
	for _, key := range []string{"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		if os.Getenv(key) == "" {
			return c, errors.New("missing configuration: " + key)
		}
	}
	if len(c.Key) < 32 {
		return c, errors.New("invalid service key")
	}
	for _, v := range []struct{ base, host string }{{c.AuthURL, "auth"}, {c.CoreURL, "core"}, {c.TestsURL, "tests"}} {
		u, e := url.Parse(v.base)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == v.host || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return c, errors.New("invalid dependency URL")
		}
	}
	m := mysql.NewConfig()
	m.User = os.Getenv("MYSQL_USER")
	m.Passwd = os.Getenv("MYSQL_PASSWORD")
	m.Net = "tcp"
	m.Addr = net.JoinHostPort(os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT"))
	m.DBName = os.Getenv("MYSQL_DATABASE")
	m.TLSConfig = os.Getenv("MYSQL_TLS")
	m.ParseTime = true
	m.Timeout = 5 * time.Second
	m.ReadTimeout = 20 * time.Second
	m.WriteTimeout = 20 * time.Second
	c.DSN = m.FormatDSN()
	return c, nil
}

func (c Config) validateOperationKeys() error {
	keys := []string{c.OperationAuthKey, c.OperationDeliveryKey, c.OperationCoreKey}
	if keys[0] == "" && keys[1] == "" && keys[2] == "" {
		return nil
	}
	seen := map[string]bool{c.Key: true}
	for _, key := range keys {
		if len(key) < 32 || seen[key] {
			return errors.New("invalid operation credential configuration")
		}
		seen[key] = true
	}
	return nil
}
