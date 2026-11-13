import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";

// Pictures of saved reviews from a private dataset, taken by
// e2e/real-review-screens.spec.ts against the real-reviews fixture daemon
// (TestLocalAppRealReviews). Not part of the test suites; see "Real reviews"
// in README.md.
const stateDir = join(tmpdir(), "baum-app-real-reviews-daemon");
process.env.BAUM_APP_STATE_DIR = stateDir;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "real-review-screens.spec.ts",
  workers: 1,
  timeout: 0,
  expect: { timeout: 20_000 },
  // Playwright empties its output directory on every run; the pictures live
  // in test-results/real-reviews, beside it.
  outputDir: "test-results/real-reviews-run",
  use: {
    baseURL: "http://127.0.0.1:4169",
    headless: true,
    viewport: { width: 1440, height: 900 },
  },
  webServer: {
    // Seeding copies review stores and clones each repository, which takes a
    // while for a long pick list.
    command: `rm -rf '${stateDir}' && mkdir -m 700 -p '${stateDir}' && BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR='${stateDir}' ./e2e/fixture -test.run '^TestLocalAppRealReviews$' -test.timeout=0`,
    url: "http://127.0.0.1:4169",
    reuseExistingServer: false,
    timeout: 600_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
