package main

import (
	"context"
	"cpm/tests/internal/tests"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 5 * time.Second}
		r, e := c.Get("http://127.0.0.1:8004/health/ready")
		if e != nil {
			os.Exit(1)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	cfg, e := tests.LoadConfig()
	if e != nil {
		slog.Error("configuration invalid", "reason", e.Error())
		os.Exit(1)
	}
	journal := true
	client, e := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI).SetMaxPoolSize(20).SetMinPoolSize(1).SetServerSelectionTimeout(5 * time.Second).SetWriteConcern(&writeconcern.WriteConcern{W: "majority", Journal: &journal}))
	if e != nil {
		slog.Error("Mongo configuration invalid")
		os.Exit(1)
	}
	defer func() {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = client.Disconnect(ctx)
	}()
	service := &tests.Server{Store: &tests.Store{Collection: client.Database(cfg.Database).Collection(tests.CollectionName)}, Dependencies: tests.NewDependencies(cfg), Config: cfg}
	if err := service.Store.EnsureAttemptIndexes(context.Background()); err != nil {
		slog.Error("attempt indexes unavailable")
		os.Exit(1)
	}
	if err := service.Store.EnsureJobIndexes(context.Background()); err != nil {
		slog.Error("calculation indexes unavailable")
		os.Exit(1)
	}
	if err := service.Store.EnsureExternalIndexes(context.Background()); err != nil {
		slog.Error("external domain indexes unavailable")
		os.Exit(1)
	}
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	service.Store.StartPublisher(workerContext, cfg.RabbitURL)
	server := &http.Server{Addr: cfg.Listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan os.Signal, 1)
	shutdownDone := make(chan struct{})
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer close(shutdownDone)
		<-done
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = server.Shutdown(ctx)
	}()
	slog.Info("tests service started", "address", cfg.Listen)
	if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		slog.Error("HTTP listener failed")
		os.Exit(1)
	}
	<-shutdownDone
}
