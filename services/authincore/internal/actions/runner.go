// Package actions executes custom JS code hooks (Zitadel actions / FusionAuth lambdas).
// Sandboxed via goja; WASM support via wazero is planned.
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"log/slog"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

// Action is the stored action definition.
type Action struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	Name          string
	Script        string
	TimeoutMS     int64
	AllowedToFail bool
}

// ExecutionTarget binds an action to a trigger.
type ExecutionTarget struct {
	ID       uuid.UUID
	ActionID uuid.UUID
	Trigger  string
	Order    int
}

// Store reads actions and execution targets.
type Store interface {
	ByID(ctx context.Context, id uuid.UUID) (*Action, error)
	TargetsByTrigger(ctx context.Context, orgID uuid.UUID, trigger string) ([]*ExecutionTarget, error)
}

// Runner executes actions for a trigger point.
type Runner struct {
	store Store
	log   *slog.Logger
}

func NewRunner(store Store, log *slog.Logger) *Runner {
	return &Runner{store: store, log: log}
}

// TriggerContext is the data passed into action scripts as `authincore`.
type TriggerContext struct {
	OrgID      string         `json:"org_id"`
	IdentityID string         `json:"identity_id,omitempty"`
	Trigger    string         `json:"trigger"`
	Payload    map[string]any `json:"payload"`
}

// Fire executes all actions bound to the given trigger, in order.
// Scripts can mutate `payload` — the final payload is returned.
func (r *Runner) Fire(ctx context.Context, orgID uuid.UUID, trigger string, payload map[string]any) (map[string]any, error) {
	targets, err := r.store.TargetsByTrigger(ctx, orgID, trigger)
	if err != nil {
		return payload, fmt.Errorf("actions targets: %w", err)
	}

	for _, target := range targets {
		action, err := r.store.ByID(ctx, target.ActionID)
		if err != nil {
			r.log.Warn("action load error", "action_id", target.ActionID, "err", err)
			continue
		}

		result, err := r.exec(ctx, action, TriggerContext{
			OrgID:   orgID.String(),
			Trigger: trigger,
			Payload: payload,
		})
		if err != nil {
			r.log.Error("action exec error", "action", action.Name, "trigger", trigger, "err", err)
			if !action.AllowedToFail {
				return payload, fmt.Errorf("action %s failed: %w", action.Name, err)
			}
			continue
		}
		if result != nil {
			payload = result
		}
	}
	return payload, nil
}

func (r *Runner) exec(ctx context.Context, action *Action, tc TriggerContext) (map[string]any, error) {
	timeout := time.Duration(action.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	vm := goja.New()

	// Expose authincore context to the script.
	ctxJSON, _ := json.Marshal(tc)
	var ctxMap map[string]any
	_ = json.Unmarshal(ctxJSON, &ctxMap)
	if err := vm.Set("authincore", ctxMap); err != nil {
		return nil, err
	}

	// Expose a simple log function.
	_ = vm.Set("log", func(msg string) {
		r.log.Info("action log", "action", action.Name, "msg", msg)
	})

	done := make(chan struct{})
	var (
		result  map[string]any
		execErr error
	)

	go func() {
		defer close(done)
		_, execErr = vm.RunString(action.Script)
		if execErr == nil {
			// Retrieve mutated payload from the VM.
			if v := vm.Get("authincore"); v != nil {
				if m, ok := v.Export().(map[string]any); ok {
					if p, ok := m["payload"].(map[string]any); ok {
						result = p
					}
				}
			}
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return result, execErr
	case <-timer.C:
		vm.Interrupt("timeout")
		<-done
		return nil, fmt.Errorf("action %s timed out after %s", action.Name, timeout)
	case <-ctx.Done():
		vm.Interrupt("cancelled")
		<-done
		return nil, ctx.Err()
	}
}
