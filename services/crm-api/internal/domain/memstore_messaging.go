package domain

import (
	"context"
	"sort"
)

// The in-memory half of the messaging consent ledger (messaging.go,
// migrations/0028_messaging_consent.sql).

// RecordConsent appends one decision. The address is normalised here rather
// than trusted from the caller, so a row can never be stored under a spelling
// CurrentConsent would not find — which would look exactly like consent that
// was recorded and then ignored.
func (s *MemStore) RecordConsent(_ context.Context, scope Scope, v *MessagingConsent) (*MessagingConsent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	if v == nil {
		return nil, ErrInvalid
	}
	record := cloneMessagingConsent(v)
	normalized, err := NormalizeChannelAddress(record.ChannelKind, record.Address)
	if err != nil {
		return nil, err
	}
	record.Address = normalized
	if record.RecordedAt.IsZero() {
		record.RecordedAt = nowStamp()
	}
	return s.consent.create(scope, record)
}

// CurrentConsent returns the newest decision for one address on one channel,
// or ErrNotFound when nobody has decided anything — which MayMessage reads as
// "unknown", not as a refusal.
func (s *MemStore) CurrentConsent(_ context.Context, scope Scope, channelKind, address string) (*MessagingConsent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	normalized, err := NormalizeChannelAddress(channelKind, address)
	if err != nil {
		return nil, err
	}
	rows, err := s.consentFor(scope, channelKind, normalized)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

// ListConsent returns every decision recorded for one address on one channel,
// newest first. This is the history a support or compliance question needs:
// not "are they opted out" but "when, by whom, and on what evidence".
func (s *MemStore) ListConsent(_ context.Context, scope Scope, channelKind, address string) ([]*MessagingConsent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	normalized, err := NormalizeChannelAddress(channelKind, address)
	if err != nil {
		return nil, err
	}
	return s.consentFor(scope, channelKind, normalized)
}

// consentFor returns the tenant's rows for one pair, newest first.
//
// The tie-break is the same one pgstore_messaging.go's ORDER BY applies, and
// for the same reason: stamps are truncated to the microsecond (clock.go), so
// two decisions recorded in the same instant tie, and ids are random. Breaking
// a tie on id alone would pick a winner at random, which for consent means a
// grant could beat a same-instant withdrawal and somebody who opted out gets
// messaged. A tie means we do not know which came first, so the more
// restrictive decision wins. Both stores must agree, or the answer would
// depend on which one a deployment happens to use.
func (s *MemStore) consentFor(scope Scope, channelKind, normalized string) ([]*MessagingConsent, error) {
	all, err := s.consent.all(scope)
	if err != nil {
		return nil, err
	}
	out := make([]*MessagingConsent, 0, 4)
	for _, row := range all {
		if row.ChannelKind == channelKind && row.Address == normalized {
			out = append(out, cloneMessagingConsent(row))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.After(out[j].RecordedAt)
		}
		if (out[i].State == ConsentWithdrawn) != (out[j].State == ConsentWithdrawn) {
			return out[i].State == ConsentWithdrawn
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}
