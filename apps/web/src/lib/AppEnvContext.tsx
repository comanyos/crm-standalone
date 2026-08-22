import { createContext, useContext, type ReactNode } from "react";
import type { AppEnv } from "../env";

const AppEnvContext = createContext<AppEnv | null>(null);

export function AppEnvProvider({ env, children }: { env: AppEnv; children: ReactNode }) {
  return <AppEnvContext.Provider value={env}>{children}</AppEnvContext.Provider>;
}

export function useAppEnv(): AppEnv {
  const ctx = useContext(AppEnvContext);
  if (!ctx) throw new Error("useAppEnv must be used within AppEnvProvider");
  return ctx;
}
