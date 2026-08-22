import { codeChallengeS256, randomToken } from "./pkce.js";
import { decodeAccessToken } from "./jwt.js";
import { consumePending, storePending } from "./pending.js";
import { clearRefreshToken, loadRefreshToken, storeRefreshToken } from "./session-store.js";
import type { AuthSession, OidcConfig, TokenResponse } from "./types.js";
import { OidcError } from "./types.js";

function authorizeUrl(config: OidcConfig, state: string, codeChallenge: string): string {
  const u = new URL("/oauth2/authorize", config.issuer);
  u.searchParams.set("response_type", "code");
  u.searchParams.set("client_id", config.clientId);
  u.searchParams.set("redirect_uri", config.redirectUri);
  u.searchParams.set("scope", config.scope);
  u.searchParams.set("state", state);
  u.searchParams.set("code_challenge", codeChallenge);
  u.searchParams.set("code_challenge_method", "S256");
  u.searchParams.set("org_id", config.orgId);
  return u.toString();
}

/** Starts the top-level Authorization Code + PKCE redirect. `redirectAfter`
 * is the in-app path to return to once the callback completes. */
export async function beginLogin(config: OidcConfig, redirectAfter: string): Promise<void> {
  const state = randomToken();
  const codeVerifier = randomToken();
  const codeChallenge = await codeChallengeS256(codeVerifier);
  storePending(state, { codeVerifier, redirectAfter });
  window.location.assign(authorizeUrl(config, state, codeChallenge));
}

export interface ParsedCallback {
  code?: string;
  state?: string;
  error?: string;
  errorDescription?: string;
}

/** Extracts the OAuth2 callback parameters from the current URL's query
 * string. Does not consume anything — safe to call more than once. */
export function parseCallback(search: string): ParsedCallback {
  const q = new URLSearchParams(search);
  return {
    code: q.get("code") ?? undefined,
    state: q.get("state") ?? undefined,
    error: q.get("error") ?? undefined,
    errorDescription: q.get("error_description") ?? undefined,
  };
}

function toSession(accessToken: string): AuthSession {
  const claims = decodeAccessToken(accessToken);
  // The JWT's own `exp` claim (absolute, seconds since epoch) is the source
  // of truth — it is what the backend enforces, so client-side "is this
  // still valid" checks must agree with it rather than with a separately
  // tracked expires_in counter that could drift.
  return { accessToken, claims, expiresAt: claims.exp * 1000 };
}

async function readTokenError(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string; error_description?: string };
    return body.error_description ?? body.error ?? `token endpoint returned ${res.status}`;
  } catch {
    return `token endpoint returned ${res.status}`;
  }
}

async function postToken(config: OidcConfig, body: URLSearchParams): Promise<TokenResponse> {
  const res = await fetch(new URL("/oauth2/token", config.issuer), {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      "X-Org-ID": config.orgId,
    },
    body,
  });
  if (!res.ok) {
    throw new OidcError("token_exchange_failed", await readTokenError(res));
  }
  return (await res.json()) as TokenResponse;
}

/** Persists the response's refresh token (AuthinCore rotates it on every
 * use — RFC 6749 §10.4 — so the previous one is already invalid) and
 * returns the decoded session for the access token. */
function persistAndDecode(tokens: TokenResponse): AuthSession {
  if (tokens.refresh_token) {
    storeRefreshToken(tokens.refresh_token);
  }
  return toSession(tokens.access_token);
}

/** Completes a callback: validates `state` against the stored pending
 * request (rejecting anything that doesn't match — this is the CSRF check)
 * and exchanges `code` for tokens. Throws OidcError on any failure,
 * including a denied/errored callback, an expired/unknown state, or a
 * failed exchange. */
export async function completeCallback(
  config: OidcConfig,
  callback: ParsedCallback,
): Promise<{ session: AuthSession; redirectAfter: string }> {
  if (callback.error) {
    throw new OidcError(callback.error, callback.errorDescription ?? callback.error);
  }
  if (!callback.code || !callback.state) {
    throw new OidcError("invalid_callback", "callback is missing code or state");
  }
  const pending = consumePending(callback.state);
  if (!pending) {
    throw new OidcError("invalid_state", "no matching login attempt for this state (expired, reused, or forged)");
  }

  const tokens = await postToken(
    config,
    new URLSearchParams({
      grant_type: "authorization_code",
      code: callback.code,
      redirect_uri: config.redirectUri,
      client_id: config.clientId,
      code_verifier: pending.codeVerifier,
    }),
  );
  return { session: persistAndDecode(tokens), redirectAfter: pending.redirectAfter };
}

