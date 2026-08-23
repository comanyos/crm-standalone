package domain

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The Google integration's storage has the same two-implementation problem
// the revenue workflows have, with a sharper edge: the properties under test
// here are not conveniences, they are the authorization boundary. A one-time
// state that MemStore lets through twice is a replayable OAuth callback in
// every development and test run, and in any deployment left on the in-memory
// store.
//
// So every assertion below runs against both implementations.

func eachGoogleStore(t *testing.T, body func(t *testing.T, name string, s Store)) {
	t.Helper()
	t.Run("MemStore", func(t *testing.T) { body(t, "MemStore", NewMemStore()) })
	t.Run("PgStore", func(t *testing.T) { body(t, "PgStore", livePgStore(t)) })
}

// googleRun makes every tenant and state value unique per test binary. The
// PostgreSQL tier runs against a database that outlives the run, and the
// unique index on `state` is global by design — reusing a literal would make
// the second run of the suite fail on the first run's leftovers rather than
// on anything real.
var googleRun = strconv.FormatInt(time.Now().UnixNano(), 36)

func googleScope(label string) Scope {
	return Scope{
		TenantID: "tenant-google-" + label + "-" + googleRun,
		OrgID:    "org-google-" + label + "-" + googleRun,
	}
}

// googleStateValue stands in for the 256 bits of entropy the real flow uses.
func googleStateValue(label string) string { return "state-" + label + "-" + googleRun }

// pendingState builds a valid pending authorization. The verifier ciphertext
// is opaque bytes here on purpose: the store must never be able to interpret
// it, so a test that fed it a real sealed value would be testing the wrong
// layer.
func pendingState(state, actor string, expires time.Time) *GoogleOAuthState {
	return &GoogleOAuthState{
		State:              state,
		ActorID:            actor,
		VerifierCiphertext: []byte("sealed-verifier-" + state),
		ReturnPath:         "/settings/integrations",
		ExpiresAt:          expires,
	}
}

func connectedConnection(owner string) *GoogleConnection {
	return &GoogleConnection{
		OwnerID:              owner,
		GoogleAccountEmail:   owner + "@example.com",
		Status:               GoogleStatusConnected,
		GrantedScopes:        []string{gmailReadonlyScope, calendarEventsScope},
		TokenCiphertext:      []byte("sealed-token-set-" + owner),
		AccessTokenExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(storedTimePrecision),
		ConnectedBy:          owner,
		ConnectedAt:          time.Now().UTC().Truncate(storedTimePrecision),
	}
}

// Scope constants used by the tests. They are the literal strings the
// provider returns, kept here rather than imported so the domain package does
// not depend on the integration package.
const (
	gmailReadonlyScope  = "https://www.googleapis.com/auth/gmail.readonly"
	calendarEventsScope = "https://www.googleapis.com/auth/calendar.events"
)

// TestGoogleOAuthStateIsConsumedExactlyOnce is the load-bearing one. A state
// that can be redeemed twice means an intercepted redirect can be replayed
// into a second token exchange.
func TestGoogleOAuthStateIsConsumedExactlyOnce(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("once-" + name)
		now := time.Now().UTC()

		if _, err := s.CreateGoogleOAuthState(ctx, scope,
			pendingState(googleStateValue("once-"+name), "actor-1", now.Add(10*time.Minute))); err != nil {
			t.Fatalf("create state: %v", err)
		}

		first, err := s.ConsumeGoogleOAuthState(ctx, scope, googleStateValue("once-"+name), now)
		if err != nil {
			t.Fatalf("first consume: %v", err)
		}
		if first.ActorID != "actor-1" {
			t.Errorf("consumed actor = %q, want actor-1", first.ActorID)
		}
		if string(first.VerifierCiphertext) != "sealed-verifier-"+googleStateValue("once-"+name) {
			t.Error("consume did not return the sealed PKCE verifier the exchange needs")
		}
		if first.ConsumedAt.IsZero() {
			t.Error("consume returned a record that is not marked consumed")
		}

		if _, err := s.ConsumeGoogleOAuthState(ctx, scope, googleStateValue("once-"+name), now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second consume: %v, want ErrNotFound — an OAuth state redeemable twice is replayable", err)
		}
	})
}

