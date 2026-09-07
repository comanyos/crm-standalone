package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

func consentScope() Scope { return Scope{TenantID: "tenant-msg", OrgID: "org-msg"} }

// TestNormalizeChannelAddressCanonicalises pins the property the whole
// consent ledger rests on: two spellings of one destination must produce one
// key. If they do not, a withdrawal recorded against "+1 (555) 010-0000"
// silently fails to stop a message to "+15550100000".
func TestNormalizeChannelAddressCanonicalises(t *testing.T) {
	equivalent := []struct {
		kind string
		a, b string
	}{
		{ChannelSMS, "+1 (555) 010-0000", "+15550100000"},
		{ChannelWhatsApp, "+44 20 7946 0958", "+442079460958"},
		{ChannelVoice, "555.010.0000", "5550100000"},
		{ChannelEmail, "Bob@Example.COM", "bob@example.com"},
		{ChannelEmail, "  bob@example.com  ", "bob@example.com"},
	}
	for _, c := range equivalent {
		gotA, err := NormalizeChannelAddress(c.kind, c.a)
		if err != nil {
			t.Fatalf("normalize %q: %v", c.a, err)
		}
		gotB, err := NormalizeChannelAddress(c.kind, c.b)
		if err != nil {
			t.Fatalf("normalize %q: %v", c.b, err)
		}
		if gotA != gotB {
			t.Errorf("%s: %q normalised to %q but %q to %q — one destination must be one key",
				c.kind, c.a, gotA, c.b, gotB)
		}
	}

	rejected := []struct{ kind, raw string }{
		{ChannelSMS, ""},
		{ChannelSMS, "   "},
		{ChannelSMS, "not a phone"},
		{ChannelSMS, "+"},
		{"carrier-pigeon", "somewhere"},
	}
	for _, c := range rejected {
		if got, err := NormalizeChannelAddress(c.kind, c.raw); err == nil {
			t.Errorf("normalize(%q, %q) = %q, want an error", c.kind, c.raw, got)
		}
	}

	// A '+' anywhere but the front is not a country-code marker, and silently
	// dropping it would merge two different numbers into one consent key.
	if _, err := NormalizeChannelAddress(ChannelSMS, "555+010"); err == nil {
		t.Error("a '+' inside a number was accepted; want an error")
	}
}

// TestContactChannelsDerivesReachability proves the country-code rule: a
// local number produces channels marked unreachable with a reason the UI can
// show, not a wa.me link that opens a chat with the wrong person and not a
// send whose consent could not be keyed.
func TestContactChannelsDerivesReachability(t *testing.T) {
	local := ContactChannels(&Contact{Email: "Bob@Example.com", Phone: "555-010-0000"})
	byKind := map[string]MessagingChannel{}
	for _, c := range local {
		byKind[c.Kind] = c
	}

	if got := byKind[ChannelEmail]; !got.Reachable || got.Address != "bob@example.com" {
		t.Errorf("email channel = %+v, want reachable and lower-cased", got)
	}
	// Every phone channel, not just WhatsApp: a number without a country code
	// cannot be keyed unambiguously in the consent ledger, so it is refused
	// rather than messaged. See TestLocalAndInternationalNumbersCannotDiverge.
	for _, kind := range []string{ChannelSMS, ChannelVoice, ChannelWhatsApp} {
		got := byKind[kind]
		if got.Reachable {
			t.Errorf("%s channel = %+v, want unreachable without a country code", kind, got)
		}
		if got.Reason == "" {
			t.Errorf("an unreachable %s channel must say why", kind)
		}
	}

	international := ContactChannels(&Contact{Phone: "+44 20 7946 0958"})
	for _, c := range international {
		if !c.Reachable {
			t.Errorf("%s channel = %+v, want reachable for a +44 number", c.Kind, c)
		}
		if c.Address != "+442079460958" {
			t.Errorf("%s address = %q, want the normalised form", c.Kind, c.Address)
		}
	}

	// No phone, no phone channels — the UI renders what is here, so an absent
	// field must not produce a button that cannot work.
	emailOnly := ContactChannels(&Contact{Email: "bob@example.com"})
	if len(emailOnly) != 1 || emailOnly[0].Kind != ChannelEmail {
		t.Errorf("channels for an email-only contact = %+v, want just email", emailOnly)
	}
	if got := ContactChannels(nil); got != nil {
		t.Errorf("channels for no contact = %+v, want nil", got)
	}
}

