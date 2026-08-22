export interface Claims { sub: string; iss: string; aud: string | string[]; exp: number; org_id: string; sid?: string; email?: string; roles?: string[]; scopes?: string[]; scope?: string; aal?: number; amr?: string[] }
export class AuthinCoreError extends Error { code: 'unauthenticated' | 'forbidden' | 'auth_unavailable' }
export class Validator {
  constructor(config: { issuer: string; audience: string; jwksURL?: string; cacheTTL?: number; fetch?: typeof fetch })
  verify(token: string, options?: { tenant?: string; scopes?: string[] }): Promise<Claims>
}
export function bearerToken(headers: Headers | Record<string, string>): string
export function expressMiddleware(validator: Validator, options?: { tenant?: (request: any) => string; scopes?: string[] }): (request: any, response: any, next: () => void) => Promise<void>
