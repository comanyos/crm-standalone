/** Static per-deployment OIDC configuration. Never contains a client secret —
 * this is a public (PKCE) client. */
export interface OidcConfig {
  /** AuthinCore's public issuer origin, e.g. "https://auth.example.com". */
  issuer: string;
  /** The org this deployment serves. AuthinCore's /oauth2/authorize and
   * /oauth2/token endpoints require it explicitly (see docs/CONFIGURATION.md). */
  orgId: string;
  clientId: string;
  /** Must exactly match a redirect URI registered on the client. */
  redirectUri: string;
  scope: string;
}

/** RFC 6749 §5.1 token response, as AuthinCore returns it. */
export interface TokenResponse {
  access_token: string;
  token_type: string;
  expires_in: number;
  refresh_token?: string;
  id_token?: string;
  scope?: string;
}

/** Decoded (not cryptographically verified) access-token claims — for UI
 * hints only. The backend remains the authorization authority. */
export interface AccessTokenClaims {
  sub: string;
  iss: string;
  aud?: string[];
  exp: number;
  iat: number;
  org_id: string;
  email?: string;
  roles?: string[];
  scopes?: string[];
  scope?: string;
}

/** An in-memory authenticated session. Never persisted to localStorage. */
export interface AuthSession {
  accessToken: string;
  claims: AccessTokenClaims;
  expiresAt: number; // epoch ms
}

export class OidcError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = "OidcError";
  }
}