// TestConsentLedgerIsAppendOnlyAndNewestWins proves the ledger answers the
// question it exists for: not only "are they opted out" but "what was decided
// most recently", with the earlier decisions still readable.
func TestConsentLedgerIsAppendOnlyAndNewestWins(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := consentScope()

	if _, err := s.CurrentConsent(ctx, scope, ChannelSMS, "+15550100000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("current consent with no rows: %v, want ErrNotFound (unknown, not refused)", err)
	}

	// Written in a spelling the gate would never look up. The store must
	// normalise on the way in, or the row is invisible to every later read.
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
	if current.State != ConsentGranted {
		t.Errorf("state = %q, want %q", current.State, ConsentGranted)
	}
	if current.Address != "+15550100000" {
		t.Errorf("stored address = %q, want the normalised form", current.Address)
	}

	if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
		ChannelKind: ChannelSMS, Address: "+15550100000",
		State: ConsentWithdrawn, Source: ConsentSourceInbound, Evidence: "inbound STOP",
	}); err != nil {
		t.Fatalf("record withdrawn: %v", err)
	}

	current, err = s.CurrentConsent(ctx, scope, ChannelSMS, "+15550100000")
	if err != nil {
		t.Fatalf("current after withdrawal: %v", err)
	}
	if current.State != ConsentWithdrawn {
		t.Errorf("state = %q, want %q — the newest decision must win", current.State, ConsentWithdrawn)
	}

	// The grant is still there. A ledger that overwrote it could not answer
	// "when did they opt out", which is the reason it is a ledger.
	history, err := s.ListConsent(ctx, scope, ChannelSMS, "+15550100000")
	if err != nil {
		t.Fatalf("list consent: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d rows, want 2 — withdrawing must append, not replace", len(history))
	}
	if history[0].State != ConsentWithdrawn || history[1].State != ConsentGranted {
		t.Errorf("history = %q then %q, want newest first", history[0].State, history[1].State)
	}

	// Consent is per (channel, address): refusing SMS must not refuse email.
	if _, err := s.CurrentConsent(ctx, scope, ChannelEmail, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("email consent after an SMS withdrawal: %v, want ErrNotFound", err)
	}

	// Another tenant's ledger is its own.
	other := Scope{TenantID: "tenant-other", OrgID: "org-other"}
	if _, err := s.CurrentConsent(ctx, other, ChannelSMS, "+15550100000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another tenant reads this consent row: %v, want ErrNotFound", err)
	}
}

// TestSameInstantConsentTieFailsSafe covers the case the wall clock cannot
// order. Stamps are truncated to the microsecond, so two decisions can carry
// the same RecordedAt; ids are random, so a tie broken on id alone would pick
// a winner at random. On a consent ledger a random winner means a grant can
// beat a same-instant withdrawal and somebody who opted out gets messaged.
func TestSameInstantConsentTieFailsSafe(t *testing.T) {
	ctx := context.Background()
	scope := consentScope()
	instant := nowStamp()

	// Both orders of writing, because the bug this guards against is exactly
	// an outcome that depends on incidental ordering.
	for _, order := range [][]string{
		{ConsentGranted, ConsentWithdrawn},
		{ConsentWithdrawn, ConsentGranted},
	} {
		s := NewMemStore()
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
			t.Errorf("writing %v resolved to %q; a tie must resolve to %q — "+
				"we do not know which came first, so the restrictive one wins",
				order, current.State, ConsentWithdrawn)
		}

		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelEmail, Address: "tie@example.com"})
		if err != nil {
			t.Fatalf("MayMessage: %v", err)
		}
		if ok {
			t.Errorf("writing %v permitted a message despite a same-instant withdrawal (reason %q)", order, reason)
		}
	}
}

