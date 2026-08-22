import http from 'k6/http'
import { check, sleep } from 'k6'
import { Rate, Trend, Counter } from 'k6/metrics'

// ---------------------------------------------------------------------------
// Custom metrics
// ---------------------------------------------------------------------------
const p95Latency      = new Trend('tenant_req_duration_ms', true)
const tenantErrors    = new Counter('tenant_errors')
const tenantSuccessRate = new Rate('tenant_success_rate')

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------
const BASE_URL    = __ENV.BASE_URL    || 'http://localhost:8080'
const TENANT_COUNT = parseInt(__ENV.TENANT_COUNT  || '1000')
const USERS_PER_TENANT = parseInt(__ENV.USERS_PER_TENANT || '100')
// Pre-generated list: tenant org IDs. In real load tests you'd seed these
// via a setup() call; here we derive deterministic UUIDs from an index.
const SCIM_TOKEN = __ENV.SCIM_TOKEN || 'k6-bearer'

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------
export const options = {
  scenarios: {
    // Mixed read/write across 1000 tenants, 100 concurrent VUs
    tenant_load: {
      executor: 'constant-vus',
      vus: 100,
      duration: '3m',
      exec: 'tenantLoad',
    },
  },

  thresholds: {
    // P95 must stay under 200ms — primary SLA target
    tenant_req_duration_ms: ['p(95)<200'],
    http_req_duration:      ['p(95)<200'],
    tenant_success_rate:    ['rate>0.995'],
    http_req_failed:        ['rate<0.005'],
  },
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Derive a deterministic UUID-like org ID from a numeric index.
 * Real tests should pre-seed orgs and read IDs from a shared data file.
 */
function orgIDForIndex(i) {
  const hex = i.toString(16).padStart(12, '0')
  return `00000000-0000-4000-8000-${hex}`
}

/**
 * Pick a random tenant for this iteration.
 */
function randomTenantID() {
  const idx = Math.floor(Math.random() * TENANT_COUNT)
  return orgIDForIndex(idx)
}

/**
 * Pick a random user index within a tenant.
 */
function randomUserIndex() {
  return Math.floor(Math.random() * USERS_PER_TENANT)
}

// ---------------------------------------------------------------------------
// Scenario: tenantLoad
//   Each VU randomly selects a tenant and performs one of three operations:
//   - 60% chance: list SCIM users (read)
//   - 30% chance: create a SCIM user (write)
//   - 10% chance: get a specific user (point-read)
// ---------------------------------------------------------------------------
export function tenantLoad() {
  const orgID = randomTenantID()
  const headers = {
    'Authorization': `Bearer ${SCIM_TOKEN}`,
	'Host':          `${orgID}.${__ENV.TENANT_DOMAIN || 'auth.example.test'}`,
    'Content-Type':  'application/scim+json',
  }

  const roll = Math.random()
  let res
  const start = Date.now()

  if (roll < 0.60) {
    // Read: list users with pagination
    const startIndex = (randomUserIndex() % 10) * 10 + 1
    res = http.get(
      `${BASE_URL}/scim/v2/Users?startIndex=${startIndex}&count=10`,
      { headers },
    )
  } else if (roll < 0.90) {
    // Write: create a new user
    const uid = `t${orgID.slice(-6)}-vu${__VU}-it${__ITER}`
    const payload = JSON.stringify({
      schemas:     ['urn:ietf:params:scim:schemas:core:2.0:User'],
      userName:    `${uid}@tenant.example.com`,
      active:      true,
      displayName: `Tenant Load User ${uid}`,
      emails:      [{ value: `${uid}@tenant.example.com`, primary: true }],
    })
    res = http.post(`${BASE_URL}/scim/v2/Users`, payload, { headers })
  } else {
    // Point-read: get a deterministic user ID (may 404 if not seeded)
    const userIdx = randomUserIndex()
    const fakeID  = orgID.replace(/-/g, '') + userIdx.toString(16).padStart(8, '0')
    res = http.get(`${BASE_URL}/scim/v2/Users/${fakeID}`, { headers })
  }

  const elapsed = Date.now() - start
  p95Latency.add(elapsed)

  const ok = check(res, {
    'status 2xx or 404': (r) =>
      (r.status >= 200 && r.status < 300) || r.status === 404,
    'not 500': (r) => r.status !== 500,
  })
  tenantSuccessRate.add(ok)
  if (!ok) tenantErrors.add(1)

  sleep(0.05) // 50ms think-time to avoid thundering herd
}

// ---------------------------------------------------------------------------
// setup: optionally pre-warm tenant orgs.
// Called once before all VUs start. Skipped if SKIP_SETUP=1.
// ---------------------------------------------------------------------------
export function setup() {
  if (__ENV.SKIP_SETUP === '1') return {}

  // Seed a small number of users in each of the first 10 tenants so
  // point-reads don't all 404. Full seeding would be a separate migration.
  const warmTenants = Math.min(10, TENANT_COUNT)
  for (let i = 0; i < warmTenants; i++) {
    const orgID = orgIDForIndex(i)
    const headers = {
      'Authorization': `Bearer ${SCIM_TOKEN}`,
	  'Host':          `${orgID}.${__ENV.TENANT_DOMAIN || 'auth.example.test'}`,
      'Content-Type':  'application/scim+json',
    }
    for (let u = 0; u < 3; u++) {
      const uid = `seed-org${i}-user${u}`
      http.post(
        `${BASE_URL}/scim/v2/Users`,
        JSON.stringify({
          schemas:  ['urn:ietf:params:scim:schemas:core:2.0:User'],
          userName: `${uid}@seed.example.com`,
          active:   true,
        }),
        { headers },
      )
    }
  }
  return { seeded: warmTenants }
}
