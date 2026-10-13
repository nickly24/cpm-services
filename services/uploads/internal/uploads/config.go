package uploads

import (
	"errors"
	"github.com/go-sql-driver/mysql"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen, DSN, AuthURL, CoreURL, CardsURL, ExamsURL, HomeworkURL, AttendanceURL, TestsURL, RabbitURL, InternalKey string
	OperationsEnabled                                                                                               bool
	OperationsIssuerKey                                                                                             string
	OperationDeliveryKeys                                                                                           map[string]string
}

func origin(raw, service string, port int) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != strconv.Itoa(port) {
		return "", errors.New("invalid dev owner origin: " + service)
	}
	h := u.Hostname()
	if h != "127.0.0.1" && h != "localhost" && h != service && h != "host.docker.internal" {
		return "", errors.New("invalid dev owner host: " + service)
	}
	return strings.TrimRight(raw, "/"), nil
}
func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("UPLOADS_LISTEN_ADDR")}
	if os.Getenv("APP_ENV") != "production" && (os.Getenv("UPLOADS_DEV_MODE") != "true" || os.Getenv("MYSQL_DATABASE") != "cpm_dev" || (os.Getenv("MYSQL_HOST") != "127.0.0.1" && os.Getenv("MYSQL_HOST") != "mysql")) {
		return c, errors.New("uploads currently supports only isolated cpm_dev")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8014"
	}
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil || (host != "127.0.0.1" && host != "0.0.0.0") || port != "8014" {
		return c, errors.New("invalid uploads dev listen address")
	}
	for _, p := range []struct {
		dst          *string
		env, service string
		port         int
	}{{&c.AuthURL, "UPLOADS_AUTH_URL", "auth", 8002}, {&c.CoreURL, "UPLOADS_CORE_URL", "core", 8003}, {&c.CardsURL, "UPLOADS_CARDS_URL", "cards", 8008}, {&c.ExamsURL, "UPLOADS_EXAMS_URL", "exams", 8010}, {&c.HomeworkURL, "UPLOADS_HOMEWORK_URL", "homework", 8006}, {&c.AttendanceURL, "UPLOADS_ATTENDANCE_URL", "attendance", 8013}, {&c.TestsURL, "UPLOADS_TESTS_URL", "tests", 8004}} {
		raw := os.Getenv(p.env)
		if raw == "" {
			raw = "http://127.0.0.1:" + strconv.Itoa(p.port)
		}
		*p.dst, e = origin(raw, p.service, p.port)
		if e != nil {
			return c, e
		}
	}
	dbport := os.Getenv("MYSQL_PORT")
	n, parseErr := strconv.Atoi(dbport)
	if parseErr != nil || n < 1 || n > 65535 || os.Getenv("MYSQL_HOST") == "" || os.Getenv("MYSQL_DATABASE") == "" {
		return c, errors.New("invalid mysql configuration")
	}
	if os.Getenv("APP_ENV") != "production" && ((os.Getenv("MYSQL_HOST") == "127.0.0.1" && dbport != "13306") || (os.Getenv("MYSQL_HOST") == "mysql" && dbport != "3306")) {
		return c, errors.New("invalid dev database port")
	}
	mc := mysql.NewConfig()
	mc.User = os.Getenv("MYSQL_USER")
	mc.Passwd = os.Getenv("MYSQL_PASSWORD")
	mc.DBName = os.Getenv("MYSQL_DATABASE")
	mc.TLSConfig = os.Getenv("MYSQL_TLS")
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(os.Getenv("MYSQL_HOST"), dbport)
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = 15 * time.Second
	mc.WriteTimeout = 15 * time.Second
	mc.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	if mc.User == "" || mc.Passwd == "" {
		return c, errors.New("database credentials required")
	}
	c.DSN = mc.FormatDSN()
	c.RabbitURL = os.Getenv("UPLOADS_RABBITMQ_URL")
	if c.RabbitURL == "" {
		c.RabbitURL = os.Getenv("RABBITMQ_URL")
	}
	if e = validateRabbitURL(c.RabbitURL); e != nil {
		return c, e
	}
	c.InternalKey = os.Getenv("UPLOADS_INTERNAL_KEY")
	if len(c.InternalKey) < 32 {
		return c, errors.New("uploads internal key required")
	}
	if e = loadOperationConfig(&c); e != nil {
		return c, e
	}
	return c, nil
}

func loadOperationConfig(c *Config) error {
	enabled := os.Getenv("UPLOADS_OPERATIONS_ENABLED")
	if enabled != "" && enabled != "0" && enabled != "1" {
		return errors.New("invalid uploads operations feature flag")
	}
	c.OperationsEnabled = enabled == "1"
	if !c.OperationsEnabled {
		return nil
	}
	c.OperationsIssuerKey = os.Getenv("AUTH_OPERATIONS_ISSUER_KEY")
	c.OperationDeliveryKeys = map[string]string{}
	seen := map[string]bool{c.InternalKey: true}
	if len(c.OperationsIssuerKey) < 32 || seen[c.OperationsIssuerKey] {
		return errors.New("invalid uploads operations issuer credential")
	}
	seen[c.OperationsIssuerKey] = true
	for owner, name := range map[string]string{"core": "CORE_OPERATION_DELIVERY_KEY", "cards": "CARDS_OPERATION_DELIVERY_KEY", "tests": "TESTS_OPERATION_DELIVERY_KEY", "exams": "EXAMS_OPERATIONS_DELIVERY_KEY"} {
		key := os.Getenv(name)
		if len(key) < 32 || seen[key] {
			return errors.New("invalid uploads operations delivery credentials")
		}
		seen[key] = true
		c.OperationDeliveryKeys[owner] = key
	}
	return nil
}

func validateRabbitURL(raw string) error {
	u, e := url.Parse(raw)
	vhost := "/cpm_dev"
	if os.Getenv("APP_ENV") == "production" {
		if os.Getenv("RABBITMQ_VHOST") == "" {
			return errors.New("RabbitMQ vhost required")
		}
		vhost = "/" + os.Getenv("RABBITMQ_VHOST")
	}
	if e != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.User == nil || u.Port() != "5672" || u.Path != vhost || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid uploads dev broker configuration")
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "rabbitmq", "host.docker.internal":
	default:
		return errors.New("invalid uploads dev broker host")
	}
	p, ok := u.User.Password()
	if !ok || p == "" || u.User.Username() == "" {
		return errors.New("uploads broker credentials required")
	}
	return nil
}