// TestLocalAndInternationalNumbersCannotDiverge is the regression test for
// the defect that produced the country-code rule.
//
// "+15550100000" and "555 010 0000" normalise to different strings, so they
// are different keys in the consent ledger. That meant a withdrawal recorded
// against one did not stop a message to the other — and editing a contact's
// phone from one format to the other silently restored permission somebody
// had taken away.
//
// Inferring the missing country code would have been worse: guessing +1 for a
// tenant whose customers are elsewhere produces a reachable *different
// person's* number, and then applies one person's consent to another. So the
// ambiguity is refused at both ends instead — a local number can be neither
// messaged nor recorded against.
func TestLocalAndInternationalNumbersCannotDiverge(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := consentScope()

	// A local number cannot enter the ledger at all, so no row can exist
	// under a key the gate would not find.
	if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
		ChannelKind: ChannelSMS, Address: "555 010 0000", State: ConsentWithdrawn,
	}); err == nil {
		t.Fatal("recorded consent against a number with no country code; " +
			"that row would be invisible to a check on the same person's +1 number")
	}

	// And a local number cannot be messaged, so the reverse gap — withdrawn
	// internationally, messaged locally — is closed too.
	if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
		ChannelKind: ChannelSMS, Address: "+15550100000", State: ConsentWithdrawn,
	}); err != nil {
		t.Fatalf("record international withdrawal: %v", err)
	}
	for _, spelling := range []string{"555 010 0000", "5550100000", "(555) 010-0000"} {
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelSMS, Address: spelling})
		if err != nil {
			t.Fatalf("MayMessage(%q): %v", spelling, err)
		}
		if ok {
			t.Errorf("MayMessage(%q) permitted a send despite a withdrawal on the same person's "+
				"international number (reason %q)", spelling, reason)
		}
	}

	// The international spellings all resolve to the withdrawal, whatever
	// punctuation the caller used.
	for _, spelling := range []string{"+15550100000", "+1 (555) 010-0000", "+1-555-010-0000"} {
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelSMS, Address: spelling})
		if err != nil {
			t.Fatalf("MayMessage(%q): %v", spelling, err)
		}
		if ok || reason != RefusalConsentWithdrawn {
			t.Errorf("MayMessage(%q) = %v, %q; want refused with %q", spelling, ok, reason, RefusalConsentWithdrawn)
		}
	}
}

func TestRecordConsentRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	scope := consentScope()

	bad := []struct {
		name string
		in   *MessagingConsent
	}{
		{"unknown channel", &MessagingConsent{ChannelKind: "pigeon", Address: "x@y.com", State: ConsentGranted}},
		{"blank address", &MessagingConsent{ChannelKind: ChannelEmail, Address: "  ", State: ConsentGranted}},
		{"unknown state", &MessagingConsent{ChannelKind: ChannelEmail, Address: "x@y.com", State: "maybe"}},
		{"unknown source", &MessagingConsent{ChannelKind: ChannelEmail, Address: "x@y.com", State: ConsentGranted, Source: "vibes"}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.RecordConsent(ctx, scope, c.in); err == nil {
				t.Fatal("stored a consent row that should have been refused")
			}
		})
	}

	if _, err := s.RecordConsent(ctx, Scope{}, &MessagingConsent{
		ChannelKind: ChannelEmail, Address: "x@y.com", State: ConsentGranted,
	}); !errors.Is(err, ErrNoTenant) {
		t.Errorf("record with no tenant: %v, want ErrNoTenant", err)
	}
}

// failingConsentReader answers every read with an error, to prove the gate
// fails closed. "We could not check" and "we may proceed" must never be the
// same outcome on a path that contacts a member of the public.
type failingConsentReader struct{ err error }

func (f failingConsentReader) CurrentConsent(context.Context, Scope, string, string) (*MessagingConsent, error) {
	return nil, f.err
}
func (f failingConsentReader) SuppressedSyncAddresses(context.Context, Scope) (map[string]bool, error) {
	return nil, f.err
}

