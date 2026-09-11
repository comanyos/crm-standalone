import { useMemo } from "react";
import { createCrmClients } from "./connect";
import { useAppEnv } from "./AppEnvContext";

/** One cookie-authenticated Connect client pair per app lifetime. */
export function useCrmClients() {
  const env = useAppEnv();
  return useMemo(() => createCrmClients(env.crmApiUrl), [env.crmApiUrl]);
}
