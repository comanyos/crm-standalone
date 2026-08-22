// Package blueprint applies declarative YAML configurations (authentik blueprints).
// A blueprint is idempotent: apply can be run repeatedly; it diffs before writing.
package blueprint

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Blueprint is the persisted blueprint record.
type Blueprint struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"org_id"`
	Name        string     `json:"name"`
	Version     string     `json:"version"`
	State       State      `json:"state"`
	YAMLContent string     `json:"-"`
	LastError   string     `json:"last_error,omitempty"`
	LastApplied *time.Time `json:"last_applied,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type State int

const (
	StatePending State = iota
	StateApplied
	StateErrored
)

// BlueprintDoc is the parsed YAML structure.
type BlueprintDoc struct {
	Version  string      `yaml:"version"`
	Metadata DocMetadata `yaml:"metadata"`
	Entries  []Entry     `yaml:"entries"`
}

type DocMetadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// Entry is one resource declaration in the blueprint.
type Entry struct {
	Model      string         `yaml:"model"` // e.g. "authincore/identity-schema"
	Identifier string         `yaml:"id"`
	State      string         `yaml:"state"` // present | absent
	Attrs      map[string]any `yaml:"attrs"`
}

// Diff records what an apply would change.
type Diff struct {
	ToCreate  []string `json:"to_create"`
	ToUpdate  []string `json:"to_update"`
	ToDelete  []string `json:"to_delete"`
	Unchanged []string `json:"unchanged"`
}

// Reconciler handles each entry model type.
type Reconciler interface {
	Model() string
	Reconcile(ctx context.Context, orgID uuid.UUID, entry Entry, dryRun bool) (action string, err error)
}

// Store persists blueprint records.
type Store interface {
	Insert(ctx context.Context, bp *Blueprint) error
	Update(ctx context.Context, bp *Blueprint) error
	ByOrgAndName(ctx context.Context, orgID uuid.UUID, name string) (*Blueprint, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*Blueprint, error)
	Delete(ctx context.Context, id, orgID uuid.UUID) error
	Transaction(ctx context.Context, fn func(context.Context) error) error
}

// Applier orchestrates blueprint apply/diff.
type Applier struct {
	store       Store
	reconcilers map[string]Reconciler
	log         *slog.Logger
}

func NewApplier(store Store, log *slog.Logger, reconcilers ...Reconciler) *Applier {
	a := &Applier{
		store:       store,
		reconcilers: make(map[string]Reconciler, len(reconcilers)),
		log:         log,
	}
	for _, r := range reconcilers {
		a.reconcilers[r.Model()] = r
	}
	return a
}

// Apply parses and applies a YAML blueprint. If dryRun, no writes are made.
func (a *Applier) Apply(ctx context.Context, orgID uuid.UUID, yamlContent string, dryRun bool) (*Blueprint, *Diff, error) {
	var doc BlueprintDoc
	if err := yaml.Unmarshal([]byte(yamlContent), &doc); err != nil {
		return nil, nil, fmt.Errorf("blueprint parse: %w", err)
	}
	if strings.TrimSpace(doc.Version) == "" || strings.TrimSpace(doc.Metadata.Name) == "" {
		return nil, nil, fmt.Errorf("blueprint version and metadata.name are required")
	}

	now := time.Now().UTC()
	bp := &Blueprint{
		ID:          uuid.New(),
		OrgID:       orgID,
		Name:        doc.Metadata.Name,
		Version:     doc.Version,
		State:       StateApplied,
		YAMLContent: yamlContent,
		LastApplied: &now,
		CreatedAt:   now,
	}
	diff := &Diff{}
	reconcile := func(applyCtx context.Context) error {
		for _, entry := range doc.Entries {
			rec, ok := a.reconcilers[entry.Model]
			if !ok {
				return fmt.Errorf("entry %s: unsupported model %q", entry.Identifier, entry.Model)
			}
			action, err := rec.Reconcile(applyCtx, orgID, entry, dryRun)
			if err != nil {
				return fmt.Errorf("entry %s (%s): %w", entry.Identifier, entry.Model, err)
			}
			switch action {
			case "created":
				diff.ToCreate = append(diff.ToCreate, entry.Identifier)
			case "updated":
				diff.ToUpdate = append(diff.ToUpdate, entry.Identifier)
			case "deleted":
				diff.ToDelete = append(diff.ToDelete, entry.Identifier)
			default:
				diff.Unchanged = append(diff.Unchanged, entry.Identifier)
			}
		}
		return nil
	}
	persist := func(applyCtx context.Context) error {
		existing, err := a.store.ByOrgAndName(applyCtx, orgID, doc.Metadata.Name)
		switch {
		case err == nil:
			bp.ID = existing.ID
			bp.CreatedAt = existing.CreatedAt
			return a.store.Update(applyCtx, bp)
		case domainerr.IsNotFound(err):
			return a.store.Insert(applyCtx, bp)
		default:
			return err
		}
	}
	if dryRun {
		if err := reconcile(ctx); err != nil {
			bp.State, bp.LastError = StateErrored, err.Error()
			return bp, diff, err
		}
		return bp, diff, nil
	}
	applyErr := a.store.Transaction(ctx, func(txCtx context.Context) error {
		if err := reconcile(txCtx); err != nil {
			return err
		}
		if err := persist(txCtx); err != nil {
			return fmt.Errorf("persist blueprint record: %w", err)
		}
		return nil
	})
	if applyErr != nil {
		bp.State, bp.LastError = StateErrored, applyErr.Error()
	}
	return bp, diff, applyErr
}

// List returns all blueprints applied for an org.
func (a *Applier) List(ctx context.Context, orgID uuid.UUID) ([]*Blueprint, error) {
	return a.store.List(ctx, orgID)
}
