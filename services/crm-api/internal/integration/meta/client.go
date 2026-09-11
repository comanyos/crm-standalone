// Package meta implements the provider-facing Meta Graph API contract used
// for Facebook and Instagram Instant Form leads. It contains no tenant logic
// and never persists a credential.
package meta

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultGraphVersion = "v25.0"

var requestedPermissions = []string{
	"pages_show_list",
	"pages_read_engagement",
	"pages_manage_metadata",
	"leads_retrieval",
	"business_management",
	"ads_read",
}

type Config struct {
	AppID         string
	AppSecret     string
	RedirectURL   string
	GraphVersion  string
	GraphBaseURL  string
	DialogBaseURL string
}

type Client struct {
	appID, appSecret, redirectURL, version string
	graphBase, dialogBase                  string
	http                                   *http.Client
}

func NewClient(cfg Config, client *http.Client) (*Client, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" || strings.TrimSpace(cfg.RedirectURL) == "" {
		return nil, errors.New("meta: app id, app secret and redirect URL are required")
	}
	version := strings.TrimSpace(cfg.GraphVersion)
	if version == "" {
		version = defaultGraphVersion
	}
	if !strings.HasPrefix(version, "v") || strings.ContainsAny(version, "/?# ") {
		return nil, fmt.Errorf("meta: invalid Graph API version %q", version)
	}
	graphBase := strings.TrimRight(cfg.GraphBaseURL, "/")
	if graphBase == "" {
		graphBase = "https://graph.facebook.com"
	}
	dialogBase := strings.TrimRight(cfg.DialogBaseURL, "/")
	if dialogBase == "" {
		dialogBase = "https://www.facebook.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	return &Client{appID: cfg.AppID, appSecret: cfg.AppSecret, redirectURL: cfg.RedirectURL,
		version: version, graphBase: graphBase, dialogBase: dialogBase, http: client}, nil
}

func Permissions() []string { return append([]string(nil), requestedPermissions...) }

func (c *Client) Begin(now time.Time) (string, string, time.Time, error) {
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", time.Time{}, fmt.Errorf("meta: generate OAuth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	params := url.Values{
		"client_id": {c.appID}, "redirect_uri": {c.redirectURL},
		"response_type": {"code"}, "state": {state},
		"scope": {strings.Join(requestedPermissions, ",")},
	}
	return c.dialogBase + "/" + c.version + "/dialog/oauth?" + params.Encode(), state, now.UTC().Add(10 * time.Minute), nil
}

type Token struct {
	AccessToken string
	ExpiresAt   time.Time
}

type providerError struct{ status int }

func (e providerError) Error() string {
	return fmt.Sprintf("meta: provider returned HTTP %d", e.status)
}
func IsAuthorizationError(err error) bool {
	var p providerError
	return errors.As(err, &p) && (p.status == http.StatusUnauthorized || p.status == http.StatusBadRequest || p.status == http.StatusForbidden)
}

func (c *Client) Exchange(ctx context.Context, code string) (Token, error) {
	params := url.Values{"client_id": {c.appID}, "client_secret": {c.appSecret},
		"redirect_uri": {c.redirectURL}, "code": {strings.TrimSpace(code)}}
	var short struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.oauthJSON(ctx, "/oauth/access_token", params, &short); err != nil {
		return Token{}, err
	}
	if short.AccessToken == "" {
		return Token{}, errors.New("meta: token response had no access token")
	}

	longParams := url.Values{"grant_type": {"fb_exchange_token"}, "client_id": {c.appID},
		"client_secret": {c.appSecret}, "fb_exchange_token": {short.AccessToken}}
	var long struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.oauthJSON(ctx, "/oauth/access_token", longParams, &long); err != nil {
		return Token{}, err
	}
	if long.AccessToken == "" {
		return Token{}, errors.New("meta: long-lived token response had no access token")
	}
	expiresIn := long.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = short.ExpiresIn
	}
	var expiry time.Time
	if expiresIn > 0 {
		expiry = time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	}
	return Token{AccessToken: long.AccessToken, ExpiresAt: expiry}, nil
}

func (c *Client) oauthJSON(ctx context.Context, path string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.graphBase+"/"+c.version+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	return c.decode(req, out)
}

type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Profile(ctx context.Context, token string) (Profile, error) {
	var out Profile
	err := c.graphGET(ctx, "/me", url.Values{"fields": {"id,name"}}, token, &out)
	return out, err
}

func (c *Client) GrantedPermissions(ctx context.Context, token string) ([]string, error) {
	var wire struct {
		Data []struct{ Permission, Status string } `json:"data"`
	}
	if err := c.graphGET(ctx, "/me/permissions", nil, token, &wire); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(wire.Data))
	for _, p := range wire.Data {
		if p.Status == "granted" {
			out = append(out, p.Permission)
		}
	}
	return out, nil
}

type Page struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"`
}

