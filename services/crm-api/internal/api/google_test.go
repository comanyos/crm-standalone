package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/auth"
	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
)

// The Google RPCs at the handler boundary. The flow itself is covered in
// internal/integration/google; what is asserted here is what the RPC layer
// alone is responsible for: that the owner comes from the token, that no
// response can carry a credential, and that an unconfigured deployment says
// so instead of erroring.

func TestGoogleRPCsActOnTheCallersOwnConnection(t *testing.T) {
	s, store := newTestServiceWithStore()
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	// Two users in one organization, each with their own connection.
	ctxA := auth.WithPrincipal(context.Background(), &auth.Principal{
		Subject: "user-a", TenantID: "tenant-1", OrgID: "org-1", Scopes: []string{"crm:*"},
	})
	ctxB := auth.WithPrincipal(context.Background(), &auth.Principal{
		Subject: "user-b", TenantID: "tenant-1", OrgID: "org-1", Scopes: []string{"crm:*"},
	})
	for _, owner := range []string{"user-a", "user-b"} {
		if _, err := store.SaveGoogleConnection(context.Background(), scope, &domain.GoogleConnection{
			OwnerID:            owner,
			GoogleAccountEmail: owner + "@gmail.example",
			Status:             domain.GoogleStatusConnected,
			TokenCiphertext:    sealTestTokens(t, "tenant-1", owner),
		}); err != nil {
			t.Fatalf("seed %s: %v", owner, err)
		}
	}

	resp, err := s.GetGoogleConnection(ctxA, connect.NewRequest(&crmv1.GetGoogleConnectionRequest{}))
	if err != nil {
		t.Fatalf("GetGoogleConnection: %v", err)
	}
	if got := resp.Msg.GetConnection().GetGoogleAccountEmail(); got != "user-a@gmail.example" {
		t.Errorf("user-a saw %q, want their own connection", got)
	}

	// Disconnecting as B must leave A's connection alone. There is no owner
	// field on the request, which is the point.
	if _, err := s.DisconnectGoogleConnection(ctxB, connect.NewRequest(
		&crmv1.DisconnectGoogleConnectionRequest{})); err != nil {
		t.Fatalf("DisconnectGoogleConnection as B: %v", err)
	}
	stored, err := store.GetGoogleConnection(context.Background(), scope, "user-a")
	if err != nil {
		t.Fatalf("read A's connection: %v", err)
	}
	if stored.Status != domain.GoogleStatusConnected {
		t.Errorf("B's disconnect changed A's status to %q", stored.Status)
	}
}

// TestGoogleResponsesCarryNoCredential walks the wire messages looking for
// anything that could be a token. It is a blunt instrument on purpose: the
// failure it guards against is a field added to the proto in a hurry.
func TestGoogleResponsesCarryNoCredential(t *testing.T) {
	s, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")
	owner := principalSubject(ctx)

	sealed := sealTestTokens(t, "tenant-1", owner)
	if _, err := store.SaveGoogleConnection(context.Background(),
		domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}, &domain.GoogleConnection{
			OwnerID:            owner,
			GoogleAccountEmail: "person@gmail.example",
			Status:             domain.GoogleStatusConnected,
			TokenCiphertext:    sealed,
		}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	get, err := s.GetGoogleConnection(ctx, connect.NewRequest(&crmv1.GetGoogleConnectionRequest{}))
	if err != nil {
		t.Fatalf("GetGoogleConnection: %v", err)
	}
	list, err := s.ListGoogleConnections(ctx, connect.NewRequest(&crmv1.ListGoogleConnectionsRequest{}))
	if err != nil {
		t.Fatalf("ListGoogleConnections: %v", err)
	}

	for name, rendered := range map[string]string{
		"GetGoogleConnection":   get.Msg.String(),
		"ListGoogleConnections": list.Msg.String(),
	} {
		for _, forbidden := range []string{
			"stub-access-token", "stub-refresh-token", string(sealed), "test-secret",
		} {
			if forbidden != "" && strings.Contains(rendered, forbidden) {
				t.Errorf("%s response carries credential material", name)
			}
		}
	}
}

