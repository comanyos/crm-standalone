package api

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	myDayPageSize  = 500
	myDayItemLimit = 50
)

// GetMyDay returns the signed-in rep's bounded daily work queue. Day
// boundaries use the browser's UTC offset so "today" is the user's today,
// while all persisted timestamps remain UTC.
func (s *CRMService) GetMyDay(
	ctx context.Context, req *connect.Request[crmv1.GetMyDayRequest],
) (*connect.Response[crmv1.GetMyDayResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	ownerID := principalSubject(ctx)
	if ownerID == "" {
		return nil, connectErr(fmt.Errorf("%w: authenticated principal is required", domain.ErrInvalid))
	}
	offsetMinutes := int(req.Msg.GetUtcOffsetMinutes())
	if offsetMinutes < -14*60 || offsetMinutes > 14*60 {
		return nil, connectErr(fmt.Errorf("%w: UTC offset is outside the supported range", domain.ErrInvalid))
	}

	now := time.Now().UTC()
	location := time.FixedZone("my-day", offsetMinutes*60)
	localNow := now.In(location)
	startOfToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC()
	startOfTomorrow := startOfToday.In(location).AddDate(0, 0, 1).UTC()
	endOfUpcoming := startOfToday.In(location).AddDate(0, 0, 8).UTC()

	tasks, _, err := s.store.ListTasks(ctx, scope, domain.Page{Size: myDayPageSize}, domain.TaskFilter{
		AssigneeID: ownerID,
		OpenOnly:   true,
	}, domain.SortTaskDue)
	if err != nil {
		return nil, connectErr(err)
	}
	overdue := make([]*crmv1.Task, 0)
	dueToday := make([]*crmv1.Task, 0)
	upcoming := make([]*crmv1.Task, 0)
	for _, task := range tasks {
		if task.DueAt.IsZero() {
			continue
		}
		switch {
		case task.DueAt.Before(startOfToday):
			overdue = appendLimitedTask(overdue, task)
		case task.DueAt.Before(startOfTomorrow):
			dueToday = appendLimitedTask(dueToday, task)
		case task.DueAt.Before(endOfUpcoming):
			upcoming = appendLimitedTask(upcoming, task)
		}
	}

	leads, _, err := s.store.ListLeads(ctx, scope, domain.Page{Size: myDayPageSize})
	if err != nil {
		return nil, connectErr(err)
	}
	sla := s.cfg.LeadSLA
	if sla == 0 {
		sla = 15 * time.Minute
	}
	newLeads := make([]*crmv1.Lead, 0)
	untouched := make([]*crmv1.Lead, 0)
	for _, lead := range leads {
		if lead.OwnerID != ownerID || lead.Status == domain.LeadStatusConverted ||
			lead.Status == domain.LeadStatusDisqualified {
			continue
		}
		if !lead.CreatedAt.Before(now.Add(-24*time.Hour)) && len(newLeads) < myDayItemLimit {
			newLeads = append(newLeads, leadToProto(lead))
		}
		if lead.Status != domain.LeadStatusNew || lead.CreatedAt.After(now.Add(-sla)) || len(untouched) >= myDayItemLimit {
			continue
		}
		activities, err := s.store.ListActivitiesForSubject(ctx, scope, domain.SubjectLead, lead.ID)
		if err != nil {
			return nil, connectErr(err)
		}
		if len(activities) == 0 {
			untouched = append(untouched, leadToProto(lead))
		}
	}

	return connect.NewResponse(&crmv1.GetMyDayResponse{MyDay: &crmv1.MyDay{
		OverdueTasks:   overdue,
		DueTodayTasks:  dueToday,
		UpcomingTasks:  upcoming,
		NewLeads:       newLeads,
		UntouchedLeads: untouched,
		LeadSlaMinutes: boundedInt32(int(sla / time.Minute)),
		GeneratedAt:    timestamppb.New(now),
	}}), nil
}

func appendLimitedTask(tasks []*crmv1.Task, task *domain.Task) []*crmv1.Task {
	if len(tasks) >= myDayItemLimit {
		return tasks
	}
	return append(tasks, taskToProto(task))
}
