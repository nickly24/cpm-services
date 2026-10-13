package ratings

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

type Config struct{ Listen, DSN, AuthURL, CoreURL, HomeworkURL, TestsURL, ExamsURL, ExamsMode, ExternalTestsMode, CalculateURL, ServiceKey, RabbitURL string }

func validURL(raw string, names ...string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	for _, n := range append(names, "localhost", "127.0.0.1") {
		if u.Hostname() == n {
			return true
		}
	}
	return false
}
func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("RATINGS_LISTEN_ADDR"), AuthURL: os.Getenv("RATINGS_AUTH_URL"), CoreURL: os.Getenv("RATINGS_CORE_URL"), HomeworkURL: os.Getenv("RATINGS_HOMEWORK_URL"), TestsURL: os.Getenv("RATINGS_TESTS_URL"), ExamsURL: os.Getenv("RATINGS_EXAMS_URL"), ExamsMode: os.Getenv("RATINGS_EXAMS_MODE"), ExternalTestsMode: os.Getenv("RATINGS_EXTERNAL_TESTS_MODE"), CalculateURL: os.Getenv("RATINGS_CALCULATE_URL"), ServiceKey: os.Getenv("AUTH_INTERNAL_KEY"), RabbitURL: os.Getenv("RABBITMQ_URL")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8012"
	}
	if c.ExamsMode == "" {
		c.ExamsMode = "required"
	}
	if c.ExamsMode != "required" && c.ExamsMode != "dev_empty" {
		return c, errors.New("invalid exams mode")
	}
	if c.ExamsMode == "dev_empty" && os.Getenv("MYSQL_DATABASE") != "cpm_dev" {
		return c, errors.New("empty exams mode is dev only")
	}
	if c.ExternalTestsMode == "" {
		c.ExternalTestsMode = "required"
	}
	if c.ExternalTestsMode != "required" && c.ExternalTestsMode != "dev_internal_only" {
		return c, errors.New("invalid external tests mode")
	}
	if c.ExternalTestsMode == "dev_internal_only" && os.Getenv("MYSQL_DATABASE") != "cpm_dev" {
		return c, errors.New("internal tests only mode is dev only")
	}
	if len(c.ServiceKey) < 32 {
		return c, errors.New("invalid service key")
	}
	for _, x := range []struct{ v, n string }{{c.AuthURL, "auth"}, {c.CoreURL, "core"}, {c.HomeworkURL, "homework"}, {c.TestsURL, "tests"}, {c.CalculateURL, "calculate"}} {
		if !validURL(x.v, x.n) {
			return c, errors.New("invalid dependency URL")
		}
	}
	examHosts := []string{"exams"}
	if os.Getenv("MYSQL_DATABASE") == "cpm_dev" {
		examHosts = append(examHosts, "host.docker.internal")
	}
	if c.ExamsURL != "" && !validURL(c.ExamsURL, examHosts...) {
		return c, errors.New("invalid exams URL")
	}
	u, e := url.Parse(c.RabbitURL)
	if e != nil || u.Hostname() == "" || u.User == nil || u.Scheme != "amqps" && !(u.Scheme == "amqp" && (u.Hostname() == "rabbitmq" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return c, errors.New("invalid RabbitMQ URL")
	}
	p, e := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if e != nil || p < 1 || p > 65535 {
		return c, errors.New("invalid mysql port")
	}
	for _, key := range []string{"MYSQL_HOST", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		if os.Getenv(key) == "" {
			return c, errors.New("missing " + key)
		}
	}
	m := mysql.NewConfig()
	m.User = os.Getenv("MYSQL_USER")
	m.Passwd = os.Getenv("MYSQL_PASSWORD")
	m.Net = "tcp"
	m.Addr = net.JoinHostPort(os.Getenv("MYSQL_HOST"), strconv.Itoa(p))
	m.DBName = os.Getenv("MYSQL_DATABASE")
	m.ParseTime = true
	m.Loc = time.UTC
	m.Timeout = 5 * time.Second
	m.ReadTimeout = 20 * time.Second
	m.WriteTimeout = 20 * time.Second
	m.ClientFoundRows = true
	m.TLSConfig = os.Getenv("MYSQL_TLS")
	m.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	c.DSN = m.FormatDSN()
	for _, v := range []*string{&c.AuthURL, &c.CoreURL, &c.HomeworkURL, &c.TestsURL, &c.ExamsURL, &c.CalculateURL} {
		*v = strings.TrimRight(*v, "/")
	}
	return c, nil
}