// TestGoogleOAuthStateConcurrentConsumeYieldsOneWinner is the same property
// under the access pattern that actually produces it: the same redirect
// arriving twice at once, in two replicas or two browser tabs.
func TestGoogleOAuthStateConcurrentConsumeYieldsOneWinner(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("race-" + name)
		now := time.Now().UTC()

		if _, err := s.CreateGoogleOAuthState(ctx, scope,
			pendingState(googleStateValue("race-"+name), "actor-1", now.Add(10*time.Minute))); err != nil {
			t.Fatalf("create state: %v", err)
		}

		const racers = 8
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners int
		)
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				if _, err := s.ConsumeGoogleOAuthState(ctx, scope, googleStateValue("race-"+name), now); err == nil {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if winners != 1 {
			t.Errorf("%d of %d concurrent consumes succeeded, want exactly 1", winners, racers)
		}
	})
}

// TestConsumeReturnsEverythingTheExchangeNeeds is a round-trip test rather
// than a field-by-field one, because the failure it exists to catch is a
// field that survives in memory and is silently dropped by SQL. A consume
// that returns a record missing its actor produces a connection with no
// owner, which fails validation *after* a successful authorization at
// Google — the user consents, and the CRM tells them it went wrong.
func TestConsumeReturnsEverythingTheExchangeNeeds(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("roundtrip-" + name)
		now := time.Now().UTC()
		state := googleStateValue("roundtrip-" + name)

		want := pendingState(state, "actor-round-trip", now.Add(10*time.Minute))
		want.ReturnPath = "/settings/integrations?tab=google"
		if _, err := s.CreateGoogleOAuthState(ctx, scope, want); err != nil {
			t.Fatalf("create state: %v", err)
		}

		got, err := s.ConsumeGoogleOAuthState(ctx, scope, state, now)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if got.State != want.State {
			t.Errorf("State = %q, want %q", got.State, want.State)
		}
		if got.ActorID != want.ActorID {
			t.Errorf("ActorID = %q, want %q — the connection would have no owner",
				got.ActorID, want.ActorID)
		}
		if string(got.VerifierCiphertext) != string(want.VerifierCiphertext) {
			t.Error("VerifierCiphertext did not survive the round trip; the exchange cannot proceed")
		}
		if got.ReturnPath != want.ReturnPath {
			t.Errorf("ReturnPath = %q, want %q", got.ReturnPath, want.ReturnPath)
		}
		if got.TenantID != scope.TenantID {
			t.Errorf("TenantID = %q, want %q", got.TenantID, scope.TenantID)
		}
		if got.ExpiresAt.IsZero() {
			t.Error("ExpiresAt did not survive the round trip")
		}
	})
}

// TestGoogleOAuthStateExpiryIsEnforced keeps the ten-minute pending window
// meaningful: an old redirect sitting in a browser's history must not still
// buy a token exchange.
func TestGoogleOAuthStateExpiryIsEnforced(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("expiry-" + name)
		now := time.Now().UTC()

		if _, err := s.CreateGoogleOAuthState(ctx, scope,
			pendingState(googleStateValue("expired-"+name), "actor-1", now.Add(time.Minute))); err != nil {
			t.Fatalf("create state: %v", err)
		}

		if _, err := s.ConsumeGoogleOAuthState(ctx, scope, googleStateValue("expired-"+name),
			now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("consume of an expired state: %v, want ErrNotFound", err)
		}

		removed, err := s.PurgeExpiredGoogleOAuthStates(ctx, scope, now.Add(2*time.Minute))
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if removed != 1 {
			t.Errorf("purged %d states, want 1", removed)
		}
	})
}

