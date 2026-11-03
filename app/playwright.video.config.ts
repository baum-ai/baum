import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";

// Records the product video on usebaum.com: the real app, served by the video
// fixture daemon (TestLocalAppVideoFixture) with made-up work, driven by
// video/demo.spec.ts. Not part of the test suites; see video/README.md.
const stateDir = join(tmpdir(), "baum-app-video-daemon");
// The app follows the system's scheme; BAUM_VIDEO_SCHEME=dark records the
// dark one, for the website in dark mode.
const scheme = process.env.BAUM_VIDEO_SCHEME === "dark" ? "dark" : "light";
process.env.BAUM_APP_STATE_DIR = stateDir;

export default defineConfig({
  testDir: "./video",
  workers: 1,
  timeout: 240_000,
  expect: { timeout: 20_000 },
  use: {
    baseURL: "http://127.0.0.1:4168",
    headless: true,
    // The website shows the video at this size, one CSS pixel to one, so the
    // app's text reads at the size it has in the app. Twice the pixels keep
    // it sharp on high-density screens.
    viewport: { width: 1056, height: 800 },
    deviceScaleFactor: 2,
    colorScheme: scheme,
  },
  webServer: {
    command: `rm -rf '${stateDir}' && mkdir -m 700 -p '${stateDir}' && BAUM_APP_VIDEO_FIXTURE=1 BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR='${stateDir}' ./e2e/video-fixture -test.run '^TestLocalAppVideoFixture$' -test.timeout=0`,
    url: "http://127.0.0.1:4168",
    reuseExistingServer: false,
    timeout: 60_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
