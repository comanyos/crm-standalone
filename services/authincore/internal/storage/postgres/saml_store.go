package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/authincore/authincore/internal/protocol/saml"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SAMLConfigStore implements saml.ConfigStore using Postgres.
type SAMLConfigStore struct{ db *pgxpool.Pool }

func NewSAMLConfigStore(db *pgxpool.Pool) *SAMLConfigStore { return &SAMLConfigStore{db: db} }

func (s *SAMLConfigStore) Save(ctx context.Context, cfg *saml.ProviderConfig) error {
	attrMap, _ := json.Marshal(cfg.AttributeMap)
	_, err := s.db.Exec(ctx,
		`INSERT INTO saml_providers
		 (id, org_id, name, metadata_url, entity_id, sso_url, certificate, name_id_format, attribute_map, active, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (id) DO UPDATE SET
		   name=EXCLUDED.name, metadata_url=EXCLUDED.metadata_url,
		   sso_url=EXCLUDED.sso_url, certificate=EXCLUDED.certificate,
		   attribute_map=EXCLUDED.attribute_map, active=EXCLUDED.active`,
		cfg.ID, cfg.OrgID, cfg.Name, cfg.MetadataURL, cfg.EntityID, cfg.SSOURL,
		cfg.Certificate, cfg.NameIDFormat, attrMap, cfg.Active, cfg.CreatedAt,
	)
	return err
}

func (s *SAMLConfigStore) ByID(ctx context.Context, orgID, id uuid.UUID) (*saml.ProviderConfig, error) {
	cfg := &saml.ProviderConfig{}
	var attrMap []byte
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, name, metadata_url, entity_id, sso_url,
		        certificate, name_id_format, attribute_map, active, created_at
		 FROM saml_providers WHERE org_id=$1 AND id=$2`, orgID, id,
	).Scan(&cfg.ID, &cfg.OrgID, &cfg.Name, &cfg.MetadataURL, &cfg.EntityID,
		&cfg.SSOURL, &cfg.Certificate, &cfg.NameIDFormat, &attrMap, &cfg.Active, &cfg.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainerr.New(domainerr.CodeNotFound, "SAML provider not found")
		}
		return nil, fmt.Errorf("saml provider by id: %w", err)
	}
	_ = json.Unmarshal(attrMap, &cfg.AttributeMap)
	return cfg, nil
}

func (s *SAMLConfigStore) List(ctx context.Context, orgID uuid.UUID) ([]*saml.ProviderConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, metadata_url, entity_id, sso_url,
		        certificate, name_id_format, attribute_map, active, created_at
		 FROM saml_providers WHERE org_id=$1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list saml providers: %w", err)
	}
	defer rows.Close()
	var out []*saml.ProviderConfig
	for rows.Next() {
		cfg := &saml.ProviderConfig{}
		var attrMap []byte
		if err := rows.Scan(&cfg.ID, &cfg.OrgID, &cfg.Name, &cfg.MetadataURL, &cfg.EntityID,
			&cfg.SSOURL, &cfg.Certificate, &cfg.NameIDFormat, &attrMap, &cfg.Active, &cfg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan saml provider: %w", err)
		}
		_ = json.Unmarshal(attrMap, &cfg.AttributeMap)
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (s *SAMLConfigStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM saml_providers WHERE id=$1 AND org_id=$2`, id, orgID)
	return err
}

// SAMLSPConfigStore persists registered SPs for the SAML IdP role.
type SAMLSPConfigStore struct{ db *pgxpool.Pool }

func NewSAMLSPConfigStore(db *pgxpool.Pool) *SAMLSPConfigStore {
	return &SAMLSPConfigStore{db: db}
}

func (s *SAMLSPConfigStore) ByEntityID(ctx context.Context, orgID uuid.UUID, entityID string) (*saml.SPConfig, error) {
	row := s.db.QueryRow(ctx,
		`SELECT id, org_id, name, entity_id, acs_url, metadata_xml,
		        name_id_format, attribute_map, active
		 FROM saml_service_providers
		 WHERE org_id=$1 AND entity_id=$2`, orgID, entityID)
	return scanSPConfig(row)
}

func (s *SAMLSPConfigStore) Save(ctx context.Context, cfg *saml.SPConfig) error {
	attrMap, _ := json.Marshal(cfg.AttributeMap)
	_, err := s.db.Exec(ctx,
		`INSERT INTO saml_service_providers
		   (id, org_id, name, entity_id, acs_url, metadata_xml, name_id_format, attribute_map, active)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (org_id, entity_id) DO UPDATE
		   SET name=$3, acs_url=$5, metadata_xml=$6, name_id_format=$7,
		       attribute_map=$8, active=$9`,
		cfg.ID, cfg.OrgID, cfg.Name, cfg.EntityID, cfg.ACSURL,
		cfg.MetadataXML, cfg.NameIDFormat, attrMap, cfg.Active)
	return err
}

func (s *SAMLSPConfigStore) List(ctx context.Context, orgID uuid.UUID) ([]*saml.SPConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, org_id, name, entity_id, acs_url, metadata_xml,
		        name_id_format, attribute_map, active
		 FROM saml_service_providers WHERE org_id=$1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cfgs []*saml.SPConfig
	for rows.Next() {
		cfg, err := scanSPConfig(rows)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs, rows.Err()
}

func (s *SAMLSPConfigStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM saml_service_providers WHERE org_id=$1 AND id=$2`, orgID, id)
	return err
}

func scanSPConfig(row interface {
	Scan(...any) error
}) (*saml.SPConfig, error) {
	var cfg saml.SPConfig
	var attrMap []byte
	if err := row.Scan(&cfg.ID, &cfg.OrgID, &cfg.Name, &cfg.EntityID, &cfg.ACSURL,
		&cfg.MetadataXML, &cfg.NameIDFormat, &attrMap, &cfg.Active); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(attrMap, &cfg.AttributeMap)
	return &cfg, nil
}

// SAMLIdPKeyStore persists the org-level IdP signing key-pair.
type SAMLIdPKeyStore struct{ db *pgxpool.Pool }

func NewSAMLIdPKeyStore(db *pgxpool.Pool) *SAMLIdPKeyStore {
	return &SAMLIdPKeyStore{db: db}
}

func (s *SAMLIdPKeyStore) Load(ctx context.Context, orgID uuid.UUID) (certPEM, keyPEM string, err error) {
	row := s.db.QueryRow(ctx,
		`SELECT sp_cert_pem, sp_key_pem FROM saml_idp_keys WHERE org_id=$1`, orgID)
	err = row.Scan(&certPEM, &keyPEM)
	return
}

func (s *SAMLIdPKeyStore) Save(ctx context.Context, orgID uuid.UUID, certPEM, keyPEM string) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO saml_idp_keys (org_id, sp_cert_pem, sp_key_pem)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (org_id) DO UPDATE SET sp_cert_pem=$2, sp_key_pem=$3`,
		orgID, certPEM, keyPEM)
	return err
}
