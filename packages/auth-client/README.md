# Auth client

Framework-agnostic OIDC Authorization Code + PKCE browser client for
AuthinCore. No React (or any UI framework) dependency — `apps/web`'s
`AuthProvider` wires this into React state and context; a future non-React
surface could reuse this package directly.

Do not duplicate authentication or token-signing logic from AuthinCore — this
package only drives the standard OAuth2/OIDC flows AuthinCore already
exposes (`/oauth2/authorize`, `/oauth2/token`, `/oauth2/revoke`,
`/api/v1/sessions/me`); it never verifies a token's signature or mints one.

## What's here

- `pkce.ts` — PKCE code_verifier/code_challenge (S256) and random
  state/token generation, via Web Crypto only (works in every browser and in
  Vitest's jsdom).
- `jwt.ts` — decodes (does not verify) an access token's payload, for UI
  hints only.
- `pending.ts` — transient, `sessionStorage`-backed PKCE state with a
  10-minute expiry, keyed by the OAuth `state` parameter.
- `session-store.ts` — persists the (rotating, single-use) refresh token in
  `sessionStorage`. See the module doc comment for why: AuthinCore's
  authorization endpoints send `X-Frame-Options: DENY`, which rules out the
  hidden-iframe "silent renew" pattern some SPAs use instead of this.
- `client.ts` — `beginLogin`, `parseCallback`, `completeCallback`,
  `renewFromStoredRefreshToken`, `revokeStoredRefreshToken`,
  `endServerSession`. The access token itself is never persisted anywhere —
  only ever held in memory by the caller (`AuthSession`).

## Testing

`npm run test` (Vitest + jsdom). Tests cover PKCE correctness (including the
RFC 7636 Appendix B worked example), state/CSRF validation, the token
exchange request shape, refresh-token rotation, and — importantly — that
concurrent renewal calls are de-duplicated into a single exchange (this is
what React 18 StrictMode's double effect invocation exercises in
`apps/web`, and is a real robustness property, not a dev-only concern).
