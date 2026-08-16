// Command api runs the okf-converter HTTP API.
//
// The API only ever *publishes* conversion jobs: it accepts an upload, hands
// back a job identifier, and returns. The conversion itself runs in a
// separate process (see cmd/worker), in its own container, so workers can be
// scaled without scaling the API and a slow conversion can never occupy an
// HTTP request.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"okf-converter/backend/internal/auth"
	"okf-converter/backend/internal/config"
	"okf-converter/backend/internal/convert"
	"okf-converter/backend/internal/db"
	"okf-converter/backend/internal/files"
	"okf-converter/backend/internal/httpx"
	"okf-converter/backend/internal/middleware"
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
	if err := store.EnsureBucket(ctx); err != nil {
		return err
	}

	rabbitConn, err := convert.Dial(cfg.RabbitMQURL)
	if err != nil {
		return err
	}
	defer rabbitConn.Close()

	fileRepo := files.NewPgFileRepository(dbPool)
	outputRepo := files.NewPgOutputRepository(dbPool)
	jobRepo := files.NewPgJobRepository(dbPool)

	publisher, err := convert.NewPublisher(rabbitConn)
	if err != nil {
		return err
	}

	authHandlers := auth.NewHandlers(auth.NewPgUserRepository(dbPool), cfg.JWTSecret, cfg.IsProduction())

	fileHandlers := files.NewHandlers(fileRepo, outputRepo, jobRepo, store)
	fileHandlers.SupportedFormat = convert.Supports
	fileHandlers.SupportedFormatMessage = "Formato no soportado. Se admiten documentos en " + convert.SupportedFormatList() + "."
	fileHandlers.EnqueueConversion = func(ctx context.Context, file files.File, objectKey string, retryOf *string) {
		convertJob, err := jobRepo.Create(ctx, file.ID, retryOf)
		if err != nil {
			log.Printf("no se pudo registrar el trabajo de conversión del archivo %s: %v", file.ID, err)
			return
		}

		job := convert.Job{
			JobID:        convertJob.ID,
			FileID:       file.ID,
			ObjectKey:    objectKey,
			ContentType:  file.ContentType,
			OriginalName: file.OriginalName,
			Size:         file.Size,
		}
		if err := publisher.Enqueue(ctx, job); err != nil {
			log.Printf("no se pudo encolar el trabajo de conversión del archivo %s: %v", file.ID, err)
		}
	}

	requireAuth := middleware.RequireAuth(authHandlers.Loader())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	mux.HandleFunc("POST /api/auth/register", authHandlers.Register)
	mux.HandleFunc("POST /api/auth/login", authHandlers.Login)
	mux.HandleFunc("GET /api/auth/me", authHandlers.Me)
	mux.HandleFunc("POST /api/auth/logout", authHandlers.Logout)
	mux.HandleFunc("POST /api/auth/check-email", authHandlers.CheckEmail)
	mux.HandleFunc("POST /api/auth/reset-password", authHandlers.ResetPassword)

	mux.Handle("POST /api/files/upload-url", requireAuth(http.HandlerFunc(fileHandlers.UploadURL)))
	mux.Handle("POST /api/files/{id}/confirm", requireAuth(http.HandlerFunc(fileHandlers.Confirm)))
	mux.Handle("GET /api/files/{id}", requireAuth(http.HandlerFunc(fileHandlers.Status)))
	mux.Handle("POST /api/files/{id}/retry", requireAuth(http.HandlerFunc(fileHandlers.Retry)))
	mux.Handle("GET /api/files/{id}/outputs", requireAuth(http.HandlerFunc(fileHandlers.Outputs)))
	mux.Handle("GET /api/files/{id}/bundle", requireAuth(http.HandlerFunc(fileHandlers.DownloadBundle)))

	handler := middleware.Recover(httpx.CORS(cfg.FrontendURL, mux))

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: handler,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("API escuchando en :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		log.Println("apagando la API")
	case err := <-serveErr:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shutdownErr := srv.Shutdown(shutdownCtx)

	if err := publisher.Close(); err != nil {
		log.Printf("error al cerrar el publicador de la cola: %v", err)
	}

	return shutdownErr
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
