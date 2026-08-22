import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const backend = env.AUTHINCORE_DEV_PROXY || 'http://127.0.0.1:18080'
  const tenantHost = env.AUTHINCORE_DEV_TENANT_HOST || 'localhost'
  const apiProxy = {
    target: backend,
    changeOrigin: false,
    headers: { Host: tenantHost },
  }

  return {
    plugins: [react()],
    build: { sourcemap: true },
    server: {
      proxy: {
        '/self-service': apiProxy,
        '/invitations': apiProxy,
      },
    },
  }
})