func TestMayMessage(t *testing.T) {
	ctx := context.Background()
	scope := consentScope()

	t.Run("unknown consent permits a direct message", func(t *testing.T) {
		s := NewMemStore()
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelSMS, Address: "+15550100000"})
		if err != nil || !ok {
			t.Fatalf("MayMessage = %v, %q, %v; want permitted", ok, reason, err)
		}
	})

	t.Run("withdrawn consent refuses", func(t *testing.T) {
		s := NewMemStore()
		if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
			ChannelKind: ChannelSMS, Address: "+15550100000", State: ConsentWithdrawn,
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
		// Asked in a different spelling than it was stored in, because that
		// is what a UI passing a raw contact field actually does.
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelSMS, Address: "+1 (555) 010-0000"})
		if err != nil {
			t.Fatalf("MayMessage: %v", err)
		}
		if ok || reason != RefusalConsentWithdrawn {
			t.Errorf("MayMessage = %v, %q; want refused with %q", ok, reason, RefusalConsentWithdrawn)
		}
	})

	t.Run("re-granting after a withdrawal permits again", func(t *testing.T) {
		s := NewMemStore()
		// Explicitly separated in time. Two rows sharing an instant tie, and a
		// tie resolves to withdrawn by design
		// (TestSameInstantConsentTieFailsSafe) — so a re-grant only wins when
		// it is actually later, which is what a real re-grant is.
		base := nowStamp()
		for i, state := range []string{ConsentWithdrawn, ConsentGranted} {
			if _, err := s.RecordConsent(ctx, scope, &MessagingConsent{
				ChannelKind: ChannelEmail, Address: "bob@example.com", State: state,
				RecordedAt: base.Add(time.Duration(i) * time.Hour),
			}); err != nil {
				t.Fatalf("record %s: %v", state, err)
			}
		}
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelEmail, Address: "bob@example.com"})
		if err != nil || !ok {
			t.Fatalf("MayMessage = %v, %q, %v; want permitted after re-grant", ok, reason, err)
		}
	})

	t.Run("a suppressed address refuses", func(t *testing.T) {
		s := NewMemStore()
		if err := s.SuppressSyncAddress(ctx, scope, &SyncSuppression{Address: "bob@example.com"}); err != nil {
			t.Fatalf("suppress: %v", err)
		}
		ok, reason, err := MayMessage(ctx, s, scope, MessagingChannel{Kind: ChannelEmail, Address: "Bob@Example.com"})
		if err != nil {
			t.Fatalf("MayMessage: %v", err)
		}
		if ok || reason != RefusalSuppressed {
			t.Errorf("MayMessage = %v, %q; want refused with %q — a person says "+
				"do-not-contact once, not once per list", ok, reason, RefusalSuppressed)
		}
	})

	t.Run("an unusable address refuses before any lookup", func(t *testing.T) {
		s := NewMemStore()
		cases := []struct {
			name    string
			channel MessagingChannel
			want    string
		}{
			{"blank", MessagingChannel{Kind: ChannelSMS, Address: "  "}, RefusalNoAddress},
			{"not a number", MessagingChannel{Kind: ChannelSMS, Address: "call me"}, RefusalUnreachable},
			{"phone without a country code", MessagingChannel{Kind: ChannelSMS, Address: "5550100000"}, RefusalUnreachable},
			{"whatsapp without a country code", MessagingChannel{Kind: ChannelWhatsApp, Address: "5550100000"}, RefusalUnreachable},
			{"not an email", MessagingChannel{Kind: ChannelEmail, Address: "bob-at-example"}, RefusalUnreachable},
		}
		for _, c := range cases {
			ok, reason, err := MayMessage(ctx, s, scope, c.channel)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if ok || reason != c.want {
				t.Errorf("%s: MayMessage = %v, %q; want refused with %q", c.name, ok, reason, c.want)
			}
		}
	})

	t.Run("fails closed when the store errors", func(t *testing.T) {
		r := failingConsentReader{err: errors.New("database unavailable")}
		ok, _, err := MayMessage(ctx, r, scope, MessagingChannel{Kind: ChannelEmail, Address: "bob@example.com"})
		if ok {
			t.Error("MayMessage permitted a send it could not check — it must fail closed")
		}
		if err == nil {
			t.Error("MayMessage swallowed the store error")
		}
	})

	t.Run("refuses without a tenant", func(t *testing.T) {
		s := NewMemStore()
		ok, _, err := MayMessage(ctx, s, Scope{}, MessagingChannel{Kind: ChannelEmail, Address: "bob@example.com"})
		if ok || !errors.Is(err, ErrNoTenant) {
			t.Errorf("MayMessage = %v, %v; want refused with ErrNoTenant", ok, err)
		}
	})
}
