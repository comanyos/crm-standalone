package gmail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The Gmail HTTP client against a stub server.
//
// The first test is the one that has to keep passing: this package's whole
// privacy stance rests on never asking Gmail for a body, and that is a
// property of the request it sends, not of what it does with the response.

func stubGmail(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := New(server.Client())
	client.baseURL = server.URL
	return client
}

// TestGetMessageNeverAsksForABody. format=metadata with an explicit header
// allowlist means Gmail returns headers only — the body does not cross the
// network, so no later change can store it without also changing this
// request, which is exactly the review this deserves.
func TestGetMessageNeverAsksForABody(t *testing.T) {
	var got url.Values
	client := stubGmail(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"id":"m-1","threadId":"t-1","internalDate":"1754040600000",
			"labelIds":["INBOX"],"payload":{"headers":[
			{"name":"Subject","value":"Renewal terms"},
			{"name":"From","value":"Bea Buyer <bea@acme.example>"},
			{"name":"To","value":"rep@ourcompany.example"}]}}`))
	})

	message, err := client.GetMessage(context.Background(), "token", "m-1")
	if err != nil {
		t.Fatalf("get message: %v", err)
	}

	if got.Get("format") != "metadata" {
		t.Errorf("format = %q, want metadata — any other value returns the message body",
			got.Get("format"))
	}
	headers := got["metadataHeaders"]
	if len(headers) == 0 {
		t.Fatal("no metadataHeaders allowlist was sent")
	}
	for _, header := range headers {
		switch header {
		case "From", "To", "Cc", "Subject", "Date":
		default:
			t.Errorf("requested header %q is outside the envelope allowlist", header)
		}
	}

	if message.Subject != "Renewal terms" || message.ThreadID != "t-1" {
		t.Errorf("decoded message = %+v", message)
	}
	if message.SentAt.IsZero() {
		t.Error("internalDate did not decode; every entry would be stamped with sync time")
	}
	if len(message.To) != 1 || message.To[0] != "rep@ourcompany.example" {
		t.Errorf("To = %v", message.To)
	}
}

// TestSentAtComesFromInternalDateNotTheDateHeader. The Date header is written
// by the sender and can say anything; internalDate is Google's own record.
func TestSentAtComesFromInternalDateNotTheDateHeader(t *testing.T) {
	client := stubGmail(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m-1","internalDate":"1754040600000","payload":{"headers":[
			{"name":"Date","value":"Tue, 1 Jan 1980 00:00:00 +0000"}]}}`))
	})

	message, err := client.GetMessage(context.Background(), "token", "m-1")
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if message.SentAt.Year() == 1980 {
		t.Error("the sender-controlled Date header was used as the message time")
	}
	if !message.SentAt.Equal(time.UnixMilli(1754040600000).UTC()) {
		t.Errorf("SentAt = %v, want the internalDate value", message.SentAt)
	}
}

// TestHistoryReportsAnExpiredCursorDistinctly. An empty page means nothing
// happened; an expired cursor means an unknown amount did. Confusing the two
// silently loses mail.
func TestHistoryReportsAnExpiredCursorDistinctly(t *testing.T) {
	client := stubGmail(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found."}}`))
	})

	_, err := client.History(context.Background(), "token", "1", "")
	if !errors.Is(err, ErrHistoryExpired) {
		t.Fatalf("history with an expired cursor: %v, want ErrHistoryExpired", err)
	}
}

