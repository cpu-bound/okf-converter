package convert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/sync/errgroup"

	"okf-converter/backend/internal/metrics"
)

// The three durable queues the conversion pipeline runs on. The main queue is
// the only thing the API and the workers share: they run as separate
// processes, in separate containers, and never talk to each other directly.
//
//	file_conversion         trabajos por convertir
//	file_conversion.retry   espera entre intentos; vence hacia la principal
//	file_conversion.dead    intentos agotados, para inspección
//
// The retry queue is how a failed job waits before being tried again: it
// holds no consumer and carries a message TTL, so every message that lands in
// it is dead-lettered back into the main queue once the delay elapses. That
// is the standard way to delay a redelivery in RabbitMQ without plugins.
//
// The delay is a property of the queue rather than of each message on
// purpose: a per-message TTL in a shared queue only expires messages at its
// head, so one long delay would block every shorter one behind it. The cost
// is that the wait is fixed rather than exponential.
const (
	queueName      = "file_conversion"
	retryQueueName = queueName + ".retry"
	deadQueueName  = queueName + ".dead"
)

// broker owns the AMQP connection and hands out a channel that was open when
// it was asked for, redialing whenever the previous one died. Both halves of
// the pipeline sit on top of one.
//
// It exists because a channel opened once at startup only works until the
// first network hiccup. After that every publish fails with "channel/
// connection is not open" and nothing ever repairs it: a process that had
// been up for a day would keep accepting uploads and silently convert none
// of them, answering 200 the whole time. An idle publisher is the most
// exposed of all, because nothing reveals the dead channel until someone
// uses it.
type broker struct {
	url string

	// setup runs on every freshly opened channel. Each side declares what it
	// needs, so a reconnect restores the topology this process depends on
	// instead of assuming the broker kept it across the outage.
	setup func(*amqp.Channel) error

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

func newBroker(url string, setup func(*amqp.Channel) error) *broker {
	return &broker{url: url, setup: setup}
}

// channel returns a healthy channel, opening a connection first if there is
// none. "Healthy" is only true as of this instant - the link can drop between
// this call and the publish that follows - which is why callers retry once
// rather than trusting the check.
func (b *broker) channel() (*amqp.Channel, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ch != nil && !b.ch.IsClosed() {
		return b.ch, nil
	}
	b.ch = nil

	if b.conn == nil || b.conn.IsClosed() {
		if b.conn != nil {
			b.conn.Close()
		}
		conn, err := amqp.Dial(b.url)
		if err != nil {
			b.conn = nil
			return nil, fmt.Errorf("dial rabbitmq: %w", err)
		}
		b.conn = conn
	}

	ch, err := b.conn.Channel()
	if err != nil {
		// The connection reported itself open but would not give a channel,
		// so drop it: asking a corpse for another channel just fails again.
		b.conn.Close()
		b.conn = nil
		return nil, fmt.Errorf("open channel: %w", err)
	}

	if err := b.setup(ch); err != nil {
		ch.Close()
		return nil, err
	}

	b.ch = ch
	return ch, nil
}

// reset drops the cached channel so the next call to channel opens a new one.
// Called after a failed publish, where the error is itself the evidence that
// the channel is gone.
func (b *broker) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		b.ch.Close()
		b.ch = nil
	}
}

func (b *broker) close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ch != nil {
		b.ch.Close()
		b.ch = nil
	}
	if b.conn != nil {
		err := b.conn.Close()
		b.conn = nil
		if err != nil {
			return fmt.Errorf("close rabbitmq connection: %w", err)
		}
	}
	return nil
}

// declareQueue declares the durable main job queue. Both sides declare it, so
// neither depends on the other having started first - whichever process
// comes up before the other creates it, and the declaration is idempotent.
func declareQueue(ch *amqp.Channel) error {
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare queue: %w", err)
	}
	return nil
}

// declareRetryTopology declares the two queues only the workers need: the
// delay queue that feeds failed jobs back into the main one, and the
// dead-letter queue where a job lands once its attempts are spent. The API
// never publishes to either, so it never declares them.
func declareRetryTopology(ch *amqp.Channel, retryDelay time.Duration) error {
	// No consumer ever reads this queue: messages leave it by expiring, and
	// the empty exchange with the main queue's name as routing key is how a
	// dead-lettered message gets routed straight back to it.
	_, err := ch.QueueDeclare(retryQueueName, true, false, false, false, amqp.Table{
		"x-message-ttl":             int32(retryDelay.Milliseconds()),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": queueName,
	})
	if err != nil {
		return fmt.Errorf("declare retry queue: %w", err)
	}

	if _, err := ch.QueueDeclare(deadQueueName, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead-letter queue: %w", err)
	}

	return nil
}

