import {
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { FILE, loadSection, readSection, updateSection, write } from "./settings.ts";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "baum-desktop-settings-"));
}

const count = (s: Record<string, unknown>) => (typeof s.value === "number" ? s.value : 0);

describe("desktop.json", () => {
  it("keeps sections across reopening and preserves unrelated data", () => {
    const dir = scratch();
    const path = join(dir, FILE);
    writeFileSync(path, '{"future":{"keep":"this"}}');
    for (const name of ["setup", "notifications", "window", "updates"]) {
      updateSection(path, name, (s) => {
        s.value = 7;
      });
    }
    for (const name of ["setup", "notifications", "window", "updates"]) {
      expect(count(readSection(path, name))).toBe(7);
    }
    expect(JSON.parse(readFileSync(path, "utf8")).future.keep).toBe("this");
    expect(readdirSync(dir)).toEqual([FILE]);
    if (process.platform !== "win32") expect(statSync(path).mode & 0o777).toBe(0o600);
  });

  it("keeps every change when updates follow each other", { timeout: 60_000 }, () => {
    const dir = scratch();
    const path = join(dir, FILE);
    for (let i = 0; i < 20; i++) {
      for (const name of ["setup", "window", "setup", "updates", "window", "updates"]) {
        updateSection(path, name, (s) => {
          s.value = count(s) + 1;
        });
      }
    }
    for (const name of ["setup", "window", "updates"]) {
      expect(count(readSection(path, name))).toBe(40);
    }
    expect(readdirSync(dir)).toEqual([FILE]);
  });

  it("never overwrites a damaged document or section", () => {
    const dir = scratch();
    const path = join(dir, FILE);
    for (const data of [
      "{",
      "[]",
      "null",
      '{"window":"bad","setup":{"value":9}}',
      '{"window":[],"setup":{"value":9}}',
      '{"window":null,"setup":{"value":9}}',
      '{"window":true,"setup":{"value":9}}',
      '{"window":5,"setup":{"value":9}}',
    ]) {
      writeFileSync(path, data);
      expect(loadSection(path, "window")).toEqual({});
      expect(() =>
        updateSection(path, "window", (s) => {
          s.value = 3;
        }),
      ).toThrow();
      expect(readFileSync(path, "utf8")).toBe(data);
    }
  });

  it("cleans up and preserves the destination when a replacement fails", () => {
    const dir = scratch();
    const path = join(dir, FILE);
    mkdirSync(path);
    writeFileSync(join(path, "keep"), "unchanged");
    expect(() => write(path, "{}")).toThrow();
    expect(readFileSync(join(path, "keep"), "utf8")).toBe("unchanged");
    expect(readdirSync(dir)).toEqual([FILE]);
  });
});
