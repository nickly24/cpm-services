package main

import (
	"context"
	"cpm/bff/internal/bff"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := &http.Client{Timeout: 3 * time.Second}
		r, e := c.Get("http://127.0.0.1:8001/health/ready")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}

	// Deliberately fixed local endpoints. This stage cannot be pointed at production.

	config := bff.Config{Auth: "http://127.0.0.1:8002", Core: "http://127.0.0.1:8003", Homework: "http://127.0.0.1:8006", Tests: "http://127.0.0.1:8004", Exams: "http://127.0.0.1:8010", Ratings: "http://127.0.0.1:8012", Attendance: "http://127.0.0.1:8013", Cards: "http://127.0.0.1:8008", Origin: "http://127.0.0.1:3001"}
	config.RedisAddress = os.Getenv("BFF_REDIS_ADDR")
	if config.RedisAddress == "" {
		config.RedisAddress = "127.0.0.1:6379"
	}
	config.RedisPassword = os.Getenv("BFF_REDIS_PASSWORD")
	config.GatewayKey = os.Getenv("BFF_TESTS_CONTEXT_KEY")
	if config.GatewayKey != "" && len(config.GatewayKey) < 32 {
		log.Fatal("BFF_TESTS_CONTEXT_KEY must contain at least 32 bytes")
	}
	listen := "127.0.0.1:8001"
	config.Uploads = "http://127.0.0.1:8014"
	if os.Getenv("BFF_DOCKER") == "1" {
		config.Auth = "http://auth:8002"
		config.Core = "http://core:8003"
		config.Homework = "http://homework:8006"
		config.Tests = "http://tests:8004"
		config.Exams = "http://host.docker.internal:8010"
		config.Ratings = "http://ratings:8012"
		config.Attendance = "http://attendance:8013"
		config.Cards = "http://cards:8008"
		config.Uploads = "http://host.docker.internal:8014"
		listen = "0.0.0.0:8001"
	}
	s := bff.New(config)
	defer s.Close()
	server := &http.Server{Addr: listen, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(c)
	}()
	log.Print("BFF listening on 127.0.0.1:8001")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
