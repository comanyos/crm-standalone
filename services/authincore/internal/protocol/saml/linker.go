// Package saml — linker.go implements IdentityLinker and SessionMinter
// to wire the SAML SP into AuthinCore's identity and session subsystems.
package saml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/authincore/authincore/internal/authz"
	"github.com/authincore/authincore/internal/command"
	domsession "github.com/authincore/authincore/internal/domain/session"
	"github.com/authincore/authincore/internal/token"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SAMLIdentityLinker implements IdentityLinker using the Postgres identity store.
// It looks up identities by external_id (formatted as "saml:{provider_id}:{name_id}")
// and creates new identities when no match is found.
type SAMLIdentityLinker struct {
	db   *pgxpool.Pool
	cmds *command.Commands
}

// NewSAMLIdentityLinker creates a new linker backed by the identity store.
func NewSAMLIdentityLinker(db *pgxpool.Pool, cmds *command.Commands) *SAMLIdentityLinker {
	return &SAMLIdentityLinker{db: db, cmds: cmds}
}

// LinkOrCreate finds an existing identity by SAML external ID or creates a new one.
// The external_id format is "saml:{provider_id}:{name_id}" to ensure uniqueness
// across multiple SAML providers within the same org.
func (l *SAMLIdentityLinker) LinkOrCreate(ctx context.Context, orgID uuid.UUID, providerID, nameID string, attrs map[string]string) (uuid.UUID, error) {
	externalID := fmt.Sprintf("saml:%s:%s", providerID, nameID)

	// Try to find existing identity by external_id.
	var identityID uuid.UUID
	err := l.db.QueryRow(ctx,
		`SELECT id FROM identities WHERE org_id=$1 AND external_id=$2 LIMIT 1`,
		orgID, externalID,
	).Scan(&identityID)

	if err == nil {
		// Found existing identity — update traits from SAML attributes.
		if err := l.updateTraitsFromSAML(ctx, identityID, orgID, attrs); err != nil {
			return uuid.Nil, fmt.Errorf("update SAML traits: %w", err)
		}
		return identityID, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("lookup SAML identity: %w", err)
	}

	// No existing identity — try to match by email attribute (common SAML JIT pattern).
	if email, ok := attrs["email"]; ok && email != "" {
		err := l.db.QueryRow(ctx,
			`SELECT id FROM identities
			 WHERE org_id=$1 AND traits @> jsonb_build_object('email', $2::text)
			 LIMIT 1`,
			orgID, email,
		).Scan(&identityID)

		if err == nil {
			// Found by email — link the SAML external_id to this identity.
			_, err = l.db.Exec(ctx,
				`UPDATE identities SET external_id=$1, updated_at=$2 WHERE id=$3 AND org_id=$4`,
				externalID, time.Now().UTC(), identityID, orgID,
			)
			if err != nil {
				return uuid.Nil, fmt.Errorf("link SAML to existing identity: %w", err)
			}
			if err := l.updateTraitsFromSAML(ctx, identityID, orgID, attrs); err != nil {
				return uuid.Nil, fmt.Errorf("update SAML traits on linked identity: %w", err)
			}
			return identityID, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("lookup identity by email: %w", err)
		}
	}

	// Create new identity via the command layer.
	traits := buildTraitsFromSAML(attrs, nameID)
	traitsJSON, err := json.Marshal(traits)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal SAML traits: %w", err)
	}

	identity, err := l.cmds.CreateIdentity(ctx, command.CreateIdentityCommand{
		OrgID:    orgID,
		SchemaID: "default",
		Traits:   traitsJSON,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create SAML identity: %w", err)
	}

	// Set the external_id on the newly created identity.
	_, err = l.db.Exec(ctx,
		`UPDATE identities SET external_id=$1, updated_at=$2 WHERE id=$3 AND org_id=$4`,
		externalID, time.Now().UTC(), identity.ID, orgID,
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("set external_id on new identity: %w", err)
	}

	return identity.ID, nil
}

