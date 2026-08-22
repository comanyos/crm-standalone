// Package platform implements the shared Module Contract surface (manifest, health,
// events, copilot actions, cross-links) that every platform module exposes.
// See ../ideas/PLATFORM-MODULE-CONTRACT.md.
package platform

import "time"

// ---- Manifest (GetManifest) ----

// NavEntry is one sidebar entry the shell renders for this module.
type NavEntry struct {
	Label string `json:"label"`
	Route string `json:"route"`
	Icon  string `json:"icon,omitempty"`
}

// Manifest is what a module tells the shell about itself.
type Manifest struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	Edition      string     `json:"edition"` // OSS | PRO | ENTERPRISE
	Band         string     `json:"band"`
	Icon         string     `json:"icon"`
	Nav          []NavEntry `json:"nav"`
	Capabilities []string   `json:"capabilities"`
}

// ---- Health (GetHealth) ----

// Status is a module's traffic-light health state.
type Status string

// The traffic-light states a module may report.
const (
	Green  Status = "GREEN"
	Yellow Status = "YELLOW"
	Red    Status = "RED"
)

// Check is one named health probe within a module.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Health is a module's overall health plus its individual checks.
type Health struct {
	Status    Status    `json:"status"`
	Summary   string    `json:"summary"`
	Checks    []Check   `json:"checks"`
	CheckedAt time.Time `json:"checked_at"`
}

// ---- Cross-links (suite glue) ----

// CrossLink points at a record owned by another module.
type CrossLink struct {
	Kind   string `json:"kind"`   // asset | alert | ticket | cost | secret
	Module string `json:"module"` // target module id
	Ref    string `json:"ref"`    // opaque id in that module
	Label  string `json:"label"`
}

// ---- Events (Activity Feed via the bus) ----

// Severity is an event's importance on the activity feed.
type Severity string

// Event severities, ordered least to most urgent.
const (
	Info     Severity = "INFO"
	Warn     Severity = "WARN"
	Critical Severity = "CRITICAL"
)

// Event is one activity-feed entry published onto the bus.
type Event struct {
	ID       string         `json:"id"`
	Module   string         `json:"module"`
	Type     string         `json:"type"`
	Severity Severity       `json:"severity"`
	TenantID string         `json:"tenant_id"`
	OrgID    string         `json:"org_id"`
	Actor    string         `json:"actor"`
	Subject  string         `json:"subject"`
	Links    []CrossLink    `json:"links,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	At       time.Time      `json:"at"`
}

// ---- Copilot actions (ListActions) ----

// Action is a Copilot-invocable operation the module exposes.
type Action struct {
	ID            string         `json:"id"`
	Description   string         `json:"description"`
	ParamsSchema  map[string]any `json:"params_schema"`
	Mutating      bool           `json:"mutating"`
	RequiredScope string         `json:"required_scope"`
}
