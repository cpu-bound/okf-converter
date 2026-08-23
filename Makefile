# OKF Converter - atajos de desarrollo y despliegue.
#
# Todo corre sobre Docker: no se necesita Go ni Node instalados en la
# máquina. Los targets de test/build del backend usan la misma imagen de Go
# que el Dockerfile, y los del frontend una imagen de Node.

# La configuración vive en .env (la misma que carga Docker Compose), así que
# los targets de abajo reportan los puertos y credenciales reales en uso.
-include .env
export

COMPOSE      := docker compose
GO_IMAGE     := golang:1.26-alpine
NODE_IMAGE   := node:22-alpine

# Servicio y réplicas para `make scale` (ej: make scale SERVICE=worker N=3).
SERVICE      ?= worker
N            ?= 3

# Servicio para `make logs-one` / `make sh` (ej: make sh S=db).
S            ?= api

.DEFAULT_GOAL := help

# --------------------------------------------------------------------------
# Despliegue
# --------------------------------------------------------------------------

.PHONY: up
up: ## Levanta todo el stack en segundo plano (construyendo lo necesario)
	$(COMPOSE) up --build -d
	@$(MAKE) --no-print-directory urls

.PHONY: up-fg
up-fg: ## Levanta el stack en primer plano, con logs (demo de despliegue)
	$(COMPOSE) up --build

.PHONY: down
down: ## Detiene y elimina los contenedores (conserva los volúmenes)
	$(COMPOSE) down

.PHONY: restart
restart: ## Reinicia los contenedores sin reconstruir
	$(COMPOSE) restart

.PHONY: rebuild
rebuild: ## Reconstruye una imagen y reinicia su servicio (make rebuild S=api)
	$(COMPOSE) up --build -d --no-deps $(S)

.PHONY: scale
scale: ## Escala los workers sin tocar la API (make scale N=4)
	$(COMPOSE) up -d --no-deps --scale $(SERVICE)=$(N) $(SERVICE)
	@$(COMPOSE) ps $(SERVICE)

.PHONY: reset
reset: ## Borra contenedores Y volúmenes (Postgres, MinIO, RabbitMQ) - entorno limpio
	$(COMPOSE) down -v --remove-orphans

.PHONY: fresh
fresh: reset up ## Entorno completamente limpio y stack levantado desde cero

# --------------------------------------------------------------------------
# Observación
# --------------------------------------------------------------------------

.PHONY: ps
ps: ## Estado de los contenedores
	$(COMPOSE) ps

.PHONY: logs
logs: ## Logs de todos los servicios (Ctrl-C para salir)
	$(COMPOSE) logs -f --tail=100

.PHONY: logs-one
logs-one: ## Logs de un servicio (make logs-one S=api)
	$(COMPOSE) logs -f --tail=100 $(S)

.PHONY: sh
sh: ## Abre una shell dentro de un contenedor (make sh S=api)
	$(COMPOSE) exec $(S) sh

.PHONY: psql
psql: ## Consola psql sobre la base de datos
	$(COMPOSE) exec db sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

.PHONY: health
health: ## Verifica que la API responda a través del proxy del frontend
	@curl -fsS $(FRONTEND_URL)/api/health && echo "" || \
		(echo "La API no responde. ¿Está levantado el stack? (make up)"; exit 1)

.PHONY: config
config: ## Muestra el compose ya resuelto con los valores de .env
	$(COMPOSE) config

.PHONY: queue
queue: ## Muestra el estado de la cola de conversión en RabbitMQ
	$(COMPOSE) exec rabbitmq rabbitmqctl list_queues name messages messages_unacknowledged consumers

.PHONY: urls
urls: ## Imprime las URLs de todos los servicios
	@echo ""
	@echo "  App          $(FRONTEND_URL)"
	@echo "  MinIO        http://localhost:$(MINIO_CONSOLE_PORT)   ($(MINIO_ROOT_USER) / $(MINIO_ROOT_PASSWORD))"
	@echo "  RabbitMQ     http://localhost:$(RABBITMQ_UI_PORT)  ($(RABBITMQ_USER) / $(RABBITMQ_PASSWORD))"
	@echo "  Prometheus   http://localhost:$(PROMETHEUS_PORT)"
	@echo "  Grafana      http://localhost:$(GRAFANA_PORT)   ($(GRAFANA_USER) / $(GRAFANA_PASSWORD))"
	@echo ""

# --------------------------------------------------------------------------
# Backend (Go, dentro de Docker)
# --------------------------------------------------------------------------

GO_RUN = docker run --rm -v "$(CURDIR)/backend":/src -w /src \
	-v okf-go-mod:/go/pkg/mod -v okf-go-build:/root/.cache/go-build $(GO_IMAGE)

.PHONY: test-backend
test-backend: ## Ejecuta las pruebas del backend en Go
	$(GO_RUN) go test ./...

.PHONY: build-backend
build-backend: ## Compila el backend (verificación rápida de que todo compila)
	$(GO_RUN) go build ./...

.PHONY: vet
vet: ## go vet sobre el backend
	$(GO_RUN) go vet ./...

.PHONY: fmt
fmt: ## Formatea el código Go
	$(GO_RUN) go fmt ./...

.PHONY: tidy
tidy: ## Actualiza go.mod / go.sum
	$(GO_RUN) go mod tidy

# --------------------------------------------------------------------------
# Frontend (Angular, dentro de Docker)
# --------------------------------------------------------------------------

NODE_RUN = docker run --rm -v "$(CURDIR)/frontend":/app -w /app $(NODE_IMAGE)

.PHONY: test-frontend
test-frontend: ## Ejecuta las pruebas del frontend (Vitest)
	$(NODE_RUN) npm test

.PHONY: build-frontend
build-frontend: ## Compila el frontend de Angular
	$(NODE_RUN) npm run build

.PHONY: install-frontend
install-frontend: ## Instala las dependencias de npm del frontend
	$(NODE_RUN) npm ci

# --------------------------------------------------------------------------
# Pruebas contra el stack levantado
# --------------------------------------------------------------------------

.PHONY: smoke
smoke: ## Prueba de punta a punta contra el stack (necesita 'make up')
	@bash scripts/smoke.sh

.PHONY: tolerancia
tolerancia: ## Idempotencia, reintentos y descartes (DETIENE MinIO un rato)
	@bash scripts/tolerancia.sh

.PHONY: carga
carga: ## Carga con perfil de horas punta, 10 min (ESCALA el worker y lo restaura)
	@bash scripts/carga.sh

# --------------------------------------------------------------------------
# Agregados
# --------------------------------------------------------------------------

.PHONY: test
test: test-backend test-frontend ## Ejecuta todas las pruebas

.PHONY: check
check: vet build-backend test ## Verificación completa antes de entregar

.PHONY: clean
clean: reset ## Igual que reset, pero además elimina las imágenes del proyecto
	-docker image rm okf-converter-api okf-converter-frontend 2>/dev/null || true
	-docker volume rm okf-go-mod okf-go-build 2>/dev/null || true

.PHONY: help
help: ## Muestra esta ayuda
	@echo ""
	@echo "  OKF Converter - targets disponibles"
	@echo ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo ""
