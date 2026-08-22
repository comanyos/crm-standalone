// Package invitation manages org invitations sent by email.
//
// Flow:
//  1. Admin calls CreateInvitation → token generated, courier queues email.
//  2. Invitee clicks link → GET /invitations/{token} returns metadata.
//  3. Invitee submits registration/login → POST /invitations/{token}/accept
//     verifies token, creates identity (or links existing), assigns default role.
package invitation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/authincore/authincore/internal/courier"
	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
)

const tokenTTL = 7 * 24 * time.Hour // 7 days

// Invitation is a pending org membership invite.
type Invitation struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	Token      string     `json:"-"`
	InviterID  uuid.UUID  `json:"inviter_id"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	IdentityID *uuid.UUID `json:"identity_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Store persists invitations.
type Store interface {
	Insert(ctx context.Context, inv *Invitation) error
	ByToken(ctx context.Context, token string) (*Invitation, error)
	Accept(ctx context.Context, token string, identityID uuid.UUID) error
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]*Invitation, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// RoleAssigner adds the identity to the org with the invited role.
type RoleAssigner interface {
	AssignRole(ctx context.Context, orgID, identityID uuid.UUID, role string) error
}

// IdentityResolver finds or creates an identity by email on accept.
type IdentityResolver interface {
	FindOrCreateByEmail(ctx context.Context, orgID uuid.UUID, email string) (uuid.UUID, error)
}

// Service manages invitation lifecycle.
type Service struct {
	store    Store
	roles    RoleAssigner
	resolver IdentityResolver
	courier  *courier.Courier
	baseURL  string
}

func New(store Store, roles RoleAssigner, resolver IdentityResolver, c *courier.Courier, baseURL string) *Service {
	return &Service{store: store, roles: roles, resolver: resolver, courier: c, baseURL: baseURL}
}

// Create generates an invitation token, persists it, and queues an invite email.
func (s *Service) Create(ctx context.Context, orgID, inviterID uuid.UUID, email, role string) (*Invitation, error) {
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	inv := &Invitation{
		ID:        uuid.New(),
		OrgID:     orgID,
		Email:     email,
		Role:      role,
		Token:     token,
		InviterID: inviterID,
		ExpiresAt: time.Now().UTC().Add(tokenTTL),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Insert(ctx, inv); err != nil {
		return nil, fmt.Errorf("store invitation: %w", err)
	}
	if s.courier != nil {
		_ = s.courier.Send(ctx, orgID, courier.TypeInvitation, email, map[string]any{
			"invite_url": fmt.Sprintf("%s/invitations/%s", s.baseURL, token),
			"role":       role,
			"expires_in": "7 days",
		})
	}
	return inv, nil
}

// Accept validates the token, resolves/creates the identity, assigns the role.
func (s *Service) Accept(ctx context.Context, token, email string) (*Invitation, error) {
	inv, err := s.store.ByToken(ctx, token)
	if err != nil {
		return nil, domainerr.New(domainerr.CodeNotFound, "invitation not found")
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "invitation expired")
	}
	if inv.AcceptedAt != nil {
		return nil, domainerr.New(domainerr.CodeAlreadyExists, "invitation already accepted")
	}
	if !strings.EqualFold(strings.TrimSpace(inv.Email), strings.TrimSpace(email)) {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, "email does not match invitation")
	}

	identityID, err := s.resolver.FindOrCreateByEmail(ctx, inv.OrgID, strings.TrimSpace(inv.Email))
	if err != nil {
		return nil, fmt.Errorf("resolve identity: %w", err)
	}

	if s.roles != nil {
		if err := s.roles.AssignRole(ctx, inv.OrgID, identityID, inv.Role); err != nil {
			return nil, fmt.Errorf("assign role: %w", err)
		}
	}

	if err := s.store.Accept(ctx, token, identityID); err != nil {
		return nil, fmt.Errorf("mark accepted: %w", err)
	}
	inv.IdentityID = &identityID
	now := time.Now().UTC()
	inv.AcceptedAt = &now
	return inv, nil
}

// List returns invitations scoped to a single organization.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*Invitation, error) {
	return s.store.ListByOrg(ctx, orgID)
}

// CreateHandler handles POST /api/v1/invitations.
func (s *Service) CreateHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgID     uuid.UUID `json:"org_id"`
		InviterID uuid.UUID `json:"inviter_id"`
		Email     string    `json:"email"`
		Role      string    `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	inv, err := s.Create(r.Context(), body.OrgID, body.InviterID, body.Email, body.Role)
	if err != nil {
		http.Error(w, fmt.Sprintf("create invitation: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(inv)
}

// GetHandler handles GET /invitations/{token} — returns invite metadata for the accept UI.
func (s *Service) GetHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	inv, err := s.store.ByToken(r.Context(), token)
	if err != nil {
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		http.Error(w, "invitation expired", http.StatusGone)
		return
	}
	// Return only public fields (omit token from response).
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":         inv.ID,
		"org_id":     inv.OrgID,
		"email":      inv.Email,
		"role":       inv.Role,
		"expires_at": inv.ExpiresAt,
	})
}

// AcceptHandler handles POST /invitations/{token}/accept.
func (s *Service) AcceptHandler(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	inv, err := s.Accept(r.Context(), token, body.Email)
	if err != nil {
		switch {
		case domainerr.IsNotFound(err):
			http.Error(w, "invitation not found", http.StatusNotFound)
		case domainerr.IsAlreadyExists(err):
			http.Error(w, "invitation already accepted", http.StatusConflict)
		case domainerr.IsInvalidArgument(err):
			http.Error(w, "invitation expired", http.StatusGone)
		default:
			http.Error(w, fmt.Sprintf("accept: %v", err), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(inv)
}

// ListHandler handles GET /api/v1/orgs/{org_id}/invitations.
func (s *Service) ListHandler(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		http.Error(w, "invalid org_id", http.StatusBadRequest)
		return
	}
	invs, err := s.store.ListByOrg(r.Context(), orgID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(invs)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
