package tests

import (
	"net/url"
	"strings"
	"testing"
)

func TestProductionMongoAuthenticationSource(t *testing.T) {
	for k, v := range map[string]string{"MONGO_HOST": "mongo.example.test", "MONGO_PORT": "27017", "MONGO_DATABASE": "production_fixture", "MONGO_USER": "fixture", "MONGO_PASSWORD": "p@ss:word", "MONGO_AUTH_SOURCE": "admin", "MONGO_DIRECT_CONNECTION": "true", "MONGO_URI": "", "AUTH_INTERNAL_KEY": strings.Repeat("a", 32), "RABBITMQ_URL": "amqp://fixture:fixture@rabbitmq:5672/cpm_prod", "TESTS_AUTH_URL": "http://auth:8002", "TESTS_CORE_URL": "http://core:8003", "TESTS_CALCULATE_URL": "http://calculate:8005", "CALCULATE_TESTS_URL": "http://tests:8004"} {
		t.Setenv(k, v)
	}
	c, e := LoadConfig()
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(c.MongoURI)
	if e != nil || u.Query().Get("authSource") != "admin" || u.Query().Get("directConnection") != "true" || u.Path != "/production_fixture" {
		t.Fatal("Mongo auth database confused with data database")
	}
	pw, _ := u.User.Password()
	if pw != "p@ss:word" {
		t.Fatal("password encoding changed")
	}
	raw := "mongodb://fixture:fixture@mongo.example.test:27017/?authSource=admin&directConnection=true"
	t.Setenv("MONGO_URI", raw)
	c, e = LoadConfig()
	if e != nil || c.MongoURI != raw {
		t.Fatal("original Mongo parameters lost")
	}
	t.Setenv("MONGO_URI", "https://mongo.example.test")
	if _, e = LoadConfig(); e == nil {
		t.Fatal("invalid URI accepted")
	}
}
