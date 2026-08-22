package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MicahParks/jwkset"
	keyfunc "github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// jwksVerifier validates tokens against a remote JWKS (AuthinCore), with auto-refresh.
type jwksVerifier struct {
	kf       keyfunc.Keyfunc
	issuer   string
	audience string
}

// newFromJWKS builds the JWKS-backed authenticator.
//
// The storage is constructed explicitly rather than through
// keyfunc.NewDefaultCtx, which builds an identical one but keeps it to
// itself. Holding the handle is what makes Ready possible: the default
// client sets NoErrorReturnFirstHTTPReq, so a start-up fetch that fails is
// swallowed and this constructor returns a perfectly healthy-looking
// authenticator that holds no keys and rejects every token presented to it.
//
// Swallowing that error is the right default for a resource server — an
// identity provider being briefly unreachable should not crash-loop the CRM,
// and the background refresh will pick the keys up. What is not acceptable is
// reporting ready while it is true, which is what Ready and /readyz exist to
// prevent.
// jwksRefreshInterval is how often the key set is re-fetched in the
// background. The library's default is an hour, which is fine for picking up
// a routine rotation and far too slow for two things that matter more:
// recovering readiness after the identity provider was briefly unreachable at
// start-up, and dropping a revoked key. Fifteen minutes bounds both without
// making this service a meaningful load on AuthinCore.
const jwksRefreshInterval = 15 * time.Minute

// jwksUnknownKIDInterval rate-limits the out-of-band refresh triggered by a
// token whose key id is not in the cache. That path is what makes a key
// rotation take effect immediately rather than at the next scheduled refresh,
// and the limiter is what stops a caller forging key ids from turning it into
// a request amplifier against AuthinCore.
//
// Ten seconds rather than the library's five minutes, because this limiter
// gates two things that both need to be quick: picking up a rotation, and
// recovering readiness after a failed start-up fetch (Ready drives this same
// path). The cost of the shorter interval is bounded at six upstream fetches
// a minute in the worst case, which is not a meaningful load on AuthinCore.
const jwksUnknownKIDInterval = 10 * time.Second

// jwksRateLimitWait bounds how long a request may block waiting for that
// limiter before giving up and failing the token.
//
// The library's default is a full minute, and the default is dangerous here:
// the key id comes from the token, so any caller can choose one that is not
// cached, and each such request would occupy a server goroutine for a minute
// before returning the rejection it was always going to return. A handful of
// those is a cheap way to exhaust the server. One second is long enough for a
// genuine rotation to win the limiter and short enough that losing it costs
// nothing — a token with an unknown key id is overwhelmingly likely to be
// invalid anyway, and the scheduled refresh still picks up a real rotation.
const jwksRateLimitWait = time.Second

