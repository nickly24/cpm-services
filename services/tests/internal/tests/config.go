package tests

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
)

type Config struct {
	Listen, MongoURI, Database, AuthURL, CoreURL, ServiceKey, RabbitURL, CalculateURL string
	OperationDeliveryKey, OperationAuthKey                                            string
	GatewayKey                                                                        string
}

func LoadConfig() (Config, error) {
	c := Config{Listen: os.Getenv("TESTS_LISTEN_ADDR"), Database: os.Getenv("MONGO_DATABASE"), AuthURL: os.Getenv("TESTS_AUTH_URL"), CoreURL: os.Getenv("TESTS_CORE_URL"), ServiceKey: os.Getenv("AUTH_INTERNAL_KEY"), RabbitURL: os.Getenv("RABBITMQ_URL"), CalculateURL: os.Getenv("TESTS_CALCULATE_URL")}
	c.OperationDeliveryKey = os.Getenv("TESTS_OPERATION_DELIVERY_KEY")
	c.OperationAuthKey = os.Getenv("AUTH_OPERATIONS_TESTS_KEY")
	c.GatewayKey = os.Getenv("BFF_TESTS_CONTEXT_KEY")
	if c.GatewayKey != "" && (len(c.GatewayKey) < 32 || c.GatewayKey == c.ServiceKey || c.GatewayKey == c.OperationDeliveryKey || c.GatewayKey == c.OperationAuthKey) {
		return c, errors.New("invalid gateway context credential")
	}
	if c.OperationDeliveryKey != "" || c.OperationAuthKey != "" {
		if len(c.OperationDeliveryKey) < 32 || len(c.OperationAuthKey) < 32 || c.OperationDeliveryKey == c.OperationAuthKey || c.OperationDeliveryKey == c.ServiceKey || c.OperationAuthKey == c.ServiceKey {
			return c, errors.New("invalid operation credential configuration")
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8004"
	}
	for _, key := range []string{"MONGO_HOST", "MONGO_PORT", "MONGO_DATABASE", "MONGO_USER", "MONGO_PASSWORD", "TESTS_AUTH_URL", "TESTS_CORE_URL", "AUTH_INTERNAL_KEY"} {
		if os.Getenv(key) == "" {
			return c, errors.New("missing configuration: " + key)
		}
	}
	if len(c.ServiceKey) < 32 {
		return c, errors.New("invalid service key")
	}
	if c.RabbitURL == "" {
		return c, errors.New("missing configuration: RABBITMQ_URL")
	}
	if u, err := url.Parse(c.RabbitURL); err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Host == "" {
		return c, errors.New("invalid Rabbit configuration")
	}
	for _, v := range []struct{ value, host string }{{c.AuthURL, "auth"}, {c.CoreURL, "core"}, {c.CalculateURL, "calculate"}} {
		u, e := url.Parse(v.value)
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !(u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == v.host)) {
			return c, errors.New("invalid dependency URL")
		}
	}
	p, e := strconv.Atoi(os.Getenv("MONGO_PORT"))
	if e != nil || p < 1 || p > 65535 {
		return c, errors.New("invalid Mongo port")
	}
	u := url.URL{Scheme: "mongodb", Host: net.JoinHostPort(os.Getenv("MONGO_HOST"), strconv.Itoa(p)), User: url.UserPassword(os.Getenv("MONGO_USER"), os.Getenv("MONGO_PASSWORD")), Path: "/" + c.Database}
	q := url.Values{"authSource": {c.Database}}
	if os.Getenv("MONGO_TLS") == "true" {
		q.Set("tls", "true")
	}
	u.RawQuery = q.Encode()
	c.MongoURI = u.String()
	return c, nil
}
