// Rasterises the icons scripts/prepare-icons.mjs resolved into target/ with
// the Chromium of the ui-test image's Playwright: target/icon-source.png
// (1024 × 1024) and target/tray-<state>.png (64 × 64). scripts/icons.py
// makes the app's icons from them. Run after prepare-icons.mjs, from oss/app
// (`just desktop::icons`).
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const desktop = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(join(desktop, "../app/package.json"));
const { chromium } = require("@playwright/test");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  for (const [name, size] of [
    ["icon-source", 1024],
    ["tray-idle", 64],
    ["tray-running", 64],
    ["tray-attention", 64],
  ]) {
    const svg = readFileSync(join(desktop, "target", `${name}.svg`)).toString("base64");
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(
      `<body style="margin:0"><img src="data:image/svg+xml;base64,${svg}" width="${size}" height="${size}" style="display:block"></body>`,
    );
    await page.locator("img").evaluate((img) => img.decode());
    await page.screenshot({ path: join(desktop, "target", `${name}.png`), omitBackground: true });
  }
} finally {
  await browser.close();
}
