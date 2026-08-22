// Package engine evaluates authentik-style policies bound to flow stages.
// Policies are pluggable: expression (JS via goja), group membership, reputation.
package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

type PolicyType string

const (
	PolicyTypeExpression PolicyType = "expression"
	PolicyTypeGroup      PolicyType = "group"
	PolicyTypeReputation PolicyType = "reputation"
)

// Policy defines the evaluation config.
type Policy struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	Name           string
	Type           PolicyType
	ExpressionCode string      // for expression policies
	GroupIDs       []uuid.UUID // for group policies
	ScoreThreshold int         // for reputation policies
}

// EvalContext carries runtime data available to policy expressions.
type EvalContext struct {
	IdentityID *uuid.UUID
	OrgID      uuid.UUID
	IP         string
	UserAgent  string
	Traits     map[string]any
	Extra      map[string]any
}

type Result struct {
	Passing bool
	Message string
}

// Engine evaluates policies for a given context.
type Engine struct {
	store  PolicyStore
	rep    ReputationStore
	groups GroupStore
	log    *slog.Logger
}

type PolicyStore interface {
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]*Policy, error)
	BoundTo(ctx context.Context, orgID uuid.UUID, bindingPoint string) ([]*Policy, error)
}

type ReputationStore interface {
	Score(ctx context.Context, orgID uuid.UUID, ip string) (int, error)
	Increment(ctx context.Context, orgID uuid.UUID, ip string, delta int) error
}

type GroupStore interface {
	IsMember(ctx context.Context, orgID, identityID, groupID uuid.UUID) (bool, error)
}

func New(store PolicyStore, rep ReputationStore, groups GroupStore, log *slog.Logger) *Engine {
	return &Engine{store: store, rep: rep, groups: groups, log: log}
}

// EvalBinding evaluates all policies bound to a given point (e.g. "flow.login.pre").
// Returns false + reason if any policy fails (deny-by-first-failure semantics).
func (e *Engine) EvalBinding(ctx context.Context, orgID uuid.UUID, bindingPoint string, ec EvalContext) (Result, error) {
	policies, err := e.store.BoundTo(ctx, orgID, bindingPoint)
	if err != nil {
		return Result{Passing: true}, fmt.Errorf("policy load for %s: %w", bindingPoint, err)
	}
	for _, p := range policies {
		result, err := e.eval(ctx, p, ec)
		if err != nil {
			e.log.Warn("policy eval error", "policy_id", p.ID, "err", err)
			continue
		}
		if !result.Passing {
			return result, nil
		}
	}
	return Result{Passing: true}, nil
}

func (e *Engine) eval(ctx context.Context, p *Policy, ec EvalContext) (Result, error) {
	switch p.Type {
	case PolicyTypeExpression:
		return e.evalExpression(p, ec)
	case PolicyTypeGroup:
		return e.evalGroup(ctx, p, ec)
	case PolicyTypeReputation:
		return e.evalReputation(ctx, p, ec)
	default:
		return Result{Passing: true}, nil
	}
}

func (e *Engine) evalExpression(p *Policy, ec EvalContext) (Result, error) {
	vm := goja.New()
	if err := vm.Set("context", ec); err != nil {
		return Result{}, err
	}
	val, err := vm.RunString(p.ExpressionCode)
	if err != nil {
		return Result{}, fmt.Errorf("expression policy %s: %w", p.ID, err)
	}
	passing := val.ToBoolean()
	msg := ""
	if !passing {
		msg = fmt.Sprintf("expression policy %q denied access", p.Name)
	}
	return Result{Passing: passing, Message: msg}, nil
}

func (e *Engine) evalGroup(ctx context.Context, p *Policy, ec EvalContext) (Result, error) {
	if ec.IdentityID == nil {
		return Result{Passing: false, Message: "group policy: no identity"}, nil
	}
	for _, gid := range p.GroupIDs {
		ok, err := e.groups.IsMember(ctx, ec.OrgID, *ec.IdentityID, gid)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return Result{Passing: true}, nil
		}
	}
	return Result{Passing: false, Message: fmt.Sprintf("group policy %q: identity not in required group", p.Name)}, nil
}

func (e *Engine) evalReputation(ctx context.Context, p *Policy, ec EvalContext) (Result, error) {
	score, err := e.rep.Score(ctx, ec.OrgID, ec.IP)
	if err != nil {
		return Result{Passing: true}, nil // fail-open on reputation errors
	}
	if score < p.ScoreThreshold {
		return Result{Passing: false, Message: fmt.Sprintf("reputation score %d below threshold %d", score, p.ScoreThreshold)}, nil
	}
	return Result{Passing: true}, nil
}
