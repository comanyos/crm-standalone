// Package flow implements the self-service flow engine (Kratos pattern).
// Each flow type (login, registration, recovery, verification, settings) is modeled
// as an explicit state machine. Strategy plugins handle credential-specific logic.
package flow

import (
	"context"
	"encoding/json"
	"fmt"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	domflow "github.com/authincore/authincore/internal/domain/flow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Strategy is a pluggable auth method (password, oidc, totp, webauthn, code).
type Strategy interface {
	ID() string
	// PopulateFlow renders UI nodes for this strategy into the flow.
	PopulateFlow(ctx context.Context, f *domflow.Flow) error
	// Handle processes a submitted flow body for this strategy.
	// Returns the identity ID on success, or an error that updates the flow UI.
	Handle(ctx context.Context, f *domflow.Flow, body json.RawMessage) (identityID *uuid.UUID, err error)
}

// Repository persists in-flight flows.
type Repository interface {
	Insert(ctx context.Context, f *domflow.Flow) error
	ByID(ctx context.Context, id uuid.UUID) (*domflow.Flow, error)
	Update(ctx context.Context, f *domflow.Flow) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// Manager orchestrates flow lifecycle.
type Manager struct {
	repo       Repository
	strategies map[string]Strategy
}

func NewManager(repo Repository, strategies ...Strategy) *Manager {
	m := &Manager{
		repo:       repo,
		strategies: make(map[string]Strategy, len(strategies)),
	}
	for _, s := range strategies {
		m.strategies[s.ID()] = s
	}
	return m
}

// KnownMethod reports whether method names a registered strategy.
//
// The submitted method arrives in a request body, so it is caller-chosen text
// until this says otherwise. Anything deriving a bounded value from it — a
// Prometheus label above all, where an unbounded label set is a memory
// exhaustion vector — must ask here first rather than passing the raw string
// through.
func (m *Manager) KnownMethod(method string) bool {
	if m == nil {
		return false
	}
	_, ok := m.strategies[method]
	return ok
}

// Create initialises a new flow, populates UI nodes from all registered strategies, and persists it.
func (m *Manager) Create(ctx context.Context, flowType domflow.Type, orgID uuid.UUID, returnTo string) (*domflow.Flow, error) {
	f := domflow.New(flowType, orgID, returnTo)
	f.UI.Action = fmt.Sprintf("/self-service/%s?flow=%s", flowTypePath(flowType), f.ID)

	for _, s := range m.strategies {
		if err := s.PopulateFlow(ctx, f); err != nil {
			return nil, fmt.Errorf("strategy %s populate: %w", s.ID(), err)
		}
	}

	if err := m.repo.Insert(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// CreateForIdentity initialises a flow with a pre-resolved identity (used for
// settings flows where the caller is already authenticated).
func (m *Manager) CreateForIdentity(ctx context.Context, flowType domflow.Type, orgID uuid.UUID, identityID uuid.UUID, returnTo string) (*domflow.Flow, error) {
	f, err := m.Create(ctx, flowType, orgID, returnTo)
	if err != nil {
		return nil, err
	}
	f.IdentityID = &identityID
	if err := m.repo.Update(ctx, f); err != nil {
		return nil, fmt.Errorf("set flow identity: %w", err)
	}
	return f, nil
}

// Get returns a flow by ID, rejecting expired flows.
func (m *Manager) Get(ctx context.Context, id uuid.UUID) (*domflow.Flow, error) {
	f, err := m.repo.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.IsExpired() {
		return nil, domainerr.New(domainerr.CodePreconditionFailed, "flow expired")
	}
	return f, nil
}

type SubmitResult struct {
	Flow       *domflow.Flow
	IdentityID *uuid.UUID
	Done       bool
}

// Submit processes a method submission against the flow.
func (m *Manager) Submit(ctx context.Context, flowID uuid.UUID, method string, body json.RawMessage) (*SubmitResult, error) {
	f, err := m.Get(ctx, flowID)
	if err != nil {
		return nil, err
	}

	s, ok := m.strategies[method]
	if !ok {
		return nil, domainerr.New(domainerr.CodeInvalidArgument, fmt.Sprintf("unknown method: %s", method))
	}

	identityID, err := s.Handle(ctx, f, body)
	if err != nil {
		// Strategy wrote error messages into the flow UI — persist and return the updated flow.
		_ = m.repo.Update(ctx, f)
		return &SubmitResult{Flow: f, Done: false}, nil
	}

	// Strategy succeeded.
	f.SetState(domflow.StatePassedChallenge)
	if identityID != nil {
		f.IdentityID = identityID
	}
	_ = m.repo.Update(ctx, f)

	return &SubmitResult{Flow: f, IdentityID: identityID, Done: true}, nil
}

func flowTypePath(t domflow.Type) string {
	switch t {
	case domflow.TypeLogin:
		return "login"
	case domflow.TypeRegistration:
		return "registration"
	case domflow.TypeRecovery:
		return "recovery"
	case domflow.TypeVerification:
		return "verification"
	case domflow.TypeSettings:
		return "settings"
	default:
		return "unknown"
	}
}

// --- Postgres repository ---

type pgFlowRepository struct {
	db *pgxpool.Pool
}

func NewPGRepository(db *pgxpool.Pool) Repository {
	return &pgFlowRepository{db: db}
}

func (r *pgFlowRepository) Insert(ctx context.Context, f *domflow.Flow) error {
	uiJSON, _ := json.Marshal(f.UI)
	_, err := r.db.Exec(ctx,
		`INSERT INTO flows (id, type, state, org_id, return_to, ui, internal_context, issued_at, expires_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		f.ID, f.Type, f.State, f.OrgID, f.ReturnTo, uiJSON, f.InternalContext, f.IssuedAt, f.ExpiresAt, f.UpdatedAt,
	)
	return err
}

func (r *pgFlowRepository) ByID(ctx context.Context, id uuid.UUID) (*domflow.Flow, error) {
	f := &domflow.Flow{}
	var uiJSON, internalCtx []byte
	row := r.db.QueryRow(ctx,
		`SELECT id, type, state, org_id, identity_id, return_to, ui, internal_context, issued_at, expires_at, updated_at
		 FROM flows WHERE id = $1`, id,
	)
	if err := row.Scan(&f.ID, &f.Type, &f.State, &f.OrgID, &f.IdentityID, &f.ReturnTo,
		&uiJSON, &internalCtx, &f.IssuedAt, &f.ExpiresAt, &f.UpdatedAt); err != nil {
		return nil, domainerr.New(domainerr.CodeNotFound, "flow not found")
	}
	_ = json.Unmarshal(uiJSON, &f.UI)
	f.InternalContext = internalCtx
	return f, nil
}

func (r *pgFlowRepository) Update(ctx context.Context, f *domflow.Flow) error {
	uiJSON, _ := json.Marshal(f.UI)
	_, err := r.db.Exec(ctx,
		`UPDATE flows SET state = $1, identity_id = $2, ui = $3, internal_context = $4, updated_at = $5 WHERE id = $6`,
		f.State, f.IdentityID, uiJSON, f.InternalContext, f.UpdatedAt, f.ID,
	)
	return err
}

func (r *pgFlowRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM flows WHERE id = $1`, id)
	return err
}
