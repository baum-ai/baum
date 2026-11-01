import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";

// The fixture daemon keeps its state, credential and socket outside the source
// tree; the spec reads the credential from the same directory.
const stateDir = join(tmpdir(), "baum-app-e2e-daemon");
process.env.BAUM_APP_STATE_DIR = stateDir;

export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  // The fixture daemon answers its first requests slowly on a busy CI runner,
  // so assertions wait longer than Playwright's 5 s default.
  expect: { timeout: 15000 },
  use: {
    baseURL: "http://127.0.0.1:4167",
    headless: true,
    viewport: { width: 1280, height: 900 },
    trace: "retain-on-failure",
    // The first-run tour would cover the pages under test; tour.spec.ts starts it.
    storageState: {
      cookies: [],
      origins: ["http://127.0.0.1:4167", "http://127.0.0.1:1420"].map((origin) => ({
        origin,
        localStorage: [{ name: "baum.tour.v1", value: "done" }],
      })),
    },
  },
  webServer: {
    command: `BAUM_APP_BROWSER_FIXTURE=1 BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR='${stateDir}' ./e2e/fixture -test.run '^TestLocalAppBrowserFixture$' -test.timeout=0`,
    url: "http://127.0.0.1:4167",
    reuseExistingServer: false,
    timeout: 30000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
