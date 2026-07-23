import { defineConfig, devices } from "@playwright/test";

const inCI = process.env.CI === "true";

export default defineConfig({
  expect: {
    timeout: 5_000,
  },
  forbidOnly: inCI,
  fullyParallel: false,
  outputDir: "test-results",
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
      },
    },
  ],
  reporter: [["line"]],
  retries: inCI ? 1 : 0,
  testDir: "./e2e",
  testMatch: "**/*.browser.ts",
  timeout: 90_000,
  use: {
    baseURL: "http://127.0.0.1:4173",
    headless: true,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },
  webServer: {
    command: "npm run dev -- --host 0.0.0.0 --port 4173 --strictPort",
    reuseExistingServer: !inCI,
    timeout: 120_000,
    url: "http://127.0.0.1:4173/e2e/harness.html",
  },
  workers: 1,
});