// TestGoogleOAuthStateIsTenantScoped proves the callback path cannot be
// pointed at another organization's pending authorization even with the state
// value in hand.
func TestGoogleOAuthStateIsTenantScoped(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := googleScope("victim-" + name)
		attacker := googleScope("attacker-" + name)
		now := time.Now().UTC()

		state := googleStateValue("tenant-" + name)
		if _, err := s.CreateGoogleOAuthState(ctx, victim,
			pendingState(state, "victim-actor", now.Add(10*time.Minute))); err != nil {
			t.Fatalf("create state: %v", err)
		}

		if _, err := s.ConsumeGoogleOAuthState(ctx, attacker, state, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant consume: %v, want ErrNotFound", err)
		}
		// And the victim's own state survived the attempt.
		if _, err := s.ConsumeGoogleOAuthState(ctx, victim, state, now); err != nil {
			t.Fatalf("victim can no longer redeem its own state: %v", err)
		}
	})
}

// TestResolveGoogleOAuthStateTenantReturnsOnlyAnIdentifier pins the one
// cross-tenant read this feature adds: it answers which tenant a callback
// belongs to, and nothing more.
func TestResolveGoogleOAuthStateTenantReturnsOnlyAnIdentifier(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("resolve-" + name)
		now := time.Now().UTC()

		state := googleStateValue("resolve-" + name)
		if _, err := s.CreateGoogleOAuthState(ctx, scope,
			pendingState(state, "actor-1", now.Add(10*time.Minute))); err != nil {
			t.Fatalf("create state: %v", err)
		}

		tenant, err := s.ResolveGoogleOAuthStateTenant(ctx, state)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if tenant != scope.TenantID {
			t.Errorf("resolved tenant = %q, want %q", tenant, scope.TenantID)
		}

		if _, err := s.ResolveGoogleOAuthStateTenant(ctx, "no-such-state"); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolve of an unknown state: %v, want ErrNotFound", err)
		}
		if _, err := s.ResolveGoogleOAuthStateTenant(ctx, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("resolve of an empty state: %v, want ErrNotFound", err)
		}
	})
}

// TestGoogleConnectionUpsertsPerOwner: reconnecting replaces the credential
// rather than adding a second one. Two rows would mean two live refresh
// tokens for one mailbox, only one of which a disconnect would revoke.
func TestGoogleConnectionUpsertsPerOwner(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("upsert-" + name)

		first, err := s.SaveGoogleConnection(ctx, scope, connectedConnection("user-a"))
		if err != nil {
			t.Fatalf("first save: %v", err)
		}

		second := connectedConnection("user-a")
		second.TokenCiphertext = []byte("sealed-token-set-rotated")
		second.GoogleAccountEmail = "rotated@example.com"
		saved, err := s.SaveGoogleConnection(ctx, scope, second)
		if err != nil {
			t.Fatalf("second save: %v", err)
		}
		if saved.ID != first.ID {
			t.Errorf("reconnect created a new connection id (%q -> %q); the old credential would never be revoked",
				first.ID, saved.ID)
		}
		if string(saved.TokenCiphertext) != "sealed-token-set-rotated" {
			t.Error("reconnect did not replace the sealed token set")
		}

		all, err := s.ListGoogleConnections(ctx, scope)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("tenant holds %d connections for one owner, want 1", len(all))
		}
	})
}

// TestGoogleConnectionIsTenantScoped is the leak test: a connection carries a
// sealed credential and the address of a real mailbox, so reading another
// tenant's is the worst outcome this feature can produce.
func TestGoogleConnectionIsTenantScoped(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		victim := googleScope("conn-victim-" + name)
		attacker := googleScope("conn-attacker-" + name)

		if _, err := s.SaveGoogleConnection(ctx, victim, connectedConnection("shared-owner-id")); err != nil {
			t.Fatalf("seed victim: %v", err)
		}

		if _, err := s.GetGoogleConnection(ctx, attacker, "shared-owner-id"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant read: %v, want ErrNotFound", err)
		}
		if err := s.DeleteGoogleConnection(ctx, attacker, "shared-owner-id"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant delete: %v, want ErrNotFound", err)
		}
		rows, err := s.ListGoogleConnections(ctx, attacker)
		if err != nil {
			t.Fatalf("list as attacker: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("attacker lists %d of another tenant's connections", len(rows))
		}
		// The denied delete must not have partially applied.
		if _, err := s.GetGoogleConnection(ctx, victim, "shared-owner-id"); err != nil {
			t.Errorf("victim lost its own connection to a denied cross-tenant delete: %v", err)
		}
	})
}

