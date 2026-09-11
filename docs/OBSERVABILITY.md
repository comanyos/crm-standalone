# Observability foundation

This repository has one Go service boundary for CRM and authentication, a local/internal Prometheus scrape surface, structured request logs, separate liveness/readiness probes, and optional OTLP tracing. It does **not** yet have SLOs, alert delivery, dashboards, on-call ownership, retention controls, managed backups, or a production deployment configuration.

## Endpoints

| Service | Liveness | Readiness | Metrics | Access posture |
| --- | --- | --- | --- | --- |
| CRM API (`http://localhost:19431`) | `GET /healthz` → `200 {"status":"ok"}` when the process serves HTTP | `GET /readyz` → `200 {"status":"ready"}` only when PostgreSQL and other dependencies required for safe request handling are ready; otherwise `503` with a generic reason | `GET /metrics` | Intentionally unauthenticated for the local/internal Compose deployment. When `CRM_METRICS_TOKEN` is set, it requires `Authorization: Bearer …`; staging/production refuse to start without a 16+ character token. Keep this route private in production and restrict it to the collector/network policy. |

Readiness never includes DSNs, hosts, passwords, tokens, or dependency topology. Liveness deliberately does not probe dependencies, so a transient upstream failure does not create a crash loop.

Typical local checks:

```sh
curl -i http://localhost:19431/healthz
curl -i http://localhost:19431/readyz
curl -s http://localhost:19431/metrics | rg '^crm_'
```

## Metrics

All metric labels must be bounded service-operation values. Tenant IDs, user IDs, emails, record IDs, request IDs, IP addresses, request bodies, token values, OTP values, challenge IDs, and cookie values are never labels.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `crm_http_requests_total` | `method`, `route`, `status` | CRM HTTP/Connect RPC and `/auth/*` request count. `route` is a registered route, RPC procedure, or `unmatched`, never a raw path. |
| `crm_http_request_duration_seconds` | `method`, `route` | CRM HTTP/Connect RPC and authentication latency histogram. |
| `crm_db_pool_{acquired,idle,total,max}_connections` | none | CRM pgx pool state, including native-auth database work. |
| `crm_db_queries_total` | `operation`, `result` | PostgreSQL query count. SQL text and arguments are never exported. |
| `crm_db_query_duration_seconds` | `operation`, `result` | PostgreSQL query latency histogram. |
| `crm_nats_connected` | `configured` (`true`/`false`) | CRM NATS connection state. A zero with `configured="false"` means the optional bus is disabled; a zero with `configured="true"` is a connectivity failure. |
| `crm_outbox_processed_total` | `outcome` (`success`, `retry`, `failure`) | CRM transactional-outbox delivery outcomes. |

Before production, add native-auth counters (for example
`crm_authentication_failures_total`) for OTP request/send/verify outcomes,
SMTP delivery failures, Google callback validation failures, session
lookup/revocation, and rate limiting. Use bounded `flow`/`reason` values such
as `expired`, `invalid`, `rate_limited`, `provider_error`, or `revoked`; never
turn external error text into a label.

The CRM also publishes bounded integration metrics for lead capture, Google connection flows, Gmail/Calendar synchronization, erasure, and workflows. Provider payloads and tenant/customer identifiers do not belong in metrics.

## Logging and redaction

The CRM API emits JSON structured access logs with `method`, safe route or RPC procedure, `status`, `duration_ms`, and `request_id`. An inbound `X-Request-ID` is preserved only when it is an 8–128 character alphanumeric, dot, underscore, or hyphen correlation token; otherwise a new random ID is created and returned. The request ID is also used in safe readiness logs.

HTTP and Connect ingress extract W3C `traceparent` and create server spans. The CRM web client creates an opaque W3C parent for browser RPCs; the CRM stores the parent with each durable outbox envelope, restores it in the dispatcher, creates an `outbox.publish` producer span, and injects only `traceparent`/optional `tracestate` into the NATS message. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable OTLP/gRPC export; `OTEL_EXPORTER_OTLP_INSECURE=true` permits an explicitly insecure local collector and `OTEL_SAMPLING_RATIO` accepts `0.0`–`1.0`. With no endpoint, propagation remains available with a no-op provider.

The allowlist-based log policy does not record headers, bodies, query values, redirect locations, cookies, principals, tenant IDs, user IDs, email addresses, or arbitrary paths. It therefore must not record opaque session tokens, bearer headers, OTPs or their digests, challenge/state values, Google authorization codes/ID tokens, SMTP credentials, integration tokens, private keys, or full PII payloads. Authentication errors use stable codes and request IDs; never interpolate credentials or provider responses into logs.

Retention, downstream log access controls, and log-shipping configuration are deployment concerns not implemented in this repository.

## Alert candidates (not configured)

Candidates include sustained 5xx rate or latency, readiness failures, database pool saturation, a configured-but-disconnected event transport, OTP/Google login failure spikes, SMTP delivery failure, session-store latency, abuse-rate-limit activation, and outbox retries/failures. Choose thresholds and ownership only after production traffic and operating policy exist.

## Verification

```sh
cd services/crm-api && go test ./internal/tracing ./internal/metrics ./internal/platform ./internal/server ./internal/auth ./internal/dispatch ./internal/domain
cd apps/web && npm test && npm run typecheck && npm run lint && npm run build
docker compose config --quiet
docker build -t crm-api-observability-check ./services/crm-api
```

Focused tests should cover request-ID preservation/replacement, JSON-log secret redaction, scrapeable metrics, readiness success/failure, browser traceparent format, durable outbox trace capture/restoration, event metadata allowlisting, CORS/Origin handling, and native-auth failure redaction. Full dependency-outage, SMTP/provider outage, load/soak, restart, failover, and session/secret-rotation drills remain open.
