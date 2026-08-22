# Security Hardening & Production Readiness

Everything needed before authincore handles real user traffic.

---

## Rate Limiting

### Endpoints to protect
- [ ] `POST /self-service/login` — per-IP and per-identifier: 5 attempts / 1 minute
- [ ] `POST /self-service/registration` — per-IP: 3 per minute
- [ ] `POST /self-service/recovery` — per-IP: 3 per minute (prevent email bombing)
- [ ] `POST /oauth2/token` — per-client: 60 per minute; per-IP: 100 per minute
- [ ] `POST /oauth2/introspect` — per-resource-server: 200 per minute
- [ ] Admin API — per-API-key: 1000 per minute

### Implementation
- [ ] Token bucket in Redis (`INCR` + `EXPIRE`)
- [ ] Middleware: `internal/api/middleware/ratelimit.go`
- [ ] 429 response with `Retry-After` header
- [ ] Configurable per-org overrides

---

## Brute-Force Protection

- [ ] Account lockout: 10 failed logins → 15-minute lockout, exponential backoff
- [ ] Lockout state stored in Redis (`identity:{id}:lockout`) + Postgres (persistent)
- [ ] Unlock: auto-expire OR admin `POST /admin/identity/{id}/unlock`
- [ ] Notify identity on lockout via email (courier)
- [ ] Suspicious IP detection: >50 failures from same IP in 5 min → block IP for 1 hour
- [ ] Credential stuffing guard: check submitted passwords against leaked hash API (k-anonymity HaveIBeenPwned)

---

## CSRF Protection

- [ ] Double-submit cookie for browser-based self-service flows
- [ ] CSRF token in flow response, validated on submit
- [ ] `SameSite=Lax` on session cookies (strict for admin portal)
- [ ] `__Host-` cookie prefix when behind HTTPS

---

## TLS & Transport

- [ ] TLS termination at server: `autocert` for Let's Encrypt or configurable cert path
- [ ] HSTS header: `Strict-Transport-Security: max-age=63072000; includeSubDomains; preload`
- [ ] Redirect HTTP → HTTPS (configurable)
- [ ] Certificate pinning for outbound OIDC upstream calls (optional, org-level)

---

## Security Headers

- [ ] `Content-Security-Policy: default-src 'none'` on all API responses
- [ ] `X-Content-Type-Options: nosniff`
- [ ] `X-Frame-Options: DENY`
- [ ] `Referrer-Policy: no-referrer`
- [ ] `Permissions-Policy: geolocation=(), microphone=()`
- [ ] Remove `Server` header

---

## Secret Management

- [ ] RSA signing key rotation: hot-swap new key, old key remains in JWKS for TTL period
- [ ] Key rotation API: `POST /admin/keys/rotate` — generates new key, sets as signing key
- [ ] JWKS serves N most-recent keys (configurable window, default: 2)
- [ ] Symmetric secrets (webhook HMAC, cookie signing) loaded from env or Vault
- [ ] Integration: HashiCorp Vault dynamic secrets for DB credentials (optional)
- [ ] Never log secrets: audit all log statements in CI (`grep -r 'password\|secret\|token' --include=*.go`)

---

## Input Validation

- [ ] `Content-Type: application/json` enforced on all JSON endpoints
- [ ] Max request body size: 1 MB (configurable)
- [ ] JSON field length limits: email ≤ 254, password ≤ 1024, trait string ≤ 4096
- [ ] Unicode normalization (NFC) on all string inputs before storage
- [ ] Sanitize redirect URIs: reject data:, javascript:, relative paths without registered prefix

---

## Dependency Security

- [ ] `govulncheck ./...` in CI — fail build on HIGH/CRITICAL vulnerabilities
- [ ] `gosec ./...` in CI — fail on G101 (hardcoded creds), G401 (weak hash), G501 (MD5/SHA1 use)
- [ ] Dependabot / Renovate for automated dependency PRs
- [ ] SBOM generation: `syft . -o cyclonedx-json > sbom.json` on each release
- [ ] CVE response process: patch within 7 days for CRITICAL, 30 days for HIGH

---

## Observability

### Structured Logging
- [x] `slog.Logger` with JSON handler wired throughout
- [ ] Request ID middleware: generate UUID per request, propagate via `X-Request-ID`
- [ ] Log request: method, path, status, latency, org_id, identity_id (no PII)
- [ ] Sanitize: never log password, token, secret, cookie value

### OpenTelemetry Tracing
- [ ] `go.opentelemetry.io/otel` + OTLP exporter (Jaeger / Tempo)
- [ ] Span per HTTP handler, DB query, Redis call, outbound HTTP
- [ ] `trace_id` injected into log records via `otelzap` / `otelslog`
- [ ] Propagate `traceparent` header from inbound requests

### Metrics (Prometheus)
- [ ] `github.com/prometheus/client_golang/prometheus`
- [ ] Counters: `authincore_logins_total{result="success|failure", org_id}`
- [ ] Histograms: `authincore_request_duration_seconds{method, path, status}`
- [ ] Gauges: `authincore_active_sessions`, `authincore_flow_queue_depth`
- [ ] `/metrics` endpoint (internal port, not exposed publicly)

### Health Checks
- [ ] `GET /healthz/live` — always 200 if process is alive
- [ ] `GET /healthz/ready` — 200 only when DB + Redis connections are healthy
- [ ] Kubernetes readiness probe on `/healthz/ready`

---

## Deployment Hardening

- [ ] Docker image: `FROM scratch` or distroless — no shell, no package manager
- [ ] Non-root user in container (`USER 1000:1000`)
- [ ] Read-only root filesystem (`readOnlyRootFilesystem: true` in K8s)
- [ ] Drop all capabilities (`securityContext.capabilities.drop: ALL`)
- [ ] Resource limits: `limits.cpu: 1`, `limits.memory: 512Mi`
- [ ] PodDisruptionBudget: `minAvailable: 1` for HA deployments
- [ ] Network policy: allow only ingress from load balancer, egress to Postgres + Redis

---

## Status Tracker

- [ ] Rate limiting middleware (Redis token bucket)
- [ ] Account lockout (10 failures → 15-min lock)
- [ ] CSRF double-submit cookie
- [ ] Security response headers
- [ ] RSA key rotation API
- [ ] `govulncheck` + `gosec` in CI
- [ ] OpenTelemetry tracing
- [ ] Prometheus metrics
- [ ] Liveness + readiness health checks
- [ ] Distroless Docker image
