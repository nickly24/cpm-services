package bff

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// LoadConfig preserves local defaults. Production must provide all addresses.
func LoadConfig() (Config, string, error) {
	c := Config{Origin: "http://127.0.0.1:3001"}
	production := os.Getenv("APP_ENV") == "production"
	docker := os.Getenv("BFF_DOCKER") == "1"
	listen := "127.0.0.1:8001"
	if docker {
		listen = "0.0.0.0:8001"
	}
	for _, item := range []struct {
		dst                 *string
		name, service, port string
	}{
		{&c.Auth, "AUTH", "auth", "8002"}, {&c.Core, "CORE", "core", "8003"},
		{&c.Homework, "HOMEWORK", "homework", "8006"}, {&c.Tests, "TESTS", "tests", "8004"},
		{&c.Exams, "EXAMS", "exams", "8010"}, {&c.Ratings, "RATINGS", "ratings", "8012"},
		{&c.Attendance, "ATTENDANCE", "attendance", "8013"}, {&c.Cards, "CARDS", "cards", "8008"},
		{&c.Uploads, "UPLOADS", "uploads", "8014"},
	} {
		raw := os.Getenv("BFF_" + item.name + "_URL")
		if raw == "" {
			if production {
				return c, "", errors.New("missing BFF dependency URL: " + item.name)
			}
			host := "127.0.0.1"
			if docker {
				host = item.service
				if item.service == "exams" || item.service == "uploads" {
					host = "host.docker.internal"
				}
			}
			raw = "http://" + net.JoinHostPort(host, item.port)
		}
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, "", errors.New("invalid BFF dependency URL: " + item.name)
		}
		*item.dst = raw
	}
	if raw := os.Getenv("BFF_ALLOWED_ORIGINS"); raw != "" {
		c.Origin = ""
		for _, value := range strings.Split(raw, ",") {
			origin := strings.TrimSpace(value)
			u, e := url.Parse(origin)
			if e != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(origin, "*") {
				return c, "", errors.New("invalid BFF origin")
			}
			c.AllowedOrigins = append(c.AllowedOrigins, origin)
		}
	} else if production {
		return c, "", errors.New("production BFF origins required")
	}
	if value := os.Getenv("BFF_COOKIE_SECURE"); value != "" {
		b, e := strconv.ParseBool(value)
		if e != nil {
			return c, "", errors.New("invalid BFF cookie setting")
		}
		c.Secure = b
	} else if production {
		return c, "", errors.New("explicit production cookie setting required")
	}
	if v := os.Getenv("BFF_LISTEN_ADDR"); v != "" {
		listen = v
	}
	if _, _, e := net.SplitHostPort(listen); e != nil {
		return c, "", errors.New("invalid BFF listen address")
	}
	c.RedisAddress = os.Getenv("BFF_REDIS_ADDR")
	if c.RedisAddress == "" {
		c.RedisAddress = "127.0.0.1:6379"
	}
	c.RedisPassword = os.Getenv("BFF_REDIS_PASSWORD")
	c.HomeworkGatewayKey = os.Getenv("BFF_HOMEWORK_CONTEXT_KEY")
	if c.HomeworkGatewayKey != "" && (len(c.HomeworkGatewayKey) < 32 || c.HomeworkGatewayKey == os.Getenv("BFF_TESTS_CONTEXT_KEY")) {
		return c, "", errors.New("invalid Homework gateway credential")
	}
	c.AttendanceGatewayKey = os.Getenv("BFF_ATTENDANCE_CONTEXT_KEY")
	if c.AttendanceGatewayKey != "" && (len(c.AttendanceGatewayKey) < 32 || c.AttendanceGatewayKey == c.HomeworkGatewayKey || c.AttendanceGatewayKey == os.Getenv("BFF_TESTS_CONTEXT_KEY")) {
		return c, "", errors.New("invalid Attendance gateway credential")
	}
	c.GatewayKey = os.Getenv("BFF_TESTS_CONTEXT_KEY")
	if c.GatewayKey != "" && len(c.GatewayKey) < 32 {
		return c, "", errors.New("invalid gateway credential")
	}
	if production && (c.RedisPassword == "" || len(c.GatewayKey) < 32) {
		return c, "", errors.New("production Redis and gateway credentials required")
	}
	return c, listen, nil
}

func (s *Server) allowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	if origin == s.config.Origin {
		return true
	}
	for _, allowed := range s.config.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *Server) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if s.allowedOrigin(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
	}
	if r.Method != http.MethodOptions {
		return false
	}
	if !s.allowedOrigin(origin) {
		failure(w, 403, "origin_forbidden")
		return true
	}
	method := r.Header.Get("Access-Control-Request-Method")
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		failure(w, 403, "method_forbidden")
		return true
	}
	headers := map[string]bool{"content-type": true, "x-cpm-request": true, "x-cpm-context": true, "idempotency-key": true, "x-exam-confirmation": true}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		name := strings.ToLower(strings.TrimSpace(header))
		if name != "" && !headers[name] {
			failure(w, 403, "header_forbidden")
			return true
		}
	}
	w.Header().Add("Vary", "Access-Control-Request-Method")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CPM-Request, X-CPM-Context, Idempotency-Key, X-Exam-Confirmation")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}
