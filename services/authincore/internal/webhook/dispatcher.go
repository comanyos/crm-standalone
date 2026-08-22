// Package webhook dispatches outbound webhook events (FusionAuth-style).
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/authincore/authincore/internal/metrics"
	"github.com/authincore/authincore/internal/tracing"

	"github.com/google/uuid"
)

// Webhook is a configured outbound endpoint.
type Webhook struct {
	ID               uuid.UUID `json:"id"`
	OrgID            uuid.UUID `json:"org_id"`
	URL              string    `json:"url"`
	Events           []string  `json:"events"`
	SigningSecret    string    `json:"signing_secret,omitempty"`
	Enabled          bool      `json:"enabled"`
	ConnectTimeoutMS int       `json:"connect_timeout_ms"`
	ReadTimeoutMS    int       `json:"read_timeout_ms"`
}

// WebhookStore reads webhook configurations.
type WebhookStore interface {
	ByOrgAndEvent(ctx context.Context, orgID uuid.UUID, eventType string) ([]*Webhook, error)
	StoreEvent(ctx context.Context, ev *DeliveryEvent) error
	UpdateEvent(ctx context.Context, ev *DeliveryEvent) error
}

// DeliveryEvent is the persisted webhook delivery record.
type DeliveryEvent struct {
	ID          uuid.UUID
	WebhookID   uuid.UUID
	EventType   string
	Payload     json.RawMessage
	State       int // 1=pending 2=delivered 3=failed
	Attempts    int
	LastError   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// Dispatcher sends webhooks with HMAC-SHA256 signatures.
type Dispatcher struct {
	store  WebhookStore
	client *http.Client
	log    *slog.Logger
	sem    chan struct{}
}

func NewDispatcher(store WebhookStore, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store:  store,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log,
		sem:    make(chan struct{}, 64),
	}
}

// Dispatch sends an event to all matching webhooks for an org.
// Returns an error only if webhook configs cannot be loaded or payload cannot be
// marshalled; individual delivery failures are fire-and-forget and do not surface here.
func (d *Dispatcher) Dispatch(ctx context.Context, orgID uuid.UUID, eventType string, payload any) error {
	ctx, span := tracing.Start(ctx, "webhook.dispatch")
	defer span.End()

	webhooks, err := d.store.ByOrgAndEvent(ctx, orgID, eventType)
	if err != nil {
		d.log.Error("webhook dispatch load", "org_id", orgID, "event", eventType, "err", err)
		return err
	}

	payloadJSON, err := json.Marshal(map[string]any{
		"id":         uuid.New().String(),
		"type":       eventType,
		"createTime": time.Now().UTC(),
		"orgId":      orgID.String(),
		"data":       payload,
	})
	if err != nil {
		d.log.Error("webhook dispatch marshal", "org_id", orgID, "event", eventType, "err", err)
		return err
	}

	for _, wh := range webhooks {
		if !wh.Enabled {
			continue
		}
		ev := &DeliveryEvent{
			ID:        uuid.New(),
			WebhookID: wh.ID,
			EventType: eventType,
			Payload:   payloadJSON,
			State:     1,
			CreatedAt: time.Now().UTC(),
		}
		if err := d.store.StoreEvent(ctx, ev); err != nil {
			return fmt.Errorf("persist webhook delivery: %w", err)
		}
		select {
		case d.sem <- struct{}{}:
			deliveryCtx := context.WithoutCancel(ctx)
			go d.send(deliveryCtx, wh, ev)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (d *Dispatcher) send(parent context.Context, wh *Webhook, ev *DeliveryEvent) {
	defer func() { <-d.sem }()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(ev.Payload))
	if err != nil {
		d.markFailed(ctx, ev, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Authincore-Event", ev.EventType)
	req.Header.Set("X-Authincore-Delivery", ev.ID.String())
	if wh.SigningSecret != "" {
		req.Header.Set("X-Authincore-Signature", sign(ev.Payload, wh.SigningSecret))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		d.markFailed(ctx, ev, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	ev.Attempts++
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now().UTC()
		ev.State = 2
		ev.DeliveredAt = &now
		d.log.Info("webhook delivered", "webhook_id", wh.ID, "event", ev.EventType)
		metrics.WebhookDeliveriesTotal.WithLabelValues("delivered").Inc()
	} else {
		d.markFailed(ctx, ev, fmt.Sprintf("HTTP %d", resp.StatusCode))
		metrics.WebhookDeliveriesTotal.WithLabelValues("failed").Inc()
	}
	if err := d.store.UpdateEvent(ctx, ev); err != nil && !errors.Is(err, context.Canceled) {
		d.log.Error("update webhook delivery", "delivery_id", ev.ID, "err", err)
	}
}

func (d *Dispatcher) markFailed(ctx context.Context, ev *DeliveryEvent, reason string) {
	ev.Attempts++
	ev.State = 3
	ev.LastError = reason
	d.log.Warn("webhook delivery failed", "webhook_id", ev.WebhookID, "reason", reason)
	metrics.WebhookDeliveriesTotal.WithLabelValues("failed").Inc()
	if err := d.store.UpdateEvent(ctx, ev); err != nil && !errors.Is(err, context.Canceled) {
		d.log.Error("update failed webhook delivery", "delivery_id", ev.ID, "err", err)
	}
}

func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
