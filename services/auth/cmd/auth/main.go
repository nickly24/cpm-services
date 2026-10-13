package main

import (
	"context"
	"cpm/auth/internal/auth"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1:8002/health/ready")
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	config, err := auth.LoadConfig()
	if err != nil {
		slog.Error("configuration invalid", "reason", err.Error())
		os.Exit(1)
	}
	tokens, err := auth.LoadTokens(config)
	if err != nil {
		slog.Error("signing key unavailable")
		os.Exit(1)
	}
	db, err := sql.Open("mysql", config.DSN)
	if err != nil {
		slog.Error("database configuration invalid")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	service := &auth.Server{Repo: &auth.Repository{DB: db}, Tokens: tokens, Config: config}
	server := &http.Server{Addr: config.Listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("auth service started", "address", config.Listen)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP listener failed")
		os.Exit(1)
	}
}
