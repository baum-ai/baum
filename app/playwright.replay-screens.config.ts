import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";

// Pictures of a replay run's layouts, taken by e2e/replay-screens.spec.ts
// against the real-reviews fixture daemon serving the run
// (TestLocalAppRealReviews with BAUM_APP_REPLAY_RUN). Not part of the test
// suites; see `just replay-screens` and "Pictures of a run" in
// ../cmd/replay/README.md.
const stateDir = join(tmpdir(), "baum-app-replay-screens-daemon");
process.env.BAUM_APP_STATE_DIR = stateDir;

export default defineConfig({
  testDir: "./e2e",
  testMatch: "replay-screens.spec.ts",
  workers: 1,
  timeout: 0,
  expect: { timeout: 20_000 },
  outputDir: "test-results/replay-screens-run",
  use: {
    baseURL: "http://127.0.0.1:4169",
    headless: true,
    actionTimeout: 30_000,
    navigationTimeout: 60_000,
    viewport: { width: 1440, height: 900 },
  },
  webServer: {
    // Seeding copies review stores and clones each repository per layout.
    command: `rm -rf '${stateDir}' && mkdir -m 700 -p '${stateDir}' && BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR='${stateDir}' ./e2e/fixture -test.run '^TestLocalAppRealReviews$' -test.timeout=0`,
    url: "http://127.0.0.1:4169",
    reuseExistingServer: false,
    timeout: 900_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
