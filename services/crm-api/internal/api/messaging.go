package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	crmv1 "github.com/santoshguru/crm/gen/crm/v1"
	"github.com/santoshguru/crm/internal/domain"
)

// Messaging channels and consent (task_crm.md §2,
// internal/domain/messaging.go).
//
// The shape worth noting is ListContactChannels: it composes the channel
// derivation and the consent gate into one answer rather than returning the
// address and letting the browser decide. A client that fetched a phone
// number and the consent rows separately would be reimplementing
// domain.MayMessage in TypeScript, and the first divergence between the two
// implementations is a message sent to somebody who opted out.

func consentToProto(v *domain.MessagingConsent) *crmv1.MessagingConsent {
	if v == nil {
		return nil
	}
	return &crmv1.MessagingConsent{
		Id:          v.ID,
		ChannelKind: v.ChannelKind,
		Address:     v.Address,
		State:       v.State,
		Source:      v.Source,
		Evidence:    v.Evidence,
		RecordedBy:  v.RecordedBy,
		RecordedAt:  optionalTimestamp(v.RecordedAt),
	}
}

// ListContactChannels returns every channel one contact can be reached on,
// each carrying the gate's verdict.
func (s *CRMService) ListContactChannels(
	ctx context.Context, req *connect.Request[crmv1.ListContactChannelsRequest],
) (*connect.Response[crmv1.ListContactChannelsResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	// Read through the caller's own scope, so a contact in another tenant is
	// NotFound rather than PermissionDenied — a refusal must not confirm that
	// a foreign id is real.
	contact, err := s.store.GetContact(ctx, scope, req.Msg.GetContactId())
	if err != nil {
		return nil, connectErr(err)
	}

	channels := domain.ContactChannels(contact)
	out := make([]*crmv1.MessagingChannel, 0, len(channels))
	for _, ch := range channels {
		entry := &crmv1.MessagingChannel{
			Kind:              ch.Kind,
			Address:           ch.Address,
			Reachable:         ch.Reachable,
			UnreachableReason: ch.Reason,
		}

		// An unreachable address is refused without consulting the ledger:
		// there is nothing to look it up by. Asking anyway would mean one
		// pointless query per channel per contact detail page.
		if ch.Reachable {
			mayMessage, refusal, err := domain.MayMessage(ctx, s.store, scope, ch)
			if err != nil {
				return nil, connectErr(err)
			}
			entry.MayMessage = mayMessage
			entry.RefusalReason = refusal

			current, err := s.store.CurrentConsent(ctx, scope, ch.Kind, ch.Address)
			switch {
			case errors.Is(err, domain.ErrNotFound):
				// Nobody has decided. Left empty: unknown, not refused.
			case err != nil:
				return nil, connectErr(err)
			default:
				entry.ConsentState = current.State
			}
		} else {
			entry.RefusalReason = domain.RefusalUnreachable
		}

		out = append(out, entry)
	}
	return connect.NewResponse(&crmv1.ListContactChannelsResponse{Channels: out}), nil
}

// RecordMessagingConsent appends one decision to the ledger.
func (s *CRMService) RecordMessagingConsent(
	ctx context.Context, req *connect.Request[crmv1.RecordMessagingConsentRequest],
) (*connect.Response[crmv1.RecordMessagingConsentResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	source := req.Msg.GetSource()
	if source == "" {
		// A decision recorded through this RPC was made by a person using the
		// application. Defaulting rather than requiring it keeps the client
		// simple; leaving it blank would lose the one thing the column is for.
		source = domain.ConsentSourceManual
	}
	consent, err := s.store.RecordConsent(ctx, scope, &domain.MessagingConsent{
		ChannelKind: req.Msg.GetChannelKind(),
		Address:     req.Msg.GetAddress(),
		State:       req.Msg.GetState(),
		Source:      source,
		Evidence:    req.Msg.GetEvidence(),
		RecordedBy:  principalSubject(ctx),
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&crmv1.RecordMessagingConsentResponse{
		Consent: consentToProto(consent),
	}), nil
}

// ListMessagingConsent returns the decision history for one address on one
// channel, newest first.
func (s *CRMService) ListMessagingConsent(
	ctx context.Context, req *connect.Request[crmv1.ListMessagingConsentRequest],
) (*connect.Response[crmv1.ListMessagingConsentResponse], error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListConsent(ctx, scope, req.Msg.GetChannelKind(), req.Msg.GetAddress())
	if err != nil {
		return nil, connectErr(err)
	}
	out := make([]*crmv1.MessagingConsent, 0, len(rows))
	for _, v := range rows {
		out = append(out, consentToProto(v))
	}
	return connect.NewResponse(&crmv1.ListMessagingConsentResponse{Consent: out}), nil
}