/** Renews the access token using the refresh token in sessionStorage — a
 * plain background POST, not a redirect or an iframe. AuthinCore's
 * authorization endpoints send `X-Frame-Options: DENY` (correct clickjacking
 * hardening on an identity provider), which rules out the hidden-iframe
 * "silent renew" pattern some SPAs use instead; this is the strategy that
 * actually works against this backend. Throws OidcError (and clears the
 * stored token) if there is none or it has been revoked/expired — callers
 * should treat that as "the user must sign in again," not a surprise.
 *
 * De-duplicates concurrent calls into a single in-flight exchange: the
 * refresh token is single-use (AuthinCore rotates it on every grant), so two
 * overlapping calls would otherwise race — the second would present a token
 * the first had already consumed and get rejected. This is exactly what
 * React 18 StrictMode's intentional double-invocation of effects surfaces in
 * development; a real app must tolerate it too, not just dev mode. */
let inFlightRenewal: Promise<AuthSession> | null = null;

export function renewFromStoredRefreshToken(config: OidcConfig): Promise<AuthSession> {
  if (inFlightRenewal) return inFlightRenewal;

  const run = async (): Promise<AuthSession> => {
    const refreshToken = loadRefreshToken();
    if (!refreshToken) {
      throw new OidcError("no_refresh_token", "no stored refresh token");
    }
    try {
      const tokens = await postToken(
        config,
        new URLSearchParams({
          grant_type: "refresh_token",
          refresh_token: refreshToken,
          client_id: config.clientId,
        }),
      );
      return persistAndDecode(tokens);
    } catch (err) {
      clearRefreshToken();
      throw err;
    }
  };

  inFlightRenewal = run().finally(() => {
    inFlightRenewal = null;
  });
  return inFlightRenewal;
}

/** Revokes the stored refresh token and clears it locally. A revoke-endpoint
 * *failure* must never block logout from completing — but the request itself
 * must still be sent, so `keepalive` keeps it alive past an unload of the
 * document that started it. */
export async function revokeStoredRefreshToken(config: OidcConfig): Promise<void> {
  const refreshToken = loadRefreshToken();
  clearRefreshToken();
  if (!refreshToken) return;
  try {
    await fetch(new URL("/oauth2/revoke", config.issuer), {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ token: refreshToken }),
      keepalive: true,
    });
  } catch {
    // The response is best-effort: the token is already gone from this
    // browser, and it expires naturally server-side.
  }
}

/** Ends the AuthinCore browser session (clears its session cookie) so a
 * later top-level login does not silently resume as the same user without
 * re-entering credentials.
 *
 * This one is not best-effort in the way revoke is: if it never lands, the
 * app has told the user they are signed out while AuthinCore still holds a
 * live session, and the next /authorize re-authenticates them with no
 * password — on a shared machine, into someone else's hands. So it gets
 * both belts: callers await it before showing the signed-out state (see
 * AuthProvider.logout), and `keepalive` keeps it in flight if the document
 * goes away first anyway. Note the keepalive half is the weaker of the two:
 * a DELETE carrying an Authorization header is not a CORS-simple request,
 * and the preflight it needs is not covered by the same guarantee, so the
 * await is what actually makes this reliable.
 *
 * Still resolves rather than throwing on a transport failure: there is no
 * useful client-side recovery, and the caller must not be wedged. */
export async function endServerSession(config: OidcConfig, accessToken: string): Promise<void> {
  try {
    await fetch(new URL("/api/v1/sessions/me", config.issuer), {
      method: "DELETE",
      headers: { Authorization: `Bearer ${accessToken}` },
      keepalive: true,
    });
  } catch {
    // Nothing to recover to; see above.
  }
}

export function isExpiringSoon(session: AuthSession, withinMs = 60_000): boolean {
  return session.expiresAt - Date.now() <= withinMs;
}
