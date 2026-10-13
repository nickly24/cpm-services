package main

import (
	"context"
	"cpm/cards/internal/cards"
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	c, e := cards.LoadConfig()
	if e != nil {
		log.Fatal(e)
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		_, port, e := net.SplitHostPort(c.Listen)
		if e != nil {
			os.Exit(1)
		}
		client := http.Client{Timeout: 4 * time.Second}
		res, e := client.Get("http://127.0.0.1:" + port + "/health/ready")
		if e != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	db, e := sql.Open("mysql", c.DSN)
	if e != nil {
		log.Fatal("database configuration failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	s := &cards.Server{DB: db, Deps: cards.NewDependencies(c)}
	srv := &http.Server{Addr: c.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Print("cards listening")
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal("server failed")
	}
}
