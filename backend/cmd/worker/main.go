// Command worker runs the conversion pipeline.
//
// It shares the codebase, the database, the object storage and the queue
// with cmd/api, but nothing else: it serves no application traffic and the
// API cannot reach it. The only coupling is the durable RabbitMQ queue the
// API publishes to, which is what makes `docker compose up --scale
// worker=N` add conversion throughput without touching the API.
//
// The HTTP server started here exists solely so Prometheus can scrape
// /metrics from every replica; it serves nothing else.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"okf-converter/backend/internal/config"
	"okf-converter/backend/internal/convert"
	"okf-converter/backend/internal/db"
	"okf-converter/backend/internal/files"
	"okf-converter/backend/internal/httpx"
	"okf-converter/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbPool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer dbPool.Close()

	store, err := storage.New(cfg.Minio)
	if err != nil {
		return err
	}

	rabbitConn, err := convert.Dial(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer rabbitConn.Close()

	jobRepo := files.NewPgJobRepository(dbPool)

	converter := convert.NewBundleConverter(
		store,
		files.NewPgFileRepository(dbPool),
		files.NewPgOutputRepository(dbPool),
		jobRepo,
	)

	consumer, err := convert.NewConsumer(rabbitConn, converter, jobRepo, convert.ConsumerConfig{
		Workers:     cfg.WorkerConcurrency,
		MaxAttempts: cfg.WorkerMaxAttempts,
		RetryDelay:  cfg.WorkerRetryDelay,
	})
	if err != nil {
		return err
	}

	group, err := consumer.Start(ctx)
	if err != nil {
		return err
	}

	slog.Info("worker iniciado",
		"concurrency", cfg.WorkerConcurrency,
		"max_attempts", cfg.WorkerMaxAttempts,
		"retry_delay", cfg.WorkerRetryDelay.String(),
	)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: metricsHandler()}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("apagando el worker")
	case err := <-serveErr:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)

	// Closing the consumer stops new deliveries; Wait then blocks until the
	// jobs already in flight finish, so a redeploy or a scale-down never
	// abandons a conversion halfway through.
	if err := consumer.Close(); err != nil {
		slog.Error("falló el cierre del consumidor de la cola", "error", err)
	}
	if err := group.Wait(); err != nil {
		slog.Error("falló el apagado del consumidor de la cola", "error", err)
	}

	return shutdownErr
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}
