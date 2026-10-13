package main

import (
	"context"
	"cpm/ratings/internal/ratings"
	"database/sql"
	_ "github.com/go-sql-driver/mysql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 4 * time.Second}
		res, e := client.Get("http://127.0.0.1:8012/health/ready")
		if e != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	c, e := ratings.LoadConfig()
	if e != nil {
		log.Error("invalid ratings configuration", "reason", e.Error())
		os.Exit(1)
	}
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		log.Error("ratings storage configuration invalid")
		os.Exit(1)
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(6)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = db.PingContext(check)
	cancel()
	if e != nil {
		log.Error("ratings database unavailable")
		os.Exit(1)
	}
	store := &ratings.Store{DB: db}
	deps := ratings.NewDependencies(c)
	srv := &http.Server{Addr: c.Listen, Handler: (&ratings.Server{Store: store, Deps: deps, Config: c}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); (&ratings.Worker{Store: store, Deps: deps, Log: log}).Run(ctx) }()
	go func() { defer wg.Done(); (&ratings.Publisher{Store: store, URL: c.RabbitURL, Log: log}).Run(ctx) }()
	go func() { defer wg.Done(); (&ratings.SourceConsumer{Store: store, URL: c.RabbitURL, Log: log}).Run(ctx) }()
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(end)
	}()
	log.Info("ratings service started", "listen", c.Listen, "exams_mode", c.ExamsMode)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Error("ratings HTTP listener failed")
		stop()
	}
	wg.Wait()
}
