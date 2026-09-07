// Package gmail talks to the Gmail API and turns what it finds into CRM
// timeline entries.
//
// The one thing to understand before reading further: this package never
// fetches a message body. Every read uses format=metadata, which returns
// headers and nothing else, so the body does not cross the network, does not
// enter this process, and cannot be stored by a later change that adds a
// column without also widening the fetch. That is deliberate — a CRM needs to
// know that a conversation happened and what it was about, not to hold a copy
// of a person's mail.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL = "https://gmail.googleapis.com/gmail/v1/users/me"
	// maxResponseBytes bounds what a provider response may cost this process.
	// Gmail's own responses are far smaller; this is protection against a
	// proxy or an error page, not against Google.
	maxResponseBytes = 4 << 20
)

// ErrHistoryExpired means Gmail no longer holds history back to the cursor we
// asked from. Google keeps roughly a week, so a mailbox that has not synced
// for longer gets this — and the only correct response is a fresh backfill,
// because an incremental sync from an expired cursor silently loses every
// message in the gap.
var ErrHistoryExpired = errors.New("gmail: history cursor has expired")

// Client is a minimal Gmail API client. It holds no credential: an access
// token is passed per call, because tokens belong to individual users and are
// refreshed independently of this client's lifetime.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// New builds a client. A nil httpClient gets a bounded default.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{httpClient: httpClient, baseURL: baseURL}
}

// Message is the envelope of one Gmail message: who it involved, when, and
// what it was about. There is deliberately no body field.
type Message struct {
	ID       string
	ThreadID string
	Subject  string
	From     string
	To       []string
	Cc       []string
	// SentAt is Gmail's internalDate, the time Google recorded for the
	// message. It is used rather than the Date header because the header is
	// written by the sender and can say anything.
	SentAt time.Time
	// LabelIDs carries Gmail's own labels. SENT is what identifies a message
	// this mailbox sent, which is more reliable than comparing the From
	// address against the account's — aliases and delegated sending both
	// break that comparison.
	LabelIDs []string
}

// IsSent reports whether the connected mailbox sent this message.
func (m Message) IsSent() bool {
	for _, label := range m.LabelIDs {
		if label == "SENT" {
			return true
		}
	}
	return false
}

// Participants returns every address on the message, normalized and
// deduplicated, with the connected account's own address removed.
//
// Dropping the account's own address is what stops every message matching the
// mailbox owner if they happen to also be a Contact — which is common, since
// people are often in their own organization's CRM.
func (m Message) Participants(selfAddress string) []string {
	self := strings.ToLower(strings.TrimSpace(selfAddress))
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	for _, group := range [][]string{{m.From}, m.To, m.Cc} {
		for _, raw := range group {
			for _, address := range parseAddresses(raw) {
				if address == "" || address == self || seen[address] {
					continue
				}
				seen[address] = true
				out = append(out, address)
			}
		}
	}
	return out
}

// parseAddresses extracts bare addresses from one header value, which may
// hold a list and may use display-name form ("Bea Buyer <bea@acme.example>").
//
// A header that will not parse is not an error worth failing a sync over: it
// contributes no addresses, so the message simply matches nothing on that
// header. Mail in the wild contains a great deal of malformed From.
func parseAddresses(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if list, err := mail.ParseAddressList(header); err == nil {
		out := make([]string, 0, len(list))
		for _, address := range list {
			out = append(out, strings.ToLower(strings.TrimSpace(address.Address)))
		}
		return out
	}
	// Fall back to the raw value when it already looks like a bare address.
	if strings.Contains(header, "@") && !strings.ContainsAny(header, " ,<>") {
		return []string{strings.ToLower(header)}
	}
	return nil
}

// Profile is the small part of users.getProfile this package needs.
type Profile struct {
	EmailAddress string
	HistoryID    string
}

// Profile reads the mailbox's address and its current history cursor.
//
// The cursor matters as much as the address: a backfill has to record where
// history stood *before* it started, or messages arriving during the backfill
// fall into the gap between "not in the pages I walked" and "before the
// cursor I saved afterwards".
func (c *Client) Profile(ctx context.Context, accessToken string) (Profile, error) {
	var wire struct {
		EmailAddress string      `json:"emailAddress"`
		HistoryID    json.Number `json:"historyId"`
	}
	if err := c.get(ctx, accessToken, "/profile", nil, &wire); err != nil {
		return Profile{}, err
	}
	return Profile{
		EmailAddress: wire.EmailAddress,
		HistoryID:    historyID(wire.HistoryID),
	}, nil
}

