package convert

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestDecodeJob(t *testing.T) {
	body := []byte(`{"FileID":"file-1","ObjectKey":"user-1/src.txt","ContentType":"text/plain","OriginalName":"notes.txt"}`)

	job, err := decodeJob(body)
	if err != nil {
		t.Fatalf("decodeJob() error = %v", err)
	}

	want := Job{FileID: "file-1", ObjectKey: "user-1/src.txt", ContentType: "text/plain", OriginalName: "notes.txt"}
	if job != want {
		t.Errorf("decodeJob() = %#v, want %#v", job, want)
	}
}

func TestDecodeJobInvalidJSON(t *testing.T) {
	if _, err := decodeJob([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// --- delivery handling ------------------------------------------------------

// fakeAcknowledger stands in for the broker's side of a delivery, so the
// decision logic in process can be exercised without one.
type fakeAcknowledger struct {
	acks  int
	nacks int
}

func (a *fakeAcknowledger) Ack(tag uint64, multiple bool) error { a.acks++; return nil }

func (a *fakeAcknowledger) Nack(tag uint64, multiple, requeue bool) error {
	a.nacks++
	return nil
}

func (a *fakeAcknowledger) Reject(tag uint64, requeue bool) error { a.nacks++; return nil }

type claimCall struct {
	JobID       string
	Redelivered bool
}

type fakeClaimer struct {
	attempts int
	claimed  bool
	claimErr error

	calls    []claimCall
	requeued []string
}

func (f *fakeClaimer) Claim(ctx context.Context, jobID string, redelivered bool) (int, bool, error) {
	f.calls = append(f.calls, claimCall{JobID: jobID, Redelivered: redelivered})
	if f.claimErr != nil {
		return 0, false, f.claimErr
	}
	return f.attempts, f.claimed, nil
}

func (f *fakeClaimer) Requeue(ctx context.Context, jobID, fileID, lastErr string) error {
	f.requeued = append(f.requeued, jobID)
	return nil
}

type recordingConverter struct {
	calls int
	err   error
}

func (c *recordingConverter) Convert(ctx context.Context, job Job) error {
	c.calls++
	return c.err
}

type published struct {
	Queue string
	Body  []byte
}

type consumerHarness struct {
	consumer  *Consumer
	claims    *fakeClaimer
	conv      *recordingConverter
	ack       *fakeAcknowledger
	published []published
}

func newConsumerHarness(maxAttempts int) *consumerHarness {
	h := &consumerHarness{
		claims: &fakeClaimer{attempts: 1, claimed: true},
		conv:   &recordingConverter{},
		ack:    &fakeAcknowledger{},
	}

	h.consumer = &Consumer{
		conv:        h.conv,
		claims:      h.claims,
		workers:     1,
		maxAttempts: maxAttempts,
		publish: func(ctx context.Context, queue string, body []byte) error {
			h.published = append(h.published, published{Queue: queue, Body: body})
			return nil
		},
	}

	return h
}

func (h *consumerHarness) deliver(job Job, redelivered bool) {
	body, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}
	h.consumer.process(context.Background(), amqp.Delivery{
		Acknowledger: h.ack,
		Redelivered:  redelivered,
		Body:         body,
	})
}

func (h *consumerHarness) deliverRaw(body string) {
	h.consumer.process(context.Background(), amqp.Delivery{
		Acknowledger: h.ack,
		Body:         []byte(body),
	})
}

func (h *consumerHarness) queues() []string {
	out := make([]string, 0, len(h.published))
	for _, p := range h.published {
		out = append(out, p.Queue)
	}
	return out
}

var testJob = Job{JobID: "job-1", FileID: "file-1", ObjectKey: "user-1/src.md", ContentType: "text/markdown", OriginalName: "notas.md"}

func TestProcessConvertsAClaimedJob(t *testing.T) {
	h := newConsumerHarness(3)

	h.deliver(testJob, false)

	if h.conv.calls != 1 {
		t.Errorf("Convert called %d times, want 1", h.conv.calls)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1", h.ack.acks)
	}
	if len(h.published) != 0 {
		t.Errorf("published %v, want nothing on success", h.queues())
	}
}

// §6: a duplicated delivery must produce a single final effect and at most
// one published bundle. The claim is what enforces it - the second delivery
// never reaches the converter at all.
func TestProcessSkipsAJobItCannotClaim(t *testing.T) {
	h := newConsumerHarness(3)
	h.claims.claimed = false

	h.deliver(testJob, false)

	if h.conv.calls != 0 {
		t.Errorf("Convert called %d times, want 0 - the job was already claimed", h.conv.calls)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1 (a skipped delivery is still done with)", h.ack.acks)
	}
	if len(h.published) != 0 {
		t.Errorf("published %v, want nothing for a skipped delivery", h.queues())
	}
	if len(h.claims.requeued) != 0 {
		t.Errorf("requeued %v, want nothing for a skipped delivery", h.claims.requeued)
	}
}

// The broker's redelivered flag distinguishes "the previous worker died" from
// "somebody published this twice", and the claim needs it to decide whether a
// job stuck in 'converting' may be taken over.
func TestProcessPassesTheRedeliveredFlagToTheClaim(t *testing.T) {
	for _, redelivered := range []bool{false, true} {
		h := newConsumerHarness(3)

		h.deliver(testJob, redelivered)

		if len(h.claims.calls) != 1 {
			t.Fatalf("Claim called %d times, want 1", len(h.claims.calls))
		}
		if got := h.claims.calls[0]; got.JobID != testJob.JobID || got.Redelivered != redelivered {
			t.Errorf("Claim(%q, %v), want (%q, %v)", got.JobID, got.Redelivered, testJob.JobID, redelivered)
		}
	}
}

func TestProcessRetriesAFailedAttempt(t *testing.T) {
	h := newConsumerHarness(3)
	h.claims.attempts = 1
	h.conv.err = errors.New("boom")

	h.deliver(testJob, false)

	if got := h.queues(); len(got) != 1 || got[0] != retryQueueName {
		t.Fatalf("published to %v, want a single message on %q", got, retryQueueName)
	}
	if len(h.claims.requeued) != 1 {
		t.Errorf("requeued %v, want the job put back into a waiting state", h.claims.requeued)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1", h.ack.acks)
	}

	// The retried message has to be the same job, or the next attempt would
	// convert something else.
	retried, err := decodeJob(h.published[0].Body)
	if err != nil {
		t.Fatalf("the retried message is not a job: %v", err)
	}
	if retried != testJob {
		t.Errorf("retried job = %#v, want %#v", retried, testJob)
	}
}

func TestProcessDeadLettersWhenAttemptsRunOut(t *testing.T) {
	h := newConsumerHarness(3)
	h.claims.attempts = 3 // this was the last one allowed
	h.conv.err = errors.New("boom")

	h.deliver(testJob, false)

	if got := h.queues(); len(got) != 1 || got[0] != deadQueueName {
		t.Fatalf("published to %v, want a single message on %q", got, deadQueueName)
	}
	// Nothing is put back into a waiting state: the file stays failed, with
	// the reason, which is what the manual retry acts on.
	if len(h.claims.requeued) != 0 {
		t.Errorf("requeued %v, want nothing once the attempts are spent", h.claims.requeued)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1", h.ack.acks)
	}
}

// MaxAttempts=1 is the "no automatic retries" setting.
func TestProcessWithASingleAttemptNeverRetries(t *testing.T) {
	h := newConsumerHarness(1)
	h.claims.attempts = 1
	h.conv.err = errors.New("boom")

	h.deliver(testJob, false)

	if got := h.queues(); len(got) != 1 || got[0] != deadQueueName {
		t.Errorf("published to %v, want the dead-letter queue", got)
	}
}

// A message that does not name a job has no state to advance and no attempt
// count to consult, so retrying it could only loop.
func TestProcessDeadLettersAMalformedMessage(t *testing.T) {
	h := newConsumerHarness(3)

	h.deliverRaw("not json")

	if h.conv.calls != 0 {
		t.Errorf("Convert called %d times, want 0", h.conv.calls)
	}
	if len(h.claims.calls) != 0 {
		t.Errorf("Claim called %d times, want 0", len(h.claims.calls))
	}
	if got := h.queues(); len(got) != 1 || got[0] != deadQueueName {
		t.Errorf("published to %v, want the dead-letter queue", got)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1", h.ack.acks)
	}
}

// If the claim itself cannot be resolved there is no way to know whether the
// job was already converted, and converting it anyway is how a second bundle
// gets published. It has to wait instead.
func TestProcessParksTheJobWhenTheClaimFails(t *testing.T) {
	h := newConsumerHarness(3)
	h.claims.claimErr = errors.New("database is down")

	h.deliver(testJob, false)

	if h.conv.calls != 0 {
		t.Errorf("Convert called %d times, want 0 - the claim never resolved", h.conv.calls)
	}
	if got := h.queues(); len(got) != 1 || got[0] != retryQueueName {
		t.Errorf("published to %v, want the retry queue", got)
	}
	if h.ack.acks != 1 {
		t.Errorf("acks = %d, want 1", h.ack.acks)
	}
}

// drain returning is the signal the whole reconnection loop hangs on: when
// the broker goes away RabbitMQ closes the deliveries channel, and if the
// worker treated that as a normal shutdown it would sit there consuming
// nothing - container up, health green, queue growing.
func TestDrainReturnsWhenTheBrokerClosesTheDeliveries(t *testing.T) {
	h := newConsumerHarness(3)
	h.consumer.workers = 2

	deliveries := make(chan amqp.Delivery)

	done := make(chan struct{})
	go func() {
		h.consumer.drain(context.Background(), deliveries)
		close(done)
	}()

	body, err := json.Marshal(Job{JobID: "job-1", FileID: "file-1"})
	if err != nil {
		t.Fatal(err)
	}
	deliveries <- amqp.Delivery{Acknowledger: h.ack, Body: body}

	// This is what a dropped connection looks like from inside the process.
	close(deliveries)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after the deliveries channel closed, so no reconnect would ever be attempted")
	}

	if h.conv.calls != 1 {
		t.Errorf("converted %d jobs, want 1 - the delivery sent before the close must still be processed", h.conv.calls)
	}
}

// A cancelled context is the other way a session ends, and it must not be
// confused with a dropped connection: this one means shut down, not reconnect.
func TestDrainReturnsWhenTheContextIsCancelled(t *testing.T) {
	h := newConsumerHarness(3)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.consumer.drain(ctx, make(chan amqp.Delivery))
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after its context was cancelled")
	}
}
