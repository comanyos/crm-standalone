package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWKS rotation and readiness (task.md §1.2).
//
// Everything here turns on the CRM verifying tokens offline against a cached
// key set. That cache is the reason token verification survives an AuthinCore
// blip, and it is also the reason a rotated key, a revoked key, and an
// unreachable provider each behave differently from what "the CRM checks with
// AuthinCore" would suggest. These tests pin each of those behaviours,
// including the ones that are trade-offs rather than wins.

// rotatingJWKS serves a JWK Set whose contents can be changed while a test is
// running, and counts how many times it has been fetched.
type rotatingJWKS struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetches atomic.Int64
	url     string
}

func newRotatingJWKS(t *testing.T) *rotatingJWKS {
	t.Helper()
	j := &rotatingJWKS{keys: map[string]*rsa.PublicKey{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		j.fetches.Add(1)
		j.mu.Lock()
		defer j.mu.Unlock()
		out := make([]map[string]string, 0, len(j.keys))
		for kid, pub := range j.keys {
			out = append(out, jwkFor(kid, pub))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": out})
	}))
	t.Cleanup(srv.Close)
	j.url = srv.URL
	return j
}

// publish adds or replaces a key in the served set.
func (j *rotatingJWKS) publish(kid string, pub *rsa.PublicKey) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.keys[kid] = pub
}

// withdraw removes a key from the served set, as revoking one would.
func (j *rotatingJWKS) withdraw(kid string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.keys, kid)
}

func jwkFor(kid string, pub *rsa.PublicKey) map[string]string {
	eBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(eBuf, uint32(pub.E))
	i := 0
	for i < len(eBuf)-1 && eBuf[i] == 0 {
		i++
	}
	return map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(eBuf[i:]),
	}
}