// historyID renders Gmail's cursor as the string this system stores.
//
// json.Number rather than uint64: Gmail's REST encoding writes 64-bit
// integers as JSON *strings* (`"historyId": "1234"`), which will not decode
// into a numeric Go field at all. Accepting either form means neither a
// change at Google nor a hand-written test fixture can silently break the
// cursor — and a broken cursor is a mailbox that re-backfills forever.
func historyID(value json.Number) string {
	text := strings.Trim(value.String(), `"`)
	if text == "" || text == "0" {
		return ""
	}
	return text
}

// ListMessageIDs walks the mailbox, newest first. query is a Gmail search
// expression; pageToken continues a previous call.
func (c *Client) ListMessageIDs(
	ctx context.Context, accessToken, query, pageToken string, pageSize int,
) (ids []string, nextPageToken string, err error) {
	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	if pageToken != "" {
		params.Set("pageToken", pageToken)
	}
	if pageSize > 0 {
		params.Set("maxResults", strconv.Itoa(pageSize))
	}
	var wire struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.get(ctx, accessToken, "/messages", params, &wire); err != nil {
		return nil, "", err
	}
	out := make([]string, 0, len(wire.Messages))
	for _, m := range wire.Messages {
		out = append(out, m.ID)
	}
	return out, wire.NextPageToken, nil
}

// GetMessage reads one message's envelope.
//
// format=metadata with an explicit header allowlist is the whole point: Gmail
// returns those headers and nothing else, so no body or attachment ever
// reaches this process.
func (c *Client) GetMessage(ctx context.Context, accessToken, id string) (Message, error) {
	params := url.Values{"format": {"metadata"}}
	for _, header := range []string{"From", "To", "Cc", "Subject", "Date"} {
		params.Add("metadataHeaders", header)
	}
	var wire struct {
		ID           string   `json:"id"`
		ThreadID     string   `json:"threadId"`
		LabelIDs     []string `json:"labelIds"`
		InternalDate string   `json:"internalDate"`
		Payload      struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}
	if err := c.get(ctx, accessToken, "/messages/"+url.PathEscape(id), params, &wire); err != nil {
		return Message{}, err
	}

	out := Message{ID: wire.ID, ThreadID: wire.ThreadID, LabelIDs: wire.LabelIDs}
	for _, header := range wire.Payload.Headers {
		switch strings.ToLower(header.Name) {
		case "subject":
			out.Subject = header.Value
		case "from":
			out.From = header.Value
		case "to":
			out.To = append(out.To, header.Value)
		case "cc":
			out.Cc = append(out.Cc, header.Value)
		}
	}
	if millis, err := strconv.ParseInt(wire.InternalDate, 10, 64); err == nil && millis > 0 {
		out.SentAt = time.UnixMilli(millis).UTC()
	}
	return out, nil
}

// HistoryPage is one page of incremental changes.
type HistoryPage struct {
	// MessageIDs are the messages added since the cursor. Deletions and label
	// changes are ignored: this sync only ever adds timeline entries, and a
	// CRM record of a conversation should not vanish because someone tidied
	// their inbox.
	MessageIDs    []string
	NextPageToken string
	// HistoryID is the cursor to store once the whole walk finishes. Gmail
	// returns the mailbox's current value on every page, so it must not be
	// saved until the last one — saving it early skips whatever the remaining
	// pages held.
	HistoryID string
}

