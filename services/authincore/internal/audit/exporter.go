//go:build enterprise

// Package audit dispatches audit events to configured SIEM endpoints.
package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/authincore/authincore/internal/eventstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is a SIEM export destination persisted in audit_export_configs.
type Config struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"org_id"`
	Type     string    `json:"type"` // splunk | datadog | generic_webhook
	Endpoint string    `json:"endpoint_url"`
	Secret   string    `json:"-"`
	Format   string    `json:"format"` // json | cef
	Enabled  bool      `json:"enabled"`
}

// ConfigStore persists tenant-scoped audit export destinations.
type ConfigStore struct{ db *pgxpool.Pool }

func NewConfigStore(db *pgxpool.Pool) *ConfigStore { return &ConfigStore{db: db} }

func (s *ConfigStore) List(ctx context.Context, orgID uuid.UUID) ([]Config, error) {
	rows, err := s.db.Query(ctx, `SELECT id, org_id, type, endpoint, secret, format, enabled FROM audit_export_configs WHERE org_id=$1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list audit exports: %w", err)
	}
	defer rows.Close()
	var out []Config
	for rows.Next() {
		var cfg Config
		if err := rows.Scan(&cfg.ID, &cfg.OrgID, &cfg.Type, &cfg.Endpoint, &cfg.Secret, &cfg.Format, &cfg.Enabled); err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (s *ConfigStore) Create(ctx context.Context, cfg *Config) error {
	if cfg.ID == uuid.Nil {
		cfg.ID = uuid.New()
	}
	_, err := s.db.Exec(ctx, `INSERT INTO audit_export_configs (id, org_id, type, endpoint, secret, format, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7)`, cfg.ID, cfg.OrgID, cfg.Type, cfg.Endpoint, cfg.Secret, cfg.Format, cfg.Enabled)
	return err
}

func (s *ConfigStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM audit_export_configs WHERE org_id=$1 AND id=$2`, orgID, id)
	return err
}

// Exporter fans out eventstore events to configured SIEM endpoints.
// It implements eventstore.Projection so it can be driven by event.Tail.
type Exporter struct {
	db     *pgxpool.Pool
	client *http.Client
	log    *slog.Logger
}

func NewExporter(db *pgxpool.Pool, log *slog.Logger) *Exporter {
	return &Exporter{
		db:     db,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// Handle implements eventstore.Projection. Called for every new event by event.Tail.
func (e *Exporter) Handle(ctx context.Context, ev *eventstore.Event) error {
	orgID := ev.OrgID
	configs, err := e.loadConfigs(ctx, orgID)
	if err != nil {
		e.log.Warn("audit: load configs", "err", err)
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"event_type": ev.EventType,
		"org_id":     orgID,
		"timestamp":  ev.CreatedAt,
		"data":       ev.Payload,
	})
	if err != nil {
		return fmt.Errorf("audit: encode event: %w", err)
	}

	var dispatchErrors []error
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if err := e.dispatch(ctx, cfg, payload); err != nil {
			dispatchErrors = append(dispatchErrors, err)
		}
	}
	return errors.Join(dispatchErrors...)
}

func (e *Exporter) dispatch(parent context.Context, cfg Config, payload []byte) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		e.log.Warn("audit: build request", "endpoint", cfg.Endpoint, "err", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Secret != "" {
		sig := computeHMAC(payload, cfg.Secret)
		req.Header.Set("X-Authincore-Signature", sig)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		e.log.Warn("audit: dispatch failed", "endpoint", cfg.Endpoint, "err", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		e.log.Warn("audit: SIEM rejected event", "endpoint", cfg.Endpoint, "status", resp.StatusCode)
		return fmt.Errorf("audit endpoint returned %s", resp.Status)
	}
	return nil
}

func (e *Exporter) loadConfigs(ctx context.Context, orgID uuid.UUID) ([]Config, error) {
	rows, err := e.db.Query(ctx,
		`SELECT id, org_id, type, endpoint, secret, format, enabled
		 FROM audit_export_configs WHERE org_id = $1 AND enabled = true`, orgID)
	if err != nil {
		return nil, fmt.Errorf("audit: query configs: %w", err)
	}
	defer rows.Close()

	var out []Config
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Type, &c.Endpoint, &c.Secret, &c.Format, &c.Enabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func computeHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