// TestGoogleRPCsRefuseAPrincipalWithoutASubject: a connection that could not
// be attributed to anyone could never be disconnected by its owner.
func TestGoogleRPCsRefuseAPrincipalWithoutASubject(t *testing.T) {
	s, _ := newTestServiceWithStore()
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		TenantID: "tenant-1", OrgID: "org-1", Scopes: []string{"crm:*"},
	})

	if _, err := s.StartGoogleConnection(ctx, connect.NewRequest(
		&crmv1.StartGoogleConnectionRequest{})); err == nil {
		t.Error("StartGoogleConnection accepted a principal with no subject")
	} else if got := connectCode(t, err); got != connect.CodePermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", got)
	}
}

// TestStartRejectsAnOffSiteReturnPathAtTheBoundary: the RPC must reject it
// rather than storing it and discovering the problem at redirect time.
func TestStartRejectsAnOffSiteReturnPathAtTheBoundary(t *testing.T) {
	s, _ := newTestServiceWithStore()
	ctx := ctxAs("tenant-1", "org-1")

	_, err := s.StartGoogleConnection(ctx, connect.NewRequest(&crmv1.StartGoogleConnectionRequest{
		ReturnPath: "https://evil.example/steal",
	}))
	if err == nil {
		t.Fatal("StartGoogleConnection accepted an absolute return path")
	}
	if got := connectCode(t, err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestUnconfiguredDeploymentSaysSoRatherThanFailing. A CRM without Google
// credentials is running correctly; the UI needs to render that state.
func TestUnconfiguredDeploymentSaysSoRatherThanFailing(t *testing.T) {
	s := NewCRMService(config.Config{}, domain.NewMemStore(), nil)
	ctx := ctxAs("tenant-1", "org-1")

	resp, err := s.GetGoogleConnection(ctx, connect.NewRequest(&crmv1.GetGoogleConnectionRequest{}))
	if err != nil {
		t.Fatalf("GetGoogleConnection with no integration: %v", err)
	}
	if resp.Msg.GetConfigured() {
		t.Error("an unconfigured deployment reported itself configured")
	}
	if resp.Msg.GetConnection().GetStatus() != "" {
		t.Errorf("status = %q, want empty", resp.Msg.GetConnection().GetStatus())
	}

	if _, err := s.StartGoogleConnection(ctx, connect.NewRequest(
		&crmv1.StartGoogleConnectionRequest{})); err == nil {
		t.Error("StartGoogleConnection succeeded with no integration configured")
	} else if got := connectCode(t, err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition — nothing is broken, "+
			"the deployment simply has no Google credentials", got)
	}
}

// TestGetGoogleConnectionAdvertisesTheRequestedScopes so the UI states the
// consent it is about to ask for from the server rather than a hardcoded copy
// that can drift.
func TestGetGoogleConnectionAdvertisesTheRequestedScopes(t *testing.T) {
	s, _ := newTestServiceWithStore()
	resp, err := s.GetGoogleConnection(ctxAs("tenant-1", "org-1"),
		connect.NewRequest(&crmv1.GetGoogleConnectionRequest{}))
	if err != nil {
		t.Fatalf("GetGoogleConnection: %v", err)
	}
	if !resp.Msg.GetConfigured() {
		t.Fatal("the test service reports itself unconfigured")
	}
	scopes := resp.Msg.GetRequestedScopes()
	if len(scopes) == 0 {
		t.Fatal("no requested scopes advertised")
	}
	for _, want := range []string{
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/calendar.events",
	} {
		found := false
		for _, scope := range scopes {
			if scope == want {
				found = true
			}
		}
		if !found {
			t.Errorf("requested scopes do not include %q", want)
		}
	}
}
