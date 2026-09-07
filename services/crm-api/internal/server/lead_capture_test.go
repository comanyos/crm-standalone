package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/leadcapture"
	"github.com/santoshguru/crm/internal/secrets"
)

// The inbound lead-capture route, end to end over real HTTP
// (task_crm.md §1, docs/LEAD_CAPTURE.md).
//
// This is the only unauthenticated write surface in the service, so these
// tests are mostly about what it refuses and what it declines to reveal. The
// happy path is one test; the rest are the ways in.

func captureKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

type captureHarness struct {
	server *httptest.Server
	store  domain.Store
	scope  domain.Scope
	token  string
	secret string
	// endpointID is the seeded endpoint, for reading its delivery log back.
	endpointID string
}

// newCaptureHarness stands up a real server with a real store and one active
// endpoint, sealed the way the running service seals it.
func newCaptureHarness(t *testing.T) captureHarness {
	t.Helper()
	ctx := context.Background()

	key := captureKey(t)
	sealer, err := secrets.New(key)
	if err != nil {
		t.Fatalf("build sealer: %v", err)
	}
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "tenant-capture", OrgID: "tenant-capture"}

	token, err := domain.NewCaptureToken()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := domain.NewCaptureSecret()
	if err != nil {
		t.Fatal(err)
	}
	// Sealed against a placeholder id first, then re-sealed once the store
	// assigns the real one — the binding names the endpoint, so it cannot be
	// built before the endpoint exists. The service does the same.
	sealed, err := sealer.Seal([]byte(secret), leadcapture.AssociatedData(scope.TenantID, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateLeadCaptureEndpoint(ctx, scope, &domain.LeadCaptureEndpoint{
		Name: "Website form", Token: token, SourceKind: domain.SourceKindGeneric,
		SecretCiphertext: sealed, Status: domain.CaptureActive,
		FieldMapping: map[string]string{
			"email": "contact_email", "company": "company", "utm_source": "source",
		},
		DefaultOwnerID: "owner-1",
	})
	if err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	resealed, err := sealer.Seal([]byte(secret), leadcapture.AssociatedData(scope.TenantID, endpoint.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateLeadCaptureSecret(ctx, scope, endpoint.ID, resealed); err != nil {
		t.Fatalf("seal endpoint secret: %v", err)
	}

	srv, err := New(config.Config{LeadCaptureSecretKey: key}, store, nil)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(srv.Close)
	http := httptest.NewServer(srv.Handler())
	t.Cleanup(http.Close)

	return captureHarness{
		server: http, store: store, scope: scope,
		token: token, secret: secret, endpointID: endpoint.ID,
	}
}

// post sends one delivery. signature is used verbatim, so a test can send a
// wrong one.
func (h captureHarness) post(t *testing.T, token string, body []byte, signature string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		h.server.URL+leadCapturePrefix+token, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set(leadcapture.SignatureHeader, signature)
	}
	res, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (h captureHarness) signed(t *testing.T, body []byte) *http.Response {
	t.Helper()
	return h.post(t, h.token, body, domain.SignCapturePayload(h.secret, body))
}

func (h captureHarness) leads(t *testing.T) []*domain.Lead {
	t.Helper()
	leads, _, err := h.store.ListLeads(context.Background(), h.scope, domain.Page{})
	if err != nil {
		t.Fatalf("list leads: %v", err)
	}
	return leads
}

func TestLeadCaptureCreatesALead(t *testing.T) {
	h := newCaptureHarness(t)
	body := []byte(`{"email":"Alex@Example.com","company":"Example Ltd","utm_source":"google-ads"}`)

	res := h.signed(t, body)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}

	leads := h.leads(t)
	if len(leads) != 1 {
		t.Fatalf("created %d leads, want 1", len(leads))
	}
	lead := leads[0]
	if lead.ContactEmail != "alex@example.com" {
		t.Errorf("contact email = %q, want it lower-cased", lead.ContactEmail)
	}
	if lead.Company != "Example Ltd" || lead.Source != "google-ads" {
		t.Errorf("mapped lead = %+v", lead)
	}
	if lead.OwnerID != "owner-1" {
		t.Errorf("owner = %q, want the endpoint's default", lead.OwnerID)
	}

	// The whole design: a captured lead is an ordinary lead, so the outbox
	// event the workflow engine reads is there without anything special.
	events, err := h.store.OutboxEventsFor(context.Background(), h.scope, lead.ID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var sawCreated bool
	for _, event := range events {
		if event.Envelope.Type == domain.EventLeadCreated {
			sawCreated = true
		}
	}
	if !sawCreated {
		t.Error("a captured lead emitted no lead.created event; automations would not fire on it")
	}
}

// TestLeadCaptureRefusesAnUnsignedOrWronglySignedDelivery is the one that
// matters most: without it the URL is an open lead-injection hole for anybody
// who learns it.
func TestLeadCaptureRefusesAnUnsignedOrWronglySignedDelivery(t *testing.T) {
	body := []byte(`{"email":"intruder@example.com","company":"Injected"}`)

	cases := []struct {
		name      string
		signature string
	}{
		{"no signature at all", ""},
		{"empty signature", " "},
		{"a signature for different content", domain.SignCapturePayload("wrong-secret", body)},
		{"the right hash under the wrong scheme", strings.TrimPrefix(
			domain.SignCapturePayload("wrong-secret", body), "sha256=")},
		{"garbage", "sha256=deadbeef"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newCaptureHarness(t)
			res := h.post(t, h.token, body, c.signature)

			// Still 202: a provider that gets an error retries, and retrying
			// a bad signature forever helps nobody. The tenant sees the
			// rejection in the delivery log.
			if res.StatusCode != http.StatusAccepted {
				t.Errorf("status = %d, want 202", res.StatusCode)
			}
			if leads := h.leads(t); len(leads) != 0 {
				t.Fatalf("an unsigned delivery created %d leads", len(leads))
			}

			deliveries, err := h.store.ListCaptureDeliveries(context.Background(), h.scope, h.endpointID, 10)
			if err != nil {
				t.Fatalf("list deliveries: %v", err)
			}
			if len(deliveries) != 1 || deliveries[0].Outcome != domain.DeliveryRejected {
				t.Fatalf("delivery log = %+v, want one rejection", deliveries)
			}
			if deliveries[0].Reason != domain.CaptureReasonBadSignature {
				t.Errorf("reason = %q, want %q", deliveries[0].Reason, domain.CaptureReasonBadSignature)
			}
		})
	}
}

// TestLeadCaptureIsNotAnEndpointOracle checks the shape of the refusal rather
// than only its effect. An unknown token and a bad signature must be
// indistinguishable, or the response can be used to test whether a token is
// real — and a token that is confirmed real is worth attacking.
func TestLeadCaptureIsNotAnEndpointOracle(t *testing.T) {
	h := newCaptureHarness(t)
	body := []byte(`{"email":"probe@example.com"}`)

	unknown := h.post(t, strings.Repeat("a", 64), body, domain.SignCapturePayload(h.secret, body))
	badSig := h.post(t, h.token, body, "sha256=deadbeef")

	if unknown.StatusCode != badSig.StatusCode {
		t.Errorf("an unknown token answers %d and a bad signature %d; the difference is an oracle",
			unknown.StatusCode, badSig.StatusCode)
	}
	for _, res := range []*http.Response{unknown, badSig} {
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(res.Body); err != nil {
			t.Fatal(err)
		}
		if buf.Len() != 0 {
			t.Errorf("the response carries a body (%q); it should reveal nothing", buf.String())
		}
	}
}

// TestLeadCaptureIsIdempotent covers the ordinary case, not an attack: every
// webhook provider retries.
func TestLeadCaptureIsIdempotent(t *testing.T) {
	h := newCaptureHarness(t)
	body := []byte(`{"id":"evt-1","email":"alex@example.com","company":"Example Ltd"}`)

	for i := 0; i < 3; i++ {
		if res := h.signed(t, body); res.StatusCode != http.StatusAccepted {
			t.Fatalf("delivery %d: status %d, want 202", i, res.StatusCode)
		}
	}

	leads := h.leads(t)
	if len(leads) != 1 {
		t.Fatalf("three deliveries of one event created %d leads, want 1", len(leads))
	}
	deliveries, err := h.store.ListCaptureDeliveries(context.Background(), h.scope, h.endpointID, 10)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Errorf("the delivery log has %d rows, want 1", len(deliveries))
	}
	// The delivery names the lead it became. The row is claimed before the
	// lead exists, so this is a second write — and without it the log says
	// "created" without saying what, which is the first question anybody
	// reading it has. Found by a live delivery against the running stack,
	// where the column came back empty.
	if deliveries[0].LeadID != leads[0].ID {
		t.Errorf("delivery lead_id = %q, want the lead it created (%q)",
			deliveries[0].LeadID, leads[0].ID)
	}
}

func TestLeadCaptureRejectsAMisconfiguredMapping(t *testing.T) {
	h := newCaptureHarness(t)
	// Valid JSON, valid signature, nothing the mapping can use.
	body := []byte(`{"unmapped":"value","another":"thing"}`)

	if res := h.signed(t, body); res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}
	if leads := h.leads(t); len(leads) != 0 {
		t.Error("a payload matching no mapped field created a blank lead; " +
			"that hides the misconfiguration behind a row somebody has to clean up")
	}
	deliveries, err := h.store.ListCaptureDeliveries(context.Background(), h.scope, h.endpointID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Reason != domain.CaptureReasonNoLeadFields {
		t.Errorf("delivery log = %+v, want one no_usable_fields rejection", deliveries)
	}
	// The payload is kept, because "the lead came in but the field is
	// missing" is unanswerable without it.
	if !strings.Contains(deliveries[0].RawPayload, "unmapped") {
		t.Error("the delivery did not keep the payload it could not map")
	}
}

func TestLeadCaptureRefusesADisabledEndpoint(t *testing.T) {
	h := newCaptureHarness(t)
	ctx := context.Background()

	endpoint, err := h.store.GetLeadCaptureEndpoint(ctx, h.scope, h.endpointID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Status = domain.CaptureDisabled
	if _, err := h.store.UpdateLeadCaptureEndpoint(ctx, h.scope, endpoint); err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}

	body := []byte(`{"email":"alex@example.com","company":"Example Ltd"}`)
	if res := h.signed(t, body); res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}
	if leads := h.leads(t); len(leads) != 0 {
		t.Error("a disabled endpoint still created a lead")
	}
}

