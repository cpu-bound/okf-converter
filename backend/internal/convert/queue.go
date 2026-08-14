package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/sync/errgroup"

	"okf-converter/backend/internal/metrics"
)

// queueName is the durable RabbitMQ queue conversion Jobs are published to
// and consumed from.
const queueName = "file_conversion"

// Dial opens a connection to the RabbitMQ broker. The caller owns the
// returned connection and is responsible for closing it.
func Dial(url string) (*amqp.Connection, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	return conn, nil
}

// RabbitQueue publishes conversion Jobs to a durable RabbitMQ queue and, via
// Start, runs a pool of goroutines consuming and processing them. Unlike an
// in-process buffered channel, a published job survives an API restart or
// crash - RabbitMQ holds it (as a persistent message on a durable queue)
// until a worker acks it.
type RabbitQueue struct {
	channel *amqp.Channel
	conv    Converter
	workers int
}

// NewRabbitQueue opens a channel on conn, declares the durable queue, and
// caps the number of unacked deliveries in flight to workers so jobs are
// spread evenly instead of one consumer prefetching everything.
func NewRabbitQueue(conn *amqp.Connection, conv Converter, workers int) (*RabbitQueue, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	if err := ch.Qos(workers, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}

	if _, err := ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("declare queue: %w", err)
	}

	return &RabbitQueue{channel: ch, conv: conv, workers: workers}, nil
}

// Enqueue publishes job as a persistent message so it survives a broker
// restart until a worker acks it.
func (q *RabbitQueue) Enqueue(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	err = q.channel.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("publish job: %w", err)
	}
	return nil
}

// Start spawns `workers` goroutines fanned out over a single deliveries
// channel, running each through conv and acking it once Convert returns -
// regardless of outcome, since Convert already persists success/failure
// onto the file record, so redelivery would just reprocess a job whose
// result has already been recorded. It returns immediately; the returned
// group finishes once ctx is done and the channel is closed.
func (q *RabbitQueue) Start(ctx context.Context) (*errgroup.Group, error) {
	deliveries, err := q.channel.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)

	for i := 0; i < q.workers; i++ {
		g.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case d, ok := <-deliveries:
					if !ok {
						return nil
					}
					q.process(ctx, d)
				}
			}
		})
	}

	return g, nil
}

func (q *RabbitQueue) process(ctx context.Context, d amqp.Delivery) {
	job, err := decodeJob(d.Body)
	if err != nil {
		slog.Error("convert: dropping malformed job message", "error", err)
		if nackErr := d.Nack(false, false); nackErr != nil {
			slog.Error("convert: nack failed", "error", nackErr)
		}
		return
	}

	start := time.Now()
	err = q.conv.Convert(ctx, job)
	duration := time.Since(start)

	metrics.JobDurationSeconds.Observe(duration.Seconds())

	if err != nil {
		metrics.JobsProcessedTotal.WithLabelValues("failed").Inc()
		slog.Error("convert job failed",
			"job_id", job.FileID,
			"object_key", job.ObjectKey,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
	} else {
		metrics.JobsProcessedTotal.WithLabelValues("success").Inc()
		slog.Info("convert job succeeded",
			"job_id", job.FileID,
			"object_key", job.ObjectKey,
			"duration_ms", duration.Milliseconds(),
		)
	}

	if err := d.Ack(false); err != nil {
		slog.Error("convert: ack failed", "job_id", job.FileID, "error", err)
	}
}

func decodeJob(body []byte) (Job, error) {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}
	return job, nil
}

// Close closes the channel, which also closes the deliveries channel
// handed to any goroutines started by Start, letting them drain their
// current job and exit.
func (q *RabbitQueue) Close() error {
	if err := q.channel.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	return nil
}
