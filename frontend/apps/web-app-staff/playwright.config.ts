import { defineConfig, devices } from "@playwright/test";

// e2e гоняется против сборки в режиме mock (MSW в браузере): бэкенд не нужен.
// В песочнице браузер предустановлен: PW_CHROMIUM=/opt/pw-browsers/chromium-1243/chrome-linux64/chrome
const exe = process.env.PW_CHROMIUM;
export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  retries: 0,
  use: { baseURL: "http://localhost:3001", trace: "retain-on-failure", ...(exe ? { launchOptions: { executablePath: exe } } : {}) },
  // касса работает на планшете и десктопе; телефон — запасной вариант
  projects: [
    { name: "tablet", use: { ...devices["Galaxy Tab S4 landscape"] } },
    { name: "desktop", use: { ...devices["Desktop Chrome"] } },
  ],
  webServer: {
    command: "npm run build:mock && VITE_API_MODE=mock npx vite preview",
    url: "http://localhost:3001",
    reuseExistingServer: true,
    timeout: 120_000,
  },
});
