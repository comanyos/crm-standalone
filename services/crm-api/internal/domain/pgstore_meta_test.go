package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPgMetaConnectionAndCapturedLeadAreDurableAndIsolated(t *testing.T) {
	store := livePgStore(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	a := Scope{TenantID: fmt.Sprintf("tenant-meta-a-%d", stamp), OrgID: "org-a"}
	b := Scope{TenantID: fmt.Sprintf("tenant-meta-b-%d", stamp), OrgID: "org-b"}
	businessID := fmt.Sprintf("business-a-%d", stamp)
	pageID := fmt.Sprintf("page-a-%d", stamp)
	formID := fmt.Sprintf("form-a-%d", stamp)

	state := &MetaOAuthState{
		State: "state-a-" + fmt.Sprint(stamp), ActorID: "owner-a", ReturnPath: "/settings/integrations",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	}
	if _, err := store.CreateMetaOAuthState(ctx, a, state); err != nil {
		t.Fatalf("create OAuth state: %v", err)
	}
	if tenant, err := store.ResolveMetaOAuthStateTenant(ctx, state.State); err != nil || tenant != a.TenantID {
		t.Fatalf("resolve OAuth state = %q, %v", tenant, err)
	}
	if _, err := store.ConsumeMetaOAuthState(ctx, b, state.State, time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B consumed tenant A state: %v", err)
	}
	if _, err := store.ConsumeMetaOAuthState(ctx, a, state.State, time.Now().UTC()); err != nil {
		t.Fatalf("tenant A consume: %v", err)
	}
	if _, err := store.ConsumeMetaOAuthState(ctx, a, state.State, time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OAuth state replay: %v", err)
	}

	connection := &MetaConnection{
		ID: "connection-a", MetaUserID: "meta-user-a", MetaUserName: "Meta Owner",
		Status: MetaStatusConnected, GrantedPermissions: []string{"leads_retrieval", "pages_show_list"},
		UserTokenCiphertext: []byte("sealed-user"), PageTokenCiphertext: []byte("sealed-page"),
		BusinessID: businessID, BusinessName: "Business A",
		PageID: pageID, PageName: "Page A", FormID: formID, FormName: "Form A",
		ConnectedBy: "owner-a", ConnectedAt: time.Now().UTC(),
	}
	storedConnection, err := store.SaveMetaConnection(ctx, a, connection)
	if err != nil {
		t.Fatalf("save connection: %v", err)
	}
	if storedConnection.BusinessID != businessID || storedConnection.PageID != pageID ||
		storedConnection.FormID != formID || string(storedConnection.PageTokenCiphertext) != "sealed-page" {
		t.Fatalf("connection round trip = %+v", storedConnection.View())
	}
	if _, err := store.GetMetaConnection(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B read tenant A connection: %v", err)
	}
	refs, err := store.ResolveMetaConnections(ctx, pageID, formID)
	if err != nil || len(refs) != 1 || refs[0].TenantID != a.TenantID || refs[0].BusinessID != businessID {
		t.Fatalf("resolved routes = %+v, %v", refs, err)
	}

	lead, created, err := store.CreateMetaCapturedLead(ctx, a,
		&Lead{FirstName: " Alex ", LastName: " Buyer ", Phone: "+1 (555) 010-0000", Source: "Facebook Lead Ads"},
		&MetaLeadAttribution{
			ConnectionID: connection.ID, ProviderLeadID: "provider-lead-a", Platform: "Facebook",
			PageID: pageID, FormID: formID, CampaignID: "campaign-a", CampaignName: "Campaign A",
			RawPayload: `{"id":"provider-lead-a"}`,
		})
	if err != nil || !created {
		t.Fatalf("create captured lead: created=%v err=%v", created, err)
	}
	if lead.FirstName != "Alex" || lead.Phone != "+15550100000" {
		t.Fatalf("person-first lead round trip = %+v", lead)
	}
	attribution, err := store.GetMetaLeadAttribution(ctx, a, lead.ID)
	if err != nil || attribution.ProviderLeadID != "provider-lead-a" || attribution.CampaignName != "Campaign A" {
		t.Fatalf("attribution = %+v, %v", attribution, err)
	}
	if _, err := store.GetMetaLeadAttribution(ctx, b, lead.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant B read tenant A attribution: %v", err)
	}
	retry, created, err := store.CreateMetaCapturedLead(ctx, a,
		&Lead{FirstName: "Duplicate", Phone: "+15550100009"},
		&MetaLeadAttribution{ConnectionID: connection.ID, ProviderLeadID: "provider-lead-a"})
	if err != nil || created || retry.ID != lead.ID {
		t.Fatalf("idempotent retry = %+v created=%v err=%v", retry, created, err)
	}
	leads, _, _ := store.ListLeads(ctx, a, Page{Size: 20})
	if len(leads) != 1 {
		t.Fatalf("idempotent retry left %d leads", len(leads))
	}
	events, err := store.OutboxEventsFor(ctx, a, lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	createdEvents := 0
	for _, event := range events {
		if event.Envelope.Type == EventLeadCreated {
			createdEvents++
		}
	}
	if createdEvents != 1 {
		t.Fatalf("captured lead has %d lead.created events, want 1", createdEvents)
	}

	_, err = store.SaveMetaConnection(ctx, b, &MetaConnection{
		ID: "connection-b", Status: MetaStatusConnected,
		UserTokenCiphertext: []byte("sealed-user-b"), PageTokenCiphertext: []byte("sealed-page-b"),
		BusinessID: businessID, PageID: pageID, FormID: formID,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("same Meta form connected to two tenants: %v", err)
	}
}

func TestMemMetaConnectionAlsoRejectsCrossWorkspaceFormFanout(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	connection := func(id string) *MetaConnection {
		return &MetaConnection{
			ID: id, Status: MetaStatusConnected,
			UserTokenCiphertext: []byte("sealed-user"), PageTokenCiphertext: []byte("sealed-page"),
			BusinessID: "business-1", PageID: "page-1", FormID: "form-1",
		}
	}
	if _, err := store.SaveMetaConnection(ctx, Scope{TenantID: "tenant-a"}, connection("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMetaConnection(ctx, Scope{TenantID: "tenant-b"}, connection("b")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate route error = %v", err)
	}
}