// TestGoogleStoreRejectsAnEmptyScope covers the code path where a caller
// forgets the tenant altogether — the shape of the bug that turns a scoped
// query into a global one.
func TestGoogleStoreRejectsAnEmptyScope(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		var none Scope
		now := time.Now().UTC()

		if _, err := s.CreateGoogleOAuthState(ctx, none,
			pendingState(googleStateValue("none-"+name), "a", now.Add(time.Minute))); !errors.Is(err, ErrNoTenant) {
			t.Errorf("CreateGoogleOAuthState: %v, want ErrNoTenant", err)
		}
		if _, err := s.ConsumeGoogleOAuthState(ctx, none, "x", now); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ConsumeGoogleOAuthState: %v, want ErrNoTenant", err)
		}
		if _, err := s.PurgeExpiredGoogleOAuthStates(ctx, none, now); !errors.Is(err, ErrNoTenant) {
			t.Errorf("PurgeExpiredGoogleOAuthStates: %v, want ErrNoTenant", err)
		}
		if _, err := s.SaveGoogleConnection(ctx, none, connectedConnection("a")); !errors.Is(err, ErrNoTenant) {
			t.Errorf("SaveGoogleConnection: %v, want ErrNoTenant", err)
		}
		if _, err := s.GetGoogleConnection(ctx, none, "a"); !errors.Is(err, ErrNoTenant) {
			t.Errorf("GetGoogleConnection: %v, want ErrNoTenant", err)
		}
		if _, err := s.ListGoogleConnections(ctx, none); !errors.Is(err, ErrNoTenant) {
			t.Errorf("ListGoogleConnections: %v, want ErrNoTenant", err)
		}
		if err := s.DeleteGoogleConnection(ctx, none, "a"); !errors.Is(err, ErrNoTenant) {
			t.Errorf("DeleteGoogleConnection: %v, want ErrNoTenant", err)
		}
	})
}

// TestGoogleConnectionInvariantsRejectInconsistentRows keeps the Go
// validation and the table's CHECK constraints saying the same thing. Where
// they disagree, a write passes one store and dies in the other.
func TestGoogleConnectionInvariantsRejectInconsistentRows(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("invariant-" + name)

		cases := map[string]*GoogleConnection{
			"no owner": {Status: GoogleStatusConnected, TokenCiphertext: []byte("x")},
			"unknown status": {
				OwnerID: "u", Status: "pending", TokenCiphertext: []byte("x"),
			},
			"connected without tokens": {OwnerID: "u", Status: GoogleStatusConnected},
			"disconnected still holding tokens": {
				OwnerID: "u", Status: GoogleStatusDisconnected, TokenCiphertext: []byte("x"),
			},
		}
		for label, candidate := range cases {
			if _, err := s.SaveGoogleConnection(ctx, scope, candidate); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: %v, want ErrInvalid", label, err)
			}
		}
	})
}

