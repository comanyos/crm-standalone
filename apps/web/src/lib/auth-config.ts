import type { OidcConfig } from "@crm/auth-client";
import type { AppEnv } from "../env";

export const AUTH_CALLBACK_PATH = "/auth/callback";

/** CRM authority is derived server-side from AuthinCore role permissions.
 * This public browser client requests identity data only; it must never ask
 * for a resource wildcard such as crm:* because request parameters are not an
 * authorization boundary. */
const SCOPE = "openid email profile";

export function buildOidcConfig(env: AppEnv): OidcConfig {
  return {
    issuer: env.authIssuerUrl,
    orgId: env.authOrgId,
    clientId: env.authClientId,
    redirectUri: new URL(AUTH_CALLBACK_PATH, window.location.origin).toString(),
    scope: SCOPE,
  };
}
