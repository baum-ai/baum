import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { initialPage, onboarded } from "./setup.ts";

describe("the first page", () => {
  it("is the setup screen until setup is finished", () => {
    expect(initialPage(false, false)).toBe("/setup.html");
    expect(initialPage(true, false)).toBe("/index.html");
  });

  it("is the connecting screen when the app cannot run baum", () => {
    expect(initialPage(false, true)).toBe("/index.html");
  });

  it("reads the setup section of desktop.json", () => {
    const dir = mkdtempSync(join(tmpdir(), "baum-setup-"));
    try {
      const path = join(dir, "desktop.json");
      expect(onboarded(path)).toBe(false);
      writeFileSync(path, JSON.stringify({ setup: { onboarded: true, commands: [] } }));
      expect(onboarded(path)).toBe(true);
      writeFileSync(path, JSON.stringify({ setup: { onboarded: "yes" } }));
      expect(onboarded(path)).toBe(false);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
