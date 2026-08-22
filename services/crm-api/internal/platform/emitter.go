package platform

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Sink receives fully-populated events (used in tests).
type Sink func(Event)

// Emitter publishes platform Events to NATS JetStream when busURL is set;
// falls back to structured logging when no bus is configured.
// Emits are fire-and-forget — callers are never blocked.
type Emitter struct {
	module string
	nc     *nats.Conn // nil when no NATS URL configured
	sink   Sink
}

// NewEmitter builds an emitter. busURL empty = log-only (dev default).
// NATS connection failures are logged and degraded to log-only — the module
// must still start and function without a running bus.
func NewEmitter(module, busURL string) *Emitter {
	e := &Emitter{module: module}
	if busURL != "" {
		nc, err := nats.Connect(busURL,
			nats.Name(module+"-emitter"),
			nats.MaxReconnects(10),
			nats.ReconnectWait(2*time.Second),
		)
		if err != nil {
			slog.Warn("nats connect failed — events will be logged locally",
				"module", module, "url", busURL, "err", err)
		} else {
			e.nc = nc
		}
	}
	return e
}

// NewEmitterWithSink builds an emitter that hands each event to sink (tests).
func NewEmitterWithSink(module string, sink Sink) *Emitter {
	return &Emitter{module: module, sink: sink}
}

// Close drains the NATS connection gracefully.
func (e *Emitter) Close() {
	if e.nc != nil {
		// Drain is best-effort on shutdown; the process is exiting either way.
		_ = e.nc.Drain()
	}
}

// Emit records a platform event fire-and-forget.
func (e *Emitter) Emit(ev Event) {
	if ev.Module == "" {
		ev.Module = e.module
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.ID == "" {
		ev.ID = ev.At.Format("20060102T150405.000000")
	}

	if e.sink != nil {
		e.sink(ev)
		return
	}

	b, _ := json.Marshal(ev)

	if e.nc != nil {
		subject := "platform." + ev.Module + "." + ev.Type
		if err := e.nc.Publish(subject, b); err != nil {
			slog.Warn("nats publish failed", "subject", subject, "err", err)
		}
		return
	}

	slog.Info("event", "module", ev.Module, "type", ev.Type, "payload", json.RawMessage(b))
}

// NATSConn returns the underlying NATS connection handle.
func (e *Emitter) NATSConn() *nats.Conn {
	return e.nc
}
