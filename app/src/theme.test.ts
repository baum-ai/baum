import { expect, it } from "vitest";
import { read } from "./tile-kit/harness";
import {
  activeTheme,
  applyTheme,
  builtinThemes,
  THEME_TOKENS,
  tileThemeMessage,
  type Theme,
  type ThemePrefs,
} from "./theme";

const dusk: Theme = {
  id: "c-1",
  name: "Dusk",
  base: "dark",
  tokens: { "--color-paper": "#e8eef4", "--brand-green-600": "#5aa9e6" },
};
const sand: Theme = {
  id: "c-2",
  name: "Sand",
  base: "light",
  tokens: { "--color-paper": "#fdf6e3" },
};
const prefs = (over: Partial<ThemePrefs> = {}): ThemePrefs => ({
  theme: "system",
  light_theme: "default-light",
  dark_theme: "default-dark",
  themes: [...builtinThemes, dusk, sand],
  ...over,
});

it("keeps the allowlist equal to the tile kit's, which a Go test keeps equal to the daemon's", () => {
  const kit = read("v1/bootstrap.js");
  const set = kit.slice(kit.indexOf("const THEME_TOKENS"), kit.indexOf("const MAX_TOKEN_VALUE"));
  const tokens = (set.match(/"--[a-z0-9-]+"/g) ?? []).map((token) => token.slice(1, -1));
  expect(tokens.sort()).toEqual([...THEME_TOKENS].sort());
});

it("picks the theme of the slot the setting names, system following the desktop", () => {
  const p = prefs({ light_theme: "c-2", dark_theme: "c-1" });
  expect(activeTheme(p, false)).toBe(sand);
  expect(activeTheme(p, true)).toBe(dusk);
  expect(activeTheme({ ...p, theme: "light" }, true)).toBe(sand);
  expect(activeTheme({ ...p, theme: "dark" }, false)).toBe(dusk);
});

it("lets any base sit in either slot", () => {
  const theme = activeTheme(prefs({ theme: "light", light_theme: "c-1" }), false);
  expect(theme.base).toBe("dark");
});

it("falls back to the built-in of the slot for an empty or vanished id", () => {
  expect(activeTheme(prefs({ light_theme: "" }), false).id).toBe("default-light");
  expect(activeTheme(prefs({ dark_theme: "gone" }), true).id).toBe("default-dark");
});

it("prefers the preview", () => {
  expect(activeTheme(prefs(), false, dusk)).toBe(dusk);
  expect(activeTheme(prefs(), false, null).id).toBe("default-light");
});

it("applies the base, the colour scheme and the tokens inline", () => {
  const root = document.createElement("html");
  applyTheme(root, dusk);
  expect(root.dataset.theme).toBe("dark");
  expect(root.style.colorScheme).toBe("dark");
  expect(root.style.getPropertyValue("--color-paper")).toBe("#e8eef4");
  expect(root.style.getPropertyValue("--brand-green-600")).toBe("#5aa9e6");
});

it("removes the tokens the previous theme set and the new one does not", () => {
  const root = document.createElement("html");
  applyTheme(root, dusk);
  applyTheme(root, sand);
  expect(root.dataset.theme).toBe("light");
  expect(root.style.getPropertyValue("--color-paper")).toBe("#fdf6e3");
  expect(root.style.getPropertyValue("--brand-green-600")).toBe("");
  applyTheme(root, builtinThemes[0]!);
  expect(root.style.getPropertyValue("--color-paper")).toBe("");
});

it("sets only allowlisted tokens", () => {
  const root = document.createElement("html");
  applyTheme(root, {
    ...sand,
    tokens: { "--color-paper": "#fff", "--not-a-token": "red", background: "red", "--x": "1" },
  });
  expect(root.style.getPropertyValue("--color-paper")).toBe("#fff");
  expect(root.style.getPropertyValue("--not-a-token")).toBe("");
  expect(root.style.getPropertyValue("--x")).toBe("");
  expect(root.style.background).toBe("");
});

it("builds the tile theme message with the allowlisted tokens", () => {
  expect(tileThemeMessage(dusk)).toEqual({
    protocol: "baum.tile/1",
    kind: "theme",
    theme: "dark",
    tokens: dusk.tokens,
  });
  expect(tileThemeMessage({ ...sand, tokens: { "--nope": "#fff" } }).tokens).toEqual({});
  expect(tileThemeMessage(builtinThemes[1]!)).toMatchObject({ theme: "dark", tokens: {} });
});
