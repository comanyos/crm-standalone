const encoder = new TextEncoder()
const decode = value => JSON.parse(new TextDecoder().decode(base64url(value)))
const base64url = value => Uint8Array.from(Buffer.from(value, 'base64url'))

export class AuthinCoreError extends Error {
  constructor(code, message, cause) { super(message, { cause }); this.name = 'AuthinCoreError'; this.code = code }
}

export class Validator {
  #keys = new Map(); #expires = 0; #config
  constructor(config) {
    if (!config?.issuer || !config?.audience) throw new TypeError('issuer and audience are required')
    this.#config = { cacheTTL: 300_000, fetch, ...config, jwksURL: config.jwksURL ?? `${config.issuer.replace(/\/$/, '')}/.well-known/jwks.json` }
  }

  async verify(raw, { tenant, scopes = [] } = {}) {
    const parts = String(raw ?? '').split('.')
    if (parts.length !== 3) throw new AuthinCoreError('unauthenticated', 'invalid access token')
    let header, claims
    try { header = decode(parts[0]); claims = decode(parts[1]) } catch (cause) { throw new AuthinCoreError('unauthenticated', 'invalid access token', cause) }
    if (header.alg !== 'RS256' || !header.kid) throw new AuthinCoreError('unauthenticated', 'unsupported signing key')
    const key = await this.#key(header.kid)
    const valid = await crypto.subtle.verify('RSASSA-PKCS1-v1_5', key, base64url(parts[2]), encoder.encode(`${parts[0]}.${parts[1]}`))
    const now = Math.floor(Date.now() / 1000)
    const audiences = Array.isArray(claims.aud) ? claims.aud : [claims.aud]
    if (!valid || claims.iss !== this.#config.issuer || !audiences.includes(this.#config.audience) || !claims.sub || !claims.org_id || !claims.exp || claims.exp <= now || (claims.nbf && claims.nbf > now)) {
      throw new AuthinCoreError('unauthenticated', 'invalid access token')
    }
    if (tenant && claims.org_id !== tenant) throw new AuthinCoreError('forbidden', 'tenant mismatch')
    const granted = new Set([...(claims.scopes ?? []), ...String(claims.scope ?? '').split(/\s+/).filter(Boolean)])
    if (!scopes.every(scope => granted.has('*') || granted.has(scope))) throw new AuthinCoreError('forbidden', 'required scope missing')
    return claims
  }

  async #key(kid) {
    if (Date.now() >= this.#expires || !this.#keys.has(kid)) await this.#refresh()
    const key = this.#keys.get(kid)
    if (!key) throw new AuthinCoreError('unauthenticated', 'unknown signing key')
    return key
  }

  async #refresh() {
    let response
    try { response = await this.#config.fetch(this.#config.jwksURL, { headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(5000) }) }
    catch (cause) { throw new AuthinCoreError('auth_unavailable', 'signing keys unavailable', cause) }
    if (!response.ok) throw new AuthinCoreError('auth_unavailable', 'signing keys unavailable')
    const set = await response.json(); const keys = new Map()
    for (const jwk of set.keys ?? []) if (jwk.kid && jwk.kty === 'RSA') keys.set(jwk.kid, await crypto.subtle.importKey('jwk', jwk, { name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' }, false, ['verify']))
    if (!keys.size) throw new AuthinCoreError('auth_unavailable', 'JWKS contains no usable keys')
    this.#keys = keys; this.#expires = Date.now() + this.#config.cacheTTL
  }
}

export function bearerToken(headers) {
  const value = typeof headers?.get === 'function' ? headers.get('authorization') : headers?.authorization
  if (!value?.startsWith('Bearer ')) throw new AuthinCoreError('unauthenticated', 'bearer token required')
  return value.slice(7).trim()
}

export function expressMiddleware(validator, options = {}) {
  return async (req, res, next) => {
    try {
      req.auth = await validator.verify(bearerToken(req.headers), { tenant: options.tenant?.(req), scopes: options.scopes ?? [] })
      next()
    } catch (error) {
      const status = error?.code === 'forbidden' ? 403 : error?.code === 'auth_unavailable' ? 503 : 401
      res.status(status).json({ code: error?.code ?? 'unauthenticated', error: error?.message ?? 'authentication failed' })
    }
  }
}
