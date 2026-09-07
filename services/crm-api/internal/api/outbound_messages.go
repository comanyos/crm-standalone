package api

import (
	"context"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// The queue of messages an automation drafted (task_crm.md §6,
// internal/domain/outbound_message.go).
//
// The one thing worth noticing is that consent is re-checked on *read*, not
// only when the draft was written. A rule that drafted a message yesterday to
// somebody who opted out this morning must not have that message offered for
// sending today, and the queue is exactly where the gap between drafting and
// sending lives. Filtering the draft out entirely would be worse — a rep who
// saw it yesterday would think the CRM lost it — so it is returned refused,
// with the reason.

func outboundMessageToProto(v *domain.OutboundMessage, mayStillSend bool, refusal string) *crmv1.OutboundMessage {
	if v == nil {
		return nil
	}
	return &crmv1.OutboundMessage{
		Id: v.ID, SubjectType: v.SubjectType, SubjectId: v.SubjectID,
		ChannelKind: v.ChannelKind, Address: v.Address,
		Subject: v.Subject, Body: v.Body, Status: v.Status,
		AssigneeId: v.AssigneeID, RuleVersionId: v.RuleVersionID,
		CreatedAt: optionalTimestamp(v.CreatedAt), SentAt: optionalTimestamp(v.SentAt),
		SentBy: v.SentBy, DiscardReason: v.DiscardReason,
		MayStillSend: mayStillSend, RefusalReason: refusal,
	}
}

// ListOutboundMessages returns the drafting queue.
func (s *CRMService) ListOutboundMessages(
	ctx context.Context, req *connect.Request[crmv1.ListOutboundMessagesRequest],
) (*connect.Response[crmv1.ListOutboundMessagesResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	filter := domain.OutboundMessageFilter{
		Status:      req.Msg.GetStatus(),
		SubjectType: req.Msg.GetSubjectType(),
		SubjectID:   req.Msg.GetSubjectId(),
	}
	if req.Msg.GetMineOnly() {
		filter.AssigneeID = principalSubject(ctx)
	}
	messages, err := s.store.ListOutboundMessages(ctx, scope, filter, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connectErr(err)
	}

	out := make([]*crmv1.OutboundMessage, 0, len(messages))
	for _, message := range messages {
		mayStillSend, refusal := false, ""
		if message.Status == domain.OutboundPending {
			// Only pending drafts are worth the check: a sent message's
			// consent is history, and a discarded one is not going anywhere.
			mayStillSend, refusal, err = domain.MayMessage(ctx, s.store, scope,
				domain.MessagingChannel{
					Kind: message.ChannelKind, Address: message.Address, Reachable: true,
				})
			if err != nil {
				return nil, connectErr(err)
			}
		}
		out = append(out, outboundMessageToProto(message, mayStillSend, refusal))
	}
	return connect.NewResponse(&crmv1.ListOutboundMessagesResponse{Messages: out}), nil
}

// MarkOutboundMessageSent records that a person sent a draft.
//
// The verb is deliberate. This service did not send anything and must not
// claim to — it is recording what a human did, the same honesty the
// click-to-chat timeline entry keeps by saying "opened" rather than "sent".
//
// Consent is re-checked here and not only in the list, because the list is a
// read: a draft could be refused between rendering the queue and pressing the
// button, and this is the last point at which refusing costs nothing.
func (s *CRMService) MarkOutboundMessageSent(
	ctx context.Context, req *connect.Request[crmv1.MarkOutboundMessageSentRequest],
) (*connect.Response[crmv1.MarkOutboundMessageSentResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.GetOutboundMessage(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}

	permitted, refusal, err := domain.MayMessage(ctx, s.store, scope, domain.MessagingChannel{
		Kind: existing.ChannelKind, Address: existing.Address, Reachable: true,
	})
	if err != nil {
		return nil, connectErr(err)
	}
	if !permitted {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			&outboundRefusedError{reason: refusal})
	}

	message, err := s.store.ResolveOutboundMessage(ctx, scope,
		req.Msg.GetId(), domain.OutboundSent, principalSubject(ctx), "")
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.MarkOutboundMessageSentResponse{
		Message: outboundMessageToProto(message, false, ""),
	}), nil
}

// DiscardOutboundMessage drops a draft nobody is going to send.
func (s *CRMService) DiscardOutboundMessage(
	ctx context.Context, req *connect.Request[crmv1.DiscardOutboundMessageRequest],
) (*connect.Response[crmv1.DiscardOutboundMessageResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	message, err := s.store.ResolveOutboundMessage(ctx, scope,
		req.Msg.GetId(), domain.OutboundDiscarded, principalSubject(ctx),
		domain.DiscardReasonManual)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.DiscardOutboundMessageResponse{
		Message: outboundMessageToProto(message, false, ""),
	}), nil
}

// outboundRefusedError carries the bounded refusal code to the client. A
// bounded code rather than a sentence, for the same reason the ledger stores
// one: anything built from the record would put a customer's details in an
// error message.
type outboundRefusedError struct{ reason string }

func (e *outboundRefusedError) Error() string {
	return "this contact may no longer be messaged: " + e.reason
}
