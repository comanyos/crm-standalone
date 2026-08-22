// Package schema validates identity traits against JSON Schemas (Kratos pattern).
// Each org can define multiple schemas; identities reference one by SchemaID.
package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Schema represents a versioned JSON Schema for identity traits.
type Schema struct {
	ID      string
	OrgID   string
	Version int
	Raw     json.RawMessage
}

// Registry loads and caches compiled schemas per org.
type Registry struct {
	mu      sync.RWMutex
	schemas map[string]*jsonschema.Schema // key: "orgID/schemaID"
	loader  Loader
}

// Loader fetches raw schema definitions from storage.
type Loader interface {
	Load(ctx context.Context, orgID, schemaID string) (*Schema, error)
}

func NewRegistry(loader Loader) *Registry {
	return &Registry{
		schemas: make(map[string]*jsonschema.Schema),
		loader:  loader,
	}
}

func (r *Registry) Validate(ctx context.Context, orgID, schemaID string, traits json.RawMessage) error {
	schema, err := r.get(ctx, orgID, schemaID)
	if err != nil {
		return err
	}

	var v any
	if err := json.Unmarshal(traits, &v); err != nil {
		return fmt.Errorf("traits unmarshal: %w", err)
	}
	if err := schema.Validate(v); err != nil {
		return fmt.Errorf("traits validation failed: %w", err)
	}
	return nil
}

func (r *Registry) get(ctx context.Context, orgID, schemaID string) (*jsonschema.Schema, error) {
	key := orgID + "/" + schemaID

	r.mu.RLock()
	if s, ok := r.schemas[key]; ok {
		r.mu.RUnlock()
		return s, nil
	}
	r.mu.RUnlock()

	raw, err := r.loader.Load(ctx, orgID, schemaID)
	if err != nil {
		return nil, fmt.Errorf("schema load %s: %w", key, err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(key, strings.NewReader(string(raw.Raw))); err != nil {
		return nil, fmt.Errorf("schema compile %s: %w", key, err)
	}
	compiled, err := compiler.Compile(key)
	if err != nil {
		return nil, fmt.Errorf("schema compile %s: %w", key, err)
	}

	r.mu.Lock()
	r.schemas[key] = compiled
	r.mu.Unlock()
	return compiled, nil
}

// Invalidate removes a cached schema (call after schema updates).
func (r *Registry) Invalidate(orgID, schemaID string) {
	r.mu.Lock()
	delete(r.schemas, orgID+"/"+schemaID)
	r.mu.Unlock()
}
