import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Порт 3000 — потому что gateway по умолчанию разрешает CORS для http://localhost:3000,
// а payment возвращает с шлюза на http://localhost:3000/orders (DEFAULT_RETURN_URL).
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const gateway = env.GATEWAY_URL || "http://localhost:8080";
  return {
    plugins: [react(), tailwindcss()],
    server: {
      port: 3000,
      strictPort: true,
      // /api/* → gateway (без префикса): исключает коллизию SPA-маршрутов /orders/:id с API и снимает CORS в dev
      proxy: { "/api": { target: gateway, changeOrigin: true, rewrite: (p) => p.replace(/^\/api/, "") } },
    },
    preview: { port: 3000, strictPort: true },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test-setup.ts"],
      globals: false,
      exclude: ["e2e/**", "node_modules/**"],
      // сквозные UI-тесты долгие (userEvent + MSW); на медленном CI дефолтных 5 с на тест не хватает
      testTimeout: 20_000,
    },
  };
});
