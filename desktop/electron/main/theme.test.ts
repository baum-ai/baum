import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import {
  applyThemeBootstrap,
  parseThemeBootstrap,
  type ThemeBootstrap,
} from "../shared/contract.ts";
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
  windowBackground,
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

function styleOf() {
  const properties: Record<string, string> = {};
  return {
    colorScheme: "",
    properties,
    setProperty(name: string, value: string) {
      properties[name] = value;
    },
  };
}

describe("window background", () => {
  const bootstrap = (base: Base, tokens: Record<string, string> = {}): ThemeBootstrap => ({
    base,
    tokens,
  });

  it("is the built-in page surface of each base", () => {
    expect(windowBackground(bootstrap("light"))).toBe("#ffffff");
    // 89% shadow over the paper, as palette.css mixes it.
    expect(windowBackground(bootstrap("dark"))).toBe("#1c1c1c");
  });

  it("follows a theme's page surface, then its seeds", () => {
    expect(windowBackground(bootstrap("dark", { "--surface-page": "#14202A" }))).toBe("#14202a");
    expect(windowBackground(bootstrap("light", { "--surface-page": "#fed" }))).toBe("#ffeedd");
    expect(windowBackground(bootstrap("light", { "--color-paper": "#faf5ec" }))).toBe("#faf5ec");
    expect(
      windowBackground(
        bootstrap("dark", { "--color-paper": "#ffffff", "--color-shadow": "#102030" }),
      ),
    ).toBe("#2a3947");
  });

  it("falls back to the recipe for a colour Electron cannot take", () => {
    expect(
      windowBackground(bootstrap("light", { "--surface-page": "color-mix(in srgb, red, blue)" })),
    ).toBe("#ffffff");
    expect(windowBackground(bootstrap("dark", { "--color-paper": "white" }))).toBe("#1c1c1c");
  });

  it("the store says when a different theme is saved", () => {
    let changes = 0;
    const store = new ThemeStore(undefined, () => changes++);
    const next = saved(palette("light", [["--surface-page", "#fafafa"]]));
    store.set(next);
    store.set(next);
    expect(changes).toBe(1);
    expect(() => store.set(saved(palette("light", [["bad", "x"]])))).toThrow();
    expect(changes).toBe(1);
  });
});

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
    const root = { dataset: {} as Record<string, string | undefined>, style: styleOf() };
    applyThemeBootstrap(root, themeBootstrap(both, true));
    expect(root.dataset.theme).toBe("dark");
    expect(root.style.colorScheme).toBe("dark");
    expect(root.style.properties).toEqual({ "--a": "d" });
  });

  it("the preload applies values as data, whatever they hold", () => {
    // Shape checks keep such values out, but applying must not rely on it.
    const hostile = "\"</script><script>alert(1)</script>';\u2028\u2029\\";
    const next = saved(palette("light", [["--surface-page", hostile]]));
    const answer = parseThemeBootstrap(JSON.parse(JSON.stringify(themeBootstrap(next, false))));
    expect(answer).toEqual(themeBootstrap(next, false));
    const root = { dataset: {} as Record<string, string | undefined>, style: styleOf() };
    applyThemeBootstrap(root, answer as ThemeBootstrap);
    expect(root.style.properties["--surface-page"]).toBe(hostile);
    expect(parseThemeBootstrap(null)).toBeNull();
    expect(parseThemeBootstrap({ base: "blue", tokens: {} })).toBeNull();
    expect(parseThemeBootstrap({ base: "dark", tokens: { "--a": 1, "--b": "c" } })).toEqual({
      base: "dark",
      tokens: { "--b": "c" },
    });
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
