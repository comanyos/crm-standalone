package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/secrets"
)

const testSealingKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

type providerFixture struct {
	mu             sync.Mutex
	revoked        bool
	denyBusinesses bool
	omitPermission bool
}

func (p *providerFixture) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/oauth/access_token") && r.URL.Query().Get("grant_type") == "fb_exchange_token":
		_, _ = w.Write([]byte(`{"access_token":"long-user-token","expires_in":3600}`))
	case strings.HasSuffix(r.URL.Path, "/oauth/access_token"):
		_, _ = w.Write([]byte(`{"access_token":"short-user-token","expires_in":60}`))
	case strings.HasSuffix(r.URL.Path, "/me/permissions") && r.Method == http.MethodDelete:
		p.revoked = true
		_, _ = w.Write([]byte(`{"success":true}`))
	case strings.HasSuffix(r.URL.Path, "/me/permissions"):
		rows := make([]map[string]string, 0, len(Permissions()))
		permissions := Permissions()
		if p.omitPermission {
			permissions = permissions[:len(permissions)-1]
		}
		for _, permission := range permissions {
			rows = append(rows, map[string]string{"permission": permission, "status": "granted"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
	case strings.HasSuffix(r.URL.Path, "/me/businesses"):
		if p.denyBusinesses {
			http.Error(w, `{"error":{"message":"expired credential with private details"}}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"business-1","name":"Savvy Business"}]}`))
	case strings.HasSuffix(r.URL.Path, "/me"):
		_, _ = w.Write([]byte(`{"id":"user-1","name":"Meta Owner"}`))
	case strings.HasSuffix(r.URL.Path, "/business-1/owned_pages"):
		_, _ = w.Write([]byte(`{"data":[{"id":"page-1","name":"Savvy Page","access_token":"page-token"}]}`))
	case strings.HasSuffix(r.URL.Path, "/page-1/leadgen_forms"):
		_, _ = w.Write([]byte(`{"data":[{"id":"form-1","name":"Book a demo","status":"ACTIVE"}]}`))
	case strings.HasSuffix(r.URL.Path, "/page-1/subscribed_apps"):
		_, _ = w.Write([]byte(`{"success":true}`))
	case strings.HasSuffix(r.URL.Path, "/form-1/leads"):
		_, _ = w.Write([]byte(`{"data":[
			{"id":"history-1","created_time":"2026-09-10T10:00:00Z","ad_id":"ad-1","form_id":"form-1","platform":"instagram","field_data":[{"name":"full_name","values":["Alex Rivera"]},{"name":"phone_number","values":["+1 (555) 010-0000"]}]},
			{"id":"history-1","created_time":"2026-09-10T10:00:00Z","ad_id":"ad-1","form_id":"form-1","platform":"instagram","field_data":[{"name":"full_name","values":["Alex Rivera"]},{"name":"phone_number","values":["+15550100000"]}]},
			{"id":"invalid-history","form_id":"form-1","platform":"facebook","field_data":[]}
		]}`))
	case strings.HasSuffix(r.URL.Path, "/lead-live"):
		_, _ = w.Write([]byte(`{"id":"lead-live","created_time":"2026-09-10T11:00:00Z","ad_id":"ad-2","form_id":"form-1","platform":"facebook","field_data":[{"name":"first_name","values":["Sam"]},{"name":"last_name","values":["Buyer"]},{"name":"email","values":["SAM@EXAMPLE.COM"]},{"name":"phone_number","values":["+15550100001"]}]}`))
	case strings.HasSuffix(r.URL.Path, "/ad-1"):
		_, _ = w.Write([]byte(`{"id":"ad-1","name":"Instagram Demo Ad","adset":{"id":"set-1","name":"Warm prospects"},"campaign":{"id":"campaign-1","name":"September demos"}}`))
	case strings.HasSuffix(r.URL.Path, "/ad-2"):
		_, _ = w.Write([]byte(`{"id":"ad-2","name":"Facebook Demo Ad","adset":{"id":"set-2","name":"Retargeting"},"campaign":{"id":"campaign-2","name":"Facebook demos"}}`))
	default:
		http.NotFound(w, r)
	}
}

func serviceFixture(t *testing.T) (*Service, *domain.MemStore, *providerFixture, *httptest.Server) {
	t.Helper()
	provider := &providerFixture{}
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	client, err := NewClient(Config{
		AppID: "app-1", AppSecret: "app-secret", RedirectURL: "https://crm.example/meta/callback",
		GraphBaseURL: server.URL, DialogBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	sealer, err := secrets.New(testSealingKey)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	store := domain.NewMemStore()
	service := NewService(store, client, sealer, "https://crm.example")
	service.now = func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }
	return service, store, provider, server
}

func authorizeFixture(t *testing.T, service *Service, scope domain.Scope) *domain.MetaConnection {
	t.Helper()
	authorizationURL, err := service.Start(context.Background(), scope, "owner-1", "/settings/integrations")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorizationURL)
	result := service.Complete(context.Background(), parsed.Query().Get("state"), "oauth-code", "")
	if !result.Connected || result.AccountName != "Meta Owner" {
		t.Fatalf("callback = %+v", result)
	}
	connection, err := service.Connection(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func TestCompleteMetaLeadAdsJourney(t *testing.T) {
	service, store, provider, server := serviceFixture(t)
	defer server.Close()
	ctx := context.Background()
	scope := domain.Scope{TenantID: "tenant-meta", OrgID: "org-meta"}

	pending := authorizeFixture(t, service, scope)
	if pending.Status != domain.MetaStatusSelectionRequired || len(pending.UserTokenCiphertext) == 0 ||
		strings.Contains(string(pending.UserTokenCiphertext), "long-user-token") {
		t.Fatalf("OAuth credential was not sealed safely: %+v", pending.View())
	}
	businesses, err := service.Businesses(ctx, scope)
	if err != nil || len(businesses) != 1 || businesses[0].ID != "business-1" {
		t.Fatalf("businesses = %+v, %v", businesses, err)
	}
	pages, err := service.Pages(ctx, scope, "business-1")
	if err != nil || len(pages) != 1 || pages[0].ID != "page-1" {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	forms, err := service.Forms(ctx, scope, "business-1", "page-1")
	if err != nil || len(forms) != 1 || forms[0].ID != "form-1" {
		t.Fatalf("forms = %+v, %v", forms, err)
	}
	connected, err := service.SelectForm(ctx, scope, "business-1", "page-1", "form-1")
	if err != nil {
		t.Fatal(err)
	}
	if connected.Status != domain.MetaStatusConnected || connected.BusinessName != "Savvy Business" ||
		connected.PageName != "Savvy Page" || connected.FormName != "Book a demo" ||
		strings.Contains(string(connected.PageTokenCiphertext), "page-token") {
		t.Fatalf("connected record = %+v", connected.View())
	}

	result, err := service.Import(ctx, scope, 500)
	if err != nil {
		t.Fatal(err)
	}
	if result.Examined != 3 || result.Created != 1 || result.Duplicates != 1 || result.Rejected != 1 {
		t.Fatalf("import = %+v", result)
	}
	leads, _, err := store.ListLeads(ctx, scope, domain.Page{Size: 20})
	if err != nil || len(leads) != 1 {
		t.Fatalf("history leads = %+v, %v", leads, err)
	}
	if leads[0].FirstName != "Alex" || leads[0].LastName != "Rivera" ||
		leads[0].Phone != "+15550100000" || leads[0].Source != "Instagram Lead Ads" {
		t.Fatalf("mapped history lead = %+v", leads[0])
	}
	attribution, err := store.GetMetaLeadAttribution(ctx, scope, leads[0].ID)
	if err != nil || attribution.CampaignName != "September demos" || attribution.AdSetName != "Warm prospects" {
		t.Fatalf("attribution = %+v, %v", attribution, err)
	}

	refs, err := store.ResolveMetaConnections(ctx, "page-1", "form-1")
	if err != nil || len(refs) != 1 || refs[0].BusinessID != "business-1" {
		t.Fatalf("webhook route = %+v, %v", refs, err)
	}
	created, err := service.ProcessWebhookLead(ctx, refs[0], "lead-live", `{"leadgen_id":"lead-live"}`)
	if err != nil || !created {
		t.Fatalf("first webhook: created=%v err=%v", created, err)
	}
	created, err = service.ProcessWebhookLead(ctx, refs[0], "lead-live", `{"leadgen_id":"lead-live"}`)
	if err != nil || created {
		t.Fatalf("retried webhook: created=%v err=%v", created, err)
	}
	leads, _, _ = store.ListLeads(ctx, scope, domain.Page{Size: 20})
	if len(leads) != 2 {
		t.Fatalf("webhook retry produced %d total leads, want 2", len(leads))
	}
	latest, _ := service.Connection(ctx, scope)
	if latest.LastWebhookAt.IsZero() {
		t.Fatal("successful live webhook did not update connection health")
	}

	if _, err := service.Disconnect(ctx, scope, false); err != nil {
		t.Fatal(err)
	}
	disconnected, _ := service.Connection(ctx, scope)
	if disconnected.Status != domain.MetaStatusDisconnected || len(disconnected.UserTokenCiphertext) != 0 ||
		len(disconnected.PageTokenCiphertext) != 0 || !provider.revoked {
		t.Fatalf("disconnect = %+v revoked=%v", disconnected.View(), provider.revoked)
	}
}

func TestAuthorizationFailureErasesTokensAndRequiresReconnect(t *testing.T) {
	service, _, provider, server := serviceFixture(t)
	defer server.Close()
	scope := domain.Scope{TenantID: "tenant-expired", OrgID: "org-expired"}
	authorizeFixture(t, service, scope)
	provider.mu.Lock()
	provider.denyBusinesses = true
	provider.mu.Unlock()

	_, err := service.Businesses(context.Background(), scope)
	if err == nil || !IsAuthorizationError(err) {
		t.Fatalf("provider authorization failure = %v", err)
	}
	connection, err := service.Connection(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != domain.MetaStatusReauthorizationRequired ||
		len(connection.UserTokenCiphertext) != 0 || len(connection.PageTokenCiphertext) != 0 ||
		connection.LastError != ReasonTokenUnavailable {
		t.Fatalf("reauthorization state = %+v", connection.View())
	}
}

func TestOAuthStateIsSingleUse(t *testing.T) {
	service, _, _, server := serviceFixture(t)
	defer server.Close()
	scope := domain.Scope{TenantID: "tenant-state", OrgID: "org-state"}
	authorizationURL, err := service.Start(context.Background(), scope, "owner-1", "/settings/integrations")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorizationURL)
	state := parsed.Query().Get("state")
	if first := service.Complete(context.Background(), state, "oauth-code", ""); !first.Connected {
		t.Fatalf("first callback = %+v", first)
	}
	if replay := service.Complete(context.Background(), state, "oauth-code", ""); replay.Reason != ReasonInvalidState {
		t.Fatalf("replayed callback = %+v", replay)
	}
}

func TestMissingPermissionRevokesGrantAndStoresNothing(t *testing.T) {
	service, store, provider, server := serviceFixture(t)
	defer server.Close()
	provider.mu.Lock()
	provider.omitPermission = true
	provider.mu.Unlock()
	scope := domain.Scope{TenantID: "tenant-permission", OrgID: "org-permission"}
	authorizationURL, err := service.Start(context.Background(), scope, "owner-1", "/settings/integrations")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorizationURL)
	result := service.Complete(context.Background(), parsed.Query().Get("state"), "oauth-code", "")
	if result.Reason != ReasonPermissionMissing || result.Connected {
		t.Fatalf("callback = %+v", result)
	}
	provider.mu.Lock()
	revoked := provider.revoked
	provider.mu.Unlock()
	if !revoked {
		t.Fatal("incomplete Meta grant was not revoked")
	}
	if _, err := store.GetMetaConnection(context.Background(), scope); err != domain.ErrNotFound {
		t.Fatalf("incomplete grant persisted a connection: %v", err)
	}
}

func TestExpiredGrantIsErasedBeforeItCanBeUsed(t *testing.T) {
	service, store, _, server := serviceFixture(t)
	defer server.Close()
	scope := domain.Scope{TenantID: "tenant-expiry", OrgID: "org-expiry"}
	connection := authorizeFixture(t, service, scope)
	connection.AccessTokenExpiresAt = service.now().Add(-time.Minute)
	if _, err := store.SaveMetaConnection(context.Background(), scope, connection); err != nil {
		t.Fatal(err)
	}

	got, err := service.Connection(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.MetaStatusReauthorizationRequired || len(got.UserTokenCiphertext) != 0 ||
		got.LastError != ReasonTokenUnavailable {
		t.Fatalf("expired connection = %+v", got.View())
	}
}
