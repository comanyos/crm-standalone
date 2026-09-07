package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/gmail"
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

// SendOutboundMessage delivers a drafted message through the caller's own
// connected Gmail account (task_crm.md §4, the gmail.send rung).
//
// This is the only place this service sends anything, and the order of what
// it does is the whole of its correctness:
//
//  1. Read the draft under the caller's scope, so another tenant's id is
//     NotFound rather than sent — before even checking whether this
//     deployment can send, or the refusal answers a question about us
//     instead of refusing them.
//  2. Re-check consent — before anything about this deployment's own
//     capabilities. The draft may be days old; this is the last moment
//     refusing costs nothing, and the moment after it costs a message to
//     somebody who opted out. It also comes first because "they opted out"
//     stops a user sending by hand, which "we cannot send mail" does not.
//  3. Claim the draft — mark it sent — *before* calling Gmail.
//  4. Send.
//
// Step three before step four is the part worth defending. Claiming first
// means a crash between the claim and the send leaves a message marked sent
// that was not; claiming after would mean a crash leaves one sent but still
// pending, which a rep or a retry then sends again. Between "possibly not
// delivered" and "possibly delivered twice", only the second is unrecoverable
// from the recipient's side — so the claim goes first, and a failed send says
// plainly that the draft is marked sent and may not have gone.
func (s *CRMService) SendOutboundMessage(
	ctx context.Context, req *connect.Request[crmv1.SendOutboundMessageRequest],
) (*connect.Response[crmv1.SendOutboundMessageResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	// The tenant check comes before the configuration check, and the order is
	// not cosmetic: reversed, a caller naming another organization's draft id
	// gets "sending is not configured" instead of "no such message" — which
	// answers a question about this deployment rather than refusing theirs.
	// Found by the cross-organization probe, which expects NotFound.
	draft, err := s.store.GetOutboundMessage(ctx, scope, req.Msg.GetId())
	if err != nil {
		return nil, connectErr(err)
	}
	// Consent before anything about this deployment's capabilities. If this
	// person opted out, that is the fact the user needs — it stops them
	// sending by hand as well, which "we cannot send mail" would not. Getting
	// this order wrong is how a refusal for the wrong reason ships.
	permitted, refusal, err := domain.MayMessage(ctx, s.store, scope, domain.MessagingChannel{
		Kind: draft.ChannelKind, Address: draft.Address, Reachable: true,
	})
	if err != nil {
		return nil, connectErr(err)
	}
	if !permitted {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			&outboundRefusedError{reason: refusal})
	}

	if draft.ChannelKind != domain.ChannelEmail {
		// Only email can be sent from here. SMS and WhatsApp need a provider
		// this deployment does not have; the rep sends those by hand.
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("only email messages can be sent from the CRM; open the app for this one"))
	}
	if s.gmailSender == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this deployment cannot send mail: no Google integration is configured "+
				"(see docs/GOOGLE_INTEGRATION.md). Send it yourself and mark it sent instead"))
	}

	// Claimed before the send — see the doc comment for why this order.
	sent, err := s.store.ResolveOutboundMessage(ctx, scope,
		draft.ID, domain.OutboundSent, principalSubject(ctx), "")
	if err != nil {
		return nil, connectErr(err)
	}

	providerID, sendErr := s.gmailSender.Send(ctx, scope, principalSubject(ctx), gmail.SendRequest{
		To: draft.Address, Subject: draft.Subject, Body: draft.Body,
	})
	if sendErr != nil {
		// The draft stays marked sent. Saying so is the honest report: the
		// alternative is releasing it, and a released draft that Gmail
		// actually delivered gets sent twice.
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("the message is marked sent but delivery could not be confirmed; "+
				"check the mailbox before resending: %w", sendErr))
	}

	// Recorded on the record's own timeline, not only in the queue: "we
	// emailed this person" belongs where somebody reading the contact will
	// look, and it is the same place a hand-sent message is recorded.
	if _, err := s.store.CreateActivity(ctx, scope, &domain.Activity{
		Kind:        "email",
		SubjectType: draft.SubjectType,
		SubjectID:   draft.SubjectID,
		Notes:       "Sent from the CRM to " + draft.Address + "\n\n" + draft.Body,
		ActorID:     principalSubject(ctx),
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		// The message is gone; a missing timeline entry is not worth failing
		// the call over and telling the user to try again.
		slog.Warn("a sent message could not be recorded on the timeline",
			"tenant", scope.TenantID, "message", draft.ID)
	}

	return connect.NewResponse(&crmv1.SendOutboundMessageResponse{
		Message:           outboundMessageToProto(sent, false, ""),
		ProviderMessageId: providerID,
	}), nil
}
