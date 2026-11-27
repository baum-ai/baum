import { defineConfig } from "@playwright/test";

// Pictures of the custom tile kit's demo tiles, taken by
// e2e/tile-kit-screens.spec.ts from frame documents the Go kit composed. Not
// part of the test suites; run `just local_app::tile-kit-screens`.
export default defineConfig({
  testDir: "./e2e",
  testMatch: "tile-kit-screens.spec.ts",
  workers: 1,
  timeout: 300_000,
  // Playwright empties its output directory on every run; the pictures live
  // in test-results/tile-kit, beside it.
  outputDir: "test-results/tile-kit-run",
  use: { headless: true },
});
