import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router-dom";
import "./index.css";
import { router } from "./App";
import { AuthProvider } from "./auth/AuthProvider";
import { ToastProvider } from "./components/ui/Toast";
import { AppEnvProvider } from "./lib/AppEnvContext";
import { buildOidcConfig } from "./lib/auth-config";
import { loadEnv, EnvValidationError } from "./env";
import { registerServiceWorker } from "./lib/registerServiceWorker";

const rootEl = document.getElementById("root")!;
const root = createRoot(rootEl);

// Before the render, and outside the try: an app that fails to start for a
// configuration reason should still install its shell, so the next visit
// opens fast enough to show the error. It is also before the auth redirect
// an unauthenticated first visit triggers — see registerServiceWorker.
registerServiceWorker();

try {
  const env = loadEnv();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
  });

  root.render(
    <StrictMode>
      <ToastProvider>
        <AppEnvProvider env={env}>
          <AuthProvider config={buildOidcConfig(env)}>
            <QueryClientProvider client={queryClient}>
              <RouterProvider router={router} />
            </QueryClientProvider>
          </AuthProvider>
        </AppEnvProvider>
      </ToastProvider>
    </StrictMode>,
  );
} catch (err) {
  const message = err instanceof EnvValidationError ? err.message : String(err);
  root.render(
    <div style={{ padding: "2rem", fontFamily: "sans-serif", whiteSpace: "pre-wrap" }}>
      <h1>Configuration error</h1>
      <p>{message}</p>
    </div>,
  );
}
