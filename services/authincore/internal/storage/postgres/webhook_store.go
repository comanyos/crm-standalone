package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/webhook"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookStore implements webhook.WebhookStore.
type WebhookStore struct{ db *pgxpool.Pool }

func NewWebhookStore(db *pgxpool.Pool) *WebhookStore { return &WebhookStore{db: db} }

func (s *WebhookStore) ByOrgAndEvent(ctx context.Context, orgID uuid.UUID, eventType string) ([]*webhook.Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, url, events, signing_secret, enabled,
		        connect_timeout_ms, read_timeout_ms
		 FROM webhooks
		 WHERE org_id = $1 AND enabled = true AND events @> to_jsonb($2::text)`,
		orgID, eventType)
	if err != nil {
		return nil, fmt.Errorf("webhooks by org and event: %w", err)
	}
	defer rows.Close()
	var out []*webhook.Webhook
	for rows.Next() {
		w := &webhook.Webhook{}
		if err := rows.Scan(&w.ID, &w.OrgID, &w.URL, &w.Events, &w.SigningSecret,
			&w.Enabled, &w.ConnectTimeoutMS, &w.ReadTimeoutMS); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *WebhookStore) StoreEvent(ctx context.Context, ev *webhook.DeliveryEvent) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO webhook_events
		   (id, webhook_id, event_type, payload, state, attempts, last_error, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		ev.ID, ev.WebhookID, ev.EventType, ev.Payload, ev.State,
		ev.Attempts, ev.LastError, ev.CreatedAt)
	return err
}

func (s *WebhookStore) UpdateEvent(ctx context.Context, ev *webhook.DeliveryEvent) error {
	_, err := s.db.Exec(ctx,
		`UPDATE webhook_events
		 SET state=$1, attempts=$2, last_error=$3, delivered_at=$4
		 WHERE id=$5`,
		ev.State, ev.Attempts, ev.LastError,
		ev.DeliveredAt, ev.ID)
	return err
}

// WebhookEventPurger removes old delivered events.
func PurgeDeliveredWebhookEvents(ctx context.Context, db *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	ct, err := db.Exec(ctx,
		`DELETE FROM webhook_events WHERE state=2 AND created_at < $1`,
		time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

func (s *WebhookStore) List(ctx context.Context, orgID uuid.UUID) ([]*webhook.Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, url, events, signing_secret, enabled, connect_timeout_ms, read_timeout_ms
		 FROM webhooks WHERE org_id=$1 ORDER BY created_at DESC`,
		orgID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()
	var out []*webhook.Webhook
	for rows.Next() {
		w := &webhook.Webhook{}
		if err := rows.Scan(&w.ID, &w.OrgID, &w.URL, &w.Events, &w.SigningSecret,
			&w.Enabled, &w.ConnectTimeoutMS, &w.ReadTimeoutMS); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *WebhookStore) Create(ctx context.Context, w *webhook.Webhook) error {
	w.ID = uuid.New()
	_, err := s.db.Exec(ctx,
		`INSERT INTO webhooks (id, org_id, url, events, signing_secret, enabled, connect_timeout_ms, read_timeout_ms)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		w.ID, w.OrgID, w.URL, w.Events, w.SigningSecret, w.Enabled, w.ConnectTimeoutMS, w.ReadTimeoutMS)
	return err
}

func (s *WebhookStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id=$1 AND org_id=$2`, id, orgID)
	return err
}
