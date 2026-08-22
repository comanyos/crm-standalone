import http from 'k6/http'
import { check, sleep } from 'k6'
import { Counter, Rate, Trend } from 'k6/metrics'

// ---------------------------------------------------------------------------
// Custom metrics
// ---------------------------------------------------------------------------
const loginErrors = new Counter('login_errors')
const tokenErrors = new Counter('token_errors')
const scimErrors  = new Counter('scim_errors')
const loginRate   = new Rate('login_success_rate')
const tokenRate   = new Rate('token_success_rate')
const loginDuration  = new Trend('login_duration_ms', true)
const tokenDuration  = new Trend('token_duration_ms', true)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------
const BASE_URL   = __ENV.BASE_URL   || 'http://localhost:8080'
const TENANT_HOST = __ENV.TENANT_HOST || 'auth.example.test'
const CLIENT_ID  = __ENV.CLIENT_ID  || 'k6-test-client'
const CLIENT_SECRET = __ENV.CLIENT_SECRET || 'k6-test-secret'
const SCIM_TOKEN = __ENV.SCIM_TOKEN || 'k6-scim-bearer'
const LOGIN_EMAIL = __ENV.LOGIN_EMAIL || `loadtest+${__VU}@example.com`
const LOGIN_PASSWORD = __ENV.LOGIN_PASSWORD || 'LoadTest$ecret1!'
const SELECTED_SCENARIO = __ENV.SCENARIO || 'all'

const scenarios = {
  login_storm: {
    executor: 'ramping-vus',
    startVUs: 0,
    stages: [
      { duration: __ENV.LOGIN_RAMP || '30s', target: parseInt(__ENV.LOGIN_VUS || '1000') },
      { duration: __ENV.LOGIN_HOLD || '60s', target: parseInt(__ENV.LOGIN_VUS || '1000') },
      { duration: __ENV.LOGIN_DOWN || '15s', target: 0 },
    ],
    gracefulRampDown: '10s',
    exec: 'loginStorm',
  },
  token_gen: {
    executor: 'constant-vus',
    vus: parseInt(__ENV.TOKEN_VUS || '500'),
    duration: __ENV.TOKEN_DURATION || '90s',
    exec: 'tokenGen',
    startTime: '0s',
  },
  scim_sync: {
    executor: 'shared-iterations',
    vus: parseInt(__ENV.SCIM_VUS || '50'),
    iterations: parseInt(__ENV.SCIM_ITERATIONS || '100000'),
    maxDuration: __ENV.SCIM_DURATION || '5m',
    exec: 'scimSync',
    startTime: '0s',
  },
}

export const options = {
  hosts: __ENV.TENANT_IP ? { [TENANT_HOST]: __ENV.TENANT_IP } : undefined,
  scenarios: SELECTED_SCENARIO === 'all'
    ? scenarios
    : { [SELECTED_SCENARIO]: scenarios[SELECTED_SCENARIO] },

  thresholds: {
    // P95 login latency must be under 500ms (browser-perceived UX target)
    http_req_duration: ['p(95)<500'],
    login_duration_ms: ['p(95)<500'],
    token_duration_ms: ['p(95)<300'],
    login_success_rate: ['rate>0.99'],
    token_success_rate: ['rate>0.99'],
    http_req_failed:    ['rate<0.01'],
  },
}

// ---------------------------------------------------------------------------
// Scenario: login_storm
//   Simulates end-users hitting the self-service login flow.
//   Step 1: GET /self-service/login to obtain flow ID.
//   Step 2: POST the form body to submit credentials.
// ---------------------------------------------------------------------------
export function loginStorm() {
  const headers = {
    'Content-Type': 'application/json',
	'Host': TENANT_HOST,
  }

  // Step 1 — create login flow
  const initRes = http.get(`${BASE_URL}/self-service/login?return_to=/`, { headers })
  const initOK = check(initRes, {
    'flow init status 200': (r) => r.status === 200,
    'flow id present':      (r) => {
      try {
        const flow = JSON.parse(r.body)
        return (flow.id ?? flow.ID) !== undefined
      } catch { return false }
    },
  })
  if (!initOK) {
    loginErrors.add(1)
    loginRate.add(false)
    sleep(1)
    return
  }

  let flowID
  try {
    const flow = JSON.parse(initRes.body)
    flowID = flow.id ?? flow.ID
  } catch {
    loginErrors.add(1)
    loginRate.add(false)
    return
  }

  // Step 2 — submit credentials
	const submitPayload = JSON.stringify({
	  method: 'password',
	  body: {
		identifier: LOGIN_EMAIL,
		password: LOGIN_PASSWORD,
	  },
	})

  const start = Date.now()
  const submitRes = http.post(
    `${BASE_URL}/self-service/login?flow=${flowID}`,
    submitPayload,
    { headers },
  )
  loginDuration.add(Date.now() - start)

  const ok = check(submitRes, {
    'login submit 200 or 302': (r) => r.status === 200 || r.status === 302,
  })
  loginRate.add(ok)
  if (!ok) loginErrors.add(1)

  sleep(0.5)
}

// ---------------------------------------------------------------------------
// Scenario: token_gen
//   Machine-to-machine flow — POST /oauth2/token with client_credentials.
// ---------------------------------------------------------------------------
export function tokenGen() {
  const start = Date.now()
	const res = http.post(
	`${BASE_URL}/api/v1/service-accounts/token`,
    {
      grant_type:    'client_credentials',
      client_id:     CLIENT_ID,
      client_secret: CLIENT_SECRET,
      scope:         'openid',
    },
    {
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
		'Host': TENANT_HOST,
      },
    },
  )
  tokenDuration.add(Date.now() - start)

  const ok = check(res, {
    'token 200':              (r) => r.status === 200,
    'access_token present':   (r) => {
      try { return JSON.parse(r.body).access_token !== undefined } catch { return false }
    },
    'token_type is Bearer':   (r) => {
      try { return JSON.parse(r.body).token_type === 'Bearer' } catch { return false }
    },
  })
  tokenRate.add(ok)
  if (!ok) tokenErrors.add(1)

  sleep(0.1)
}

// ---------------------------------------------------------------------------
// Scenario: scim_sync
//   Simulates Okta / Azure AD push-provisioning 100k users via SCIM.
//   Each iteration creates one user; 50 VUs × 2000 iterations = 100k total.
// ---------------------------------------------------------------------------
export function scimSync() {
  const uid = `scim-load-${__VU}-${__ITER}`
  const payload = JSON.stringify({
    schemas:  ['urn:ietf:params:scim:schemas:core:2.0:User'],
    userName: `${uid}@loadtest.example.com`,
    active:   true,
    emails:   [{ value: `${uid}@loadtest.example.com`, primary: true }],
    displayName: `Load Test User ${uid}`,
  })

  const res = http.post(
    `${BASE_URL}/scim/v2/Users`,
    payload,
    {
      headers: {
        'Content-Type':  'application/scim+json',
        'Authorization': `Bearer ${SCIM_TOKEN}`,
		'Host':          TENANT_HOST,
      },
    },
  )

  const ok = check(res, {
    'SCIM create 201': (r) => r.status === 201,
    'Location header present': (r) => r.headers['Location'] !== undefined,
  })
  if (!ok) scimErrors.add(1)
}
