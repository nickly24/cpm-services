package main

import (
	"context"
	"cpm/calculate/internal/calculate"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://127.0.0.1:8005/health/ready")
		if err != nil {
			os.Exit(1)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	cfg, err := calculate.LoadConfig()
	if err != nil {
		slog.Error("configuration invalid", "reason", err.Error())
		os.Exit(1)
	}
	journal := true
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI).SetMaxPoolSize(cfg.MongoPoolSize).SetMinPoolSize(1).SetServerSelectionTimeout(5 * time.Second).SetWriteConcern(&writeconcern.WriteConcern{W: "majority", Journal: &journal}))
	if err != nil {
		slog.Error("Mongo configuration invalid")
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(ctx)
	}()
	store := &calculate.Store{Collection: client.Database(cfg.Database).Collection(calculate.CollectionName)}
	indexCtx, cancelIndexes := context.WithTimeout(context.Background(), 20*time.Second)
	err = store.EnsureIndexes(indexCtx)
	cancelIndexes()
	if err != nil {
		slog.Error("Mongo inbox initialization failed")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	consumer := &calculate.Consumer{URL: cfg.RabbitURL, Store: store, ReceiptWorkers: cfg.ReceiptWorkers}
	worker := &calculate.Worker{Store: store, Tests: calculate.NewTestsClient(cfg.TestsURL, cfg.ServiceKey)}
	workerDone := make(chan struct{})
	gradingDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		consumer.Run(ctx)
	}()
	go func() {
		defer close(gradingDone)
		if err := worker.RunPool(ctx, cfg.GradingWorkers); err != nil {
			slog.Error("grading worker pool failed", "reason", err.Error())
			stop()
		}
	}()
	service := &calculate.Server{Store: store, Consumer: consumer, ServiceKey: cfg.ServiceKey, Worker: worker}
	var ratingsDone, ratingsConsumerDone chan struct{}
	if cfg.RatingsURL != "" {
		ratingsStore := &calculate.Store{Collection: client.Database(cfg.Database).Collection(calculate.RatingsCollectionName)}
		indexCtx, cancelIndexes := context.WithTimeout(ctx, 20*time.Second)
		err = ratingsStore.EnsureRatingsIndexes(indexCtx)
		cancelIndexes()
		if err != nil {
			slog.Error("Mongo rating inbox initialization failed")
			os.Exit(1)
		}
		ratingsConsumer := &calculate.Consumer{URL: cfg.RabbitURL, Store: ratingsStore, Queue: calculate.RatingsQueueName, Parser: calculate.ParseRatingMessage, Topology: calculate.DeclareRatingsTopology}
		ratingsWorker := &calculate.RatingWorker{Store: ratingsStore, Ratings: calculate.NewRatingsClient(cfg.RatingsURL, cfg.ServiceKey)}
		service.RatingsStore, service.RatingsConsumer, service.RatingsWorker = ratingsStore, ratingsConsumer, ratingsWorker
		ratingsDone, ratingsConsumerDone = make(chan struct{}), make(chan struct{})
		go func() { defer close(ratingsConsumerDone); ratingsConsumer.Run(ctx) }()
		go func() { defer close(ratingsDone); ratingsWorker.Run(ctx) }()
	}
	server := &http.Server{Addr: cfg.Listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("calculate grading service started", "address", cfg.Listen, "grading_workers", cfg.GradingWorkers, "background_workers", 1)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP listener failed")
		stop()
	}
	stop()
	<-shutdownDone
	select {
	case <-workerDone:
	case <-time.After(10 * time.Second):
	}
	// RunPool does not return until every bounded worker has completed its
	// cancellation cleanup. Do not disconnect Mongo underneath in-flight writes.
	<-gradingDone
	if ratingsDone != nil {
		select {
		case <-ratingsDone:
		case <-time.After(10 * time.Second):
		}
		select {
		case <-ratingsConsumerDone:
		case <-time.After(10 * time.Second):
		}
	}
}