func TestLeadCaptureBoundsTheBody(t *testing.T) {
	h := newCaptureHarness(t)
	huge := make([]byte, domain.MaxCaptureBodyBytes+1024)
	for i := range huge {
		huge[i] = 'x'
	}

	res := h.post(t, h.token, huge, domain.SignCapturePayload(h.secret, huge))
	// The one case worth a distinct status: a provider sending too much needs
	// to know, and the limit is public in docs/LEAD_CAPTURE.md.
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", res.StatusCode)
	}
	if leads := h.leads(t); len(leads) != 0 {
		t.Error("an oversized body created a lead")
	}
}

func TestLeadCaptureRefusesNonPost(t *testing.T) {
	h := newCaptureHarness(t)
	res, err := h.server.Client().Get(h.server.URL + leadCapturePrefix + h.token)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", res.StatusCode)
	}
}

// TestLeadCaptureRouteIsAbsentWithoutAKey is the "off is a real state" case.
// A deployment that has not decided to accept inbound leads must not have a
// URL that does — and it must not have one that accepts unsigned posts
// either, which is what a degraded mode would mean.
func TestLeadCaptureRouteIsAbsentWithoutAKey(t *testing.T) {
	srv, err := New(config.Config{}, domain.NewMemStore(), nil)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(srv.Close)
	http := httptest.NewServer(srv.Handler())
	t.Cleanup(http.Close)

	res, err := http.Client().Post(http.URL+leadCapturePrefix+strings.Repeat("a", 64),
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 404 {
		t.Errorf("status = %d with no sealing key configured, want 404", res.StatusCode)
	}
}

func TestLeadCaptureRefusesAMalformedKey(t *testing.T) {
	if _, err := New(config.Config{LeadCaptureSecretKey: "not-a-key"}, domain.NewMemStore(), nil); err == nil {
		t.Error("a server started with a malformed lead-capture key; " +
			"silently disabling a feature somebody configured is worse than refusing to start")
	}
}

// TestLeadCaptureSecretIsBoundToItsTenant proves the sealing binding at the
// level that matters: a ciphertext copied into another tenant's row must fail
// to open rather than yielding a working signing secret.
func TestLeadCaptureSecretIsBoundToItsTenant(t *testing.T) {
	h := newCaptureHarness(t)
	ctx := context.Background()

	endpoint, err := h.store.GetLeadCaptureEndpoint(ctx, h.scope, h.endpointID)
	if err != nil {
		t.Fatal(err)
	}

	other := domain.Scope{TenantID: "tenant-thief", OrgID: "tenant-thief"}
	stolenToken, err := domain.NewCaptureToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateLeadCaptureEndpoint(ctx, other, &domain.LeadCaptureEndpoint{
		Name: "Stolen", Token: stolenToken, SourceKind: domain.SourceKindGeneric,
		// The victim's sealed secret, verbatim.
		SecretCiphertext: endpoint.SecretCiphertext, Status: domain.CaptureActive,
		FieldMapping: map[string]string{"email": "contact_email"},
	}); err != nil {
		t.Fatalf("seed the copied endpoint: %v", err)
	}

	body := []byte(`{"email":"victim@example.com"}`)
	res := h.post(t, stolenToken, body, domain.SignCapturePayload(h.secret, body))
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}

	leads, _, err := h.store.ListLeads(ctx, other, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 0 {
		t.Error("a sealed secret copied into another tenant still verified a signature")
	}
}

// TestLeadCaptureDeliveriesAreTenantIsolated: one tenant's delivery log names
// the people who filled in its forms.
func TestLeadCaptureDeliveriesAreTenantIsolated(t *testing.T) {
	h := newCaptureHarness(t)
	body := []byte(`{"email":"alex@example.com","company":"Example Ltd"}`)
	if res := h.signed(t, body); res.StatusCode != http.StatusAccepted {
		t.Fatal("seed delivery failed")
	}

	other := domain.Scope{TenantID: "tenant-other", OrgID: "tenant-other"}
	deliveries, err := h.store.ListCaptureDeliveries(context.Background(), other, h.endpointID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(deliveries) != 0 {
		t.Errorf("another tenant read %d deliveries, want 0", len(deliveries))
	}
	if _, err := h.store.GetLeadCaptureEndpoint(context.Background(), other, h.endpointID); err == nil {
		t.Error("another tenant read the endpoint configuration")
	}
}

// TestSignCapturePayloadMatchesTheDocumentedRecipe pins the contract the
// documentation tells integrators to implement. If this drifts, every
// configured caller breaks at once and the error they see is "bad signature".
func TestSignCapturePayloadMatchesTheDocumentedRecipe(t *testing.T) {
	// Computed independently of the implementation, the way an integrator
	// following docs/LEAD_CAPTURE.md would: HMAC-SHA256 of the exact body,
	// lowercase hex, prefixed "sha256=".
	got := domain.SignCapturePayload("secret", []byte(`{"a":1}`))
	if !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("signature = %q, want a sha256= prefix", got)
	}
	hexPart := strings.TrimPrefix(got, "sha256=")
	if len(hexPart) != 64 || strings.ToLower(hexPart) != hexPart {
		t.Errorf("signature body = %q, want 64 lowercase hex characters", hexPart)
	}
	// Deterministic: the same body and secret always sign the same.
	if again := domain.SignCapturePayload("secret", []byte(`{"a":1}`)); again != got {
		t.Error("signing is not deterministic")
	}
	// And sensitive to both inputs.
	if same := domain.SignCapturePayload("secret", []byte(`{"a":2}`)); same == got {
		t.Error("a different body signed identically")
	}
	if same := domain.SignCapturePayload("other", []byte(`{"a":1}`)); same == got {
		t.Error("a different secret signed identically")
	}
}

func TestCaptureDedupKeyPrefersTheProvidersEventID(t *testing.T) {
	body := []byte(`{"id":"evt-1","email":"a@b.com"}`)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	byEvent := domain.CaptureDedupKey(payload, body)
	if !strings.HasPrefix(byEvent, "provider:") {
		t.Errorf("dedup key = %q, want the provider's own id", byEvent)
	}

	// A second post of the same lead whose bytes differ — a retry that
	// re-serialises, or adds a timestamp — must still be the same key.
	other := []byte(`{"id":"evt-1","email":"a@b.com","retry":true}`)
	var otherPayload map[string]any
	if err := json.Unmarshal(other, &otherPayload); err != nil {
		t.Fatal(err)
	}
	if again := domain.CaptureDedupKey(otherPayload, other); again != byEvent {
		t.Errorf("a re-serialised retry keyed as %q, want %q — that is a second lead", again, byEvent)
	}

	// With no event id, byte-identical retries still collapse.
	plain := []byte(`{"email":"a@b.com"}`)
	var plainPayload map[string]any
	if err := json.Unmarshal(plain, &plainPayload); err != nil {
		t.Fatal(err)
	}
	first := domain.CaptureDedupKey(plainPayload, plain)
	if !strings.HasPrefix(first, "body:") {
		t.Errorf("dedup key = %q, want a body hash", first)
	}
	if second := domain.CaptureDedupKey(plainPayload, plain); second != first {
		t.Error("a byte-identical retry produced a different key")
	}
}

func TestLeadFromCaptureRendersNumbersReadably(t *testing.T) {
	endpoint := &domain.LeadCaptureEndpoint{
		Name:         "Form",
		FieldMapping: map[string]string{"score": "score", "company": "company"},
	}
	// JSON numbers arrive as float64. A score of 5 must not become "5.000000"
	// and a company must never be rendered from one at all.
	payload := map[string]any{"score": float64(5), "company": "Example Ltd"}
	lead, ok := domain.LeadFromCapture(endpoint, payload)
	if !ok {
		t.Fatal("nothing usable found in a payload that maps two fields")
	}
	if lead.Score != 5 {
		t.Errorf("score = %d, want 5", lead.Score)
	}
	if lead.Company != "Example Ltd" {
		t.Errorf("company = %q", lead.Company)
	}
	if lead.Source != "capture:Form" {
		t.Errorf("source = %q, want it defaulted to the endpoint — where a lead came "+
			"from is the first thing anybody filters on", lead.Source)
	}
	if _, ok := domain.LeadFromCapture(endpoint, map[string]any{"nothing": "here"}); ok {
		t.Error("an unmappable payload reported itself usable")
	}
	// A form that sends null or false means "not answered", not "false".
	if _, ok := domain.LeadFromCapture(endpoint, map[string]any{"company": nil}); ok {
		t.Error("a null value counted as a usable field")
	}
}

func TestCaptureRateLimitIsPerEndpoint(t *testing.T) {
	h := newCaptureHarness(t)
	// Well past the per-minute allowance; the limiter must start refusing,
	// and refusing must not create leads.
	accepted := 0
	for i := 0; i < 200; i++ {
		body := []byte(fmt.Sprintf(`{"id":"evt-%d","email":"a%d@example.com","company":"C"}`, i, i))
		if res := h.signed(t, body); res.StatusCode == http.StatusAccepted {
			accepted++
		}
	}
	// Every response is 202 by design — the caller learns nothing — so the
	// evidence is in what was actually created.
	if accepted != 200 {
		t.Errorf("%d of 200 answered 202; the route must not distinguish a rate limit", accepted)
	}
	if leads := h.leads(t); len(leads) >= 200 {
		t.Errorf("created %d leads from 200 deliveries; the rate limit did nothing", len(leads))
	}
}
