import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Backend ports are overridable so UI tests can run a hermetic backend
// alongside a live local stack (defaults match the devtop dev servers).
const backendPort = process.env.DEVTOP_BACKEND_PORT || '8000'
const copilotPort = process.env.DEVTOP_COPILOT_PORT || '4000'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Cache dir is overridable (UI tests point it at a temp dir to avoid the
  // stale root-owned node_modules/.vite tree left by the Docker dev stack).
  cacheDir: process.env.DEVTOP_VITE_CACHE_DIR || 'node_modules/.vite',
  server: {
    // The test backend writes fixture data (threads, viewstate, sqlite) under
    // e2e/fixtures — exclude it so those writes never trigger a full reload.
    watch: {
      ignored: ['**/e2e/fixtures/**'],
    },
    proxy: {
      '/api/copilotkit': {
        target: `http://127.0.0.1:${copilotPort}`,
        changeOrigin: true,
        secure: false,
      },
      // Matches any /api route EXCEPT if it starts with /api/copilotkit
      '^/api/(?!copilotkit)': {
        target: `http://127.0.0.1:${backendPort}`,
        changeOrigin: true,
        secure: false,
      },
    },
  },
})
