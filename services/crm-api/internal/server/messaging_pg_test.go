package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/gen/crm/v1/crmv1connect"
)

// The messaging consent surface end to end (task_crm.md §2): a real HTTP
// server, real RS256 tokens verified against a real JWKS, the real scope
// interceptor, and a real PostgreSQL store connected as crm_app — the role
// row-level security binds.
//
// The domain tests prove the gate's logic and the API tests prove the
// handlers compose it. Neither proves that the thing a signed token actually
// reaches is the thing RLS actually constrains, which is what this tier is
// for and what makes "verified live" mean something here.

func consentOrg(label string) string {
	return fmt.Sprintf("org-consent-%s-%d", label, time.Now().UnixNano())
}

var (
	consentReaderScopes = []string{"crm:account:write", "crm:contact:write", "crm:consent:read"}
	consentWriterScopes = append([]string{"crm:consent:write"}, consentReaderScopes...)
)

// seedMessagingSubject creates an account and a contact over HTTP and returns
// the contact id and email.
func seedMessagingSubject(
	ctx context.Context, t *testing.T, c crmv1connect.CRMServiceClient, token, label string,
) (contactID, email string) {
	t.Helper()

	account, err := c.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: label + " Holdings", OwnerId: "owner-" + label},
	}, token))
	if err != nil {
		t.Fatalf("[%s] create account: %v", label, err)
	}

	email = fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano())
	contact, err := c.CreateContact(ctx, withToken(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{
			Email: email, AccountId: account.Msg.GetAccount().GetId(),
			FirstName: label, LastName: "Buyer",
			Phone: "+441632960001",
		},
	}, token))
	if err != nil {
		t.Fatalf("[%s] create contact: %v", label, err)
	}
	return contact.Msg.GetContact().GetId(), email
}

// TestMessagingConsentOverPostgres walks the whole loop a user does: read the
// channels, record a withdrawal, read them again and find that channel
// refused.
func TestMessagingConsentOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()
	org := consentOrg("loop")
	token := mint(org, consentWriterScopes)

	contactID, email := seedMessagingSubject(ctx, t, client, token, "loop")

	res, err := client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}, token))
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	byKind := map[string]*crmv1.MessagingChannel{}
	for _, ch := range res.Msg.GetChannels() {
		byKind[ch.GetKind()] = ch
	}
	for _, kind := range []string{"email", "sms", "voice", "whatsapp"} {
		ch, ok := byKind[kind]
		if !ok {
			t.Fatalf("no %s channel", kind)
		}
		if !ch.GetMayMessage() {
			t.Errorf("%s = %+v, want permitted before anyone has decided anything", kind, ch)
		}
	}
	// Derived from Contact.Phone, normalised on the way out.
	if got := byKind["whatsapp"].GetAddress(); got != "+441632960001" {
		t.Errorf("whatsapp address = %q, want the normalised phone", got)
	}

	if _, err := client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: email, State: "withdrawn",
		Source: "inbound_keyword", Evidence: "replied STOP",
	}, token)); err != nil {
		t.Fatalf("record withdrawal: %v", err)
	}

	res, err = client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}, token))
	if err != nil {
		t.Fatalf("list channels after withdrawal: %v", err)
	}
	for _, ch := range res.Msg.GetChannels() {
		switch ch.GetKind() {
		case "email":
			if ch.GetMayMessage() {
				t.Error("email is still permitted after a withdrawal")
			}
			if ch.GetRefusalReason() != "consent_withdrawn" {
				t.Errorf("email refusal = %q, want consent_withdrawn", ch.GetRefusalReason())
			}
			if ch.GetConsentState() != "withdrawn" {
				t.Errorf("email consent_state = %q, want withdrawn", ch.GetConsentState())
			}
		default:
			if !ch.GetMayMessage() {
				t.Errorf("%s was refused by an email withdrawal; consent is per channel", ch.GetKind())
			}
		}
	}

	// The ledger keeps the decision, with who and on what evidence.
	history, err := client.ListMessagingConsent(ctx, withToken(&crmv1.ListMessagingConsentRequest{
		ChannelKind: "email", Address: email,
	}, token))
	if err != nil {
		t.Fatalf("list consent: %v", err)
	}
	if len(history.Msg.GetConsent()) != 1 {
		t.Fatalf("history has %d rows, want 1", len(history.Msg.GetConsent()))
	}
	row := history.Msg.GetConsent()[0]
	if row.GetRecordedBy() != "user-"+org {
		t.Errorf("recorded_by = %q, want the token's subject", row.GetRecordedBy())
	}
	if row.GetEvidence() != "replied STOP" || row.GetSource() != "inbound_keyword" {
		t.Errorf("evidence/source = %q/%q, want them preserved", row.GetEvidence(), row.GetSource())
	}
}

