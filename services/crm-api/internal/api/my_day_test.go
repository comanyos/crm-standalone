package api

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

func TestGetMyDayBuildsOwnedDailyQueue(t *testing.T) {
	service, store := newTestServiceWithStore()
	service.cfg.LeadSLA = time.Nanosecond
	ctx := ctxAs("tenant-my-day", "tenant-my-day")
	scope := domain.Scope{TenantID: "tenant-my-day", OrgID: "tenant-my-day"}

	offset := 330
	location := time.FixedZone("test", offset*60)
	localNow := time.Now().In(location)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 12, 0, 0, 0, location)

	for _, task := range []*domain.Task{
		{Title: "Late call", Status: domain.TaskOpen, AssigneeID: "test-user", DueAt: today.AddDate(0, 0, -1)},
		{Title: "Today's demo", Status: domain.TaskOpen, AssigneeID: "test-user", DueAt: today},
		{Title: "Next meeting", Status: domain.TaskOpen, AssigneeID: "test-user", DueAt: today.AddDate(0, 0, 2)},
		{Title: "Somebody else's", Status: domain.TaskOpen, AssigneeID: "other", DueAt: today},
	} {
		if _, err := store.CreateTask(ctx, scope, task); err != nil {
			t.Fatalf("CreateTask(%q): %v", task.Title, err)
		}
	}

	untouched, err := store.CreateLead(ctx, scope, &domain.Lead{FirstName: "Fresh", OwnerID: "test-user"})
	if err != nil {
		t.Fatal(err)
	}
	contacted, err := store.CreateLead(ctx, scope, &domain.Lead{FirstName: "Contacted", OwnerID: "test-user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateActivity(ctx, scope, &domain.Activity{
		Kind: "call", SubjectType: domain.SubjectLead, SubjectID: contacted.ID,
		OccurredAt: time.Now(), ActorID: "test-user",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLead(ctx, scope, &domain.Lead{FirstName: "Other", OwnerID: "other"}); err != nil {
		t.Fatal(err)
	}

	res, err := service.GetMyDay(ctx, connect.NewRequest(&crmv1.GetMyDayRequest{UtcOffsetMinutes: int32(offset)}))
	if err != nil {
		t.Fatalf("GetMyDay: %v", err)
	}
	got := res.Msg.GetMyDay()
	if len(got.GetOverdueTasks()) != 1 || got.GetOverdueTasks()[0].GetTitle() != "Late call" {
		t.Fatalf("overdue = %+v", got.GetOverdueTasks())
	}
	if len(got.GetDueTodayTasks()) != 1 || got.GetDueTodayTasks()[0].GetTitle() != "Today's demo" {
		t.Fatalf("today = %+v", got.GetDueTodayTasks())
	}
	if len(got.GetUpcomingTasks()) != 1 || got.GetUpcomingTasks()[0].GetTitle() != "Next meeting" {
		t.Fatalf("upcoming = %+v", got.GetUpcomingTasks())
	}
	if len(got.GetNewLeads()) != 2 {
		t.Fatalf("new leads = %d, want 2", len(got.GetNewLeads()))
	}
	if len(got.GetUntouchedLeads()) != 1 || got.GetUntouchedLeads()[0].GetId() != untouched.ID {
		t.Fatalf("untouched = %+v", got.GetUntouchedLeads())
	}
}

func TestGetMyDayRejectsImpossibleUTCOffset(t *testing.T) {
	service := newTestService()
	_, err := service.GetMyDay(ctxAs("tenant-my-day", "tenant-my-day"),
		connect.NewRequest(&crmv1.GetMyDayRequest{UtcOffsetMinutes: 900}))
	if err == nil {
		t.Fatal("GetMyDay accepted an impossible UTC offset")
	}
}
