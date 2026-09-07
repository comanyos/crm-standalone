package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The PostgreSQL half of the messaging consent ledger (messaging.go,
// migrations/0028_messaging_consent.sql).

const messagingConsentColumns = `id, channel_kind, address, state, source, evidence, recorded_by, recorded_at`

func scanMessagingConsent(row pgx.Row) (*MessagingConsent, error) {
	var v MessagingConsent
	if err := row.Scan(&v.ID, &v.ChannelKind, &v.Address, &v.State,
		&v.Source, &v.Evidence, &v.RecordedBy, &v.RecordedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// RecordConsent appends one decision. There is no update path here and none
// in the Store interface: withdrawing is a new row saying so, which is what
// lets the table answer when a decision was made rather than only what it
// currently is.
func (s *PgStore) RecordConsent(ctx context.Context, scope Scope, v *MessagingConsent) (*MessagingConsent, error) {
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
	if err := record.Validate(); err != nil {
		return nil, err
	}
	if record.ID == "" {
		record.ID = newID()
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = nowStamp()
	}

	out, err := scanMessagingConsent(s.db.QueryRow(ctx, scope,
		`INSERT INTO messaging_consent
		   (tenant_id, id, channel_kind, address, state, source, evidence, recorded_by, recorded_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 RETURNING `+messagingConsentColumns,
		scope.TenantID, record.ID, record.ChannelKind, record.Address, record.State,
		record.Source, record.Evidence, record.RecordedBy, record.RecordedAt))
	if err != nil {
		return nil, fmt.Errorf("record consent: %w", err)
	}
	return out, nil
}

// CurrentConsent returns the newest decision for one pair, or ErrNotFound
// when nobody has decided anything.
//
// The middle ORDER BY term is the one worth explaining. Stamps are truncated
// to the microsecond PostgreSQL keeps (clock.go), so two decisions recorded
// in the same instant tie — and ids are random, so breaking the tie on id
// alone would pick a winner at random. For a consent ledger that means a
// grant could beat a withdrawal recorded at the same moment, and somebody who
// opted out gets messaged. A tie means we do not know which came first, so
// the more restrictive decision wins: withdrawn sorts first.
func (s *PgStore) CurrentConsent(ctx context.Context, scope Scope, channelKind, address string) (*MessagingConsent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	normalized, err := NormalizeChannelAddress(channelKind, address)
	if err != nil {
		return nil, err
	}
	out, err := scanMessagingConsent(s.db.QueryRow(ctx, scope,
		`SELECT `+messagingConsentColumns+` FROM messaging_consent
		  WHERE tenant_id = $1 AND channel_kind = $2 AND address = $3
		  ORDER BY recorded_at DESC, (state = 'withdrawn') DESC, id DESC
		  LIMIT 1`,
		scope.TenantID, channelKind, normalized))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("current consent: %w", err)
	}
	return out, nil
}

// ListConsent returns every decision for one pair, newest first.
func (s *PgStore) ListConsent(ctx context.Context, scope Scope, channelKind, address string) ([]*MessagingConsent, error) {
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	normalized, err := NormalizeChannelAddress(channelKind, address)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, scope,
		`SELECT `+messagingConsentColumns+` FROM messaging_consent
		  WHERE tenant_id = $1 AND channel_kind = $2 AND address = $3
		  ORDER BY recorded_at DESC, (state = 'withdrawn') DESC, id DESC`,
		scope.TenantID, channelKind, normalized)
	if err != nil {
		return nil, fmt.Errorf("list consent: %w", err)
	}
	defer rows.Close()

	out := make([]*MessagingConsent, 0, 4)
	for rows.Next() {
		v, err := scanMessagingConsent(rows)
		if err != nil {
			return nil, fmt.Errorf("list consent: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list consent: %w", err)
	}
	return out, nil
}