// updateTraitsFromSAML merges SAML assertion attributes into the identity's traits.
func (l *SAMLIdentityLinker) updateTraitsFromSAML(ctx context.Context, identityID, orgID uuid.UUID, attrs map[string]string) error {
	if len(attrs) == 0 {
		return nil
	}

	// Fetch current traits.
	var currentTraits json.RawMessage
	err := l.db.QueryRow(ctx,
		`SELECT traits FROM identities WHERE id=$1 AND org_id=$2`,
		identityID, orgID,
	).Scan(&currentTraits)
	if err != nil {
		return fmt.Errorf("fetch current traits: %w", err)
	}

	// Merge SAML attributes into existing traits.
	var traits map[string]interface{}
	if err := json.Unmarshal(currentTraits, &traits); err != nil {
		traits = make(map[string]interface{})
	}
	for k, v := range attrs {
		traits[k] = v
	}

	merged, err := json.Marshal(traits)
	if err != nil {
		return fmt.Errorf("marshal merged traits: %w", err)
	}

	_, err = l.db.Exec(ctx,
		`UPDATE identities SET traits=$1, updated_at=$2 WHERE id=$3 AND org_id=$4`,
		merged, time.Now().UTC(), identityID, orgID,
	)
	return err
}

// buildTraitsFromSAML constructs identity traits from SAML assertion attributes.
func buildTraitsFromSAML(attrs map[string]string, nameID string) map[string]string {
	traits := make(map[string]string)
	// Copy all SAML attributes as traits.
	for k, v := range attrs {
		traits[k] = v
	}
	// Ensure we have at least an identifier.
	if _, ok := traits["email"]; !ok {
		traits["email"] = nameID
	}
	return traits
}

// SAMLSessionMinter implements SessionMinter using the command layer and token service.
// It creates a session record and returns a signed JWT session token.
type SAMLSessionMinter struct {
	cmds     *command.Commands
	tokenSvc *token.Service
	claims   *authz.ClaimsResolver
}

// NewSAMLSessionMinter creates a new session minter.
func NewSAMLSessionMinter(cmds *command.Commands, tokenSvc *token.Service) *SAMLSessionMinter {
	return &SAMLSessionMinter{cmds: cmds, tokenSvc: tokenSvc}
}

// WithClaimsResolver attaches the resolver that fills a SAML session token's
// roles and scopes. Without it, SSO users authenticate successfully and then
// hold a token that authorises nothing — a quieter failure than a rejected
// login and a much harder one to diagnose.
func (m *SAMLSessionMinter) WithClaimsResolver(cr *authz.ClaimsResolver) *SAMLSessionMinter {
	m.claims = cr
	return m
}

// MintSession creates a new session for the identity and returns a session token.
func (m *SAMLSessionMinter) MintSession(ctx context.Context, orgID, identityID uuid.UUID, method string) (string, error) {
	session, err := m.cmds.CreateSession(ctx, command.CreateSessionCommand{
		IdentityID: identityID,
		OrgID:      orgID,
		Methods: []domsession.AuthMethod{
			{
				Method:      method,
				CompletedAt: time.Now().UTC(),
			},
		},
		TTL: 24 * time.Hour,
	})
	if err != nil {
		return "", fmt.Errorf("create SAML session: %w", err)
	}

	roles, scopes, err := m.claims.Resolve(ctx, orgID, identityID)
	if err != nil {
		return "", fmt.Errorf("resolve SAML session claims: %w", err)
	}

	// Mint a JWT access token for the session.
	// AAL1 for SAML SSO (single-factor from IdP).
	tok, err := m.tokenSvc.MintAccessToken(ctx, identityID, orgID, session.ID, roles, scopes, "", 1)
	if err != nil {
		return "", fmt.Errorf("mint SAML session token: %w", err)
	}

	return tok, nil
}
