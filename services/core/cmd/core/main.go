package main

import (
	"context"
	"cpm/core/internal/core"
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
		client := &http.Client{Timeout: 4 * time.Second}
		r, e := client.Get("http://127.0.0.1:8003/health/ready")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	cfg, e := core.LoadConfig()
	if e != nil {
		slog.Error("configuration invalid", "reason", e.Error())
		os.Exit(1)
	}
	db, e := sql.Open("mysql", cfg.DSN)
	if e != nil {
		slog.Error("database configuration invalid")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	service := &core.Server{DB: db, Auth: core.NewAuthClient(cfg), InternalKey: cfg.InternalKey, Reports: core.NewReportClient(cfg)}
	server := &http.Server{Addr: cfg.Listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-done
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("core service started", "address", cfg.Listen)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		slog.Error("HTTP listener failed")
		os.Exit(1)
	}
}
