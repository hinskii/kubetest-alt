import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Control Center (Go) serves the build from its binary (go:embed), so the
// build lands in internal/controlcenter/webui/dist. In dev, Vite serves the
// UI and proxies everything the Go server still owns to `make dev-ui`.
const backend = process.env.KUBETEST_CC_URL ?? "http://localhost:8090";
const goOwned = ["/api", "/live", "/static", "/clusters", "/healthz", "/metrics"];

export default defineConfig({
  plugins: [
    tanstackRouter() /* options: tsr.config.json, shared with `tsr generate` */,
    react(),
    tailwindcss(),
  ],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: {
    outDir: "../internal/controlcenter/webui/dist",
    emptyOutDir: false, // keeps dist/.gitkeep; old hashed files are removed by `make web-build`
    assetsDir: "assets",
  },
  server: {
    port: 5173,
    proxy: Object.fromEntries(goOwned.map((p) => [p, { target: backend, changeOrigin: true, ws: true }])),
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
