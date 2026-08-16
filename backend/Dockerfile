FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# Both binaries come from the same module and the same build: the API and
# the workers share every package below internal/, and differ only in which
# half of the queue they use. The compose file picks the entrypoint.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...


FROM gcr.io/distroless/static-debian12

COPY --from=build /out/api /api
COPY --from=build /out/worker /worker

EXPOSE 3000

USER nonroot:nonroot

ENTRYPOINT ["/api"]
