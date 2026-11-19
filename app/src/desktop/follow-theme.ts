import { invoke } from "./native";
import { applyTheme, builtinThemes, type Theme, type ThemeBase } from "../theme";

/** What the app caches for these screens (`theme_state`): the mode and the
 *  theme in each slot, `null` for a slot with its built-in theme. */
export type SavedTheme = {
  mode?: string;
  light?: { base: ThemeBase; tokens: Record<string, string> } | null;
  dark?: { base: ThemeBase; tokens: Record<string, string> } | null;
};

/** The theme to show for the cached state: the slot the mode picks (system
 *  follows the operating system), or that slot's built-in theme when the
 *  state is missing or the slot is empty. */
export function resolveTheme(saved: SavedTheme | null | undefined, systemDark: boolean): Theme {
  const mode = saved?.mode;
  const slot: ThemeBase =
    mode === "light" || mode === "dark" ? mode : systemDark ? "dark" : "light";
  const palette = saved?.[slot];
  if (palette && (palette.base === "light" || palette.base === "dark"))
    return {
      id: `saved-${slot}`,
      name: slot,
      base: palette.base,
      tokens: palette.tokens ?? {},
    };
  return builtinThemes.find((theme) => theme.id === `default-${slot}`)!;
}

/** Makes a native screen follow the active theme. The windows' initialization
 *  script (`theme.rs`) has already painted the saved theme; this reads it
 *  again, applies it with the app's own `applyTheme`, follows the system's
 *  appearance in system mode, and reads again when the window is shown or
 *  focused, since the theme changes in another window. Without the cached
 *  state (a browser, an older app) it follows the system's light or dark. */
export function followTheme(): () => void {
  const dark = window.matchMedia("(prefers-color-scheme: dark)");
  let saved: SavedTheme | null | undefined;
  let loaded = false;
  let reads = 0;
  const apply = () => {
    if (loaded) applyTheme(document.documentElement, resolveTheme(saved, dark.matches));
  };
  const read = () => {
    const mine = ++reads;
    invoke<SavedTheme | null>("theme_state").then(
      (next) => {
        if (mine !== reads) return;
        saved = next && typeof next === "object" ? next : null;
        loaded = true;
        apply();
      },
      () => {
        if (mine !== reads) return;
        saved = null;
        loaded = true;
        apply();
      },
    );
  };
  const visible = () => {
    if (document.visibilityState === "visible") read();
  };
  dark.addEventListener("change", apply);
  window.addEventListener("focus", read);
  document.addEventListener("visibilitychange", visible);
  read();
  return () => {
    reads++;
    dark.removeEventListener("change", apply);
    window.removeEventListener("focus", read);
    document.removeEventListener("visibilitychange", visible);
  };
}
