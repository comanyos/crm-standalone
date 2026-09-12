package domain

import (
	"net"
	"testing"
	"time"
)

func TestNextScheduleOccurrencePreservesWallClockAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, time.March, 7, 9, 0, 0, 0, location)
	next, recurring := NextScheduleOccurrence(before, location.String(), ScheduleDaily, 1)
	if !recurring {
		t.Fatal("daily schedule was treated as one-time")
	}
	local := next.In(location)
	if local.Hour() != 9 || local.Day() != 8 {
		t.Fatalf("next run = %v, want 09:00 on the next local day", local)
	}
	if next.Sub(before.UTC()) != 23*time.Hour {
		t.Fatalf("DST transition advanced by %v, want 23 hours while preserving 09:00", next.Sub(before.UTC()))
	}
}

func TestMonthlyScheduleClampsToTheLastDay(t *testing.T) {
	start := time.Date(2027, time.January, 31, 10, 30, 0, 0, time.UTC)
	next, recurring := NextScheduleOccurrence(start, "UTC", ScheduleMonthly, 1)
	if !recurring || next != time.Date(2027, time.February, 28, 10, 30, 0, 0, time.UTC) {
		t.Fatalf("31 January monthly recurrence = %v, want 28 February 10:30", next)
	}
}

func TestRecurrenceAdvancesFromEachWallClockOccurrence(t *testing.T) {
	start := time.Date(2026, time.September, 1, 9, 0, 0, 0, time.UTC)
	next, recurring := NextScheduleOccurrence(start, "UTC", ScheduleWeekly, 2)
	if !recurring || next != time.Date(2026, time.September, 15, 9, 0, 0, 0, time.UTC) {
		t.Fatalf("fortnightly recurrence = %v, want 15 September 09:00", next)
	}
}

func TestWebhookEndpointsRejectInternalDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://hooks.example.com/path",
		"https://127.0.0.1/hook",
		"https://10.1.2.3/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://example.com:8443/hook",
		"https://user:pass@example.com/hook",
		"https://example.com/hook#fragment",
	} {
		endpoint := &WorkflowWebhookEndpoint{Name: "Test", URL: rawURL,
			SigningSecretCiphertext: []byte("ciphertext"), CreatedBy: "admin"}
		if err := endpoint.Validate(); err == nil {
			t.Errorf("Validate accepted unsafe URL %q", rawURL)
		}
	}
	for _, rawIP := range []string{"100.64.0.1", "192.0.2.1", "198.18.0.1", "2001:db8::1"} {
		if !UnsafeWebhookIP(net.ParseIP(rawIP)) {
			t.Errorf("UnsafeWebhookIP accepted reserved address %s", rawIP)
		}
	}
}
