import { tmpdir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "@playwright/test";
import application from "./playwright.config";

const directory = join(tmpdir(), "baum-app-vite-test");
export default defineConfig({
  ...application,
  testDir: "./development-tests",
  use: { ...application.use, baseURL: "http://127.0.0.1:5173" },
  webServer: [
    application.webServer as Exclude<typeof application.webServer, unknown[] | undefined>,
    {
      command: `node --input-type=module -e 'import {mkdirSync,writeFileSync} from "node:fs"; mkdirSync(${JSON.stringify(directory)},{recursive:true}); writeFileSync(${JSON.stringify(join(directory, "daemon-origin"))},"http://127.0.0.1:4167")' && BAUM_DESKTOP_DEV_DIR='${directory}' pnpm exec vite`,
      url: "http://127.0.0.1:5173",
      reuseExistingServer: false,
      timeout: 30000,
    },
  ],
});
