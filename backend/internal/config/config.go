// Package config loads and validates the process configuration from
// environment variables. Every value comes from the environment - the actual
// settings live in the repo-root .env that docker-compose.yml interpolates,
// never in this code.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type MinioConfig struct {
	Endpoint  string
	Port      int
	UseSSL    bool
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string

	// PublicEndpoint/PublicPort are used only for signing presigned URLs,
	// which the browser follows directly - must be reachable from outside
	// the Docker network. They fall back to Endpoint/Port/UseSSL when unset.
	PublicEndpoint string
	PublicPort     int
	PublicUseSSL   bool
}

type Config struct {
	Env         string
	Port        string
	FrontendURL string

	JWTSecret   string
	DatabaseURL string
	RabbitMQURL string

	// WorkerConcurrency is how many jobs a single worker process converts in
	// parallel (cmd/worker only; the API never consumes). Total throughput is
	// this times the number of worker containers.
	WorkerConcurrency int

	Minio MinioConfig
}

// IsProduction reports whether the cookie Secure flag should be set, i.e.
// whether APP_ENV says this deployment is served over HTTPS.
func (c Config) IsProduction() bool {
	return c.Env == "production"
}

func Load() (Config, error) {
	minioPort, err := intEnv("MINIO_PORT", 9000)
	if err != nil {
		return Config{}, err
	}

	publicPort := minioPort
	if v := os.Getenv("MINIO_PUBLIC_PORT"); v != "" {
		publicPort, err = strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("MINIO_PUBLIC_PORT: %w", err)
		}
	}

	publicEndpoint := os.Getenv("MINIO_PUBLIC_ENDPOINT")
	minioEndpoint := stringEnv("MINIO_ENDPOINT", "minio")
	if publicEndpoint == "" {
		publicEndpoint = minioEndpoint
	}

	accessKey, err := required("MINIO_ACCESS_KEY")
	if err != nil {
		return Config{}, err
	}

	secretKey, err := required("MINIO_SECRET_KEY")
	if err != nil {
		return Config{}, err
	}

	jwtSecret, err := required("JWT_SECRET")
	if err != nil {
		return Config{}, err
	}

	databaseURL, err := required("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}

	rabbitMQURL, err := required("RABBITMQ_URL")
	if err != nil {
		return Config{}, err
	}

	minioUseSSL := boolEnv("MINIO_USE_SSL")

	workerConcurrency, err := intEnv("WORKER_CONCURRENCY", 2)
	if err != nil {
		return Config{}, err
	}
	if workerConcurrency < 1 {
		return Config{}, fmt.Errorf("WORKER_CONCURRENCY must be at least 1")
	}

	return Config{
		Env:         os.Getenv("APP_ENV"),
		Port:        stringEnv("PORT", "3000"),
		FrontendURL: stringEnv("FRONTEND_URL", "http://localhost:8080"),

		JWTSecret:   jwtSecret,
		DatabaseURL: databaseURL,
		RabbitMQURL: rabbitMQURL,

		WorkerConcurrency: workerConcurrency,

		Minio: MinioConfig{
			Endpoint:  minioEndpoint,
			Port:      minioPort,
			UseSSL:    minioUseSSL,
			AccessKey: accessKey,
			SecretKey: secretKey,
			Bucket:    stringEnv("MINIO_BUCKET", "user-files"),
			Region:    stringEnv("MINIO_REGION", "us-east-1"),

			PublicEndpoint: publicEndpoint,
			PublicPort:     publicPort,
			// Mirrors the original `publicUseSSL || useSSL` JS fallback: a
			// falsy (absent or "false") public value still falls back to the
			// internal flag, regardless of whether the var was explicitly set.
			PublicUseSSL: boolEnv("MINIO_PUBLIC_USE_SSL") || minioUseSSL,
		},
	}, nil
}

func required(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func stringEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func intEnv(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

func boolEnv(name string) bool {
	return os.Getenv(name) == "true"
}
