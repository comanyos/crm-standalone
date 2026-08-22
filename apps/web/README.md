# CRM web application

The browser CRM. React 18 + TypeScript (strict) + Vite, TanStack Query for
data fetching/caching, React Router, React Hook Form + Zod for forms,
Tailwind for styling, and a typed Connect-ES client (`@crm/contracts`)
generated from `services/crm-api/proto`. OIDC Authorization Code + PKCE
login/session logic lives in `@crm/auth-client` (`packages/auth-client`).

## Prerequisites

- The full standalone stack running and bootstrapped — from the repo root:
  ```sh
  make dev-key && make up
  BOOTSTRAP_ORG_NAME='Acme CRM' BOOTSTRAP_ORG_DOMAIN='localhost' \
  BOOTSTRAP_ADMIN_NAME='Admin' BOOTSTRAP_ADMIN_EMAIL='admin@localhost' \
  BOOTSTRAP_ADMIN_PASSWORD='use-a-unique-12-character-minimum-password' \
  make bootstrap
  ```
  Set the printed `organization_id` as `CRM_WEB_AUTH_ORG_ID` in the ignored
  root `.env` before `make up` builds the persistent browser container.
- `npm install` at the repo root (this is an npm workspace; installing here
  alone will not link `@crm/contracts`/`@crm/auth-client`).

## Persistent local app

`make up` builds the web application and serves it at `CRM_WEB_ORIGIN`
(`http://localhost:3000` by default). It uses an nginx SPA fallback, so the
OIDC redirect to `/auth/callback` remains available after terminal sessions
end. The browser image contains public OIDC configuration only; never supply a
client secret to it.

## Vite development setup

```sh
cp apps/web/.env.example apps/web/.env
# edit apps/web/.env: set VITE_AUTH_ORG_ID to the organization_id from bootstrap
npm run dev --workspace=@crm/web
```

Open `http://localhost:3000`. Signing in redirects to AuthinCore's hosted
login page (`http://localhost:18080/login`) — sign in with the bootstrap
administrator's email/password, approve the consent screen on first login,
and you land on the dashboard.

**Port 3000 must be free by default.** The OIDC client's `redirect_uri` and
both backends' CORS allowlists (`CORS_ALLOWED_ORIGINS`) must match the web
origin exactly. `vite.config.ts` sets `strictPort: true` so a conflict fails
loudly instead of silently running on a different origin (which would break
login, not just the dev server).

If another local application owns port 3000, use one free port consistently:

1. In the ignored root `.env`, set `CRM_WEB_PORT=<port>`,
   `CRM_WEB_ORIGIN=http://localhost:<port>`, and
   `CRM_WEB_REDIRECT_URI=http://localhost:<port>/auth/callback`.
2. In the ignored `apps/web/.env`, set `VITE_WEB_PORT=<port>` only when using
   the Vite development server.
3. Recreate the standalone AuthinCore and CRM API services, then re-run
   `make bootstrap` with the same organization and administrator values so
   the `crm-web` client's redirect URI is reconciled.

The web server binds to loopback by default; it is intentionally not exposed
to the local network during development.

## Why no dev proxy?

`services/authincore/admin-ui` and `hosted-ui` (in this same repo) proxy
AuthinCore through Vite in dev. This app deliberately does not: it's an OIDC
*client* on its own origin, not AuthinCore's own first-party UI, and the
login redirect is a full top-level navigation to AuthinCore's real origin
regardless of any proxy. The token exchange, refresh, and logout calls rely
on AuthinCore's (and crm-api's) `CORS_ALLOWED_ORIGINS` allowlist instead —
already configured for `http://localhost:3000` by default in the root
`compose.yaml`, and the same mechanism a production deployment with the web
app on its own subdomain would need anyway.

## Session model

- The access token lives in memory only — never in `localStorage` or
  `sessionStorage`.
- The refresh token (rotated by AuthinCore on every use) lives in
  `sessionStorage` — cleared when the tab/browser closes, but present across
  a reload. AuthinCore's authorization endpoints send `X-Frame-Options: DENY`
  (correct hardening on an identity provider), which rules out the
  hidden-iframe "silent renew" pattern some SPAs use instead; the
  `grant_type=refresh_token` exchange is the strategy this architecture
  actually supports. See `packages/auth-client/src/client.ts` and
  `session-store.ts`.
- Renewal fires ~60s before the access token's `exp` claim. If it fails
  (revoked/expired refresh token), the user sees a toast and is prompted to
  sign in again — not silently kicked out mid-action.
- Sign out clears local state, revokes the stored refresh token, and ends
  AuthinCore's own browser session (`DELETE /api/v1/sessions/me`), so a
  reload afterward genuinely requires signing in again rather than silently
  resuming.

## Commands

| Command | What it does |
| --- | --- |
| `make up` | Persistent Docker-served CRM app at `CRM_WEB_ORIGIN` |
| `npm run dev` | Vite development server on `VITE_WEB_PORT` (3000 by default) |
| `npm run build` | Type-check (`tsc -b`) + production build |
| `npm run test` | Unit/component tests (Vitest + Testing Library) |
| `npm run test:e2e` | Browser tests (Playwright) — needs the backend stack up and bootstrapped, and `apps/web/.env` configured; see `tests-e2e/auth.spec.ts` |
| `npm run lint` | ESLint (includes `jsx-a11y` for the WCAG 2.2 AA baseline) |
| `npm run typecheck` | `tsc -b --noEmit` |

## What exists today (Phase 2 of `task.md`)

- Application shell: sidebar, top bar, command palette (`⌘K`, navigation
  only for now — Phase 6 adds live search), breadcrumbs/page headers, a
  global toast system.
- Design-system primitives: `Button`, `Input`, `Table`, `Dialog`/
  `ConfirmDialog` (built on the native `<dialog>` element), `Badge`,
  `Skeleton`, `Pagination`, and `EmptyState`/`ErrorState`/`ForbiddenState`/
  `UnavailableState` patterns.
- Full OIDC Authorization Code + PKCE login/callback/renewal/logout, a
  `RequireAuth` route guard, and a permission-hint API (`hasScope`/
  `hasRole`) — the backend remains the authorization authority.
- A live Dashboard page (`crm.v1.CRMService/GetSummary`, not mocked) proving
  the Connect-ES client, auth, and CORS wiring end-to-end.
- Accounts, Contacts, and Leads have live list/detail/create/edit workflows.
  Lead conversion uses the CRM API's atomic conversion contract. Opportunities
  remains an honest placeholder until the pipeline/stage module is built.
