import { defineConfig, devices } from "@playwright/test";

// The standalone app is built from source into a scratch directory and served
// by a fixture server that stands in for `baum internal view`, so the suite needs
// neither the Go binary nor any hosted API.
const port = Number.parseInt(process.env.E2E_PORT ?? "4173", 10);

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command:
      "pnpm exec vite build --config vite.app.config.ts --outDir ../e2e-dist && node --experimental-strip-types e2e/server.ts",
    url: `http://127.0.0.1:${port}/api/health`,
    reuseExistingServer: false,
    timeout: 120_000,
    env: { PORT: String(port), E2E_DIST: "e2e-dist" },
  },
});
