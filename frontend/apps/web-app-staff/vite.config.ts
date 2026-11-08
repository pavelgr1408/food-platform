import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Порт 3001: рядом с web-app (3000). /api/* → gateway без CORS (dev-proxy), как в web-app.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const gateway = env.GATEWAY_URL || "http://localhost:8080";
  return {
    plugins: [react(), tailwindcss()],
    server: {
      port: 3001,
      strictPort: true,
      proxy: { "/api": { target: gateway, changeOrigin: true, rewrite: (p) => p.replace(/^\/api/, "") } },
    },
    preview: { port: 3001, strictPort: true },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test-setup.ts"],
      globals: false,
      exclude: ["e2e/**", "node_modules/**"],
      testTimeout: 20_000,
    },
  };
});
