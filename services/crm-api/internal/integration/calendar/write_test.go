package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/santoshguru/crm/internal/domain"
)

func TestCalendarCreateSendsInvitesRecurrenceAndReminders(t *testing.T) {
	var method, path, auth string
	var body eventWire
	client := stubCalendar(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"event-1","etag":"etag-1","status":"confirmed","summary":"Demo","start":{"dateTime":"2026-09-14T09:00:00+05:30","timeZone":"Asia/Kolkata"},"end":{"dateTime":"2026-09-14T10:00:00+05:30","timeZone":"Asia/Kolkata"}}`))
	})
	start := time.Date(2026, time.September, 14, 3, 30, 0, 0, time.UTC)
	if _, err := client.Create(context.Background(), "access-token", "primary", EventInput{
		Title: "Demo", StartsAt: start, EndsAt: start.Add(time.Hour), TimeZone: "Asia/Kolkata",
		Attendees: []string{"buyer@example.com"}, Recurrence: []string{"RRULE:FREQ=WEEKLY;COUNT=4"},
		ReminderMinutes: []int{30},
	}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || !strings.Contains(path, "/calendars/primary/events?sendUpdates=all") {
		t.Fatalf("request = %s %s", method, path)
	}
	if auth != "Bearer access-token" || len(body.Attendees) != 1 || body.Attendees[0].Email != "buyer@example.com" {
		t.Fatalf("authorization/attendees were not sent: %q %+v", auth, body.Attendees)
	}
	if len(body.Recurrence) != 1 || body.Recurrence[0] != "RRULE:FREQ=WEEKLY;COUNT=4" ||
		len(body.Reminders.Overrides) != 1 || body.Reminders.Overrides[0].Minutes != 30 {
		t.Fatalf("recurrence/reminders were not sent: %+v %+v", body.Recurrence, body.Reminders)
	}
}

type memoryWriteAPI struct {
	event   Event
	creates int
}

func (a *memoryWriteAPI) Create(_ context.Context, _ string, _ string, in EventInput) (Event, error) {
	a.creates++
	if a.event.ID != "" {
		return Event{}, ErrEventConflict
	}
	a.event = Event{ID: in.ID, ETag: "v1", ICalUID: in.ID + "@google", Status: "confirmed", Title: in.Title,
		StartsAt: in.StartsAt.UTC(), EndsAt: in.EndsAt.UTC(), TimeZone: in.TimeZone}
	return a.event, nil
}
func (a *memoryWriteAPI) Get(context.Context, string, string, string) (Event, error) {
	return a.event, nil
}
func (a *memoryWriteAPI) Update(_ context.Context, _ string, _ string, _ string, etag string, in EventInput) (Event, error) {
	if etag != a.event.ETag {
		return Event{}, ErrEventChanged
	}
	a.event.Title, a.event.ETag, a.event.StartsAt, a.event.EndsAt = in.Title, "v2", in.StartsAt.UTC(), in.EndsAt.UTC()
	return a.event, nil
}
func (a *memoryWriteAPI) Cancel(context.Context, string, string, string, string) error {
	a.event.Status = "cancelled"
	return nil
}

func TestManagedCalendarWritesAreIdempotentAndAudited(t *testing.T) {
	store, contact := syncFixture(t)
	ctx, scope := context.Background(), testScope()
	provider := &memoryWriteAPI{}
	writer := NewWriter(store, provider, staticToken{})
	start := time.Date(2026, time.September, 15, 9, 0, 0, 0, time.UTC)
	input := ManagedEventInput{RequestID: "browser-request-1", OwnerID: owner, ActorID: "rep-1",
		SubjectType: domain.SubjectContact, SubjectID: contact.ID,
		Event: EventInput{Title: "Discovery", StartsAt: start, EndsAt: start.Add(time.Hour), TimeZone: "UTC"}}
	first, _, err := writer.Create(ctx, scope, input)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := writer.Create(ctx, scope, input)
	if err != nil || first.ID != second.ID {
		t.Fatalf("idempotent create = %+v, %v", second, err)
	}
	activities, err := store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if err != nil || len(activities) != 1 {
		t.Fatalf("two identical creates wrote %d audit entries, want one", len(activities))
	}
	input.RequestID, input.ETag, input.Event.ID, input.Event.Title = "", first.ETag, first.ID, "Discovery updated"
	if _, _, err := writer.Update(ctx, scope, input); err != nil {
		t.Fatal(err)
	}
	activities, _ = store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if len(activities) != 2 || !strings.Contains(activities[0].Notes+activities[1].Notes, "Meeting updated") {
		t.Fatalf("title edit audit = %+v", activities)
	}
	if _, err := writer.Cancel(ctx, scope, owner, "rep-1", first.ID, "v2"); err != nil {
		t.Fatal(err)
	}
	activities, _ = store.ListActivitiesForSubject(ctx, scope, domain.SubjectContact, contact.ID)
	if len(activities) != 3 || !strings.Contains(activities[0].Notes+activities[1].Notes+activities[2].Notes, "Meeting cancelled") {
		t.Fatalf("cancel audit = %+v", activities)
	}
}
