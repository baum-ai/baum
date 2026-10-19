import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4167",
    headless: true,
    viewport: { width: 1280, height: 900 },
    trace: "retain-on-failure",
  },
  webServer: {
    command:
      "BAUM_APP_BROWSER_FIXTURE=1 BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR=e2e/daemon-state ./e2e/fixture -test.run '^TestLocalAppBrowserFixture$' -test.timeout=0",
    url: "http://127.0.0.1:4167",
    reuseExistingServer: false,
    timeout: 30000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
