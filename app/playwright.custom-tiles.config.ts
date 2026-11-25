import { defineConfig } from "@playwright/test";
import application from "./playwright.config";

// Custom tiles on the tiles page: the fixture daemon started with
// BAUM_APP_CUSTOM_TILES=1, so acme/demo#7's reviews carry the synthetic
// custom tiles (oss/internal/tileview/tileviewtest), whose frames load from
// the daemon's real frame route. The main suite starts the fixture without
// them, so its tile page and screenshots are unchanged.
const fixture = application.webServer as Exclude<
  typeof application.webServer,
  unknown[] | undefined
>;
export default defineConfig({
  ...application,
  testDir: "./custom-tile-tests",
  // Playwright empties its output directory on every run; the pictures
  // (CUSTOM_TILE_SHOTS=1) live in test-results/custom-tiles, beside it.
  outputDir: "test-results/custom-tiles-run",
  webServer: { ...fixture, command: `BAUM_APP_CUSTOM_TILES=1 ${fixture.command}` },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
