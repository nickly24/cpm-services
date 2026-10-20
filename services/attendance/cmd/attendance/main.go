package main

import (
	"context"
	"cpm/attendance/internal/attendance"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		r, e := c.Get("http://127.0.0.1:8013/health/ready")
		if e != nil {
			os.Exit(1)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	cfg, e := attendance.LoadConfig()
	if e != nil {
		log.Fatal(e)
	}
	db, e := sql.Open("mysql", cfg.DSN)
	if e != nil {
		log.Fatal("invalid database configuration")
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(6)
	db.SetConnMaxLifetime(3 * time.Minute)
	s := attendance.Server{Store: attendance.Store{DB: db}, Peers: attendance.NewPeers(cfg.AuthURL, cfg.CoreURL, cfg.Key), QRKey: cfg.QRKey, GatewayKey: cfg.GatewayKey}
	httpServer := http.Server{Addr: cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = httpServer.Shutdown(c)
	}()
	log.Printf("attendance listening on %s", cfg.Listen)
	if e = httpServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal("http server failed")
	}
}