func newFromJWKS(ctx context.Context, jwksURL, issuer, audience string) (*Authenticator, error) {
	remote, err := jwkset.NewStorageFromHTTP(jwksURL, jwkset.HTTPClientStorageOptions{
		Ctx: ctx,
		// A failed first fetch must not abort start-up: an identity provider
		// that is briefly unreachable should not crash-loop the CRM. It must
		// also not go unnoticed, which is what Ready and /readyz are for.
		NoErrorReturnFirstHTTPReq: true,
		RefreshInterval:           jwksRefreshInterval,
		RefreshErrorHandler: func(ctx context.Context, err error) {
			slog.ErrorContext(ctx, "failed to refresh the JWK Set; "+
				"token verification will keep using the cached keys until this succeeds",
				"err", err, "jwks_url", jwksURL)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: load JWKS %q: %w", jwksURL, err)
	}
	storage, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{
		HTTPURLs:          map[string]jwkset.Storage{jwksURL: remote},
		RateLimitWaitMax:  jwksRateLimitWait,
		RefreshUnknownKID: rate.NewLimiter(rate.Every(jwksUnknownKIDInterval), 1),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: build JWKS client for %q: %w", jwksURL, err)
	}
	kf, err := keyfunc.New(keyfunc.Options{Storage: storage})
	if err != nil {
		return nil, fmt.Errorf("auth: build keyfunc for JWKS %q: %w", jwksURL, err)
	}
	return &Authenticator{
		v:       &jwksVerifier{kf: kf, issuer: issuer, audience: audience},
		storage: storage,
		jwksURL: jwksURL,
	}, nil
}

// ErrNoSigningKeys reports an authenticator that cannot verify anything
// because its JWK Set is empty.
var ErrNoSigningKeys = errors.New("auth: no signing keys available")

// Ready reports whether this authenticator can verify a token right now.
//
// It is deliberately a readiness signal and not a liveness one. A CRM whose
// JWKS cache is empty is running correctly and will recover on its own once
// the identity provider answers; what it must not do is accept traffic in the
// meantime, because every request it serves will fail authentication and look
// to the caller like their credentials are wrong.
//
// Auth being disabled (local development, no AUTH_JWKS_URL) is ready by
// definition: there is no key set to be missing.
func (a *Authenticator) Ready(ctx context.Context) error {
	if a.disabled {
		return nil
	}
	if a.storage == nil {
		return errors.New("auth: authenticator has no JWK Set storage")
	}
	keys, err := a.storage.KeyReadAll(ctx)
	if err != nil {
		return fmt.Errorf("auth: read JWK Set from %s: %w", a.jwksURL, err)
	}
	if len(keys) > 0 {
		return nil
	}

	// An empty cache is the case worth working at. KeyReadAll reads the cache
	// and never fetches, so an instance whose first fetch failed would report
	// not-ready until the next scheduled refresh — sitting out of the load
	// balancer long after the identity provider came back.
	//
	// Reading a key id that cannot exist drives the library's unknown-KID
	// path, which does refresh. The read itself is expected to fail; the
	// refresh it triggers is the point. Its own rate limiter keeps this from
	// becoming a request amplifier when a readiness probe runs every few
	// seconds.
	_, _ = a.storage.KeyRead(ctx, readinessProbeKID)
	keys, err = a.storage.KeyReadAll(ctx)
	if err != nil {
		return fmt.Errorf("auth: read JWK Set from %s: %w", a.jwksURL, err)
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w from %s", ErrNoSigningKeys, a.jwksURL)
	}
	return nil
}

// readinessProbeKID is a key id no issuer will mint, used only to drive the
// unknown-KID refresh above.
const readinessProbeKID = "crm-readiness-probe-key-id-does-not-exist"

func (v *jwksVerifier) verify(_ context.Context, tokenStr string) (*Principal, error) {
	// WithExpirationRequired is not the parser default, and the default is
	// the wrong one here: this service does not call AuthinCore per request,
	// so a token's expiry is the *only* thing that ends a session after an
	// organization or user is deactivated. A token minted without an exp
	// claim would never expire, making that window unbounded.
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS512", "ES256", "ES384"}),
		jwt.WithExpirationRequired(),
	}
	if v.issuer != "" {
		opts = append(opts, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	tok, err := jwt.Parse(tokenStr, v.kf.Keyfunc, opts...)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return nil, fmt.Errorf("auth: invalid token claims")
	}
	return principalFromClaims(claims), nil
}

func principalFromClaims(c jwt.MapClaims) *Principal {
	orgID := strClaim(c, "org_id")
	tenantID := strClaim(c, "tenant_id")
	if tenantID == "" {
		// AuthinCore's canonical tenancy claim is org_id. The standalone CRM
		// uses that organization as its tenant boundary.
		tenantID = orgID
	}
	return &Principal{
		Subject:  strClaim(c, "sub"),
		TenantID: tenantID,
		OrgID:    orgID,
		Roles:    strsClaim(c, "roles"),
		Scopes:   scopesClaim(c),
	}
}

func strClaim(c jwt.MapClaims, key string) string {
	s, _ := c[key].(string)
	return s
}

// strsClaim reads a claim that may be a JSON array of strings.
func strsClaim(c jwt.MapClaims, key string) []string {
	raw, ok := c[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// scopesClaim accepts either "scopes" (array) or OAuth "scope" (space-delimited string).
func scopesClaim(c jwt.MapClaims) []string {
	if s := strsClaim(c, "scopes"); len(s) > 0 {
		return s
	}
	if s, ok := c["scope"].(string); ok && s != "" {
		return strings.Fields(s)
	}
	return nil
}
