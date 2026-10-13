package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"cpm/file-work-service/internal/filework"
	"github.com/google/uuid"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("LISTEN_ADDR")
		if addr == "" {
			addr = ":8007"
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			os.Exit(1)
		}
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			os.Exit(1)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	config, err := filework.LoadConfig()
	if err != nil {
		log.Error("configuration invalid", "reason", err.Error())
		os.Exit(1)
	}
	for _, binary := range []string{config.QPDF, config.Ghostscript} {
		if _, err = exec.LookPath(binary); err != nil {
			log.Error("PDF runtime dependency missing")
			os.Exit(1)
		}
	}
	storage, err := filework.NewS3Storage(config)
	if err != nil {
		log.Error("S3 configuration invalid")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	instanceID, err := uuid.NewRandom()
	if err != nil {
		log.Error("worker instance initialization failed")
		os.Exit(1)
	}
	w := &filework.BrokerWorker{RabbitURL: config.RabbitURL, Log: log, Engine: filework.Engine{Claim: filework.HomeworkClient{URL: config.HomeworkURL, Key: config.HomeworkServiceKey}, Storage: storage, Compressor: filework.PDFCompressor{QPDF: config.QPDF, Ghostscript: config.Ghostscript}, TempDir: config.TempDir, Timeout: config.ProcessTimeout}}
	go w.Run(ctx)
	monitor := filework.NewHealthMonitor(instanceID.String(), config.HomeworkURL, config.HomeworkServiceKey, w, true, storage)
	go monitor.Run(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(map[string]bool{"status": true})
	})
	mux.HandleFunc("GET /readyz", monitor.ReadyzHandler)
	server := &http.Server{Addr: config.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		server.Shutdown(shutdownCtx)
	}()
	log.Info("file-work-service started", "listen_addr", config.ListenAddr)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("health server failed")
		os.Exit(1)
	}
}
