# Savvy CRM web application

The browser CRM. It uses React 18, strict TypeScript, Vite, TanStack Query,
React Router, React Hook Form, Zod, Tailwind CSS, and typed Connect-ES clients
generated in `@crm/contracts`.

## Authentication

Authentication is first-party and passwordless. The public marketing site's
combined Login / Sign-up dialog owns account entry:

1. `POST /auth/email/request` sends a six-digit email code.
2. `POST /auth/email/verify` verifies it and sets the HttpOnly
   `savvy_session` cookie.
3. `GET /auth/session` restores the UI session after a reload.
4. `POST /auth/logout` revokes and clears the session.

Google login in that dialog is available as a secondary option only when
`GET /auth/capabilities` returns `{ "google": true }`. It starts through
`GET /auth/google/start?flow=login`; the API owns OAuth state and callback
handling, so no Google client secret enters the browser bundle. A missing or
unreachable capabilities endpoint simply leaves email OTP available.

The browser never stores or reads the opaque session token. Auth fetches from
the marketing dialog and Connect RPC calls from this app use
`credentials: "include"`, with separate exact CORS allowlists. The app's
`/login` route exists only for old bookmarks and immediately opens the
marketing dialog. `hasScope` and `hasRole` only control UI hints; the API
remains the authorization authority.

The app signs out after 30 minutes without deliberate activity across all open
CRM tabs. Activity heartbeats are throttled and contain no user/session data.
Logout hides protected data immediately, but the UI claims success only after
the API confirms revocation; failures and timeouts expose a retry action.

## Local development

Install workspace dependencies at the repository root, start the CRM API, then:

```sh
cp apps/web/.env.example apps/web/.env
npm run dev --workspace=@crm/web
```

The required browser configuration is `VITE_CRM_API_URL` plus
`VITE_CRM_AUTH_PORTAL_URL`. Authentication secrets, SMTP credentials, and
signing keys belong to the CRM API and must never use a `VITE_` prefix.

`VITE_WEB_PORT` defaults to `3000`, and Vite uses `strictPort: true`. If the
port is taken, choose another one and add the resulting exact origin to the
API's credentialed CORS allowlist. The development server binds to loopback by
default.

## Commands

| Command | What it does |
| --- | --- |
| `npm run dev` | Run the Vite development server |
| `npm run build` | Type-check and create the production bundle |
| `npm run test` | Run Vitest unit/component tests |
| `npm run test:e2e` | Run Playwright against a live CRM API |
| `npm run lint` | Run ESLint, including accessibility rules |
| `npm run typecheck` | Run TypeScript without emitting output |

For passwordless browser tests, set the API's development-only `AUTH_DEV_OTP`
and provide the same six-digit value as `E2E_LOGIN_CODE`. Production and
staging reject the fixed-code setting.
