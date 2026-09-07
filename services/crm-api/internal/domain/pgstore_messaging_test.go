package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// The PostgreSQL tier for the messaging consent ledger.
//
// messaging_test.go proves the gate's logic against MemStore. That is the
// store a DATABASE_URL-less deployment and most of the API suite actually
// use, so it has to be right — but it is not the one a production deployment
// runs. These tests hold PgStore to the same observable contract, because a
// divergence here would mean consent behaves differently depending on which
// store a deployment happens to have, and the direction of that divergence is
// "somebody who opted out gets messaged".

func livePgConsentScope(label string) Scope {
	stamp := time.Now().UnixNano()
	return Scope{
		TenantID: fmt.Sprintf("tenant-consent-%s-%d", label, stamp),
		OrgID:    fmt.Sprintf("org-consent-%s-%d", label, stamp),
	}
}

// TestPgConsentLedgerMatchesMemStore walks the same sequence both stores must
// answer identically: unknown, granted, withdrawn, re-granted — asked each
// time in a different spelling than it was written in.
func TestPgConsentLedgerMatchesMemStore(t *testing.T) {
	pg := livePgStore(t)
	ctx := context.Background()

	stores := map[string]Store{"pg": pg, "mem": NewMemStore()}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			scope := livePgConsentScope(name)

			if _, err := s.CurrentConsent(ctx, scope, ChannelSMS, "+15550100000"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("current with no rows: %v, want ErrNotFound", err)
			}

			// Written punctuated, read back plain. If the store did not
			// normalise on write, this row would be invisible to every later
			// read — consent recorded and then silently ignored.
			if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
				ChannelKind: ChannelSMS, Address: "+1 (555) 010-0000",
				State: ConsentGranted, Source: ConsentSourceImport, RecordedBy: "importer",
			}); err != nil {
				t.Fatalf("record granted: %v", err)
			}
			current, err := s.CurrentConsent(ctx, scope, ChannelSMS, "+15550100000")
			if err != nil {
				t.Fatalf("current after grant: %v", err)
			}
			if current.Address != "+15550100000" {
				t.Errorf("stored address = %q, want the normalised form", current.Address)
			}
			if current.State != ConsentGranted {
				t.Errorf("state = %q, want %q", current.State, ConsentGranted)
			}

			// A later decision must win, and the earlier one must survive.
			if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
				ChannelKind: ChannelSMS, Address: "+15550100000",
				State: ConsentWithdrawn, Source: ConsentSourceInbound, Evidence: "inbound STOP",
				RecordedAt: nowStamp().Add(time.Second),
			}); err != nil {
				t.Fatalf("record withdrawn: %v", err)
			}
			current, err = s.CurrentConsent(ctx, scope, ChannelSMS, "+1-555-010-0000")
			if err != nil {
				t.Fatalf("current after withdrawal: %v", err)
			}
			if current.State != ConsentWithdrawn {
				t.Errorf("state = %q, want %q — the newest decision must win", current.State, ConsentWithdrawn)
			}

			history, err := s.ListConsent(ctx, scope, ChannelSMS, "+15550100000")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(history) != 2 {
				t.Fatalf("history has %d rows, want 2 — withdrawing appends, it does not replace", len(history))
			}
			if history[0].State != ConsentWithdrawn || history[1].State != ConsentGranted {
				t.Errorf("history = %q then %q, want newest first", history[0].State, history[1].State)
			}

			// Asked in a third spelling again, because a UI passes whatever
			// the contact record holds. Punctuation must not matter; the
			// country code must be there (see
			// TestLocalAndInternationalNumbersCannotDiverge).
			ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelSMS, Address: "+1 555 010 0000"})
			if err != nil {
				t.Fatalf("MayMessage: %v", err)
			}
			if ok || reason != RefusalConsentWithdrawn {
				t.Errorf("MayMessage = %v, %q; want refused with %q", ok, reason, RefusalConsentWithdrawn)
			}
		})
	}
}

// TestPgSameInstantConsentTieFailsSafe is the PostgreSQL half of the
// tie-break. The ordering lives in SQL there and in a sort comparator in
// MemStore, so it is two implementations of one rule and has to be checked
// twice — a tie that resolved to "granted" here would mean a withdrawal
// recorded in the same microsecond is discarded.
func TestPgSameInstantConsentTieFailsSafe(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()
	instant := nowStamp()

	for i, order := range [][]string{
		{ConsentGranted, ConsentWithdrawn},
		{ConsentWithdrawn, ConsentGranted},
	} {
		scope := livePgConsentScope(fmt.Sprintf("tie-%d", i))
		for _, state := range order {
			if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
				ChannelKind: ChannelEmail, Address: "tie@example.com",
				State: state, RecordedAt: instant,
			}); err != nil {
				t.Fatalf("record %s: %v", state, err)
			}
		}
		current, err := s.CurrentConsent(ctx, scope, ChannelEmail, "tie@example.com")
		if err != nil {
			t.Fatalf("current: %v", err)
		}
		if current.State != ConsentWithdrawn {
			t.Errorf("writing %v resolved to %q; a tie must resolve to %q",
				order, current.State, ConsentWithdrawn)
		}
	}
}

// TestPgConsentIsTenantIsolated proves the ledger obeys RLS through the store
// API, not only through the catalog-driven sweep: one tenant's withdrawal
// must neither be readable by another nor block another's sending.
func TestPgConsentIsTenantIsolated(t *testing.T) {
	s := livePgStore(t)
	ctx := context.Background()

	a := livePgConsentScope("alpha")
	b := livePgConsentScope("beta")

	if _, err := s.RecordConsent(ctx, a, &MessagingConsent{
		ChannelKind: ChannelEmail, Address: "shared@example.com", State: ConsentWithdrawn,
	}); err != nil {
		t.Fatalf("record for alpha: %v", err)
	}

	if _, err := s.CurrentConsent(ctx, b, ChannelEmail, "shared@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("beta reads alpha's consent row: %v, want ErrNotFound", err)
	}
	if rows, err := s.ListConsent(ctx, b, ChannelEmail, "shared@example.com"); err != nil || len(rows) != 0 {
		t.Errorf("beta lists %d of alpha's consent rows (err %v), want 0", len(rows), err)
	}

	// The important half: alpha's refusal is alpha's. Beta may have its own
	// relationship with the same person, and inheriting another
	// organization's withdrawal would be its own kind of data leak.
	ok, reason, err := MayMessage(ctx, s, b, MessagingChannel{Kind: ChannelEmail, Address: "shared@example.com"})
	if err != nil {
		t.Fatalf("MayMessage for beta: %v", err)
	}
	if !ok {
		t.Errorf("beta refused (%q) because alpha withdrew — consent is per tenant", reason)
	}
}
