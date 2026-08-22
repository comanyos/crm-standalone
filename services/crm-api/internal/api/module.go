package api

import (
	"context"

	"connectrpc.com/connect"
	platformv1 "github.com/santoshguru/crm/gen/platform/v1"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModuleService implements the platform contract every module exposes.
type ModuleService struct {
	cfg   config.Config
	store domain.Store
}

// NewModuleService wires the contract service.
func NewModuleService(cfg config.Config, store domain.Store) *ModuleService {
	return &ModuleService{cfg: cfg, store: store}
}

// GetManifest returns this module's identity, navigation and capabilities.
func (s *ModuleService) GetManifest(
	_ context.Context, _ *connect.Request[platformv1.GetManifestRequest],
) (*connect.Response[platformv1.GetManifestResponse], error) {
	manifest := &platformv1.Manifest{
		Id:      "crm",
		Name:    "CRM",
		Version: "0.1.0",
		Band:    "experience",
		Icon:    "users",
		Nav: []*platformv1.NavEntry{
			{Label: "Overview", Route: "/", Icon: "layout-dashboard"},
			{Label: "Accounts", Route: "/accounts", Icon: "users"},
			{Label: "Contacts", Route: "/contacts", Icon: "users"},
			{Label: "Leads", Route: "/leads", Icon: "users"},
			{Label: "Opportunities", Route: "/opportunities", Icon: "users"},
		},
		Capabilities: []string{"accounts", "contacts", "leads", "opportunities", "activities"},
	}
	switch s.cfg.Edition {
	case "PRO":
		manifest.Edition = platformv1.Edition_EDITION_PRO
	case "ENTERPRISE":
		manifest.Edition = platformv1.Edition_EDITION_ENTERPRISE
	default:
		manifest.Edition = platformv1.Edition_EDITION_OSS
	}

	return connect.NewResponse(&platformv1.GetManifestResponse{
		Manifest: manifest,
	}), nil
}

// GetHealth reports the module's current health to the shell.
func (s *ModuleService) GetHealth(
	_ context.Context, _ *connect.Request[platformv1.GetHealthRequest],
) (*connect.Response[platformv1.GetHealthResponse], error) {
	return connect.NewResponse(&platformv1.GetHealthResponse{
		Health: &platformv1.Health{
			Status:    platformv1.Status_STATUS_GREEN,
			Summary:   "CRM is healthy",
			CheckedAt: timestamppb.Now(),
		},
	}), nil
}

// ListActions returns the Copilot-invocable actions this module exposes.
func (s *ModuleService) ListActions(
	_ context.Context, _ *connect.Request[platformv1.ListActionsRequest],
) (*connect.Response[platformv1.ListActionsResponse], error) {
	return connect.NewResponse(&platformv1.ListActionsResponse{
		Actions: []*platformv1.Action{},
	}), nil
}
