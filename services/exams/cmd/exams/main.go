package main

import (
	"context"
	"cpm/exams/internal/exams"
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
		c := &http.Client{Timeout: 4 * time.Second}
		r, e := c.Get("http://127.0.0.1:8010/health/ready")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	cfg, e := exams.LoadConfig()
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
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go exams.RunMaintenance(ctx, db, cfg.RabbitURL)
	s := &exams.Server{DB: db, Deps: exams.NewDependencies(cfg), InternalKey: cfg.Key, OperationKey: cfg.OperationsDeliveryKey}
	server := &http.Server{Addr: cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	slog.Info("exams service started", "address", cfg.Listen)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		slog.Error("HTTP listener failed")
		os.Exit(1)
	}
}
