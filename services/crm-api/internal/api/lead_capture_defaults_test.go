package api

import (
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

func TestNewCaptureEndpointDefaultMappingAcceptsAPersonFirstLead(t *testing.T) {
	service, store := newTestServiceWithStore()
	ctx := ctxAs("tenant-capture-defaults", "org-capture-defaults")
	response, err := service.CreateLeadCaptureEndpoint(ctx, connect.NewRequest(&crmv1.CreateLeadCaptureEndpointRequest{
		Name: "Default website form",
	}))
	if err != nil {
		t.Fatal(err)
	}
	wantMapping := map[string]string{
		"first_name": "first_name", "last_name": "last_name", "email": "contact_email",
		"phone": "phone", "company": "company", "source": "source",
	}
	for incoming, target := range wantMapping {
		if response.Msg.GetEndpoint().GetFieldMapping()[incoming] != target {
			t.Fatalf("default mapping[%q] = %q, want %q", incoming,
				response.Msg.GetEndpoint().GetFieldMapping()[incoming], target)
		}
	}
	endpoint, err := store.GetLeadCaptureEndpoint(ctx, domain.Scope{
		TenantID: "tenant-capture-defaults", OrgID: "org-capture-defaults",
	}, response.Msg.GetEndpoint().GetId())
	if err != nil {
		t.Fatal(err)
	}
	lead, ok := domain.LeadFromCapture(endpoint, map[string]any{
		"first_name": " Alex ", "last_name": " Rivera ", "phone": "+1 (555) 010-0000",
	})
	if !ok || lead.FirstName != "Alex" || lead.LastName != "Rivera" || lead.Phone != "+15550100000" {
		t.Fatalf("default mapping produced lead=%+v ok=%v", lead, ok)
	}
}
