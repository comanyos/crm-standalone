import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const port = Number(env.VITE_WEB_PORT ?? "3000");
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error("VITE_WEB_PORT must be a valid TCP port");
  }

  return {
    plugins: [react()],
    server: {
      host: env.VITE_WEB_HOST ?? "127.0.0.1",
      port,
      // Fail loudly if the configured port is taken rather than silently
      // drifting to a different origin: the API's credentialed CORS
      // allowlist must match this value.
      strictPort: true,
    },
  };
});
