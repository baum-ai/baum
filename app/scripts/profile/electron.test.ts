import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it } from "node:test";

const desktop = join(import.meta.dirname, "../../../desktop");

describe("the isolated image's chromedriver", () => {
  it("is the one for the Electron the app pins", () => {
    const dockerfile = readFileSync(join(desktop, "isolated.Dockerfile"), "utf8");
    const pinned = dockerfile.match(/^ARG ELECTRON_VERSION=(\S+)$/m)?.[1];
    const app = JSON.parse(readFileSync(join(desktop, "package.json"), "utf8")) as {
      devDependencies: Record<string, string>;
    };
    const electron = app.devDependencies.electron;
    assert.match(electron ?? "", /^\d+\.\d+\.\d+$/, "package.json pins an exact Electron");
    // chromedriver drives only the Chromium of its own Electron release.
    assert.equal(pinned, electron);
  });
});
