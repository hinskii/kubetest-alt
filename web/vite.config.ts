import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// GUI and apiserver are same-origin in production (§step-18: `/` = GUI,
// `/api` = apiserver, so cookies from oauth2-proxy work without CORS).
// Dev proxies `/api` at the apiserver's port-forward (default 18080 from
// test/e2e/run.sh) so `npm run dev` works against a real kind cluster.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.KT_API || 'http://127.0.0.1:18080',
        changeOrigin: false,
        rewrite: (p) => p.replace(/^\/api/, ''),
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    exclude: ['node_modules', 'dist', 'src/test/e2e/**', 'src/test/screenshots/**'],
  },
})
