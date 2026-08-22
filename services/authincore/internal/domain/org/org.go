package org

import (
	"time"

	"github.com/google/uuid"
)

type State int

const (
	StateUnspecified State = iota
	StateActive
	StateInactive
	StateRemoved
)

// Org is the realm / tenant isolation boundary (Keycloak realm + Zitadel org).
type Org struct {
	ID            uuid.UUID
	InstanceID    uuid.UUID
	Name          string
	PrimaryDomain string
	State         State
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func New(instanceID uuid.UUID, name, primaryDomain string) *Org {
	now := time.Now().UTC()
	return &Org{
		ID:            uuid.New(),
		InstanceID:    instanceID,
		Name:          name,
		PrimaryDomain: primaryDomain,
		State:         StateActive,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// Project groups applications within an org.
type Project struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewProject(orgID uuid.UUID, name string) *Project {
	now := time.Now().UTC()
	return &Project{
		ID:        uuid.New(),
		OrgID:     orgID,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

type AppType int

const (
	AppTypeUnspecified AppType = iota
	AppTypeOIDC
	AppTypeSAML
	AppTypeAPI
)

type OIDCConfig struct {
	ClientID               string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	ResponseTypes          []string
	GrantTypes             []string
	ApplicationType        string
	AuthMethodType         string
	DevMode                bool
}

// Application is the OIDC/SAML client within a project.
type Application struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	OrgID      uuid.UUID
	Name       string
	Type       AppType
	OIDCConfig *OIDCConfig
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func NewApplication(projectID, orgID uuid.UUID, name string, appType AppType) *Application {
	now := time.Now().UTC()
	return &Application{
		ID:        uuid.New(),
		ProjectID: projectID,
		OrgID:     orgID,
		Name:      name,
		Type:      appType,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
