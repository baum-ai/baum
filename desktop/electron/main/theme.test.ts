import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import { FILE } from "./settings.ts";
import {
  type Base,
  check,
  defaultSaved,
  MAX_NAME,
  MAX_TOKENS,
  MAX_TOTAL,
  MAX_VALUE,
  type Palette,
  parseSaved,
  registerThemeCommands,
  type Saved,
  ThemeStore,
  themeBootstrap,
  themeBootstrapScript,
  withinTotal,
} from "./theme.ts";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "baum-desktop-theme-"));
}

function palette(base: Base, tokens: [string, string][]): Palette {
  return { base, tokens: Object.fromEntries(tokens) };
}

function saved(light: Palette): Saved {
  return { mode: "light", light, dark: null };
}

describe("theme", () => {
  it("a saved theme survives a restart", () => {
    const dir = scratch();
    const next: Saved = {
      mode: "dark",
      light: palette("light", [["--surface-page", "white"]]),
      dark: palette("dark", [["--color-paper", "black"]]),
    };
    new ThemeStore(join(dir, FILE)).set(next);
    expect(new ThemeStore(join(dir, FILE)).get()).toEqual(next);
  });

  it("a failed write is not kept in memory", () => {
    const dir = scratch();
    // A file where the directory should be makes every write fail.
    const blocker = join(dir, "blocker");
    writeFileSync(blocker, "x");
    const store = new ThemeStore();
    const failing = new ThemeStore(join(blocker, FILE));
    expect(store.get()).toEqual(defaultSaved());
    const next = saved(palette("light", [["--surface-page", "white"]]));
    expect(() => failing.set(next)).toThrow(/cannot save the theme/);
    expect(failing.get()).toEqual(defaultSaved());
    // The retry is not mistaken for already saved.
    expect(() => failing.set(next)).toThrow();
  });

  it("nothing saved follows the system", () => {
    expect(new ThemeStore(join(scratch(), FILE)).get()).toEqual(defaultSaved());
    expect(defaultSaved().mode).toBe("system");
  });

  it("each limit refuses and leaves the saved theme", () => {
    const store = new ThemeStore();
    const good = saved(palette("light", [["--surface-page", "white"]]));
    store.set(good);
    const tooMany = palette(
      "light",
      Array.from({ length: MAX_TOKENS + 1 }, (_, i) => [`--t-${i}`, "black"]),
    );
    const longValue = "x".repeat(MAX_VALUE + 1);
    const longName = `--${"a".repeat(MAX_NAME + 1)}`;
    const bad = [
      saved(tooMany),
      saved(palette("light", [["--surface-page", longValue]])),
      saved(palette("light", [[longName, "white"]])),
      saved(palette("light", [["surface-page", "white"]])),
      saved(palette("light", [["--", "white"]])),
      saved(palette("light", [["--Surface", "white"]])),
      saved(palette("light", [["--a b", "white"]])),
      saved(palette("light", [["--a;b", "white"]])),
    ];
    for (const next of bad) {
      expect(() => store.set(next)).toThrow();
      expect(store.get()).toEqual(good);
    }
    // The longest allowed name and value pass.
    const edge = saved(palette("light", [[`--${"a".repeat(MAX_NAME)}`, "x".repeat(MAX_VALUE)]]));
    store.set(edge);
    expect(store.get()).toEqual(edge);
  });

  it("the total size is limited", () => {
    expect(() => withinTotal(MAX_TOTAL)).not.toThrow();
    expect(() => withinTotal(MAX_TOTAL + 1)).toThrow();
    // Two themes at every other limit stay under it.
    const slot = palette(
      "dark",
      Array.from({ length: MAX_TOKENS }, (_, i) => [
        `--${String(i).padStart(38, "0")}`,
        "x".repeat(MAX_VALUE),
      ]),
    );
    expect(() => check({ mode: "system", light: slot, dark: slot })).not.toThrow();
  });

  it("only known words are a mode or a base", () => {
    for (const mode of ["system", "light", "dark"]) {
      expect(parseSaved({ mode }).mode).toBe(mode);
    }
    for (const word of ["auto", "Dark", "", "sepia", 1, null]) {
      expect(() => parseSaved({ mode: word }), String(word)).toThrow();
      expect(() => parseSaved({ mode: "light", light: { base: word } }), String(word)).toThrow();
    }
    expect(() => parseSaved({ light: { base: "system" } })).toThrow();
    expect(() => parseSaved({ light: { base: "light", tokens: { "--a": 1 } } })).toThrow();
    expect(() => parseSaved({ light: { base: "light", tokens: [] } })).toThrow();
    expect(parseSaved({}).mode).toBe("system");
    expect(parseSaved({ light: { base: "light" } }).light).toEqual({ base: "light", tokens: {} });
  });

  it("a malformed saved section is the default", () => {
    for (const content of [
      "{",
      '{"theme":{"mode":"sepia"}}',
      '{"theme":{"mode":"light","light":{"base":"light","tokens":{"x":"y"}}}}',
    ]) {
      const dir = scratch();
      mkdirSync(dir, { recursive: true });
      writeFileSync(join(dir, FILE), content);
      expect(new ThemeStore(join(dir, FILE)).get()).toEqual(defaultSaved());
    }
  });

  it("keeps the other sections of desktop.json", () => {
    const dir = scratch();
    const path = join(dir, FILE);
    writeFileSync(path, '{"window":{"zoom":1.5}}');
    new ThemeStore(path).set(saved(palette("light", [["--a", "b"]])));
    expect(JSON.parse(readFileSync(path, "utf8")).window).toEqual({ zoom: 1.5 });
  });

  it("the script cannot be broken out of", () => {
    // Shape checks keep such values out, but the script must not rely on it.
    const hostile = "\"</script><script>alert(1)</script>';\u2028\u2029\\";
    const next = saved(palette("light", [["--surface-page", hostile]]));
    const script = themeBootstrapScript(themeBootstrap(next, false));
    expect(script).not.toContain("</script");
    expect(script).not.toContain("<script");
    expect(script).not.toContain("\u2028");
    expect(script).not.toContain("\u2029");
    expect(script).toContain("\\u003c/script>");
    expect(script).toContain('\\"\\u003c/script>');
    // The embedded data parses back to exactly the theme.
    const start = script.indexOf("var theme = ") + "var theme = ".length;
    const end = script.indexOf(";\n", start);
    expect(JSON.parse(script.slice(start, end))).toEqual(themeBootstrap(next, false));
  });

  it("the bootstrap picks the slot and sets the base", () => {
    expect(themeBootstrap(defaultSaved(), false)).toEqual({ base: "light", tokens: {} });
    expect(themeBootstrap(defaultSaved(), true)).toEqual({ base: "dark", tokens: {} });
    const both: Saved = {
      mode: "system",
      light: palette("light", [["--a", "l"]]),
      dark: palette("dark", [["--a", "d"]]),
    };
    expect(themeBootstrap(both, true).tokens).toEqual({ "--a": "d" });
    expect(themeBootstrap(both, false).tokens).toEqual({ "--a": "l" });
    expect(themeBootstrap({ ...both, mode: "dark" }, false).base).toBe("dark");
    // A slot's base can differ from the slot.
    expect(
      themeBootstrap({ mode: "dark", light: null, dark: palette("light", []) }, true).base,
    ).toBe("light");
    const script = themeBootstrapScript(themeBootstrap(both, true));
    expect(script).toContain("root.dataset.theme = theme.base");
    expect(script).toContain("root.style.colorScheme = theme.base");
    expect(script).toContain("setProperty");
  });
});

describe("theme commands", () => {
  beforeEach(clearCommands);
  const native = { window: null, role: "setup-screen", origin: "app://baum" } as const;
  const daemon = { window: null, role: "daemon-pages", origin: "http://127.0.0.1:4100" } as const;

  it("save_theme stores and theme_state reads", async () => {
    registerThemeCommands(new ThemeStore());
    const light = { base: "light", tokens: { "--surface-page": "white" } };
    await dispatch("save_theme", { mode: "light", light, dark: null }, daemon);
    expect(await dispatch("theme_state", {}, native)).toEqual({ mode: "light", light, dark: null });
    await expect(
      dispatch("save_theme", { mode: "sepia", light: null, dark: null }, daemon),
    ).rejects.toThrow();
    await expect(dispatch("save_theme", { light }, daemon)).rejects.toThrow();
    await expect(dispatch("save_theme", { mode: "light" }, native)).rejects.toThrow(/not allowed/);
  });
});
