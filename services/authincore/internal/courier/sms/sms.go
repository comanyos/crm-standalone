// Package sms provides an SMS sender interface and a Twilio/Vonage implementation.
package sms

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Sender sends a single SMS message.
type Sender interface {
	Send(ctx context.Context, to, body string) error
}

// TwilioSender sends SMS via Twilio REST API.
type TwilioSender struct {
	accountSID string
	authToken  string
	from       string
	client     *http.Client
}

func NewTwilio(accountSID, authToken, from string) *TwilioSender {
	return &TwilioSender{accountSID: accountSID, authToken: authToken, from: from, client: &http.Client{}}
}

func NewTwilioFromEnv() (*TwilioSender, bool) {
	sid := os.Getenv("TWILIO_ACCOUNT_SID")
	tok := os.Getenv("TWILIO_AUTH_TOKEN")
	from := os.Getenv("TWILIO_FROM")
	if sid == "" || tok == "" || from == "" {
		return nil, false
	}
	return NewTwilio(sid, tok, from), true
}

func (t *TwilioSender) Send(ctx context.Context, to, body string) error {
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", t.accountSID)
	form := url.Values{"To": {to}, "From": {t.from}, "Body": {body}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("twilio build request: %w", err)
	}
	req.SetBasicAuth(t.accountSID, t.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("twilio send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("twilio error %d: %s", resp.StatusCode, b)
	}
	return nil
}

// VonageSender sends SMS via Vonage REST API.
type VonageSender struct {
	apiKey    string
	apiSecret string
	from      string
	client    *http.Client
}

func NewVonage(apiKey, apiSecret, from string) *VonageSender {
	return &VonageSender{apiKey: apiKey, apiSecret: apiSecret, from: from, client: &http.Client{}}
}

func NewVonageFromEnv() (*VonageSender, bool) {
	key := os.Getenv("VONAGE_API_KEY")
	sec := os.Getenv("VONAGE_API_SECRET")
	from := os.Getenv("VONAGE_FROM")
	if key == "" || sec == "" || from == "" {
		return nil, false
	}
	return NewVonage(key, sec, from), true
}

func (v *VonageSender) Send(ctx context.Context, to, body string) error {
	form := url.Values{
		"api_key": {v.apiKey}, "api_secret": {v.apiSecret},
		"to": {to}, "from": {v.from}, "text": {body},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://rest.nexmo.com/sms/json", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("vonage build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("vonage send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("vonage error %d: %s", resp.StatusCode, b)
	}
	return nil
}
