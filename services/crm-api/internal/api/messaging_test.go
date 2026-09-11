package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
)

// ListContactChannels exists to hand a client one answer rather than the
// ingredients for it. These tests pin that: the verdict, the reason, and the
// consent state all arrive together, so nothing downstream has to re-derive
// whether a contact may be messaged.

func channelsByKind(t *testing.T, res *connect.Response[crmv1.ListContactChannelsResponse]) map[string]*crmv1.MessagingChannel {
	t.Helper()
	out := map[string]*crmv1.MessagingChannel{}
	for _, ch := range res.Msg.GetChannels() {
		out[ch.GetKind()] = ch
	}
	return out
}

// seedMessagingContact creates an account and a contact on it. A Contact
// requires an account, so every channel test needs both.
func seedMessagingContact(t *testing.T, s *CRMService, ctx context.Context, email, phone string) string {
	t.Helper()
	account, err := s.CreateAccount(ctx, connect.NewRequest(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Messaging Holdings", OwnerId: "owner"},
	}))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	contact, err := s.CreateContact(ctx, connect.NewRequest(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{
			Email: email, AccountId: account.Msg.GetAccount().GetId(),
			FirstName: "Bob", LastName: "Buyer", Phone: phone,
		},
	}))
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}
	return contact.Msg.GetContact().GetId()
}

func TestListContactChannelsCarriesTheGatesVerdict(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-msg", "org-msg")

	contactID := seedMessagingContact(t, s, ctx, "bob@example.com", "+441632960001")

	res, err := s.ListContactChannels(ctx, connect.NewRequest(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}))
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	byKind := channelsByKind(t, res)
	for _, kind := range []string{"email", "sms", "voice", "whatsapp"} {
		ch, ok := byKind[kind]
		if !ok {
			t.Fatalf("no %s channel returned", kind)
		}
		if !ch.GetReachable() || !ch.GetMayMessage() {
			t.Errorf("%s = %+v, want reachable and permitted", kind, ch)
		}
		if ch.GetConsentState() != "" {
			t.Errorf("%s consent_state = %q, want empty for unknown", kind, ch.GetConsentState())
		}
	}

	// Withdrawing on one channel must refuse that channel and leave the
	// others alone — the ledger is keyed on the pair, not on the person.
	if _, err := s.RecordMessagingConsent(ctx, connect.NewRequest(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: "bob@example.com", State: "withdrawn",
	})); err != nil {
		t.Fatalf("record withdrawal: %v", err)
	}

	res, err = s.ListContactChannels(ctx, connect.NewRequest(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}))
	if err != nil {
		t.Fatalf("list channels after withdrawal: %v", err)
	}
	byKind = channelsByKind(t, res)

	email := byKind["email"]
	if email.GetMayMessage() {
		t.Error("email is still permitted after a withdrawal")
	}
	if email.GetRefusalReason() != "consent_withdrawn" {
		t.Errorf("email refusal_reason = %q, want %q", email.GetRefusalReason(), "consent_withdrawn")
	}
	if email.GetConsentState() != "withdrawn" {
		t.Errorf("email consent_state = %q, want %q", email.GetConsentState(), "withdrawn")
	}
	// Still reachable: the address works, we are just not allowed to use it.
	// Collapsing the two would tell the user to fix an address that is fine.
	if !email.GetReachable() {
		t.Error("a withdrawal made the address unreachable; those are different facts")
	}
	if sms := byKind["sms"]; !sms.GetMayMessage() {
		t.Error("withdrawing email consent also refused sms; consent is per channel")
	}
}

