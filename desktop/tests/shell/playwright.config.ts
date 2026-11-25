import { defineConfig } from "@playwright/test";

// The Electron shell's harness (README "Checking navigation"): the built app
// (`pnpm build`) under a virtual display (`pnpm test:shell` runs it under
// xvfb-run), attached to a mock daemon. Each spec file launches its own app.
export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.ts",
  workers: 1,
  fullyParallel: false,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  outputDir: "../../build/shell-results",
});
