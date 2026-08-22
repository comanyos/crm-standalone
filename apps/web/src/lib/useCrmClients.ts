import { useMemo, useRef } from "react";
import { createCrmClients } from "./connect";
import { useAuth } from "../auth/AuthProvider";
import { useAppEnv } from "./AppEnvContext";

/** One Connect client pair per app lifetime. The access token is read via a
 * ref updated on every render rather than captured in the transport at
 * creation time, so a renewed token is picked up without rebuilding the
 * client (and without the client depending on `session` and being torn down
 * on every renewal). */
export function useCrmClients() {
  const env = useAppEnv();
  const { session } = useAuth();
  const tokenRef = useRef<string | null>(null);
  tokenRef.current = session?.accessToken ?? null;

  return useMemo(() => createCrmClients(env.crmApiUrl, () => tokenRef.current), [env.crmApiUrl]);
}
