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
// and consumed from. It's the only thing the API and the workers share:
// they run as separate processes, in separate containers, and never talk to
// each other directly.
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

// declareQueue declares the durable job queue. Both sides declare it, so
// neither depends on the other having started first - whichever process
// comes up before the other creates it, and the declaration is idempotent.
func declareQueue(ch *amqp.Channel) error {
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare queue: %w", err)
	}
	return nil
}

// Publisher is the API's half of the queue: it only publishes jobs and
// never consumes them. Keeping publishing and consuming as separate types
// is what lets cmd/api and cmd/worker be different binaries - the API has
// no Converter and cannot process anything even by accident.
type Publisher struct {
	channel *amqp.Channel
}

func NewPublisher(conn *amqp.Connection) (*Publisher, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	if err := declareQueue(ch); err != nil {
		ch.Close()
		return nil, err
	}

	return &Publisher{channel: ch}, nil
}

// Enqueue publishes job as a persistent message so it survives a broker
// restart until a worker acks it. It returns as soon as the broker has the
// message - the conversion itself happens later, in another process.
func (p *Publisher) Enqueue(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	err = p.channel.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("publish job: %w", err)
	}

	metrics.JobsEnqueuedTotal.Inc()
	return nil
}

func (p *Publisher) Close() error {
	if err := p.channel.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	return nil
}

// Consumer is the worker's half of the queue: it consumes jobs and runs
// them through a Converter. Concurrency comes in two independent layers -
// `workers` goroutines inside one process, and however many worker
// containers are running (docker compose up --scale worker=N). RabbitMQ
// hands each job to exactly one of them, so adding containers adds
// throughput without touching the API.
type Consumer struct {
	channel *amqp.Channel
	conv    Converter
	workers int
}

// NewConsumer opens a channel on conn, declares the durable queue, and caps
// the number of unacked deliveries in flight to workers so jobs are spread
// evenly across consumers instead of one prefetching everything - which is
// what makes scaling out the worker container actually distribute load.
func NewConsumer(conn *amqp.Connection, conv Converter, workers int) (*Consumer, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	if err := ch.Qos(workers, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}

	if err := declareQueue(ch); err != nil {
		ch.Close()
		return nil, err
	}

	return &Consumer{channel: ch, conv: conv, workers: workers}, nil
}

// Start spawns `workers` goroutines fanned out over a single deliveries
// channel, running each job through conv and acking it once Convert returns -
// regardless of outcome, since Convert already persists success/failure
// onto the file record, so redelivery would just reprocess a job whose
// result has already been recorded. It returns immediately; the returned
// group finishes once ctx is done and the channel is closed.
func (c *Consumer) Start(ctx context.Context) (*errgroup.Group, error) {
	deliveries, err := c.channel.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)

	for i := 0; i < c.workers; i++ {
		g.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case d, ok := <-deliveries:
					if !ok {
						return nil
					}
					c.process(ctx, d)
				}
			}
		})
	}

	return g, nil
}

func (c *Consumer) process(ctx context.Context, d amqp.Delivery) {
	job, err := decodeJob(d.Body)
	if err != nil {
		slog.Error("convert: dropping malformed job message", "error", err)
		if nackErr := d.Nack(false, false); nackErr != nil {
			slog.Error("convert: nack failed", "error", nackErr)
		}
		return
	}

	metrics.JobsInFlight.Inc()
	start := time.Now()
	err = c.conv.Convert(ctx, job)
	duration := time.Since(start)
	metrics.JobsInFlight.Dec()

	metrics.JobDurationSeconds.Observe(duration.Seconds())

	if err != nil {
		metrics.JobsProcessedTotal.WithLabelValues("failed").Inc()
		slog.Error("convert job failed",
			"job_id", job.JobID,
			"file_id", job.FileID,
			"object_key", job.ObjectKey,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
	} else {
		metrics.JobsProcessedTotal.WithLabelValues("success").Inc()
		slog.Info("convert job succeeded",
			"job_id", job.JobID,
			"file_id", job.FileID,
			"object_key", job.ObjectKey,
			"duration_ms", duration.Milliseconds(),
		)
	}

	if err := d.Ack(false); err != nil {
		slog.Error("convert: ack failed", "job_id", job.JobID, "error", err)
	}
}

func decodeJob(body []byte) (Job, error) {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}
	return job, nil
}

// Close closes the channel, which also closes the deliveries channel handed
// to any goroutines started by Start, letting them drain their current job
// and exit.
func (c *Consumer) Close() error {
	if err := c.channel.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	return nil
}
