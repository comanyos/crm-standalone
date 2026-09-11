package domain

import (
	"context"
	"testing"
)

func TestFailedCaptureDeliveryCanBeReclaimed(t *testing.T) {
	store := NewMemStore()
	scope := Scope{TenantID: "retry-tenant", OrgID: "retry-tenant"}
	first, created, err := store.RecordCaptureDelivery(context.Background(), scope, &LeadCaptureDelivery{
		EndpointID: "endpoint", DedupKey: "event-1", Outcome: DeliveryCreated, RawPayload: `{}`,
	})
	if err != nil || !created {
		t.Fatalf("initial claim: created=%v err=%v", created, err)
	}
	if err := store.FailCaptureDelivery(context.Background(), scope, first.ID, CaptureReasonStoreFailed); err != nil {
		t.Fatal(err)
	}
	retried, reclaimed, err := store.RecordCaptureDelivery(context.Background(), scope, &LeadCaptureDelivery{
		EndpointID: "endpoint", DedupKey: "event-1", Outcome: DeliveryCreated, RawPayload: `{"retry":true}`,
	})
	if err != nil || !reclaimed {
		t.Fatalf("retry claim: reclaimed=%v err=%v", reclaimed, err)
	}
	if retried.ID != first.ID || retried.Outcome != DeliveryCreated {
		t.Fatalf("reclaimed delivery = %+v, original id %q", retried, first.ID)
	}
}
