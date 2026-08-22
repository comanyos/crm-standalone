package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// A publish that can fail out loud. Emitter, above, is deliberately
// fire-and-forget telemetry; a transactional outbox drained through it would
// mark every event delivered whether or not the bus ever saw it. This is a
// separate path that shares the connection and nothing else. Mirrors leave's
// and hcm's internal/platform/publisher.go, which this is copied from.

// ErrNoBus is returned when a publish is attempted with no bus configured.
var ErrNoBus = errors.New("platform: no event bus configured")

// StreamName is the JetStream stream that holds this module's domain events.
const StreamName = "CRM"

// StreamSubjects is the wildcard the stream captures. Envelope.Subject() must
// stay inside it, or a publish lands on core NATS with no stream to store it.
const StreamSubjects = "crm.>"

// DuplicateWindow is how far back JetStream remembers a Nats-Msg-Id.
const DuplicateWindow = 2 * time.Minute

// Publisher delivers a payload to a subject and reports whether the bus took it.
type Publisher struct {
	nc *nats.Conn

	// js is the JetStream context when the server has JetStream enabled. When
	// it is nil every publish falls back to core NATS, which acknowledges
	// receipt and promises nothing about persistence.
	js nats.JetStreamContext

	flushTimeout time.Duration
}

// NewPublisher wraps an existing NATS connection. A nil connection is valid
// and makes every Publish return ErrNoBus, so a module without a bus still
// starts.
func NewPublisher(nc *nats.Conn) *Publisher {
	return &Publisher{nc: nc, flushTimeout: 5 * time.Second}
}

// EnableJetStream binds a JetStream context and ensures the stream exists.
// The error is returned rather than logged: a deployment that wanted
// durability and did not get it should fail loudly at startup.
func (p *Publisher) EnableJetStream() error {
	if !p.Configured() {
		return ErrNoBus
	}
	js, err := p.nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}

	cfg := &nats.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{StreamSubjects},
		Storage:    nats.FileStorage,
		Retention:  nats.LimitsPolicy,
		Duplicates: DuplicateWindow,
	}
	if _, err := js.StreamInfo(StreamName); err != nil {
		if _, err := js.AddStream(cfg); err != nil {
			return fmt.Errorf("add stream %s: %w", StreamName, err)
		}
	} else if _, err := js.UpdateStream(cfg); err != nil {
		return fmt.Errorf("update stream %s: %w", StreamName, err)
	}

	p.js = js
	return nil
}

// Durable reports whether publishes are persisted by a stream rather than
// merely accepted by the server.
func (p *Publisher) Durable() bool { return p != nil && p.js != nil }

// PublisherFor builds a publisher from an Emitter, sharing its connection.
func PublisherFor(e *Emitter) *Publisher {
	if e == nil {
		return NewPublisher(nil)
	}
	return NewPublisher(e.NATSConn())
}

// Configured reports whether a bus is attached.
func (p *Publisher) Configured() bool { return p != nil && p.nc != nil }

// Publish sends payload on subject and waits for the server to acknowledge
// receipt.
func (p *Publisher) Publish(ctx context.Context, subject string, payload []byte) error {
	return p.PublishWithID(ctx, subject, payload, "")
}

// PublishWithID publishes with a deduplication key. msgID travels as the
// Nats-Msg-Id header, which JetStream uses to discard a repeat of the same
// event inside DuplicateWindow.
func (p *Publisher) PublishWithID(ctx context.Context, subject string, payload []byte, msgID string) error {
	if !p.Configured() {
		return ErrNoBus
	}

	msg := newPublishMessage(ctx, subject, payload, msgID)

	if p.js != nil {
		pubCtx, cancel := context.WithTimeout(ctx, p.flushTimeout)
		defer cancel()
		if _, err := p.js.PublishMsg(msg, nats.Context(pubCtx)); err != nil {
			return fmt.Errorf("jetstream publish %s: %w", subject, err)
		}
		return nil
	}

	if err := p.nc.PublishMsg(msg); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}

	flushCtx, cancel := context.WithTimeout(ctx, p.flushTimeout)
	defer cancel()
	if err := p.nc.FlushWithContext(flushCtx); err != nil {
		return fmt.Errorf("flush %s: %w", subject, err)
	}
	return nil
}

func newPublishMessage(ctx context.Context, subject string, payload []byte, msgID string) *nats.Msg {
	msg := &nats.Msg{Subject: subject, Data: payload, Header: nats.Header{}}
	// A message continues the dispatcher producer span for consumers that
	// understand W3C Trace Context. No request headers, payload fields, tenant
	// identifiers, or credentials are copied into transport metadata.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	// nats.Header is case-sensitive, whereas HTTP headers are not. Copy only
	// the standard W3C keys with their wire-format lowercase spelling.
	for _, key := range []string{"traceparent", "tracestate"} {
		if value := carrier.Get(key); value != "" {
			msg.Header.Set(key, value)
		}
	}
	if msgID != "" {
		msg.Header.Set(nats.MsgIdHdr, msgID)
	}
	return msg
}
