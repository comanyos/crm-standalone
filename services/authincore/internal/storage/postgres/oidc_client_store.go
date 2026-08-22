package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/protocol/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OIDCClientStore implements oidc.ClientStore using the applications table.
type OIDCClientStore struct {
	db *pgxpool.Pool
}

func NewOIDCClientStore(db *pgxpool.Pool) *OIDCClientStore {
	return &OIDCClientStore{db: db}
}

type oidcAppConfig struct {
	ClientID        string   `json:"client_id"`
	ClientSecret    string   `json:"client_secret"`
	RedirectURIs    []string `json:"redirect_uris"`
	GrantTypes      []string `json:"grant_types"`
	ResponseTypes   []string `json:"response_types"`
	ApplicationType string   `json:"application_type"`
	AuthMethodType  string   `json:"auth_method_type"`
}

// ByClientID resolves an OIDC client from the applications table.
func (s *OIDCClientStore) ByClientID(ctx context.Context, orgID uuid.UUID, clientID string) (*oidc.Client, error) {
	var appID uuid.UUID
	var configJSON []byte

	err := s.db.QueryRow(ctx,
		`SELECT id, config FROM applications
		 WHERE org_id = $1
		   AND type = 1
		   AND config->>'client_id' = $2`,
		orgID, clientID,
	).Scan(&appID, &configJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "client not found")
		}
		return nil, fmt.Errorf("oidc client by client_id: %w", err)
	}

	var cfg oidcAppConfig
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal oidc client config: %w", err)
	}

	return &oidc.Client{
		ID:              appID,
		ClientID:        cfg.ClientID,
		ClientSecret:    cfg.ClientSecret,
		RedirectURIs:    cfg.RedirectURIs,
		GrantTypes:      cfg.GrantTypes,
		ResponseTypes:   cfg.ResponseTypes,
		ApplicationType: cfg.ApplicationType,
		AuthMethodType:  cfg.AuthMethodType,
	}, nil
}
