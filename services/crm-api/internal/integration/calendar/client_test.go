package calendar

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

func stubCalendar(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := New(server.Client())
	client.baseURL = server.URL
	return client
}

// TestListNeverAsksForDescriptionOrLocation. As with Gmail's format=metadata,
// the privacy stance is a property of the request: a field that never arrives
// cannot be stored by a later change that adds a column.
func TestListNeverAsksForDescriptionOrLocation(t *testing.T) {
	var got url.Values
	client := stubCalendar(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	if _, err := client.List(context.Background(), "token", ListOptions{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	fields := got.Get("fields")
	if fields == "" {
		t.Fatal("no field mask was sent; Google would return whole events including their bodies")
	}
	for _, forbidden := range []string{"description", "location", "attachments", "conferenceData"} {
		if strings.Contains(fields, forbidden) {
			t.Errorf("the field mask asks for %q: %s", forbidden, fields)
		}
	}
	if got.Get("singleEvents") != "true" {
		t.Error("singleEvents is not set; a recurring series would arrive as one abstract event")
	}
	if got.Get("showDeleted") != "true" {
		t.Error("showDeleted is not set; cancellations would never reach the timeline")
	}
}

// TestAllDayDatesResolveInTheEventsOwnZone is the parsing half of the
// timezone story. Google sends `date: "2026-08-01"` meaning the whole of
// 1 August wherever the calendar lives; reading it as UTC puts the day
// boundary in the wrong place for exactly the users who notice.
func TestAllDayDatesResolveInTheEventsOwnZone(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"e-1","status":"confirmed","summary":"On site",
			"start":{"date":"2026-08-01","timeZone":"Asia/Kolkata"},
			"end":{"date":"2026-08-02","timeZone":"Asia/Kolkata"}}]}`))
	})

	page, err := client.List(context.Background(), "token", ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("decoded %d events", len(page.Events))
	}
	event := page.Events[0]
	if !event.AllDay {
		t.Error("an event sent as a date was not marked all-day")
	}
	if event.TimeZone != "Asia/Kolkata" {
		t.Errorf("time zone = %q", event.TimeZone)
	}
	// 1 August local midnight in +05:30 is 31 July 18:30 UTC.
	if event.StartsAt.UTC().Day() != 31 || event.StartsAt.UTC().Hour() != 18 {
		t.Errorf("start = %v, want 2026-07-31T18:30Z — the date was read in the wrong zone",
			event.StartsAt.UTC())
	}
}

// TestTimedEventsDecodeTheirOffset.
func TestTimedEventsDecodeTheirOffset(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"e-1","status":"confirmed",
			"start":{"dateTime":"2026-08-03T09:00:00+01:00","timeZone":"Europe/London"},
			"end":{"dateTime":"2026-08-03T10:00:00+01:00","timeZone":"Europe/London"}}]}`))
	})

	page, err := client.List(context.Background(), "token", ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	event := page.Events[0]
	if event.AllDay {
		t.Error("a timed event was marked all-day")
	}
	if !event.StartsAt.Equal(time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 08:00Z", event.StartsAt)
	}
	if event.TimeZone != "Europe/London" {
		t.Errorf("time zone = %q", event.TimeZone)
	}
}

// TestAnUnparseableEventDoesNotFailThePage. A calendar containing one strange
// row should still sync the other two hundred.
func TestAnUnparseableEventDoesNotFailThePage(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[
			{"id":"e-bad","status":"confirmed","start":{}},
			{"id":"e-good","status":"confirmed","start":{"dateTime":"2026-08-03T09:00:00Z"}}]}`))
	})

	page, err := client.List(context.Background(), "token", ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != "e-good" {
		t.Errorf("events = %+v, want the parseable one only", page.Events)
	}
}

// TestAnUnknownZoneNameFallsBackHonestly: the stored zone says UTC, which is
// at least true, rather than a name nothing can load.
func TestAnUnknownZoneNameFallsBackHonestly(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"e-1","status":"confirmed",
			"start":{"date":"2026-08-01","timeZone":"Mars/Olympus_Mons"}}]}`))
	})

	page, err := client.List(context.Background(), "token", ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Events[0].TimeZone != "UTC" {
		t.Errorf("time zone = %q, want UTC rather than a name nothing can load",
			page.Events[0].TimeZone)
	}
}

// TestAnExpiredSyncTokenIsDistinct: 410 means an unknown amount was missed,
// which is not the same as an empty page.
func TestAnExpiredSyncTokenIsDistinct(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":{"code":410,"message":"Sync token is no longer valid"}}`))
	})

	_, err := client.List(context.Background(), "token", ListOptions{SyncToken: "stale"})
	if !errors.Is(err, ErrSyncTokenExpired) {
		t.Fatalf("list with an expired token: %v, want ErrSyncTokenExpired", err)
	}
}

// TestASyncTokenIsNeverCombinedWithAWindow. Google rejects the combination,
// and the failure would look like a broken integration rather than a bad
// request.
func TestASyncTokenIsNeverCombinedWithAWindow(t *testing.T) {
	var got url.Values
	client := stubCalendar(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	if _, err := client.List(context.Background(), "token", ListOptions{
		SyncToken: "tok",
		TimeMin:   time.Now(), TimeMax: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got.Get("syncToken") != "tok" {
		t.Errorf("syncToken = %q", got.Get("syncToken"))
	}
	if got.Get("timeMin") != "" || got.Get("timeMax") != "" || got.Get("orderBy") != "" {
		t.Errorf("a sync token was sent alongside %v, which Google rejects", got)
	}
}

// TestProviderErrorsDoNotLeakResponseBodies.
func TestProviderErrorsDoNotLeakResponseBodies(t *testing.T) {
	const secretish = "calendar rep@ourcompany.example event Private/Therapy"
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"` + secretish + `"}}`))
	})

	_, err := client.List(context.Background(), "token", ListOptions{})
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if strings.Contains(err.Error(), "Therapy") || strings.Contains(err.Error(), "ourcompany") {
		t.Errorf("the provider response body reached the error value: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error should still name the status code: %v", err)
	}
}

// TestRequestsRefuseToGoOutWithoutAToken.
func TestRequestsRefuseToGoOutWithoutAToken(t *testing.T) {
	client := stubCalendar(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := client.List(context.Background(), "", ListOptions{}); err == nil {
		t.Error("a request went out with no access token")
	}
	if _, err := client.CalendarTimeZone(context.Background(), "", "primary"); err == nil {
		t.Error("a settings request went out with no access token")
	}
}

// TestCalendarTimeZoneReadsTheDefault.
func TestCalendarTimeZoneReadsTheDefault(t *testing.T) {
	var auth string
	client := stubCalendar(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"timeZone":"America/New_York"}`))
	})

	zone, err := client.CalendarTimeZone(context.Background(), "token", "")
	if err != nil {
		t.Fatalf("read zone: %v", err)
	}
	if zone != "America/New_York" {
		t.Errorf("zone = %q", zone)
	}
	if auth != "Bearer token" {
		t.Errorf("Authorization = %q", auth)
	}
}
