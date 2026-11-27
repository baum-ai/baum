import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";

// The fixture daemon keeps its state, credential and socket outside the source
// tree; the spec reads the credential from the same directory.
const stateDir = join(tmpdir(), "baum-app-e2e-daemon");
process.env.BAUM_APP_STATE_DIR = stateDir;

// CI runs this suite on three runners, split by file so that each takes about
// as long as the others (.github/workflows/ci.yml). BAUM_E2E_GROUP=a and c run
// the files listed here; b runs every other file, so a new spec lands there.
// Rebalance from a JSON report's durations when the runners drift apart.
const groups: Record<string, string[]> = {
  a: ["review-view.spec.ts", "app.spec.ts", "themes.spec.ts", "tile-check.spec.ts"],
  c: ["session-panel.spec.ts"],
};
const group = process.env.BAUM_E2E_GROUP;
const glob = (files: string[]) => files.map((file) => `**/e2e/${file}`);
let selection = {};
if (group === "b") {
  selection = { testIgnore: glob(Object.values(groups).flat()) };
} else if (group) {
  const files = groups[group];
  if (!files) {
    throw new Error(`BAUM_E2E_GROUP must be a, b or c, not ${JSON.stringify(group)}`);
  }
  selection = { testMatch: glob(files) };
}

export default defineConfig({
  testDir: "./e2e",
  ...selection,
  workers: 1,
  // The fixture daemon answers its first requests slowly on a busy CI runner,
  // so assertions wait longer than Playwright's 5 s default.
  expect: { timeout: 15000 },
  use: {
    baseURL: "http://127.0.0.1:4167",
    headless: true,
    viewport: { width: 1280, height: 900 },
    trace: "retain-on-failure",
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