type Business struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Businesses(ctx context.Context, userToken string) ([]Business, error) {
	var wire struct {
		Data []Business `json:"data"`
	}
	err := c.graphGET(ctx, "/me/businesses", url.Values{"fields": {"id,name"}, "limit": {"100"}}, userToken, &wire)
	if err != nil {
		return nil, err
	}
	return wire.Data, nil
}

func (c *Client) Pages(ctx context.Context, businessID, userToken string) ([]Page, error) {
	if strings.TrimSpace(businessID) == "" {
		return nil, errors.New("meta: business id is blank")
	}
	var wire struct {
		Data []Page `json:"data"`
	}
	err := c.graphGET(ctx, "/"+url.PathEscape(businessID)+"/owned_pages",
		url.Values{"fields": {"id,name,access_token"}, "limit": {"100"}}, userToken, &wire)
	if err != nil {
		return nil, err
	}
	return wire.Data, nil
}

type Form struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (c *Client) Forms(ctx context.Context, pageID, pageToken string) ([]Form, error) {
	var wire struct {
		Data []Form `json:"data"`
	}
	err := c.graphGET(ctx, "/"+url.PathEscape(pageID)+"/leadgen_forms",
		url.Values{"fields": {"id,name,status"}, "limit": {"100"}}, pageToken, &wire)
	if err != nil {
		return nil, err
	}
	return wire.Data, nil
}

func (c *Client) SubscribePage(ctx context.Context, pageID, pageToken string) error {
	form := url.Values{"subscribed_fields": {"leadgen"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.graphBase+"/"+c.version+"/"+url.PathEscape(pageID)+"/subscribed_apps",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+pageToken)
	var out struct {
		Success bool `json:"success"`
	}
	if err := c.decode(req, &out); err != nil {
		return err
	}
	if !out.Success {
		return errors.New("meta: Page subscription was not accepted")
	}
	return nil
}

type FieldData struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}
type Lead struct {
	ID          string      `json:"id"`
	CreatedTime string      `json:"created_time"`
	AdID        string      `json:"ad_id"`
	FormID      string      `json:"form_id"`
	Platform    string      `json:"platform"`
	FieldData   []FieldData `json:"field_data"`
}

const leadFields = "id,created_time,ad_id,form_id,platform,field_data"

func (c *Client) Lead(ctx context.Context, leadID, pageToken string) (Lead, error) {
	var out Lead
	err := c.graphGET(ctx, "/"+url.PathEscape(leadID), url.Values{"fields": {leadFields}}, pageToken, &out)
	return out, err
}

func (c *Client) Leads(ctx context.Context, formID, pageToken string, limit int) ([]Lead, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	out := make([]Lead, 0, limit)
	after := ""
	for len(out) < limit {
		pageSize := limit - len(out)
		if pageSize > 100 {
			pageSize = 100
		}
		params := url.Values{"fields": {leadFields}, "limit": {strconv.Itoa(pageSize)}}
		if after != "" {
			params.Set("after", after)
		}
		var wire struct {
			Data   []Lead `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.graphGET(ctx, "/"+url.PathEscape(formID)+"/leads", params, pageToken, &wire); err != nil {
			return nil, err
		}
		out = append(out, wire.Data...)
		if wire.Paging.Next == "" || wire.Paging.Cursors.After == "" || len(wire.Data) == 0 {
			break
		}
		after = wire.Paging.Cursors.After
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type AdAttribution struct{ AdID, AdName, AdSetID, AdSetName, CampaignID, CampaignName string }

func (c *Client) Attribution(ctx context.Context, adID, pageToken string) (AdAttribution, error) {
	if strings.TrimSpace(adID) == "" {
		return AdAttribution{}, nil
	}
	var wire struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		AdSet *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"adset"`
		Campaign *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"campaign"`
	}
	err := c.graphGET(ctx, "/"+url.PathEscape(adID), url.Values{"fields": {"id,name,adset{id,name},campaign{id,name}"}}, pageToken, &wire)
	if err != nil {
		return AdAttribution{AdID: adID}, err
	}
	out := AdAttribution{AdID: wire.ID, AdName: wire.Name}
	if out.AdID == "" {
		out.AdID = adID
	}
	if wire.AdSet != nil {
		out.AdSetID, out.AdSetName = wire.AdSet.ID, wire.AdSet.Name
	}
	if wire.Campaign != nil {
		out.CampaignID, out.CampaignName = wire.Campaign.ID, wire.Campaign.Name
	}
	return out, nil
}

func (c *Client) Revoke(ctx context.Context, userToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.graphBase+"/"+c.version+"/me/permissions", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	var out map[string]any
	err = c.decode(req, &out)
	if IsAuthorizationError(err) {
		return nil
	}
	return err
}

func (c *Client) graphGET(ctx context.Context, path string, params url.Values, token string, out any) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("meta: access token is blank")
	}
	if params == nil {
		params = url.Values{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.graphBase+"/"+c.version+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.decode(req, out)
}

func (c *Client) decode(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("meta: provider request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("meta: read provider response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerError{status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(out); err != nil {
		return errors.New("meta: provider returned invalid JSON")
	}
	return nil
}
