package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/config"
	"github.com/santoshguru/crm/internal/domain"
	metaintegration "github.com/santoshguru/crm/internal/integration/meta"
	"github.com/santoshguru/crm/internal/secrets"
)

const metaWebhookTestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func signMetaWebhook(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func metaWebhookFixture(t *testing.T) (*Server, *domain.MemStore, *httptest.Server, domain.Scope) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/lead-ok"):
			_, _ = io.WriteString(w, `{"id":"lead-ok","created_time":"2026-09-10T11:00:00Z","ad_id":"ad-ok","form_id":"form-1","platform":"facebook","field_data":[{"name":"full_name","values":["Alex Buyer"]},{"name":"phone_number","values":["+15550100000"]}]}`)
		case strings.HasSuffix(r.URL.Path, "/lead-invalid"):
			_, _ = io.WriteString(w, `{"id":"lead-invalid","form_id":"form-1","platform":"instagram","field_data":[]}`)
		case strings.HasSuffix(r.URL.Path, "/lead-transient"):
			http.Error(w, "temporary provider failure with private details", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/ad-ok"):
			_, _ = io.WriteString(w, `{"id":"ad-ok","name":"Demo Ad","campaign":{"id":"campaign-1","name":"Demo Campaign"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	client, err := metaintegration.NewClient(metaintegration.Config{
		AppID: "app-id", AppSecret: "app-secret", RedirectURL: "https://crm.example/meta/callback",
		GraphBaseURL: provider.URL, DialogBaseURL: provider.URL,
	}, provider.Client())
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	sealer, err := secrets.New(metaWebhookTestKey)
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "tenant-webhook", OrgID: "org-webhook"}
	pageToken, err := sealer.Seal([]byte("page-token"), []byte("meta:page:"+scope.TenantID+":connection-1"))
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	userToken, err := sealer.Seal([]byte("user-token"), []byte("meta:user:"+scope.TenantID+":connection-1"))
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	_, err = store.SaveMetaConnection(context.Background(), scope, &domain.MetaConnection{
		ID: "connection-1", Status: domain.MetaStatusConnected,
		UserTokenCiphertext: userToken, PageTokenCiphertext: pageToken,
		BusinessID: "business-1", BusinessName: "Savvy Business",
		PageID: "page-1", PageName: "Savvy Page", FormID: "form-1", FormName: "Demo Form",
	})
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	service := metaintegration.NewService(store, client, sealer, "https://crm.example")
	return &Server{
		cfg:   config.Config{MetaAppSecret: "app-secret", MetaWebhookVerifyToken: "verify-token-long-enough"},
		store: store, meta: service,
	}, store, provider, scope
}

func metaLeadPayload(leadID, pageID, formID string) []byte {
	return []byte(`{"object":"page","entry":[{"id":"` + pageID + `","changes":[{"field":"leadgen","value":{"leadgen_id":"` + leadID + `","page_id":"` + pageID + `","form_id":"` + formID + `"}}]}]}`)
}

func deliverMetaWebhook(t *testing.T, server *Server, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, metaWebhookPath, strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", signature)
	res := httptest.NewRecorder()
	server.metaWebhookHandler().ServeHTTP(res, req)
	return res
}

func TestMetaWebhookChallengeRequiresTheDeploymentToken(t *testing.T) {
	server, _, provider, _ := metaWebhookFixture(t)
	defer provider.Close()

	valid := httptest.NewRequest(http.MethodGet, metaWebhookPath+"?hub.mode=subscribe&hub.verify_token=verify-token-long-enough&hub.challenge=challenge-123", nil)
	validResult := httptest.NewRecorder()
	server.metaWebhookHandler().ServeHTTP(validResult, valid)
	if validResult.Code != http.StatusOK || validResult.Body.String() != "challenge-123" {
		t.Fatalf("valid challenge: status=%d body=%q", validResult.Code, validResult.Body.String())
	}

	wrong := httptest.NewRequest(http.MethodGet, metaWebhookPath+"?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=challenge-123", nil)
	wrongResult := httptest.NewRecorder()
	server.metaWebhookHandler().ServeHTTP(wrongResult, wrong)
	if wrongResult.Code != http.StatusForbidden {
		t.Fatalf("wrong token status = %d", wrongResult.Code)
	}
}

func TestMetaWebhookVerifiesSignatureCreatesOnceAndStoresAttribution(t *testing.T) {
	server, store, provider, scope := metaWebhookFixture(t)
	defer provider.Close()
	body := metaLeadPayload("lead-ok", "page-1", "form-1")

	invalid := deliverMetaWebhook(t, server, body, "sha256="+strings.Repeat("0", 64))
	if invalid.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d", invalid.Code)
	}
	leads, _, _ := store.ListLeads(context.Background(), scope, domain.Page{Size: 20})
	if len(leads) != 0 {
		t.Fatal("an invalidly signed webhook created a lead")
	}

	signature := signMetaWebhook(body, "app-secret")
	first := deliverMetaWebhook(t, server, body, signature)
	if first.Code != http.StatusOK || first.Body.String() != "EVENT_RECEIVED" {
		t.Fatalf("first delivery: status=%d body=%q", first.Code, first.Body.String())
	}
	second := deliverMetaWebhook(t, server, body, signature)
	if second.Code != http.StatusOK {
		t.Fatalf("retry status = %d", second.Code)
	}
	leads, _, err := store.ListLeads(context.Background(), scope, domain.Page{Size: 20})
	if err != nil || len(leads) != 1 {
		t.Fatalf("leads after retry = %+v, %v", leads, err)
	}
	if leads[0].FirstName != "Alex" || leads[0].LastName != "Buyer" || leads[0].Phone != "+15550100000" {
		t.Fatalf("captured lead = %+v", leads[0])
	}
	attribution, err := store.GetMetaLeadAttribution(context.Background(), scope, leads[0].ID)
	if err != nil || attribution.ProviderLeadID != "lead-ok" || attribution.CampaignName != "Demo Campaign" {
		t.Fatalf("attribution = %+v, %v", attribution, err)
	}
}

func TestMetaWebhookRetriesOnlyTransientFailures(t *testing.T) {
	server, store, provider, scope := metaWebhookFixture(t)
	defer provider.Close()

	transientBody := metaLeadPayload("lead-transient", "page-1", "form-1")
	transient := deliverMetaWebhook(t, server, transientBody, signMetaWebhook(transientBody, "app-secret"))
	if transient.Code != http.StatusServiceUnavailable {
		t.Fatalf("transient provider failure status = %d, want 503", transient.Code)
	}

	invalidBody := metaLeadPayload("lead-invalid", "page-1", "form-1")
	invalid := deliverMetaWebhook(t, server, invalidBody, signMetaWebhook(invalidBody, "app-secret"))
	if invalid.Code != http.StatusOK {
		t.Fatalf("permanently invalid lead status = %d, want acknowledgement", invalid.Code)
	}
	leads, _, _ := store.ListLeads(context.Background(), scope, domain.Page{Size: 20})
	if len(leads) != 0 {
		t.Fatalf("invalid or transient deliveries created %d leads", len(leads))
	}
}

func TestValidMetaSignatureRejectsMalformedHeaders(t *testing.T) {
	body := []byte("payload")
	if !validMetaSignature(body, signMetaWebhook(body, "secret"), "secret") {
		t.Fatal("valid HMAC was rejected")
	}
	for _, header := range []string{"", "sha1=abc", "sha256=not-hex", "sha256=" + strings.Repeat("0", 62)} {
		if validMetaSignature(body, header, "secret") {
			t.Fatalf("malformed signature %q was accepted", header)
		}
	}
}