// History lists what changed since startHistoryID.
//
// A cursor Gmail has expired comes back as ErrHistoryExpired rather than an
// empty page, because those two must not be confused: an empty page means
// nothing happened, and an expired cursor means an unknown amount did.
func (c *Client) History(
	ctx context.Context, accessToken, startHistoryID, pageToken string,
) (HistoryPage, error) {
	params := url.Values{
		"startHistoryId": {startHistoryID},
		"historyTypes":   {"messageAdded"},
	}
	if pageToken != "" {
		params.Set("pageToken", pageToken)
	}
	var wire struct {
		History []struct {
			MessagesAdded []struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"messagesAdded"`
		} `json:"history"`
		NextPageToken string      `json:"nextPageToken"`
		HistoryID     json.Number `json:"historyId"`
	}
	if err := c.get(ctx, accessToken, "/history", params, &wire); err != nil {
		return HistoryPage{}, err
	}

	page := HistoryPage{
		NextPageToken: wire.NextPageToken,
		HistoryID:     historyID(wire.HistoryID),
	}
	seen := make(map[string]bool, 16)
	for _, entry := range wire.History {
		for _, added := range entry.MessagesAdded {
			if id := added.Message.ID; id != "" && !seen[id] {
				seen[id] = true
				page.MessageIDs = append(page.MessageIDs, id)
			}
		}
	}
	return page, nil
}

func (c *Client) get(ctx context.Context, accessToken, path string, params url.Values, into any) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("gmail: access token is required")
	}
	endpoint := c.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("gmail: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gmail: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/history") {
			return ErrHistoryExpired
		}
		// The body is not included. Gmail error bodies echo request
		// parameters, and an error value in this codebase reaches a log line.
		return fmt.Errorf("gmail: %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(into); err != nil {
		return fmt.Errorf("gmail: decode %s response: %w", path, err)
	}
	return nil
}

// Sending (task_crm.md §4, the gmail.send rung).
//
// This is the one place the package writes rather than reads, and the only
// place it handles a message body at all. The package doc above says bodies
// never cross the network — that remains true of every *read*, which is the
// property that matters: it means a customer's incoming mail cannot be stored
// here. A message this organization is itself sending is not somebody else's
// correspondence, and it has to be transmitted to be sent.

// SendRequest is one outgoing message.
type SendRequest struct {
	// From is the connected mailbox's own address. Gmail rejects a From that
	// is not an address the authenticated account may send as, which is the
	// behaviour we want: a rep sends as themselves.
	From    string
	To      string
	Subject string
	Body    string
}

// Validate rejects a message Gmail would refuse or that would arrive wrong.
func (r SendRequest) Validate() error {
	if strings.TrimSpace(r.From) == "" || strings.TrimSpace(r.To) == "" {
		return errors.New("gmail: a message needs a sender and a recipient")
	}
	if strings.TrimSpace(r.Body) == "" {
		return errors.New("gmail: a message needs a body")
	}
	// A header value containing a newline is header injection: it lets a
	// crafted subject or address append Bcc, Reply-To or a second body. The
	// subject reaches here from a tenant's own template, so this is a guard
	// against a mistake more than an attacker — but it is exactly the mistake
	// that turns one message into a mass mailing.
	for name, value := range map[string]string{
		"from": r.From, "to": r.To, "subject": r.Subject,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("gmail: %s header contains a line break", name)
		}
	}
	return nil
}

// Send delivers one message as the authenticated mailbox and returns Gmail's
// own message id.
//
// The id is worth keeping: it is what a later feature would use to correlate
// a reply, and it is the only proof outside Gmail that the send happened.
func (c *Client) Send(ctx context.Context, accessToken string, req SendRequest) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("gmail: access token is required")
	}
	if err := req.Validate(); err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]string{"raw": encodeRFC5322(req)})
	if err != nil {
		return "", fmt.Errorf("gmail: encode message: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/messages/send", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("gmail: build send request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gmail: send failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		// No body in the error, for the same reason every other call here
		// omits it: Gmail error bodies echo request content, and an error
		// value in this codebase reaches a log line — which would put the
		// message, and the recipient, in the logs.
		return "", fmt.Errorf("gmail: send returned HTTP %d", resp.StatusCode)
	}

	var sent struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&sent); err != nil {
		// The message may well have been sent — this is a decode failure on
		// the response, not a refusal. The caller must treat an error here as
		// "unknown", never as "not sent", or a retry sends it twice.
		return "", fmt.Errorf("gmail: decode send response: %w", err)
	}
	return sent.ID, nil
}

// encodeRFC5322 builds the message Gmail expects, base64url encoded.
//
// Written by hand rather than with net/mail, which parses rather than
// composes, or a MIME library this tree does not have. The message is
// deliberately the simplest thing that renders correctly everywhere: one
// text/plain part, UTF-8, no attachments, no alternative HTML. Anything
// richer is a formatting decision nobody has made.
func encodeRFC5322(req SendRequest) string {
	var b strings.Builder
	b.WriteString("From: " + req.From + "\r\n")
	b.WriteString("To: " + req.To + "\r\n")
	if strings.TrimSpace(req.Subject) != "" {
		// Encoded-word per RFC 2047, unconditionally. A subject is a
		// tenant's own words and may hold anything; base64 in UTF-8 is
		// correct for all of it, and deciding per character when to encode is
		// how a subject arrives as mojibake for one recipient in ten.
		b.WriteString("Subject: =?utf-8?B?" +
			base64.StdEncoding.EncodeToString([]byte(req.Subject)) + "?=\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	// Base64 rather than 8bit or quoted-printable: it cannot produce a line
	// longer than the limit, cannot contain a bare CR or LF, and cannot end a
	// line with whitespace — three ways a hand-built body silently breaks.
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunk76(base64.StdEncoding.EncodeToString([]byte(req.Body))))

	// URL-safe, unpadded: what Gmail's `raw` field requires.
	return base64.RawURLEncoding.EncodeToString([]byte(b.String()))
}

// chunk76 wraps base64 at the 76 characters RFC 2045 allows.
func chunk76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}
