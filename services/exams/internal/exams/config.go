package exams

import (
	"errors"
	"github.com/go-sql-driver/mysql"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"
)

type ServiceConfig struct{ Listen, DSN, AuthURL, CoreURL, Key, RabbitURL, OperationsAuthKey, OperationsDeliveryKey string }

func LoadConfig() (ServiceConfig, error) {
	c := ServiceConfig{Listen: os.Getenv("EXAMS_LISTEN_ADDR"), AuthURL: os.Getenv("EXAMS_AUTH_URL"), CoreURL: os.Getenv("EXAMS_CORE_URL"), Key: os.Getenv("AUTH_INTERNAL_KEY"), RabbitURL: os.Getenv("RABBITMQ_URL"), OperationsAuthKey: os.Getenv("AUTH_OPERATIONS_EXAMS_KEY"), OperationsDeliveryKey: os.Getenv("EXAMS_OPERATIONS_DELIVERY_KEY")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8010"
	}
	for _, k := range []string{"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		if os.Getenv(k) == "" {
			return c, errors.New("missing configuration: " + k)
		}
	}
	if len(c.Key) < 32 {
		return c, errors.New("invalid service key")
	}
	if c.OperationsAuthKey != "" || c.OperationsDeliveryKey != "" {
		if len(c.OperationsAuthKey) < 32 || len(c.OperationsDeliveryKey) < 32 || c.OperationsAuthKey == c.Key || c.OperationsDeliveryKey == c.Key || c.OperationsAuthKey == c.OperationsDeliveryKey {
			return c, errors.New("invalid distinct operations credentials")
		}
	}
	if c.RabbitURL != "" {
		u, err := url.Parse(c.RabbitURL)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "amqps" || u.Scheme == "amqp" && (u.Hostname() == "rabbitmq" || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return c, errors.New("invalid RabbitMQ URL")
		}
	}
	port, err := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid MYSQL_PORT")
	}
	for _, base := range []string{c.AuthURL, c.CoreURL} {
		u, e := url.Parse(base)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "auth" || u.Hostname() == "core" || u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
			return c, errors.New("invalid dependency URL")
		}
	}
	m := mysql.NewConfig()
	m.User = os.Getenv("MYSQL_USER")
	m.Passwd = os.Getenv("MYSQL_PASSWORD")
	m.Net = "tcp"
	m.Addr = net.JoinHostPort(os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_PORT"))
	m.DBName = os.Getenv("MYSQL_DATABASE")
	m.ParseTime = true
	m.TLSConfig = os.Getenv("MYSQL_TLS")
	m.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	m.Loc = time.UTC
	m.Timeout = 5 * time.Second
	m.ReadTimeout = 30 * time.Second
	m.WriteTimeout = 30 * time.Second
	c.DSN = m.FormatDSN()
	return c, nil
}
