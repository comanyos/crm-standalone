package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{
		AppID: "app-123", AppSecret: "app-secret", RedirectURL: "https://crm.example/meta/callback",
		GraphBaseURL: server.URL, DialogBaseURL: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestBeginRequestsTheCompleteLeastPrivilegeLeadAdsGrant(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := testClient(t, server)

	authorizationURL, state, expiresAt, err := client.Begin(time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/v25.0/dialog/oauth" || parsed.Query().Get("client_id") != "app-123" ||
		parsed.Query().Get("redirect_uri") != "https://crm.example/meta/callback" {
		t.Fatalf("authorization URL = %s", authorizationURL)
	}
	if len(state) < 40 || parsed.Query().Get("state") != state {
		t.Fatalf("state was not a high-entropy value bound to the URL: %q", state)
	}
	if !expiresAt.Equal(time.Unix(1_700_000_000, 0).UTC().Add(10 * time.Minute)) {
		t.Fatalf("state expiry = %s", expiresAt)
	}
	want := Permissions()
	got := strings.Split(parsed.Query().Get("scope"), ",")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %v, want %v", got, want)
	}
	if strings.Contains(authorizationURL, "app-secret") {
		t.Fatal("authorization URL exposed the Meta app secret")
	}
}

func TestExchangePromotesTheShortTokenAndRedactsProviderErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("code") == "good-code" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "short-token", "expires_in": 60})
			return
		}
		if r.URL.Query().Get("grant_type") == "fb_exchange_token" &&
			r.URL.Query().Get("fb_exchange_token") == "short-token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "long-token", "expires_in": 3600})
			return
		}
		http.Error(w, `{"error":{"message":"provider-private-token-and-user-details"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	client := testClient(t, server)

	token, err := client.Exchange(context.Background(), "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "long-token" || token.ExpiresAt.Before(time.Now().UTC().Add(59*time.Minute)) {
		t.Fatalf("promoted token = %+v", token)
	}
	if requests.Load() != 2 {
		t.Fatalf("exchange made %d calls, want short then long-lived", requests.Load())
	}

	_, err = client.Exchange(context.Background(), "bad-code")
	if err == nil || !IsAuthorizationError(err) {
		t.Fatalf("bad exchange error = %v", err)
	}
	if strings.Contains(err.Error(), "provider-private") {
		t.Fatalf("provider body leaked through error: %v", err)
	}
}

func TestBusinessPageFormCallsKeepTokensOutOfURLs(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("access_token") {
			t.Error("access token was placed in a query string")
		}
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v25.0/me/businesses":
			_, _ = w.Write([]byte(`{"data":[{"id":"business-1","name":"Savvy"}]}`))
		case "/v25.0/business-1/owned_pages":
			_, _ = w.Write([]byte(`{"data":[{"id":"page-1","name":"Savvy Page","access_token":"page-token"}]}`))
		case "/v25.0/page-1/leadgen_forms":
			_, _ = w.Write([]byte(`{"data":[{"id":"form-1","name":"Quote","status":"ACTIVE"}]}`))
		case "/v25.0/page-1/subscribed_apps":
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	ctx := context.Background()

	if rows, err := client.Businesses(ctx, "user-token"); err != nil || len(rows) != 1 {
		t.Fatalf("businesses = %+v, %v", rows, err)
	}
	pages, err := client.Pages(ctx, "business-1", "user-token")
	if err != nil || len(pages) != 1 || pages[0].AccessToken != "page-token" {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	if rows, err := client.Forms(ctx, "page-1", pages[0].AccessToken); err != nil || len(rows) != 1 {
		t.Fatalf("forms = %+v, %v", rows, err)
	}
	if err := client.SubscribePage(ctx, "page-1", pages[0].AccessToken); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /v25.0/me/businesses Bearer user-token",
		"GET /v25.0/business-1/owned_pages Bearer user-token",
		"GET /v25.0/page-1/leadgen_forms Bearer page-token",
		"POST /v25.0/page-1/subscribed_apps Bearer page-token",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("provider calls = %#v, want %#v", seen, want)
	}
}

func TestHistoricalLeadPagingUsesOpaqueCursorsAndHonorsTheLimit(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("after") != "" {
				t.Fatalf("first page query = %v", r.URL.Query())
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}],"paging":{"cursors":{"after":"opaque-cursor"},"next":"present"}}`))
			return
		}
		if r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("after") != "opaque-cursor" {
			t.Fatalf("second page query = %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"3"},{"id":"4"}]}`))
	}))
	defer server.Close()
	client := testClient(t, server)

	rows, err := client.Leads(context.Background(), "form-1", "page-token", 3)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(rows) != 3 || rows[2].ID != "3" {
		t.Fatalf("calls=%d rows=%+v", calls, rows)
	}
}