// TestHistoryDeduplicatesWithinAPage. Gmail reports a message once per
// history record, and one message can appear in several.
func TestHistoryDeduplicatesWithinAPage(t *testing.T) {
	client := stubGmail(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"historyId":"1200","history":[
			{"messagesAdded":[{"message":{"id":"m-1"}},{"message":{"id":"m-2"}}]},
			{"messagesAdded":[{"message":{"id":"m-1"}}]}]}`))
	})

	page, err := client.History(context.Background(), "token", "1000", "")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(page.MessageIDs) != 2 {
		t.Errorf("message ids = %v, want two distinct ones", page.MessageIDs)
	}
	if page.HistoryID != "1200" {
		t.Errorf("history id = %q, want 1200", page.HistoryID)
	}
}

// TestHistoryIDDecodesGmailsStringEncoding. Gmail writes 64-bit integers as
// JSON strings, so a numeric Go field does not decode them at all — the
// symptom is a mailbox whose cursor never advances and which therefore
// re-backfills on every pass.
func TestHistoryIDDecodesGmailsStringEncoding(t *testing.T) {
	for _, body := range []string{
		`{"emailAddress":"rep@ourcompany.example","historyId":"987654321"}`, // what Gmail sends
		`{"emailAddress":"rep@ourcompany.example","historyId":987654321}`,   // tolerated
	} {
		client := stubGmail(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		profile, err := client.Profile(context.Background(), "token")
		if err != nil {
			t.Fatalf("profile from %s: %v", body, err)
		}
		if profile.HistoryID != "987654321" {
			t.Errorf("history id = %q from %s, want 987654321", profile.HistoryID, body)
		}
	}
}

// TestHistoryAsksOnlyForAddedMessages. Sync only ever adds timeline entries;
// a CRM record of a conversation must not vanish because someone tidied their
// inbox, so deletions and label changes are not even requested.
func TestHistoryAsksOnlyForAddedMessages(t *testing.T) {
	var got url.Values
	client := stubGmail(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"historyId":"1"}`))
	})

	if _, err := client.History(context.Background(), "token", "1", ""); err != nil {
		t.Fatalf("history: %v", err)
	}
	if types := got["historyTypes"]; len(types) != 1 || types[0] != "messageAdded" {
		t.Errorf("historyTypes = %v, want just messageAdded", types)
	}
}

// TestProviderErrorsDoNotLeakResponseBodies. Gmail error bodies echo request
// parameters, and an error value in this codebase reaches a log line.
func TestProviderErrorsDoNotLeakResponseBodies(t *testing.T) {
	const secretish = "rep@ourcompany.example asked for label Private/Medical"
	client := stubGmail(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"` + secretish + `"}}`))
	})

	_, err := client.GetMessage(context.Background(), "token", "m-1")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if strings.Contains(err.Error(), "ourcompany") || strings.Contains(err.Error(), "Medical") {
		t.Errorf("the provider response body reached the error value: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error should still name the status code: %v", err)
	}
}

// TestRequestsCarryTheAccessToken, and refuse to go out without one.
func TestRequestsCarryTheAccessToken(t *testing.T) {
	var auth string
	client := stubGmail(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"emailAddress":"rep@ourcompany.example","historyId":"42"}`))
	})

	profile, err := client.Profile(context.Background(), "the-token")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if auth != "Bearer the-token" {
		t.Errorf("Authorization = %q", auth)
	}
	if profile.HistoryID != "42" {
		t.Errorf("history id = %q, want 42", profile.HistoryID)
	}

	if _, err := client.Profile(context.Background(), ""); err == nil {
		t.Error("a request went out with no access token")
	}
}

// TestListPagesThroughTheMailbox.
func TestListPagesThroughTheMailbox(t *testing.T) {
	var gotQuery, gotPageToken string
	client := stubGmail(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotPageToken = r.URL.Query().Get("pageToken")
		_, _ = w.Write([]byte(`{"messages":[{"id":"m-1"},{"id":"m-2"}],"nextPageToken":"next"}`))
	})

	ids, next, err := client.ListMessageIDs(context.Background(), "token", backfillWindow, "page-2", 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 || next != "next" {
		t.Errorf("ids = %v, next = %q", ids, next)
	}
	if gotQuery != backfillWindow {
		t.Errorf("query = %q, want the bounded backfill window", gotQuery)
	}
	if gotPageToken != "page-2" {
		t.Errorf("pageToken = %q", gotPageToken)
	}
}

// TestBackfillWindowIsBounded states the constant's intent directly: an
// unbounded first pass walks a decade of mail to find the few messages that
// match a Contact.
func TestBackfillWindowIsBounded(t *testing.T) {
	if !strings.Contains(backfillWindow, "newer_than:") {
		t.Errorf("backfill window %q has no time bound", backfillWindow)
	}
	if !strings.Contains(backfillWindow, "-in:chats") {
		t.Errorf("backfill window %q does not exclude chat records, which are not email", backfillWindow)
	}
}