// signWithKID mints a valid token signed by a specific key.
func signWithKID(t *testing.T, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "u", "iss": "authincore", "org_id": "org-1",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestKeyRotationTakesEffectWithoutARestart is the property an operator needs
// during a rotation: AuthinCore starts signing with a new key, and the CRM
// accepts tokens signed by it without anybody restarting the CRM.
//
// It works because a token whose key id is not cached drives an out-of-band
// refresh. Without that path, a rotation would be invisible until the next
// scheduled refresh, and every user would be signed out in the meantime.
func TestKeyRotationTakesEffectWithoutARestart(t *testing.T) {
	jwks := newRotatingJWKS(t)
	oldKey, newKeyPair := newKey(t), newKey(t)
	jwks.publish("key-1", &oldKey.PublicKey)

	a, err := FromConfig(context.Background(), jwks.url, "authincore", "")
	if err != nil {
		t.Fatalf("build authenticator: %v", err)
	}
	ctx := context.Background()

	if _, err := a.v.verify(ctx, signWithKID(t, oldKey, "key-1")); err != nil {
		t.Fatalf("the original key does not verify: %v", err)
	}

	// AuthinCore rotates: both keys are published, as an overlap window does.
	jwks.publish("key-2", &newKeyPair.PublicKey)

	if _, err := a.v.verify(ctx, signWithKID(t, newKeyPair, "key-2")); err != nil {
		t.Fatalf("a token signed by the newly published key was rejected without a restart: %v", err)
	}

	// And the previous key keeps working while it is still published, which
	// is the whole point of an overlap window — tokens already in users'
	// hands were signed with it.
	if _, err := a.v.verify(ctx, signWithKID(t, oldKey, "key-1")); err != nil {
		t.Errorf("the previous key stopped verifying during the overlap: %v", err)
	}
}

// TestAnUnknownKeyIdIsRejectedRatherThanGuessed covers the other half: a key
// id that is not published anywhere must fail, not fall back to trying every
// cached key.
func TestAnUnknownKeyIdIsRejectedRatherThanGuessed(t *testing.T) {
	jwks := newRotatingJWKS(t)
	real, forged := newKey(t), newKey(t)
	jwks.publish("key-1", &real.PublicKey)

	a, err := FromConfig(context.Background(), jwks.url, "authincore", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := a.v.verify(ctx, signWithKID(t, forged, "key-never-published")); err == nil {
		t.Error("a token naming an unpublished key id was accepted")
	}
	// Signed by a key that is not published, but naming a key id that is.
	if _, err := a.v.verify(ctx, signWithKID(t, forged, "key-1")); err == nil {
		t.Error("a token naming a real key id but signed by another key was accepted")
	}
}

// TestUnknownKeyIdRefreshIsRateLimited proves the refresh triggered by an
// unknown key id cannot be used to amplify requests against AuthinCore. A
// caller who can present tokens can choose the key id in them, so without a
// limiter this path would let them drive one upstream fetch per request.
func TestUnknownKeyIdRefreshIsRateLimited(t *testing.T) {
	jwks := newRotatingJWKS(t)
	key := newKey(t)
	jwks.publish("key-1", &key.PublicKey)

	a, err := FromConfig(context.Background(), jwks.url, "authincore", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Warm the cache and let the first unknown-KID allowance be consumed.
	if _, err := a.v.verify(ctx, signWithKID(t, key, "key-1")); err != nil {
		t.Fatalf("warm-up verify: %v", err)
	}
	forged := newKey(t)
	_, _ = a.v.verify(ctx, signWithKID(t, forged, "forged-kid-0"))

	const attempts = 6
	before := jwks.fetches.Load()
	start := time.Now()
	for i := range attempts {
		_, _ = a.v.verify(ctx, signWithKID(t, forged, "forged-kid-"+string(rune('a'+i))))
	}
	elapsed := time.Since(start)
	fetches := jwks.fetches.Load() - before

	if fetches >= attempts {
		t.Errorf("%d tokens with unknown key ids caused %d upstream fetches; "+
			"the unknown-KID refresh is not rate limited", attempts, fetches)
	}

	// Rate limiting must not be implemented by making the caller wait. The
	// key id comes from the token, so a caller who can be made to block is a
	// caller who can exhaust the server's goroutines with junk tokens.
	if budget := attempts * jwksRateLimitWait; elapsed > budget {
		t.Errorf("%d unknown-key-id verifications took %s, more than the %s bound; "+
			"a request that loses the limiter is blocking rather than failing fast",
			attempts, elapsed.Round(time.Millisecond), budget)
	}
}

// deadJWKS serves 500 until it is switched on, so a test can watch an
// authenticator start degraded and then recover.
func deadJWKS(t *testing.T, kid string, key *rsa.PrivateKey) (url string, revive func()) {
	t.Helper()
	var serving atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !serving.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{jwkFor(kid, &key.PublicKey)},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() { serving.Store(true) }
}

// TestReadyReportsFalseWhileTheKeySetIsEmpty is the readiness contract.
//
// The library swallows a failed first fetch on purpose — an identity provider
// that is briefly unreachable should not crash-loop the CRM — so the
// constructor happily returns an authenticator holding no keys, which then
// rejects every token presented to it. Serving traffic in that state makes an
// outage look to every user like their own credentials are wrong.
func TestReadyReportsFalseWhileTheKeySetIsEmpty(t *testing.T) {
	key := newKey(t)
	url, _ := deadJWKS(t, "key-1", key)

	ctx := context.Background()
	a, err := FromConfig(ctx, url, "authincore", "")
	if err != nil {
		t.Fatalf("the constructor failed rather than starting degraded: %v", err)
	}

	if err := a.Ready(ctx); err == nil {
		t.Fatal("Ready reported ready with an empty key set; the service would accept " +
			"traffic it cannot authenticate")
	}
	// Nothing can be verified in this state, which is why readiness matters.
	if _, err := a.v.verify(ctx, signWithKID(t, key, "key-1")); err == nil {
		t.Error("a token verified against an empty key set")
	}
}

// TestReadyRecoversWithoutARestart is the other half, and the reason Ready
// does more than read the cache. KeyReadAll never fetches, so an instance
// whose first fetch failed would keep reporting not-ready until the next
// scheduled refresh — sitting out of the load balancer long after the
// identity provider came back.
//
// A separate authenticator from the test above on purpose: each has its own
// unknown-KID rate limiter, so this one's first probe is not competing with
// an allowance some earlier assertion already spent.
func TestReadyRecoversWithoutARestart(t *testing.T) {
	key := newKey(t)
	url, revive := deadJWKS(t, "key-1", key)

	ctx := context.Background()
	a, err := FromConfig(ctx, url, "authincore", "")
	if err != nil {
		t.Fatal(err)
	}
	revive()

	if err := a.Ready(ctx); err != nil {
		t.Fatalf("Ready did not fetch the key set once the provider answered: %v", err)
	}
	if _, err := a.v.verify(ctx, signWithKID(t, key, "key-1")); err != nil {
		t.Errorf("verification did not recover after the provider came back: %v", err)
	}
}

// TestDisabledAuthenticatorIsReady records the local-development case: with
// no AUTH_JWKS_URL there is no key set, and no key set to be missing.
func TestDisabledAuthenticatorIsReady(t *testing.T) {
	if err := Disabled().Ready(context.Background()); err != nil {
		t.Errorf("a disabled authenticator reported not ready: %v", err)
	}
}

// TestRevokedKeyKeepsWorkingUntilTheNextRefresh documents a trade-off rather
// than a win, because it is the kind of thing that is only discovered during
// an incident.
//
// Withdrawing a key from the JWK Set does not immediately stop tokens signed
// by it. The key id is still cached, so nothing triggers the unknown-KID
// refresh, and the cache is only corrected by the scheduled refresh —
// jwksRefreshInterval, currently fifteen minutes. Revoking a compromised
// signing key therefore has a window of up to that interval, during which
// already-issued tokens signed by it keep verifying.
//
// Shortening the window means shortening the interval, or adding
// introspection; closing it entirely means giving up offline verification.
// That is a §10.1 decision. What must not happen is somebody assuming
// revocation is instant.
func TestRevokedKeyKeepsWorkingUntilTheNextRefresh(t *testing.T) {
	jwks := newRotatingJWKS(t)
	key := newKey(t)
	jwks.publish("key-1", &key.PublicKey)

	a, err := FromConfig(context.Background(), jwks.url, "authincore", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.v.verify(ctx, signWithKID(t, key, "key-1")); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}

	jwks.withdraw("key-1")

	if _, err := a.v.verify(ctx, signWithKID(t, key, "key-1")); err != nil {
		t.Skipf("a withdrawn key stopped verifying immediately (%v) — the cache behaviour "+
			"changed, and the revocation window documented here no longer exists; "+
			"update docs/ARCHITECTURE.md and delete this test", err)
	}
	t.Logf("a token signed by a withdrawn key still verifies from cache; the revocation "+
		"window is up to jwksRefreshInterval (%s)", jwksRefreshInterval)
}
