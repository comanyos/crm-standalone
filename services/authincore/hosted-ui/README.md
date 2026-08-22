# AuthinCore hosted UI

The hosted UI is a Vite application and must be served over HTTP. Opening
`index.html` with a `file://` URL cannot load its TypeScript modules or call the
AuthinCore API.

## Local development

1. Start PostgreSQL and Redis with the root development Compose file.
2. Start AuthinCore on port `18080`.
3. Copy `.env.example` to `.env.local` and set
   `AUTHINCORE_DEV_TENANT_HOST` to a tenant domain in the local database.
4. Run `npm ci`, then `npm run open`.

Vite proxies `/self-service` and `/invitations` to the configured local API and
sends the tenant host required by AuthinCore tenant resolution.

## Production build

Run `npm ci && npm test && npm run build`. The Go binary embeds the generated
`dist` directory and serves the login, registration, recovery, verification,
settings, consent, and invitation routes itself.
