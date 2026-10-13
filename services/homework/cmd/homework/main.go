package main

import (
	"context"
	"cpm/homework/internal/homework"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("HOMEWORK_LISTEN_ADDR")
		if addr == "" || addr == "0.0.0.0:8006" {
			addr = "127.0.0.1:8006"
		}
		c := http.Client{Timeout: 3 * time.Second}
		r, e := c.Get("http://" + addr + "/health/ready")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	c, e := homework.LoadConfig()
	if e != nil {
		slog.Error("homework configuration invalid")
		os.Exit(1)
	}
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		slog.Error("homework database unavailable")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	e = db.PingContext(pingCtx)
	pingCancel()
	if e != nil {
		slog.Error("homework database unavailable")
		os.Exit(1)
	}
	storage, e := homework.NewStorage(c)
	if e != nil {
		slog.Error("homework storage configuration invalid")
		os.Exit(1)
	}
	s := &homework.Server{DB: db, Deps: homework.NewDependencies(c), Storage: storage, Config: c}
	go s.RunBroker(ctx)
	go s.RunMaintenance(ctx)
	server := &http.Server{Addr: c.Listen, Handler: s.Routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		server.Shutdown(shutdown)
	}()
	slog.Info("homework started", "listen", c.Listen)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		slog.Error("homework HTTP listener failed")
		os.Exit(1)
	}
}
