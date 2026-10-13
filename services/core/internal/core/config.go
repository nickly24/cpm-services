package core

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
	Listen, DSN, AuthURL, InternalKey                          string
	ReportTestsURL, ReportExamsURL, ReportAttendanceURL        string
	OperationOwnerKey, OperationDeliveryKey, CardsOperationKey string
}

func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("CORE_LISTEN_ADDR"), AuthURL: strings.TrimRight(os.Getenv("CORE_AUTH_URL"), "/"), InternalKey: os.Getenv("AUTH_INTERNAL_KEY")}
	c.OperationOwnerKey = os.Getenv("AUTH_OPERATIONS_CORE_KEY")
	c.OperationDeliveryKey = os.Getenv("CORE_OPERATION_DELIVERY_KEY")
	c.CardsOperationKey = os.Getenv("CORE_CARDS_OPERATION_KEY")
	if e := validateCoreOperationKeys(c); e != nil {
		return c, e
	}
	c.ReportTestsURL = strings.TrimRight(os.Getenv("CORE_REPORTS_TESTS_URL"), "/")
	c.ReportExamsURL = strings.TrimRight(os.Getenv("CORE_REPORTS_EXAMS_URL"), "/")
	c.ReportAttendanceURL = strings.TrimRight(os.Getenv("CORE_REPORTS_ATTENDANCE_URL"), "/")
	for _, peer := range []struct{ value, name, host, port string }{{c.ReportTestsURL, "CORE_REPORTS_TESTS_URL", "tests", "8004"}, {c.ReportExamsURL, "CORE_REPORTS_EXAMS_URL", "exams", "8010"}, {c.ReportAttendanceURL, "CORE_REPORTS_ATTENDANCE_URL", "attendance", "8013"}} {
		if peer.value != "" && !validReportURL(peer.value, peer.host, peer.port) {
			return c, errors.New("invalid " + peer.name)
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8003"
	}
	for _, k := range []string{"MYSQL_HOST", "MYSQL_PORT", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD", "CORE_AUTH_URL", "AUTH_INTERNAL_KEY"} {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			return c, errors.New("required environment variable: " + k)
		}
	}
	if len(c.InternalKey) < 32 {
		return c, errors.New("AUTH_INTERNAL_KEY too short")
	}
	u, e := url.Parse(c.AuthURL)
	if e != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "auth"))) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return c, errors.New("invalid CORE_AUTH_URL")
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
	mc.ClientFoundRows = true
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = 10 * time.Second
	mc.WriteTimeout = 10 * time.Second
	mc.TLSConfig = os.Getenv("MYSQL_TLS")
	mc.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	c.DSN = mc.FormatDSN()
	return c, nil
}

func validReportURL(raw, service, port string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() != port {
		return false
	}
	return u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == service || service == "exams" && u.Hostname() == "host.docker.internal"
}
