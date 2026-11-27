import { defineConfig } from "@playwright/test";

// Pictures of tile types (the library's, or any directory of the library's
// layout), taken by e2e/tile-type-screens.spec.ts from what the Go library
// check wrote. Not part of the test suites; run
// `just local_app::tile-type-screens`.
export default defineConfig({
  testDir: "./e2e",
  testMatch: "tile-type-screens.spec.ts",
  workers: 1,
  timeout: 900_000,
  outputDir: "test-results/tile-types-run",
  use: { headless: true },
});
