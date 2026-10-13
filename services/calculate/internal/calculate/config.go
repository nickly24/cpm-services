package calculate

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Listen, MongoURI, Database, RabbitURL, ServiceKey, TestsURL, RatingsURL string
	GradingWorkers                                                          int
	ReceiptWorkers                                                          int
	MongoPoolSize                                                           uint64
}

func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("CALCULATE_LISTEN_ADDR"), Database: os.Getenv("MONGO_DATABASE"), RabbitURL: os.Getenv("RABBITMQ_URL"), ServiceKey: os.Getenv("AUTH_INTERNAL_KEY"), TestsURL: os.Getenv("CALCULATE_TESTS_URL"), RatingsURL: os.Getenv("CALCULATE_RATINGS_URL"), GradingWorkers: 4, ReceiptWorkers: 4, MongoPoolSize: 10}
	if raw := os.Getenv("CALCULATE_GRADING_WORKERS"); raw != "" {
		workers, err := strconv.Atoi(raw)
		if err != nil || workers < 1 || workers > 32 {
			return c, errors.New("invalid grading worker count")
		}
		c.GradingWorkers = workers
	}
	if raw := os.Getenv("CALCULATE_RECEIPT_WORKERS"); raw != "" {
		workers, err := strconv.Atoi(raw)
		if err != nil || workers < 1 || workers > 8 {
			return c, errors.New("invalid receipt worker count")
		}
		c.ReceiptWorkers = workers
	}
	if raw := os.Getenv("CALCULATE_MONGO_POOL_SIZE"); raw != "" {
		size, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || size < 2 || size > 128 {
			return c, errors.New("invalid Mongo pool size")
		}
		c.MongoPoolSize = size
	}
	if c.Listen == "" {
		c.Listen = os.Getenv("CALCULATE_LISTEN")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8005"
	}
	for _, key := range []string{"MONGO_HOST", "MONGO_PORT", "MONGO_DATABASE", "MONGO_USER", "MONGO_PASSWORD", "RABBITMQ_URL", "AUTH_INTERNAL_KEY", "CALCULATE_TESTS_URL"} {
		if os.Getenv(key) == "" {
			return c, errors.New("missing configuration: " + key)
		}
	}
	if len(c.ServiceKey) < 32 {
		return c, errors.New("invalid service key")
	}
	testsURL, err := url.Parse(c.TestsURL)
	if err != nil || testsURL.Hostname() == "" || testsURL.User != nil || testsURL.RawQuery != "" || testsURL.Fragment != "" || testsURL.Path != "" && testsURL.Path != "/" || !(testsURL.Scheme == "https" || testsURL.Scheme == "http" && (testsURL.Hostname() == "tests" || testsURL.Hostname() == "localhost" || testsURL.Hostname() == "127.0.0.1")) {
		return c, errors.New("invalid tests URL")
	}
	c.TestsURL = strings.TrimRight(c.TestsURL, "/")
	if c.RatingsURL != "" {
		ratingsURL, err := url.Parse(c.RatingsURL)
		if err != nil || ratingsURL.Hostname() == "" || ratingsURL.User != nil || ratingsURL.RawQuery != "" || ratingsURL.Fragment != "" || ratingsURL.Path != "" && ratingsURL.Path != "/" || !(ratingsURL.Scheme == "https" || ratingsURL.Scheme == "http" && (ratingsURL.Hostname() == "ratings" || ratingsURL.Hostname() == "localhost" || ratingsURL.Hostname() == "127.0.0.1")) {
			return c, errors.New("invalid ratings URL")
		}
		c.RatingsURL = strings.TrimRight(c.RatingsURL, "/")
	}
	u, err := url.Parse(c.RabbitURL)
	if err != nil || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Fragment != "" || !(u.Scheme == "amqps" || u.Scheme == "amqp" && (u.Hostname() == "rabbitmq" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return c, errors.New("invalid RabbitMQ URL")
	}
	p, err := strconv.Atoi(os.Getenv("MONGO_PORT"))
	if err != nil || p < 1 || p > 65535 {
		return c, errors.New("invalid Mongo port")
	}
	u = &url.URL{Scheme: "mongodb", Host: net.JoinHostPort(os.Getenv("MONGO_HOST"), strconv.Itoa(p)), User: url.UserPassword(os.Getenv("MONGO_USER"), os.Getenv("MONGO_PASSWORD")), Path: "/" + c.Database}
	q := url.Values{"authSource": {c.Database}}
	if os.Getenv("MONGO_TLS") == "true" {
		q.Set("tls", "true")
	}
	u.RawQuery = q.Encode()
	c.MongoURI = u.String()
	return c, nil
}
