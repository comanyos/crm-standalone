# AuthinCore Benchmarks

Results are recorded after each significant release. Hardware: 4 vCPU / 8GB RAM (single node, no HA).

## How to reproduce

```bash
# Start stack
docker compose up -d
docker exec authincore-authincore-1 /authincore migrate

# Run benchmarks
k6 run test/load/login_storm.js    > bench/login_storm_$(date +%Y%m%d).txt
k6 run test/load/tenant_load.js    > bench/tenant_load_$(date +%Y%m%d).txt
```

---

## Local release smoke — 2026-08-12

This is a correctness/latency smoke test on a developer Mac, not the accepted
production capacity benchmark. It used the real HTTP server, PostgreSQL, Redis,
tenant resolution, password verification, and flow persistence.

| Scenario | VUs | Duration | Iterations | P95 HTTP | P95 login | Error % |
|---|---:|---:|---:|---:|---:|---:|
| Password login smoke | 1 | 6s | 10 | 34.17 ms | 38.94 ms | 0.00% |

All configured k6 thresholds passed and login success was 100%. The production
capacity, soak, and chaos runs below still require the target Kubernetes
environment and its telemetry.

## v0.1.0 — production capacity not yet measured

| Scenario | VUs | Duration | RPS | P95 | P99 | Error % | CPU | Memory |
|---|---|---|---|---|---|---|---|---|
| Login storm | 1000 | 60s | — | — | — | — | — | — |
| Token generation (client_credentials) | 500 | 60s | — | — | — | — | — | — |
| SCIM bulk import | 50 | until 100k | — | — | — | — | — | — |
| Multi-tenant (1000 tenants) | 100 | 60s | — | — | — | — | — | — |

*Run `make load` and paste results here.*

---

## Methodology

- k6 scripts in `test/load/`
- Each scenario runs 3 times; median reported
- CPU/Memory from `docker stats` during peak
- Error % = non-2xx responses / total requests
- P95/P99 from k6 `http_req_duration` metric

## Targets (to call it production-ready)

| Metric | Target |
|---|---|
| Login P95 | < 100ms |
| Token P95 | < 50ms |
| SCIM import (100k users) | < 5 min |
| Multi-tenant P95 | < 200ms |
| Error rate under load | < 0.1% |
| Memory after 72h | stable (no leak) |
