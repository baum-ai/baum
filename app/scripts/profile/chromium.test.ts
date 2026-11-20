import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { describe, it } from "node:test";

const app = join(import.meta.dirname, "../..");

describe("the isolated image's Chromium", () => {
  it("is the one the locked Playwright runs", () => {
    const dockerfile = readFileSync(join(app, "../desktop/isolated.Dockerfile"), "utf8");
    const arg = (name: string) => dockerfile.match(new RegExp(`^ARG ${name}=(\\S+)$`, "m"))?.[1];
    const lock = readFileSync(join(app, "pnpm-lock.yaml"), "utf8");
    const locked = lock.match(/^ {2}playwright-core@([\d.]+):$/m)?.[1];
    assert.ok(locked, "pnpm-lock.yaml locks playwright-core");
    assert.equal(arg("PLAYWRIGHT_VERSION"), locked);
    const require = createRequire(join(app, "package.json"));
    const test = dirname(require.resolve("@playwright/test/package.json"));
    const core = dirname(
      createRequire(join(test, "package.json")).resolve("playwright-core/package.json"),
    );
    const browsers = JSON.parse(readFileSync(join(core, "browsers.json"), "utf8")) as {
      browsers: { name: string; browserVersion: string }[];
    };
    const chromium = browsers.browsers.find((b) => b.name === "chromium");
    // chromedriver comes from Chrome for Testing for this version.
    assert.equal(arg("CHROMIUM_VERSION"), chromium?.browserVersion);
  });
});
