package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/santoshguru/crm/internal/domain"
)

// Sending, against a stub Gmail (task_crm.md §4).
//
// What this proves is the message this service *would* put on the wire, and
// every decision around it. What it does not prove is that Gmail accepts it:
// that needs a real OAuth client, which no test environment here has. The
// bytes are asserted rather than the outcome, which is the strongest thing
// available without one — and the encoding is where a hand-built message
// actually goes wrong.

func decodeRaw(t *testing.T, raw string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("the raw field is not base64url: %v", err)
	}
	return string(decoded)
}

// captureTransport records the one request the client makes.
type captureTransport struct {
	body   string
	auth   string
	url    string
	status int
	reply  string
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	c.body, c.auth, c.url = string(body), r.Header.Get("Authorization"), r.URL.String()
	status, reply := c.status, c.reply
	if status == 0 {
		status, reply = 200, `{"id":"gmail-msg-1"}`
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(reply)),
		Header:     make(http.Header),
	}, nil
}

func TestSendBuildsAMessageThatSurvivesTheWire(t *testing.T) {
	transport := &captureTransport{}
	client := New(&http.Client{Transport: transport})

	id, err := client.Send(context.Background(), "token-1", SendRequest{
		From: "rep@example.com", To: "alex@example.com",
		Subject: "About your enquiry — £50 off",
		// A long line, a non-ASCII character and a trailing space: the three
		// things that break a hand-built body.
		Body: "Hi Alex,\n\n" + strings.Repeat("This is a long paragraph. ", 12) +
			"\n\nRegards — Sam ",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "gmail-msg-1" {
		t.Errorf("message id = %q, want Gmail's own id", id)
	}
	if transport.auth != "Bearer token-1" {
		t.Errorf("authorization = %q", transport.auth)
	}
	if !strings.HasSuffix(transport.url, "/messages/send") {
		t.Errorf("url = %q, want the send endpoint", transport.url)
	}

	var payload struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal([]byte(transport.body), &payload); err != nil {
		t.Fatalf("request body: %v", err)
	}
	message := decodeRaw(t, payload.Raw)

	if !strings.Contains(message, "From: rep@example.com\r\n") {
		t.Error("the message does not send as the connected mailbox")
	}
	if !strings.Contains(message, "To: alex@example.com\r\n") {
		t.Error("the message has no recipient")
	}
	// A non-ASCII subject must be encoded, or it arrives as mojibake for some
	// recipients and is silently truncated for others.
	if !strings.Contains(message, "Subject: =?utf-8?B?") {
		t.Errorf("the subject is not RFC 2047 encoded:\n%s", message)
	}
	// Every line must be CRLF-terminated and within the limit. A bare LF or
	// an over-long line is how a message gets mangled by an intermediary.
	for _, line := range strings.Split(message, "\r\n") {
		if strings.Contains(line, "\n") {
			t.Errorf("a bare newline survived into the message: %q", line)
		}
		if len(line) > 998 {
			t.Errorf("a line is %d characters, over the RFC 5322 limit", len(line))
		}
	}

	// The body round-trips exactly, trailing space and all — base64 is chosen
	// precisely so nothing has to be trimmed or escaped.
	_, encoded, found := strings.Cut(message, "\r\n\r\n")
	if !found {
		t.Fatal("the message has no body separator")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\r\n", ""))
	if err != nil {
		t.Fatalf("body is not base64: %v", err)
	}
	if !strings.HasSuffix(string(decoded), "Regards — Sam ") {
		t.Errorf("the body did not round-trip; got tail %q", string(decoded)[len(decoded)-20:])
	}
}

// TestSendRefusesHeaderInjection is the one that matters most here. A newline
// in a subject or an address appends whatever follows as another header —
// Bcc, Reply-To, or a second body — which turns one message into a mailing.
func TestSendRefusesHeaderInjection(t *testing.T) {
	client := New(&http.Client{Transport: &captureTransport{}})
	base := SendRequest{From: "rep@example.com", To: "alex@example.com", Body: "Hi"}

	injections := []SendRequest{
		{From: base.From, To: "alex@example.com\r\nBcc: everyone@example.com", Body: "Hi"},
		{From: "rep@example.com\nBcc: everyone@example.com", To: base.To, Body: "Hi"},
		{From: base.From, To: base.To, Subject: "Hello\r\nBcc: everyone@example.com", Body: "Hi"},
	}
	for _, req := range injections {
		if _, err := client.Send(context.Background(), "token", req); err == nil {
			t.Errorf("accepted a header containing a line break: %+v", req)
		}
	}
}

func TestSendValidatesBeforeCallingGmail(t *testing.T) {
	transport := &captureTransport{}
	client := New(&http.Client{Transport: transport})

	for _, req := range []SendRequest{
		{To: "alex@example.com", Body: "Hi"},
		{From: "rep@example.com", Body: "Hi"},
		{From: "rep@example.com", To: "alex@example.com"},
	} {
		if _, err := client.Send(context.Background(), "token", req); err == nil {
			t.Errorf("accepted an incomplete message: %+v", req)
		}
	}
	if transport.body != "" {
		t.Error("an invalid message still reached the network")
	}
	if _, err := client.Send(context.Background(), "", SendRequest{
		From: "a@b.com", To: "c@d.com", Body: "Hi",
	}); err == nil {
		t.Error("sent without an access token")
	}
}

func TestSendDoesNotLeakTheProviderBody(t *testing.T) {
	// Gmail's error bodies echo request content. An error value here reaches
	// a log line, so including one would put the message and the recipient in
	// the logs of every failed send.
	transport := &captureTransport{status: 403, reply: `{"error":{"message":"alex@example.com is blocked"}}`}
	client := New(&http.Client{Transport: transport})

	_, err := client.Send(context.Background(), "token", SendRequest{
		From: "rep@example.com", To: "alex@example.com", Body: "Hi",
	})
	if err == nil {
		t.Fatal("a 403 reported success")
	}
	if strings.Contains(err.Error(), "alex@example.com") {
		t.Errorf("the error carries the provider body: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// stubTokens satisfies TokenSource.
type stubTokens struct {
	token string
	err   error
}

func (s stubTokens) AccessToken(context.Context, domain.Scope, string) (string, error) {
	return s.token, s.err
}

// stubSendAPI records what the Sender asked for.
type stubSendAPI struct {
	got   SendRequest
	token string
	err   error
}

func (s *stubSendAPI) Send(_ context.Context, token string, req SendRequest) (string, error) {
	s.token, s.got = token, req
	return "gmail-1", s.err
}

func TestSenderSendsAsTheConnectedMailbox(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}

	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID: "rep-1", GoogleAccountEmail: "rep@company.example",
		Status: domain.GoogleStatusConnected, TokenCiphertext: []byte("sealed"),
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	api := &stubSendAPI{}
	sender := NewSender(store, api, stubTokens{token: "access-1"})

	id, err := sender.Send(ctx, scope, "rep-1", SendRequest{
		// A From supplied by the caller must be ignored: Gmail only sends as
		// an address the account owns, and letting one through would be a
		// spoofing affordance rather than a feature.
		From: "someone.else@evil.example",
		To:   "alex@example.com", Body: "Hi",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "gmail-1" {
		t.Errorf("id = %q", id)
	}
	if api.got.From != "rep@company.example" {
		t.Errorf("sent as %q, want the connected mailbox", api.got.From)
	}
	if api.token != "access-1" {
		t.Errorf("token = %q, want the owner's own", api.token)
	}
}

func TestSenderRefusesWithoutAUsableConnection(t *testing.T) {
	ctx := context.Background()
	store := domain.NewMemStore()
	scope := domain.Scope{TenantID: "tenant-1", OrgID: "org-1"}
	sender := NewSender(store, &stubSendAPI{}, stubTokens{token: "t"})

	// Nobody has connected a mailbox.
	if _, err := sender.Send(ctx, scope, "rep-1", SendRequest{To: "a@b.com", Body: "Hi"}); !errors.Is(err, ErrNoMailbox) {
		t.Errorf("send with no connection: %v, want ErrNoMailbox", err)
	}

	// Connected but needing reauthorization: the token will not refresh, so
	// attempting the send would fail at the provider with a worse message.
	if _, err := store.SaveGoogleConnection(ctx, scope, &domain.GoogleConnection{
		OwnerID: "rep-1", GoogleAccountEmail: "rep@company.example",
		Status: domain.GoogleStatusReauthorizationRequired, TokenCiphertext: nil,
	}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if _, err := sender.Send(ctx, scope, "rep-1", SendRequest{To: "a@b.com", Body: "Hi"}); !errors.Is(err, ErrNoMailbox) {
		t.Errorf("send with a stale connection: %v, want ErrNoMailbox", err)
	}
}
