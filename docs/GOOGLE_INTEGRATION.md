# Google CRM integration

The Google foundation supports Gmail activity sync/send and Google Calendar
event management. It is intentionally limited to these approved scopes:

- `https://www.googleapis.com/auth/gmail.readonly`
- `https://www.googleapis.com/auth/gmail.send`
- `https://www.googleapis.com/auth/calendar.events`

## Local configuration

Set these in the environment that runs `services/crm-api`; never add their
values to `.env.example`, source control, browser configuration, or chat.

| Variable | Purpose |
| --- | --- |
| `GOOGLE_OAUTH_CLIENT_ID` | OAuth web-client ID (not secret) |
| `GOOGLE_OAUTH_CLIENT_SECRET` | OAuth web-client secret |
| `GOOGLE_OAUTH_REDIRECT_URL` | Registered callback; local: `http://localhost:19431/integrations/google/callback` |
| `GOOGLE_OAUTH_TOKEN_KEY` | Base64-encoded 32 random bytes for AES-256-GCM token storage |

Generate the token-encryption key locally with `openssl rand -base64 32` and
inject it using the same deployment-secret mechanism as the client secret.
The CRM rejects a partially configured Google integration; in staging and
production its redirect URL must be HTTPS.

## Current implementation boundary

`internal/integration/googleoauth` implements authorization URL generation,
PKCE, token exchange/refresh, and authenticated encryption. Before exposing a
"Connect Google" control, the CRM must add a tenant-scoped persistence layer
for one-time OAuth state and encrypted connections, then wire the callback to
atomically consume state. Tokens and PKCE verifiers must remain server-side,
are never browser API responses, and must be purged/revoked on disconnect.

Google marks Gmail read access as restricted. The Cloud project is configured
for verification, but verification submission is intentionally deferred until
the connection UI is live and a truthful demo video can be supplied.