// TestMessagingConsentScopeEnforcementOverPostgres proves the split that
// justified giving consent its own scope pair: a principal may be able to see
// that somebody opted out without being able to assert that they opted in.
func TestMessagingConsentScopeEnforcementOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()
	org := consentOrg("scopes")

	writer := mint(org, consentWriterScopes)
	reader := mint(org, consentReaderScopes)

	contactID, email := seedMessagingSubject(ctx, t, client, writer, "scopes")

	// The reader can look.
	if _, err := client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}, reader)); err != nil {
		t.Fatalf("reader cannot list channels: %v", err)
	}
	if _, err := client.ListMessagingConsent(ctx, withToken(&crmv1.ListMessagingConsentRequest{
		ChannelKind: "email", Address: email,
	}, reader)); err != nil {
		t.Fatalf("reader cannot list consent: %v", err)
	}

	// And cannot decide.
	_, err := client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: email, State: "granted",
	}, reader))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("reader recording consent: %v (code %v), want PermissionDenied",
			err, connect.CodeOf(err))
	}

	// A principal with no consent scope at all cannot even see the channels,
	// though it holds every contact scope — the address is incidental, the
	// verdict is the payload.
	blind := mint(org, []string{"crm:contact:read", "crm:contact:write", "crm:account:write"})
	_, err = client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contactID,
	}, blind))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a contact-only principal listing channels: %v (code %v), want PermissionDenied",
			err, connect.CodeOf(err))
	}
}

// TestMessagingConsentTenantIsolationOverPostgres is the property that has to
// hold under real row-level security rather than under a Go predicate: one
// organization's withdrawal is invisible to another, and — the direction that
// matters more — is not overturnable by one.
func TestMessagingConsentTenantIsolationOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()

	orgA, orgB := consentOrg("alpha"), consentOrg("beta")
	tokenA := mint(orgA, consentWriterScopes)
	tokenB := mint(orgB, consentWriterScopes)

	contactA, email := seedMessagingSubject(ctx, t, client, tokenA, "alpha")

	if _, err := client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: email, State: "withdrawn",
	}, tokenA)); err != nil {
		t.Fatalf("alpha records withdrawal: %v", err)
	}

	// Naming alpha's contact must read as absent, never as forbidden: a
	// refusal that distinguishes the two confirms the id is real.
	_, err := client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contactA,
	}, tokenB))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("beta reading alpha's contact channels: %v (code %v), want NotFound",
			err, connect.CodeOf(err))
	}

	// Beta sees no decision for the same address, and its own grant does not
	// reach alpha's ledger.
	seen, err := client.ListMessagingConsent(ctx, withToken(&crmv1.ListMessagingConsentRequest{
		ChannelKind: "email", Address: email,
	}, tokenB))
	if err != nil {
		t.Fatalf("beta lists consent: %v", err)
	}
	if len(seen.Msg.GetConsent()) != 0 {
		t.Errorf("beta reads %d of alpha's consent rows, want 0", len(seen.Msg.GetConsent()))
	}

	if _, err := client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "email", Address: email, State: "granted",
	}, tokenB)); err != nil {
		t.Fatalf("beta records its own grant: %v", err)
	}

	after, err := client.ListMessagingConsent(ctx, withToken(&crmv1.ListMessagingConsentRequest{
		ChannelKind: "email", Address: email,
	}, tokenA))
	if err != nil {
		t.Fatalf("alpha lists consent: %v", err)
	}
	if len(after.Msg.GetConsent()) != 1 || after.Msg.GetConsent()[0].GetState() != "withdrawn" {
		t.Error("beta's grant overturned alpha's withdrawal; one tenant must not be able to " +
			"re-permit contact with somebody another tenant was told to stop contacting")
	}
}

// TestMessagingRejectsAmbiguousNumbersOverPostgres pins the country-code rule
// at the outermost boundary, since it is the rule that keeps every consent
// row on one canonical key.
func TestMessagingRejectsAmbiguousNumbersOverPostgres(t *testing.T) {
	client, mint := livePgServer(t)
	ctx := context.Background()
	org := consentOrg("ambiguous")
	token := mint(org, consentWriterScopes)

	account, err := client.CreateAccount(ctx, withToken(&crmv1.CreateAccountRequest{
		Account: &crmv1.Account{Name: "Ambiguous Holdings", OwnerId: "owner"},
	}, token))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	contact, err := client.CreateContact(ctx, withToken(&crmv1.CreateContactRequest{
		Contact: &crmv1.Contact{
			Email:     fmt.Sprintf("local-%d@example.com", time.Now().UnixNano()),
			AccountId: account.Msg.GetAccount().GetId(),
			FirstName: "Local", Phone: "555-010-0000",
		},
	}, token))
	if err != nil {
		t.Fatalf("create contact: %v", err)
	}

	res, err := client.ListContactChannels(ctx, withToken(&crmv1.ListContactChannelsRequest{
		ContactId: contact.Msg.GetContact().GetId(),
	}, token))
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	for _, ch := range res.Msg.GetChannels() {
		if ch.GetKind() == "email" {
			continue
		}
		if ch.GetReachable() || ch.GetMayMessage() {
			t.Errorf("%s = %+v, want refused without a country code", ch.GetKind(), ch)
		}
		if ch.GetUnreachableReason() == "" {
			t.Errorf("%s gives no reason; the user cannot fix what they are not told", ch.GetKind())
		}
	}

	// And the ledger refuses the same ambiguity, so no row can be written
	// under a key the gate would never look up.
	_, err = client.RecordMessagingConsent(ctx, withToken(&crmv1.RecordMessagingConsentRequest{
		ChannelKind: "sms", Address: "5550100000", State: "withdrawn",
	}, token))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("recording consent for a local number: %v (code %v), want InvalidArgument",
			err, connect.CodeOf(err))
	}
}
