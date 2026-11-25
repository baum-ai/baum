// The active colour theme, cached for the screens that cannot ask the daemon.
// The setup, connecting and tray screens allow only the app's own bridge in
// their CSP, and the daemon's pages sit on a loopback origin whose port
// changes, so their `localStorage` is not shared. A daemon page therefore tells
// the app what the saved theme resolves to (`save_theme`), the app keeps it in
// the `theme` section of `desktop.json`, and the native screens read it
// (`theme_state`). The app checks only the shape of what it stores; the daemon
// owns what a theme may contain. `themeBootstrap` is what the preload applies
// to the app's windows before their first paint (`THEME_CHANNEL`).
import type { ThemeBootstrap } from "../shared/contract.ts";
import { registerCommand } from "./commands.ts";
import { loadSection, updateSection } from "./settings.ts";

const SECTION = "theme";

export const MAX_TOKENS = 40;
export const MAX_NAME = 40;
export const MAX_VALUE = 64;
export const MAX_TOTAL = 32 * 1024;

export type Mode = "system" | "light" | "dark";
export type Base = "light" | "dark";

/** A theme as a screen applies it: the built-in palette it changes and its
 *  token overrides. */
export interface Palette {
  base: Base;
  tokens: Record<string, string>;
}

/** What the screens need to pick the active theme: the mode and the theme in
 *  each slot. A slot that is null has its built-in theme. */
export interface Saved {
  mode: Mode;
  light: Palette | null;
  dark: Palette | null;
}

export const defaultSaved = (): Saved => ({ mode: "system", light: null, dark: null });

const bytes = (text: string) => Buffer.byteLength(text);

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parsePalette(value: unknown): Palette | null {
  if (value === undefined || value === null) return null;
  if (!isObject(value)) throw new Error("a theme must be an object");
  const { base, tokens = {} } = value;
  if (base !== "light" && base !== "dark") throw new Error("a theme's base is light or dark");
  if (!isObject(tokens)) throw new Error("a theme's tokens must be an object");
  const entries = Object.entries(tokens).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  for (const [name, token] of entries) {
    if (typeof token !== "string") throw new Error(`the value of ${name} must be a string`);
  }
  return { base, tokens: Object.fromEntries(entries) as Record<string, string> };
}

/** Types only: the words a mode and base allow and the field types. Absent
 *  fields take their defaults. */
export function parseSaved(value: unknown): Saved {
  if (!isObject(value)) throw new Error("a theme must be an object");
  const mode = value.mode === undefined ? "system" : value.mode;
  if (mode !== "system" && mode !== "light" && mode !== "dark") {
    throw new Error("a theme's mode is system, light or dark");
  }
  return { mode, light: parsePalette(value.light), dark: parsePalette(value.dark) };
}

function tokenNameOk(name: string): boolean {
  return (
    name.startsWith("--") &&
    name.length > 2 &&
    bytes(name) <= MAX_NAME + 2 &&
    /^--[a-z0-9-]+$/.test(name)
  );
}

function checkPalette(palette: Palette): number {
  const entries = Object.entries(palette.tokens);
  if (entries.length > MAX_TOKENS) throw new Error(`a theme has at most ${MAX_TOKENS} tokens`);
  let size = 0;
  for (const [name, value] of entries) {
    if (!tokenNameOk(name)) throw new Error(`${JSON.stringify(name)} is not a token name`);
    if (bytes(value) > MAX_VALUE)
      throw new Error(`the value of ${name} is over ${MAX_VALUE} bytes`);
    size += bytes(name) + bytes(value);
  }
  return size;
}

/** Shape only: names, counts and sizes. */
export function check(saved: Saved): void {
  let total = 0;
  for (const palette of [saved.light, saved.dark]) {
    if (palette) total += checkPalette(palette);
  }
  withinTotal(total);
}

export function withinTotal(size: number): void {
  if (size > MAX_TOTAL) throw new Error(`a theme is over ${MAX_TOTAL / 1024} KiB`);
}

const same = (a: Saved, b: Saved) => JSON.stringify(a) === JSON.stringify(b);

/** The saved theme and where it is kept; without a path it stays in memory. */
export class ThemeStore {
  private saved: Saved;

  constructor(private readonly path?: string) {
    this.saved = path === undefined ? defaultSaved() : ThemeStore.load(path);
  }

  private static load(path: string): Saved {
    try {
      const saved = parseSaved(loadSection(path, SECTION));
      check(saved);
      return saved;
    } catch {
      console.error("baum desktop: the saved theme is malformed, using the default");
      return defaultSaved();
    }
  }

  get(): Saved {
    return structuredClone(this.saved);
  }

  /** Replaces and saves the theme, or refuses it and changes nothing: a theme
   *  that fails the shape check or cannot be written (including into a damaged
   *  section) leaves the current one in place. */
  set(next: Saved): void {
    check(next);
    if (same(this.saved, next)) return;
    // Memory follows the disk, so a failed write is neither shown nor
    // mistaken for saved when the same theme comes again.
    if (this.path !== undefined) {
      try {
        updateSection(this.path, SECTION, (section) => {
          for (const key of Object.keys(section)) delete section[key];
          Object.assign(section, structuredClone(next));
        });
      } catch (e) {
        console.error(`baum desktop: cannot save the theme: ${String(e)}`);
        throw new Error(`cannot save the theme: ${e instanceof Error ? e.message : String(e)}`);
      }
    }
    this.saved = structuredClone(next);
  }
}

/** Chooses the slot (the system's colour scheme in system mode) and returns
 *  its base and tokens. Unlike `applyTheme`, which manages only the
 *  allowlisted tokens, it carries every stored token, so one outside the
 *  allowlist would stick. Only a daemon page calling `save_theme` directly can
 *  store one, and the effect is cosmetic. The page's own code applies the live
 *  theme right after. */
export function themeBootstrap(saved: Saved, prefersDark: boolean): ThemeBootstrap {
  const slot: Base =
    saved.mode === "light" || saved.mode === "dark" ? saved.mode : prefersDark ? "dark" : "light";
  const theme = saved[slot];
  return { base: theme ? theme.base : slot, tokens: theme ? { ...theme.tokens } : {} };
}

/** `theme_state` for the native screens and `save_theme` for the daemon's pages. */
export function registerThemeCommands(store: ThemeStore): void {
  registerCommand("theme_state", () => store.get());
  registerCommand("save_theme", (args) => {
    if (args.mode === undefined) throw new Error("missing mode");
    store.set(parseSaved({ mode: args.mode, light: args.light, dark: args.dark }));
  });
}
