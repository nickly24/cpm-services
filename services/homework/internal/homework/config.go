package homework

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

type Config struct{ Listen, DSN, AuthURL, CoreURL, CoreKey, Key, RabbitURL, S3Endpoint, S3PublicEndpoint, S3Region, S3Bucket, S3Access, S3Secret, GatewayKey string }

func LoadConfig() (Config, error) {
	c := Config{GatewayKey: os.Getenv("BFF_HOMEWORK_CONTEXT_KEY"), Listen: os.Getenv("HOMEWORK_LISTEN_ADDR"), AuthURL: os.Getenv("HOMEWORK_AUTH_URL"), CoreURL: os.Getenv("HOMEWORK_CORE_URL"), CoreKey: os.Getenv("AUTH_INTERNAL_KEY"), Key: os.Getenv("HOMEWORK_SERVICE_KEY"), RabbitURL: os.Getenv("RABBITMQ_URL"), S3Endpoint: os.Getenv("S3_ENDPOINT_URL"), S3PublicEndpoint: os.Getenv("S3_PUBLIC_ENDPOINT_URL"), S3Region: os.Getenv("S3_REGION"), S3Bucket: os.Getenv("S3_BUCKET"), S3Access: os.Getenv("S3_ACCESS_KEY_ID"), S3Secret: os.Getenv("S3_SECRET_ACCESS_KEY")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8006"
	}
	if c.S3PublicEndpoint == "" {
		c.S3PublicEndpoint = c.S3Endpoint
	}
	for _, v := range []string{c.AuthURL, c.CoreURL, c.CoreKey, c.Key, c.RabbitURL, c.S3Endpoint, c.S3Bucket, c.S3Access, c.S3Secret, os.Getenv("MYSQL_HOST"), os.Getenv("MYSQL_DATABASE"), os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD")} {
		if strings.TrimSpace(v) == "" {
			return c, errors.New("missing required configuration")
		}
	}
	if c.GatewayKey != "" && (len(c.GatewayKey) < 32 || c.GatewayKey == c.Key || c.GatewayKey == c.CoreKey || c.GatewayKey == os.Getenv("BFF_TESTS_CONTEXT_KEY")) {
		return c, errors.New("invalid Homework gateway credential")
	}
	if len(c.Key) < 32 || len(c.CoreKey) < 32 {
		return c, errors.New("service key too short")
	}
	if c.Key == c.CoreKey {
		return c, errors.New("worker and core service keys must be distinct")
	}
	for _, base := range []string{c.AuthURL, c.CoreURL, c.S3Endpoint, c.S3PublicEndpoint} {
		u, e := url.Parse(base)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("invalid dependency URL")
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
	mc.ClientFoundRows = true
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = 15 * time.Second
	mc.WriteTimeout = 15 * time.Second
	mc.TLSConfig = os.Getenv("MYSQL_TLS")
	mc.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	c.DSN = mc.FormatDSN()
	return c, nil
}