// Publisher is the API's half of the queue: it only publishes jobs and
// never consumes them. Keeping publishing and consuming as separate types
// is what lets cmd/api and cmd/worker be different binaries - the API has
// no Converter and cannot process anything even by accident.
type Publisher struct {
	broker *broker
}

// NewPublisher connects to the broker and declares the job queue. It fails if
// the broker is unreachable right now, so a bad URL stops the process at
// startup instead of surfacing hours later as an upload that never converts.
func NewPublisher(url string) (*Publisher, error) {
	b := newBroker(url, declareQueue)
	if _, err := b.channel(); err != nil {
		return nil, err
	}
	return &Publisher{broker: b}, nil
}

// Enqueue publishes job as a persistent message so it survives a broker
// restart until a worker acks it. It returns as soon as the broker has the
// message - the conversion itself happens later, in another process.
func (p *Publisher) Enqueue(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}

	// Two attempts, not a loop. The first can land on a channel that died
	// while it sat idle - the common case, and one nothing reveals until it
	// is used - and the second runs on a channel opened just now. If that
	// fails too the broker is genuinely unreachable, and the caller has to
	// hear about it rather than have it buried in a log line.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ch, err := p.broker.channel()
		if err != nil {
			lastErr = err
			continue
		}

		err = ch.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		})
		if err == nil {
			metrics.JobsEnqueuedTotal.Inc()
			return nil
		}

		lastErr = err
		p.broker.reset()
	}

	return fmt.Errorf("publish job: %w", lastErr)
}

func (p *Publisher) Close() error {
	return p.broker.close()
}

// Consumer is the worker's half of the queue: it consumes jobs and runs
// them through a Converter. Concurrency comes in two independent layers -
// `workers` goroutines inside one process, and however many worker
// containers are running (docker compose up --scale worker=N). RabbitMQ
// hands each job to exactly one of them, so adding containers adds
// throughput without touching the API.
type Consumer struct {
	broker *broker
	conv   Converter
	claims JobClaimer

	workers     int
	maxAttempts int

	// publish is how a delivery gets parked on the retry or dead-letter
	// queue. It is a field rather than a direct call on channel so that the
	// decision logic in process - which is the whole of the idempotency and
	// retry policy - can be tested without a broker.
	publish func(ctx context.Context, queue string, body []byte) error
}

// JobClaimer is satisfied by *files.PgJobRepository. Declared here (rather
// than importing internal/files) so this package depends on files only
// through the narrow slice of behavior it actually needs - and, just as
// importantly, so the API's JobRepository interface does not carry these:
// claiming work is the workers' business alone.
type JobClaimer interface {
	// Claim atomically takes ownership of a job. claimed=false means some
	// other delivery of the same job already has it, and this one must be
	// acked without converting anything. attempts is the post-increment
	// count, so a job's first claim returns 1.
	Claim(ctx context.Context, jobID string, redelivered bool) (attempts int, claimed bool, err error)
	// Requeue puts a job that failed but will be retried back into a waiting
	// state, together with its file, so nothing reports a final failure for
	// work that is still in progress.
	Requeue(ctx context.Context, jobID, fileID, lastErr string) error
}

// ConsumerConfig is the worker-side tuning, all of it from the environment
// (see internal/config).
type ConsumerConfig struct {
	// Workers is how many jobs this process converts in parallel.
	Workers int
	// MaxAttempts caps how many times a job is converted before it is given
	// up on and dead-lettered. 1 disables automatic retries.
	MaxAttempts int
	// RetryDelay is how long a failed job waits before the next attempt.
	RetryDelay time.Duration
}

