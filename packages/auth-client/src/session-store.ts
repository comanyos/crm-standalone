/** Persists the OAuth2 refresh token across page reloads. sessionStorage
 * (not localStorage): cleared when the tab/browser closes, unlike
 * localStorage, while still surviving a reload — which is what makes
 * "reload while the session is valid" work at all.
 *
 * AuthinCore's authorization endpoints set `X-Frame-Options: DENY` (correct
 * clickjacking hardening on an identity provider — not something to weaken
 * for client convenience), which rules out the hidden-iframe "silent renew"
 * pattern some SPAs use instead. The standard `grant_type=refresh_token`
 * exchange (a plain background fetch, no iframe, no framing involved) is the
 * strategy this architecture actually supports; storing the refresh token
 * is the tradeoff that makes it possible. The access token itself is never
 * persisted here or anywhere else — see AuthSession, which only ever lives
 * in memory. */

const KEY = "crm_auth_refresh_token";

export function storeRefreshToken(token: string): void {
  sessionStorage.setItem(KEY, token);
}

export function loadRefreshToken(): string | null {
  return sessionStorage.getItem(KEY);
}

export function clearRefreshToken(): void {
  sessionStorage.removeItem(KEY);
}
