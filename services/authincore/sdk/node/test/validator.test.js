import assert from 'node:assert/strict'
import test from 'node:test'
import { AuthinCoreError, Validator, bearerToken } from '../src/index.js'

const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url')

test('validates signature, issuer, audience, tenant and scopes', async () => {
  const pair = await crypto.subtle.generateKey({ name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['sign', 'verify'])
  const jwk = await crypto.subtle.exportKey('jwk', pair.publicKey); jwk.kid = 'key-1'; jwk.alg = 'RS256'
  const header = encode({ alg: 'RS256', kid: 'key-1' })
  const payload = encode({ sub: 'identity', iss: 'https://issuer.test', aud: 'companyos', exp: Math.floor(Date.now() / 1000) + 60, org_id: 'org-a', scopes: ['db:read'] })
  const signature = Buffer.from(await crypto.subtle.sign('RSASSA-PKCS1-v1_5', pair.privateKey, new TextEncoder().encode(`${header}.${payload}`))).toString('base64url')
  const validator = new Validator({ issuer: 'https://issuer.test', audience: 'companyos', fetch: async () => new Response(JSON.stringify({ keys: [jwk] }), { status: 200 }) })
  const claims = await validator.verify(`${header}.${payload}.${signature}`, { tenant: 'org-a', scopes: ['db:read'] })
  assert.equal(claims.sub, 'identity')
  await assert.rejects(() => validator.verify(`${header}.${payload}.${signature}`, { tenant: 'org-b' }), error => error instanceof AuthinCoreError && error.code === 'forbidden')
})

test('extracts bearer credentials', () => {
  assert.equal(bearerToken({ authorization: 'Bearer token-value' }), 'token-value')
  assert.throws(() => bearerToken({}), { code: 'unauthenticated' })
})