// NewConsumer opens a channel on conn, declares the queues, and caps the
// number of unacked deliveries in flight to Workers so jobs are spread evenly
// across consumers instead of one prefetching everything - which is what
// makes scaling out the worker container actually distribute load.
func NewConsumer(url string, conv Converter, claims JobClaimer, cfg ConsumerConfig) (*Consumer, error) {
	c := &Consumer{
		conv:        conv,
		claims:      claims,
		workers:     cfg.Workers,
		maxAttempts: cfg.MaxAttempts,
	}

	// The prefetch cap and the queue declarations are re-applied on every
	// channel, including the ones opened after a reconnect: they are
	// properties of this consumer's session, not one-time setup.
	c.broker = newBroker(url, func(ch *amqp.Channel) error {
		if err := ch.Qos(cfg.Workers, 0, false); err != nil {
			return fmt.Errorf("set qos: %w", err)
		}
		if err := declareQueue(ch); err != nil {
			return err
		}
		return declareRetryTopology(ch, cfg.RetryDelay)
	})

	// Resolved per call rather than bound to one channel, so a retry
	// scheduled after a reconnect goes out on the live channel.
	c.publish = func(ctx context.Context, queue string, body []byte) error {
		ch, err := c.broker.channel()
		if err != nil {
			return err
		}
		return ch.PublishWithContext(ctx, "", queue, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		})
	}

	if _, err := c.broker.channel(); err != nil {
		return nil, err
	}

	return c, nil
}

// Start spawns `workers` goroutines fanned out over a single deliveries
// channel, running each job through conv and acking it once Convert returns -
// regardless of outcome, since Convert already persists success/failure
// onto the file record, so redelivery would just reprocess a job whose
// result has already been recorded. It returns immediately; the returned
// group finishes once ctx is done and the channel is closed.
func (c *Consumer) Start(ctx context.Context) (*errgroup.Group, error) {
	// The first subscription has to succeed. A worker that cannot consume at
	// all is a misconfiguration, and failing here reports it at startup
	// instead of leaving a container that looks healthy and does nothing.
	deliveries, err := c.consume()
	if err != nil {
		return nil, err
	}

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		for {
			c.drain(ctx, deliveries)

			if ctx.Err() != nil {
				return nil
			}

			// The deliveries channel closed on its own, which means the link
			// to the broker went down. Resubscribing is the whole point: a
			// worker that quietly stops consuming keeps its container up,
			// its health green and its queue growing.
			slog.Warn("conversión: se perdió la conexión con RabbitMQ, reconectando")
			c.broker.reset()

			deliveries, err = c.resubscribe(ctx)
			if err != nil {
				return nil
			}
		}
	})

	return g, nil
}

// drain fans one subscription out over `workers` goroutines and returns once
// the deliveries channel closes or ctx is done - so its return is precisely
// the signal that the session ended, however it ended.
func (c *Consumer) drain(ctx context.Context, deliveries <-chan amqp.Delivery) {
	var wg sync.WaitGroup

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok {
						return
					}
					c.process(ctx, d)
				}
			}
		}()
	}

	wg.Wait()
}

func (c *Consumer) consume() (<-chan amqp.Delivery, error) {
	ch, err := c.broker.channel()
	if err != nil {
		return nil, err
	}

	deliveries, err := ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consume: %w", err)
	}
	return deliveries, nil
}

// resubscribe retries until it has a working subscription or ctx is done. The
// wait doubles up to a ceiling so a broker that stays down produces a slow
// trickle of attempts rather than a dial loop.
func (c *Consumer) resubscribe(ctx context.Context) (<-chan amqp.Delivery, error) {
	const maxDelay = 30 * time.Second
	delay := time.Second

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		deliveries, err := c.consume()
		if err == nil {
			slog.Info("conversión: reconectado a RabbitMQ")
			return deliveries, nil
		}

		slog.Error("conversión: no se pudo reconectar a RabbitMQ",
			"error", err, "siguiente_intento_en", delay)
		c.broker.reset()

		if delay < maxDelay {
			delay *= 2
		}
	}
}

