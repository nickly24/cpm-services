package filework

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	RabbitURL          string
	HomeworkURL        string
	HomeworkServiceKey string
	S3Endpoint         string
	S3Region           string
	S3Bucket           string
	S3AccessKey        string
	S3SecretKey        string
	TempDir            string
	ProcessTimeout     time.Duration
	QPDF               string
	Ghostscript        string
}

func LoadConfig() (Config, error) {
	c := Config{ListenAddr: env("LISTEN_ADDR", ":8007"), RabbitURL: os.Getenv("RABBITMQ_URL"), HomeworkURL: env("HOMEWORK_URL", "http://homework:8006"), HomeworkServiceKey: os.Getenv("HOMEWORK_SERVICE_KEY"), S3Endpoint: os.Getenv("S3_ENDPOINT_URL"), S3Region: env("S3_REGION", "us-east-1"), S3Bucket: os.Getenv("S3_BUCKET"), S3AccessKey: os.Getenv("S3_ACCESS_KEY_ID"), S3SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"), TempDir: env("PDF_TEMP_DIR", "/tmp"), QPDF: env("QPDF_BINARY", "qpdf"), Ghostscript: env("GHOSTSCRIPT_BINARY", "gs")}
	seconds, err := strconv.Atoi(env("PDF_PROCESS_TIMEOUT_SECONDS", "120"))
	if err != nil || seconds < 5 || seconds > 240 {
		return c, errors.New("PDF_PROCESS_TIMEOUT_SECONDS must be 5..240")
	}
	c.ProcessTimeout = time.Duration(seconds) * time.Second
	if c.RabbitURL == "" || c.HomeworkServiceKey == "" || c.S3Bucket == "" || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return c, errors.New("missing worker dependency configuration")
	}
	if len(c.HomeworkServiceKey) < 32 {
		return c, errors.New("HOMEWORK_SERVICE_KEY must be at least 32 bytes")
	}
	for _, value := range []string{c.HomeworkURL, c.S3Endpoint} {
		u, e := url.Parse(value)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("invalid dependency URL")
		}
	}
	u, e := url.Parse(c.RabbitURL)
	if e != nil || u.Host == "" || (u.Scheme != "amqp" && u.Scheme != "amqps") {
		return c, errors.New("invalid RABBITMQ_URL")
	}
	s3, _ := url.Parse(c.S3Endpoint)
	if s3.Path != "" && s3.Path != "/" {
		return c, errors.New("S3_ENDPOINT_URL must not contain a path")
	}
	c.HomeworkURL = strings.TrimRight(c.HomeworkURL, "/")
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
