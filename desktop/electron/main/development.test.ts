import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { developmentDir, frontend, isSetupPage, setupPage } from "./development.ts";

const launch = "http://127.0.0.1:4100/#token=abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";

describe("development", () => {
  it("is on only for an unpackaged app with BAUM_DESKTOP_DEV_DIR", () => {
    expect(developmentDir({ BAUM_DESKTOP_DEV_DIR: "/d" }, false)).toBe("/d");
    expect(developmentDir({ BAUM_DESKTOP_DEV_DIR: "/d" }, true)).toBeNull();
    expect(developmentDir({}, false)).toBeNull();
    expect(frontend(launch, null)).toBe(launch);
    expect(setupPage(null)).toBeNull();
    expect(isSetupPage("http://localhost:1420/setup.html", null)).toBe(false);
  });

  it("loads the daemon through Vite and shares only its origin", () => {
    const dir = mkdtempSync(join(tmpdir(), "baum-dev-"));
    try {
      expect(frontend(launch, dir)).toBe(launch.replace(":4100", ":1420"));
      expect(readFileSync(join(dir, "daemon-origin"), "utf8")).toBe("http://127.0.0.1:4100");
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
    expect(() => frontend(launch, "/nonexistent/dir")).toThrow(/development proxy/);
  });

  it("recognises Vite's setup screen exactly", () => {
    expect(setupPage("/d")).toBe("http://localhost:1420/setup.html");
    expect(isSetupPage("http://localhost:1420/setup.html#step", "/d")).toBe(true);
    for (const url of [
      "http://localhost:1420/setup.html?x",
      "http://localhost:1420/setup",
      "http://127.0.0.1:1420/setup.html",
      "http://localhost:1421/setup.html",
    ]) {
      expect(isSetupPage(url, "/d"), url).toBe(false);
    }
  });
});
