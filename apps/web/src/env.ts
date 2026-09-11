import { z } from "zod";

function schemaFor(isProd: boolean) {
  const isLoopbackHttpUrl = (value: string) => {
    try {
      const url = new URL(value);
      return (
        url.protocol === "http:" &&
        (url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "[::1]")
      );
    } catch {
      return false;
    }
  };

  const httpsInProd = (label: string) =>
    z
      .string()
      .url({ message: `${label} must be an absolute URL` })
      .refine((v) => !isProd || v.startsWith("https://") || isLoopbackHttpUrl(v), {
        message: `${label} must use https:// in a production build (http:// is allowed only for localhost)`,
      });

  return z.object({
    VITE_CRM_API_URL: httpsInProd("VITE_CRM_API_URL"),
    VITE_CRM_AUTH_PORTAL_URL: httpsInProd("VITE_CRM_AUTH_PORTAL_URL"),
  });
}

export class EnvValidationError extends Error {
  constructor(public issues: string[]) {
    super(`Invalid environment configuration:\n- ${issues.join("\n- ")}`);
    this.name = "EnvValidationError";
  }
}

export interface AppEnv {
  crmApiUrl: string;
  authPortalUrl: string;
}

/** Validates the Vite env (defaulting to import.meta.env) at module load. A
 * misconfigured deployment fails fast and loud here rather than surfacing as
 * a confusing runtime error deep inside the auth flow. `source`/`isProd` are
 * injectable so tests don't depend on Vite's build-time env replacement. */
export function loadEnv(
  source: Record<string, unknown> = import.meta.env,
  isProd = import.meta.env.PROD,
): AppEnv {
  const result = schemaFor(isProd).safeParse(source);
  if (!result.success) {
    throw new EnvValidationError(result.error.issues.map((i) => `${i.path.join(".")}: ${i.message}`));
  }
  const v = result.data;
  return {
    crmApiUrl: v.VITE_CRM_API_URL,
    authPortalUrl: v.VITE_CRM_AUTH_PORTAL_URL,
  };
}
