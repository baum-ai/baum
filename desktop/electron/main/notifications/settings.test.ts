import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { FILE } from "../settings.ts";
import { defaultSettings, Store } from "./settings.ts";
import { KINDS } from "./toast.ts";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "baum-desktop-notifications-"));
}

describe("notification settings", () => {
  it("everything is on and unasked by default", () => {
    const settings = Store.inDir(scratch()).load();
    expect(KINDS.every((k) => settings.kinds[k])).toBe(true);
    expect(settings.permission).toBe("unknown");
  });

  it("settings survive a restart", () => {
    const dir = scratch();
    const store = Store.inDir(dir);
    const settings = store.load();
    settings.kinds.automatic = false;
    settings.permission = "denied";
    store.save(settings);
    const again = Store.inDir(dir).load();
    expect(again).toEqual(settings);
    expect(!again.kinds.automatic && again.kinds.failed).toBe(true);
  });

  it("unspecified kinds are on", () => {
    const dir = scratch();
    writeFileSync(join(dir, FILE), '{"notifications":{"kinds":{"failed":false,"future":false}}}');
    const settings = Store.inDir(dir).load();
    expect(!settings.kinds.failed && settings.kinds.finished && settings.kinds.automatic).toBe(
      true,
    );
    // The other kinds keep their defaults.
    const others = [
      "checks_failed",
      "conflict",
      "changes_requested",
      "ready_to_merge",
      "fix_pushed",
      "fix_failed",
      "session_permission",
      "session_input",
    ] as const;
    expect(others.every((k) => settings.kinds[k])).toBe(true);
  });

  it("a damaged file is the defaults", () => {
    const dir = scratch();
    writeFileSync(join(dir, FILE), "{");
    const store = Store.inDir(dir);
    expect(store.load()).toEqual(defaultSettings());
    expect(() => store.save(defaultSettings())).toThrow();
    expect(readFileSync(join(dir, FILE), "utf8")).toBe("{");
  });
});