func TestListLeadChannelsCarriesTheSamePerChannelConsentVerdict(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-lead-msg", "org-lead-msg")

	created, err := s.CreateLead(ctx, connect.NewRequest(&crmv1.CreateLeadRequest{
		Lead: &crmv1.Lead{
			FirstName: "Maya", LastName: "Prospect",
			ContactEmail: "maya@example.com", Phone: "+919876543210",
		},
	}))
	if err != nil {
		t.Fatalf("create person-first lead: %v", err)
	}

	if _, err := s.RecordMessagingConsent(ctx, connect.NewRequest(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "whatsapp", Address: "+91 98765 43210", State: "withdrawn",
		Source: "capture", Evidence: "provider form opt-out",
	})); err != nil {
		t.Fatalf("record WhatsApp withdrawal: %v", err)
	}

	res, err := s.ListLeadChannels(ctx, connect.NewRequest(&crmv1.ListLeadChannelsRequest{
		LeadId: created.Msg.GetLead().GetId(),
	}))
	if err != nil {
		t.Fatalf("list lead channels: %v", err)
	}
	byKind := make(map[string]*crmv1.MessagingChannel, len(res.Msg.GetChannels()))
	for _, channel := range res.Msg.GetChannels() {
		byKind[channel.GetKind()] = channel
	}

	whatsApp := byKind["whatsapp"]
	if whatsApp == nil {
		t.Fatal("WhatsApp channel is absent from a phone-first Lead")
	}
	if !whatsApp.GetReachable() || whatsApp.GetMayMessage() {
		t.Errorf("WhatsApp = %+v, want reachable but refused", whatsApp)
	}
	if whatsApp.GetConsentState() != "withdrawn" || whatsApp.GetRefusalReason() != "consent_withdrawn" {
		t.Errorf("WhatsApp consent = %q / %q, want withdrawn / consent_withdrawn",
			whatsApp.GetConsentState(), whatsApp.GetRefusalReason())
	}
	if sms := byKind["sms"]; sms == nil || !sms.GetMayMessage() {
		t.Error("withdrawing WhatsApp also refused SMS; Lead consent must be per channel")
	}
	if email := byKind["email"]; email == nil || !email.GetMayMessage() {
		t.Error("withdrawing WhatsApp also refused email; Lead consent must be per channel")
	}
}

// TestListContactChannelsRefusesALocalNumber pins the country-code rule at the
// RPC boundary: the channel comes back with a reason the UI can show, rather
// than being silently absent or silently sendable.
func TestListContactChannelsRefusesALocalNumber(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-msg", "org-msg")

	contactID := seedMessagingContact(t, s, ctx, "local@example.com", "555-010-0000")

	res, err := s.ListContactChannels(ctx, connect.NewRequest(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}))
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	byKind := channelsByKind(t, res)

	for _, kind := range []string{"sms", "voice", "whatsapp"} {
		ch, ok := byKind[kind]
		if !ok {
			t.Fatalf("%s channel is absent; it should be present and refused, so the UI can say why", kind)
		}
		if ch.GetReachable() || ch.GetMayMessage() {
			t.Errorf("%s = %+v, want unreachable and refused without a country code", kind, ch)
		}
		if ch.GetUnreachableReason() == "" {
			t.Errorf("%s gives no reason; the user cannot fix what they are not told", kind)
		}
	}
	if email := byKind["email"]; !email.GetMayMessage() {
		t.Error("a local phone number refused the email channel too")
	}
}

func TestRecordMessagingConsentValidatesAtTheBoundary(t *testing.T) {
	s := newTestService()
	ctx := ctxAs("tenant-msg", "org-msg")

	bad := []struct {
		name string
		req  *crmv1.RecordMessagingConsentRequest
	}{
		{"unknown channel", &crmv1.RecordMessagingConsentRequest{ChannelKind: "pigeon", Address: "x@y.com", State: "granted"}},
		{"unknown state", &crmv1.RecordMessagingConsentRequest{ChannelKind: "email", Address: "x@y.com", State: "maybe"}},
		{"blank address", &crmv1.RecordMessagingConsentRequest{ChannelKind: "email", Address: "", State: "granted"}},
		{"local number", &crmv1.RecordMessagingConsentRequest{ChannelKind: "sms", Address: "5550100000", State: "granted"}},
		{"unknown source", &crmv1.RecordMessagingConsentRequest{ChannelKind: "email", Address: "x@y.com", State: "granted", Source: "vibes"}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.RecordMessagingConsent(ctx, connect.NewRequest(c.req)); err == nil {
				t.Fatal("accepted a consent record that should have been refused")
			} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want InvalidArgument", connect.CodeOf(err))
			}
		})
	}

	// The address is normalised on the way in, and the actor is taken from
	// the token rather than the request — a client cannot attribute a consent
	// decision to somebody else.
	res, err := s.RecordMessagingConsent(ctx, connect.NewRequest(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "sms", Address: "+44 1632 960 001", State: "withdrawn",
	}))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := res.Msg.GetConsent().GetAddress(); got != "+441632960001" {
		t.Errorf("stored address = %q, want the normalised form", got)
	}
	if got := res.Msg.GetConsent().GetRecordedBy(); got != principalSubject(ctx) {
		t.Errorf("recorded_by = %q, want the authenticated principal %q", got, principalSubject(ctx))
	}
	// Source defaults rather than being lost: the column exists to say where
	// a decision came from, and blank answers nothing.
	if got := res.Msg.GetConsent().GetSource(); got != "manual" {
		t.Errorf("source = %q, want it defaulted to %q", got, "manual")
	}
}
