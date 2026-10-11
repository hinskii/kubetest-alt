import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Control Center (Go) serves the build from its binary (go:embed), so the
// build lands in internal/controlcenter/webui/dist. In dev, Vite serves the
// UI and proxies everything the Go server still owns to `make dev-ui`.
const backend = process.env.KUBETEST_CC_URL ?? "http://localhost:8090";
const goOwned = ["/api", "/live", "/clusters", "/healthz", "/metrics"];

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
    rolldownOptions: {
      output: {
        codeSplitting: {
          // CodeMirror's core in one named chunk, shared by the read-only
          // YAML view and the editor (budgeted in .size-limit.json); the
          // editor's languages stay in the editor's chunk.
          groups: [
            {
              name: "codemirror",
              test: /[\\/]node_modules[\\/](@codemirror[\\/](state|view|language|commands|search|autocomplete|lint|lang-yaml)|@lezer[\\/](common|highlight|lr|yaml)|@uiw[\\/]|crelt|style-mod|w3c-keyname)[\\/]/,
            },
          ],
        },
      },
    },
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
    coverage: {
      provider: "v8",
      include: ["src/**"],
      // Generated code, shadcn's copied primitives and test helpers.
      exclude: [
        "src/**/*.test.{ts,tsx}",
        "src/test/**",
        "src/routeTree.gen.ts",
        "src/api/schema.d.ts",
        "src/components/ui/**",
        "src/main.tsx",
      ],
      reporter: ["text-summary"],
      thresholds: { lines: 80 },
    },
  },
});
