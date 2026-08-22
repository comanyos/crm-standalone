# Observability foundation

This repository now has a local/internal Prometheus scrape surface, structured request logs, separate liveness/readiness probes, and optional OTLP tracing. It does **not** yet have SLOs, alert delivery, dashboards, on-call ownership, retention controls, managed backups, or a production deployment configuration.

## Endpoints

| Service | Liveness | Readiness | Metrics | Access posture |
| --- | --- | --- | --- | --- |
| CRM API (`http://localhost:19431`) | `GET /healthz` → `200 {"status":"ok"}` when the process serves HTTP | `GET /readyz` → `200 {"status":"ready"}` only when Postgres (when configured) and the JWKS verifier are ready; otherwise `503` with a generic reason | `GET /metrics` | Intentionally unauthenticated for the local/internal Compose deployment. When `CRM_METRICS_TOKEN` is set, it requires `Authorization: Bearer …`; staging/production refuse to start without a 16+ character token. Do not expose this port publicly in a production network; restrict it to the collector/network policy. |
| AuthinCore (`http://localhost:18080`) | `GET /healthz` → `200 {"status":"ok"}` when the process serves HTTP | `GET /readyz` → `200 {"status":"ok"}` only when configured DB/Redis pings succeed; otherwise `503 {"status":"not_ready","reason":"dependencies unavailable"}` | `GET /metrics` | Protected by AuthinCore authentication: a verified session or `Authorization: Bearer $ADMIN_TOKEN` when configured. |

Neither readiness response includes DSNs, hosts, passwords, tokens, or a dependency topology. Liveness deliberately does not probe dependencies, so a transient upstream failure does not cause a crash loop.

`make up` maps those service ports to the host. Typical local checks:

```sh
curl -i http://localhost:19431/healthz
curl -i http://localhost:19431/readyz
curl -s http://localhost:19431/metrics | rg '^crm_(http|db_pool|outbox|jwks)'
curl -i http://localhost:18080/healthz
curl -i http://localhost:18080/readyz
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" http://localhost:18080/metrics | rg '^authincore_'
```

## Metrics

All metric labels are bounded service-operation values. Tenant IDs, user IDs, emails, record IDs, request IDs, IP addresses, request bodies, token values, and cookie values are never labels.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `crm_http_requests_total` | `method`, `route`, `status` | CRM HTTP/Connect RPC request count. `route` is a fixed health/metrics route, registered route, RPC procedure, or `unmatched`. |
| `crm_http_request_duration_seconds` | `method`, `route` | CRM HTTP/Connect RPC latency histogram. |
| `crm_db_pool_{acquired,idle,total,max}_connections` | none | CRM pgx application-pool state at scrape time, when PostgreSQL is configured. |
| `crm_db_queries_total` | `operation`, `result` | CRM PostgreSQL query count. `operation` is a bounded SQL verb/category; SQL text and arguments are never exported. |
| `crm_db_query_duration_seconds` | `operation`, `result` | CRM PostgreSQL query latency histogram. |
| `crm_nats_connected` | `configured` (`true`/`false`) | CRM NATS connection state. A zero with `configured="false"` means the optional bus is disabled; a zero with `configured="true"` is a connectivity failure. |
| `crm_outbox_processed_total` | `outcome` (`success`, `retry`, `failure`) | CRM transactional-outbox delivery outcomes. |
| `crm_jwks_validation_failures_total` | `reason` (`missing_bearer_token`, `invalid_token`) | Rejected CRM JWT/JWKS authentication, without token material. |
| `authincore_http_requests_total` | `method`, `path`, `status` | AuthinCore HTTP and Connect request count; `path` is a registered route or fixed Connect procedure. |
| `authincore_http_request_duration_seconds` | `method`, `path` | AuthinCore HTTP and Connect request latency histogram. |
| `authincore_db_pool_{acquired,idle,total,max}_connections` | none | AuthinCore pgx pool state at scrape time. |
| `authincore_db_queries_total` | `operation`, `result` | AuthinCore PostgreSQL query count with bounded operation/result labels. |
| `authincore_db_query_duration_seconds` | `operation`, `result` | AuthinCore PostgreSQL query latency histogram. |
| `authincore_oidc_validation_failures_total` | `code` | OAuth/OIDC token-endpoint validation failures by protocol error code only. |

AuthinCore retains its existing login, token, session, policy, FGA, and webhook counters. The CRM does not yet publish integration-specific metrics.

## Logging and redaction

Both services emit JSON structured access logs with `method`, safe `route` or RPC procedure, `status`, `duration_ms`, and `request_id`. An inbound `X-Request-ID` is preserved only when it is a 8–128 character alphanumeric, dot, underscore, or hyphen correlation token; otherwise a new random ID is created and returned in the response header. The request ID is also used in safe readiness logs.

Both HTTP and Connect ingress points extract W3C `traceparent` and create server spans. The CRM web client creates an opaque W3C parent for browser RPCs; CRM stores the parent with each durable outbox envelope, restores it in the dispatcher, creates an `outbox.publish` producer span, and injects only `traceparent`/optional `tracestate` into the NATS message. This closes the implemented browser → CRM → outbox → NATS path without copying tenant IDs, tokens, request headers, or payloads into trace metadata. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable OTLP/gRPC export; `OTEL_EXPORTER_OTLP_INSECURE=true` permits an explicitly insecure local collector and `OTEL_SAMPLING_RATIO` accepts `0.0`–`1.0` (default `1.0`). With no endpoint, propagation remains available with a no-op provider. No request values, tenant identifiers, headers, bodies, or SQL text are added as span attributes. Future webhooks and third-party integrations must add their own explicit propagation and tests when those features exist.

The access-log policy is deliberately allowlist-based: it does not log headers, bodies, query values, redirect locations, cookies, principals, tenant IDs, user IDs, email addresses, or arbitrary paths. Therefore it cannot log passwords, bearer/access/ID/refresh tokens, authorization headers, cookies, private keys, authorization codes, or full PII payloads. Worker logs also omit tenant IDs and payloads. Application errors should follow the same policy; credentials must never be interpolated into log messages.

Retention, downstream log access controls, and log-shipping configuration are deployment concerns not implemented in this repository.

## Alert candidates (not configured)

The following are candidates for a future alerting implementation, not active alerts or SLOs: sustained 5xx rate or latency increase per fixed route, readiness failures, database pool saturation, a configured-but-disconnected NATS client, sustained JWKS validation failures, and outbox retries/failures. Choose thresholds and ownership only after production traffic and operating policy exist.

## Verification

```sh
cd services/crm-api && go test ./internal/tracing ./internal/metrics ./internal/platform ./internal/server ./internal/auth ./internal/dispatch ./internal/domain
cd services/authincore && go test ./internal/tracing ./internal/api/http ./internal/api/middleware ./internal/metrics ./internal/protocol/oidc
cd apps/web && npm test -- --run src/lib/connect.test.ts && npm run typecheck && npm run lint && npm run build
docker compose config --quiet
docker build -t crm-api-observability-check ./services/crm-api
docker build -t authincore-observability-check ./services/authincore
```

The focused tests cover request-ID preservation/replacement, JSON-log secret redaction, scrapeable CRM metrics, CRM readiness success/failure, AuthinCore readiness non-disclosure, browser traceparent format, durable CRM outbox trace capture/restoration, NATS trace metadata allowlisting, and CRM CORS preflight. Full dependency-outage, load/soak, restart, failover, and key-rotation drills remain open.
