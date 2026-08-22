// Package platform defines the shared CompanyOS module contract. This release
// snapshot makes product builds reproducible without relying on sibling folders.
package platform

import (
	"context"
	"time"
)

type NavEntry struct {
	Label string `json:"label"`
	Route string `json:"route"`
	Icon  string `json:"icon,omitempty"`
}

type Edition string

const (
	EditionOSS        Edition = "OSS"
	EditionPro        Edition = "PRO"
	EditionEnterprise Edition = "ENTERPRISE"
)

type Manifest struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	Edition      Edition    `json:"edition"`
	Band         string     `json:"band"`
	Icon         string     `json:"icon"`
	Nav          []NavEntry `json:"nav"`
	Capabilities []string   `json:"capabilities"`
}

type Status string

const (
	Green  Status = "GREEN"
	Yellow Status = "YELLOW"
	Red    Status = "RED"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Health struct {
	Status    Status    `json:"status"`
	Summary   string    `json:"summary"`
	Checks    []Check   `json:"checks"`
	CheckedAt time.Time `json:"checked_at"`
}

type Action struct {
	ID            string         `json:"id"`
	Description   string         `json:"description"`
	ParamsSchema  map[string]any `json:"params_schema"`
	Mutating      bool           `json:"mutating"`
	RequiredScope string         `json:"required_scope"`
}

type Module interface {
	Manifest() Manifest
	Health(context.Context) Health
	Actions() []Action
	Invoke(context.Context, string, map[string]any) (any, error)
}
