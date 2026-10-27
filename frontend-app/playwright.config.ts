import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  workers: 2,
  timeout: 45000,
  expect: { timeout: 10000 },
  reporter: "list",
  use: {
    ignoreHTTPSErrors: process.env.E2E_LOCAL_CA === "1",
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8080",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects:
    process.env.E2E_BROWSER === "firefox"
      ? [{ name: "firefox", use: { ...devices["Desktop Firefox"] } }]
      : [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