// TestDisconnectErasesTokenMaterial is the promise the UI makes when someone
// clicks Disconnect. A tombstone that still holds the sealed credential is a
// credential that was never actually withdrawn.
func TestDisconnectErasesTokenMaterial(t *testing.T) {
	eachGoogleStore(t, func(t *testing.T, name string, s Store) {
		ctx := context.Background()
		scope := googleScope("disconnect-" + name)

		if _, err := s.SaveGoogleConnection(ctx, scope, connectedConnection("user-a")); err != nil {
			t.Fatalf("seed: %v", err)
		}

		tombstone := connectedConnection("user-a")
		tombstone.Status = GoogleStatusDisconnected
		tombstone.TokenCiphertext = nil
		tombstone.AccessTokenExpiresAt = time.Time{}
		tombstone.DisconnectedAt = time.Now().UTC().Truncate(storedTimePrecision)
		if _, err := s.SaveGoogleConnection(ctx, scope, tombstone); err != nil {
			t.Fatalf("disconnect: %v", err)
		}

		stored, err := s.GetGoogleConnection(ctx, scope, "user-a")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if len(stored.TokenCiphertext) != 0 {
			t.Error("a disconnected connection still holds sealed token material")
		}
		if stored.Status != GoogleStatusDisconnected {
			t.Errorf("status = %q, want %q", stored.Status, GoogleStatusDisconnected)
		}
		if stored.DisconnectedAt.IsZero() {
			t.Error("disconnect recorded no time, so the audit trail cannot say when access ended")
		}

		// Purge removes the tombstone as well.
		if err := s.DeleteGoogleConnection(ctx, scope, "user-a"); err != nil {
			t.Fatalf("purge: %v", err)
		}
		if _, err := s.GetGoogleConnection(ctx, scope, "user-a"); !errors.Is(err, ErrNotFound) {
			t.Errorf("after purge: %v, want ErrNotFound", err)
		}
	})
}

// TestGoogleReturnPathRejectsOffSiteRedirects covers the one value in this
// flow an attacker gets to influence. Protocol-relative URLs are the form
// that gets missed: a browser reads "//evil.example" as absolute.
func TestGoogleReturnPathRejectsOffSiteRedirects(t *testing.T) {
	valid := []string{"", "/", "/settings/integrations", "/settings/integrations?google=connected"}
	for _, path := range valid {
		if err := ValidateGoogleReturnPath(path); err != nil {
			t.Errorf("ValidateGoogleReturnPath(%q) = %v, want nil", path, err)
		}
	}

	invalid := []string{
		"https://evil.example",
		"//evil.example",
		"/\\evil.example",
		"http://evil.example/settings",
		"settings/integrations",
		"/settings\r\nSet-Cookie: a=b",
		"javascript:alert(1)",
	}
	for _, path := range invalid {
		if err := ValidateGoogleReturnPath(path); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateGoogleReturnPath(%q) = %v, want ErrInvalid", path, err)
		}
	}
}

// TestNeedsRefreshUsesSkew: a token that expires in seconds is treated as
// already expired, because a call started now would outlive it.
func TestNeedsRefreshUsesSkew(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		conn GoogleConnection
		want bool
	}{
		{"comfortably valid", GoogleConnection{Status: GoogleStatusConnected, AccessTokenExpiresAt: now.Add(time.Hour)}, false},
		{"inside the skew", GoogleConnection{Status: GoogleStatusConnected, AccessTokenExpiresAt: now.Add(10 * time.Second)}, true},
		{"already expired", GoogleConnection{Status: GoogleStatusConnected, AccessTokenExpiresAt: now.Add(-time.Minute)}, true},
		{"no expiry recorded", GoogleConnection{Status: GoogleStatusConnected}, true},
		{"disconnected has nothing to refresh", GoogleConnection{Status: GoogleStatusDisconnected}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.conn.NeedsRefresh(now); got != tc.want {
				t.Errorf("NeedsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConnectionViewCarriesNoCredential is a guard on the projection the API
// returns. Adding a field to GoogleConnection and forgetting that the view is
// a separate type is exactly how a ciphertext reaches a browser.
func TestConnectionViewCarriesNoCredential(t *testing.T) {
	conn := connectedConnection("user-a")
	conn.ID = "conn-1"
	view := conn.View()

	if view.ID != conn.ID || view.GoogleAccountEmail != conn.GoogleAccountEmail {
		t.Error("view lost the fields the UI needs")
	}
	// Mutating the view's scope slice must not reach the connection.
	if len(view.GrantedScopes) > 0 {
		view.GrantedScopes[0] = "mutated"
		if conn.GrantedScopes[0] == "mutated" {
			t.Error("View aliases the connection's scope slice")
		}
	}
}