// process handles one delivery. Every path through it ends in an ack: the
// message is either converted, deliberately skipped, parked in the retry
// queue or dead-lettered, and in all four cases this copy of it is done. What
// decides between them is the atomic claim - never the message itself, which
// is why a duplicated delivery cannot produce a second bundle.
func (c *Consumer) process(ctx context.Context, d amqp.Delivery) {
	job, err := decodeJob(d.Body)
	if err != nil {
		// Nothing here can be retried: the message does not name a job, so
		// there is no state to advance and no attempt count to consult.
		slog.Error("conversión: mensaje de trabajo mal formado, se envía a la cola de descartes", "error", err)
		c.publishTo(ctx, deadQueueName, d.Body)
		metrics.JobsDeadLetteredTotal.WithLabelValues("malformed").Inc()
		c.ack(d, "")
		return
	}

	attempts, claimed, err := c.claims.Claim(ctx, job.JobID, d.Redelivered)
	if err != nil {
		// The database is unreachable, so there is no way to tell whether
		// this job was already converted - and converting it blindly is
		// exactly how a second bundle gets published. Park it instead: the
		// retry queue's delay throttles the cycle while the database is down.
		slog.Error("conversión: no se pudo reclamar el trabajo, se reprograma",
			"job_id", job.JobID, "error", err)
		c.publishTo(ctx, retryQueueName, d.Body)
		c.ack(d, job.JobID)
		return
	}

	if !claimed {
		metrics.JobsSkippedTotal.Inc()
		slog.Info("conversión: entrega descartada, el trabajo ya no está disponible para reclamar",
			"job_id", job.JobID, "file_id", job.FileID, "redelivered", d.Redelivered)
		c.ack(d, job.JobID)
		return
	}

	metrics.JobsInFlight.Inc()
	start := time.Now()
	convErr := c.conv.Convert(ctx, job)
	duration := time.Since(start)
	metrics.JobsInFlight.Dec()

	metrics.JobDurationSeconds.Observe(duration.Seconds())

	if convErr == nil {
		metrics.JobsProcessedTotal.WithLabelValues("success").Inc()
		slog.Info("trabajo de conversión completado",
			"job_id", job.JobID,
			"file_id", job.FileID,
			"object_key", job.ObjectKey,
			"attempt", attempts,
			"duration_ms", duration.Milliseconds(),
		)
		c.ack(d, job.JobID)
		return
	}

	metrics.JobsProcessedTotal.WithLabelValues("failed").Inc()
	c.handleFailure(ctx, d, job, attempts, duration, convErr)
	c.ack(d, job.JobID)
}

// handleFailure decides what a failed attempt costs: another go at it, or the
// end of the line. Convert has already recorded the failure on the file and
// the job, so all that is left is whether to put them back into a waiting
// state and schedule the next attempt.
func (c *Consumer) handleFailure(ctx context.Context, d amqp.Delivery, job Job, attempts int, duration time.Duration, convErr error) {
	exhausted := attempts >= c.maxAttempts

	slog.Error("trabajo de conversión fallido",
		"job_id", job.JobID,
		"file_id", job.FileID,
		"object_key", job.ObjectKey,
		"attempt", attempts,
		"max_attempts", c.maxAttempts,
		"duration_ms", duration.Milliseconds(),
		"exhausted", exhausted,
		"error", convErr,
	)

	if exhausted {
		// The file and job stay 'failed' with the reason Convert recorded,
		// which is what the user sees - and what the manual retry (§5.2)
		// acts on. The dead-letter queue keeps the message itself around for
		// inspection rather than dropping it.
		c.publishTo(ctx, deadQueueName, d.Body)
		metrics.JobsDeadLetteredTotal.WithLabelValues("exhausted").Inc()
		return
	}

	// Put the job back in a waiting state *before* scheduling the retry, so
	// there is no window in which the retry could be claimed while the record
	// still says the previous attempt failed.
	if err := c.claims.Requeue(ctx, job.JobID, job.FileID, convErr.Error()); err != nil {
		slog.Error("conversión: no se pudo devolver el trabajo a la cola de espera",
			"job_id", job.JobID, "error", err)
		// Leaving it 'failed' is recoverable - the claim accepts 'failed' -
		// so the retry is still worth scheduling.
	}

	c.publishTo(ctx, retryQueueName, d.Body)
	metrics.JobsRetriedTotal.Inc()
}

// publishTo puts body on one of the worker-side queues. A failure here is
// logged rather than returned: the delivery is acked either way, and the
// alternative - leaving it unacked - would have the broker redeliver it into
// the same failure.
func (c *Consumer) publishTo(ctx context.Context, queue string, body []byte) {
	if err := c.publish(ctx, queue, body); err != nil {
		slog.Error("conversión: no se pudo publicar el mensaje", "queue", queue, "error", err)
	}
}

func (c *Consumer) ack(d amqp.Delivery, jobID string) {
	if err := d.Ack(false); err != nil {
		slog.Error("conversión: falló el ack del mensaje", "job_id", jobID, "error", err)
	}
}

func decodeJob(body []byte) (Job, error) {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}
	return job, nil
}

// Close drops the connection, which also closes the deliveries channel handed
// to the goroutines started by Start, letting them finish their current job
// and exit.
func (c *Consumer) Close() error {
	return c.broker.close()
}
