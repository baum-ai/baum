import type { CSSProperties } from "react";

/** Custom colour themes: which theme is active, and putting it on the page.
 *  Pure functions; preferences.tsx runs them in an effect. */

/** The palette tokens a theme may set: the same list as `themes.Tokens` in
 *  oss/internal/themes (a test keeps them equal) and `THEME_TOKENS` in the tile
 *  kit's bootstrap.js. Nothing else is ever set on the page. */
export const THEME_TOKENS: ReadonlySet<string> = new Set([
  "--color-ink",
  "--color-paper",
  "--color-shadow",
  "--brand-forest",
  "--brand-sand-100",
  "--brand-green-600",
  "--color-critical",
  "--color-blue",
  "--series-1",
  "--series-2",
  "--series-3",
  "--series-4",
  "--series-5",
  "--series-6",
  "--series-7",
  "--series-8",
  "--series-caramel",
  "--series-sage",
  "--series-espresso",
  "--surface-page",
  "--surface-card",
  "--surface-canvas",
  "--surface-popup",
  "--text-primary",
  "--text-secondary",
  "--text-inverse",
  "--border-default",
  "--border-focus",
  "--action-primary",
  "--action-primary-hover",
  "--nav-hover",
  "--nav-selected",
  "--status-positive",
  "--status-caution",
  "--status-critical",
]);

export type ThemeBase = "light" | "dark";
export const DEFAULT_LIGHT = "default-light";
export const DEFAULT_DARK = "default-dark";

/** A theme: the built-in palette it changes (`base`) and its overrides. */
export type Theme = {
  id: string;
  name: string;
  base: ThemeBase;
  tokens: Record<string, string>;
  builtin?: boolean;
};

/** What `activeTheme` reads of the preferences. */
export type ThemePrefs = {
  theme: "system" | "light" | "dark";
  light_theme: string;
  dark_theme: string;
  themes: Theme[];
};

/** The two built-ins every installation has, with no tokens: what the app
 *  shows before the daemon answers and when it answers without them. The other
 *  built-ins and every built-in's tokens come from the daemon
 *  (`themes.Builtins` in oss/internal/themes), so they are defined once. */
export const builtinThemes: Theme[] = [
  { id: DEFAULT_LIGHT, name: "Default light", base: "light", tokens: {}, builtin: true },
  { id: DEFAULT_DARK, name: "Default dark", base: "dark", tokens: {}, builtin: true },
];

/** The slot the `theme` setting picks: system follows the operating system. */
export function activeSlot(prefs: Pick<ThemePrefs, "theme">, systemDark: boolean): ThemeBase {
  return prefs.theme === "system" ? (systemDark ? "dark" : "light") : prefs.theme;
}

/** What the agent's theme choice (`propose_theme_choice`) holds: an existing
 *  theme, the slot it is for (`in_use` when the agent named none: the daemon
 *  cannot see the operating system's appearance, so the app resolves it) and
 *  the theme mode. */
export type ThemeChoice = {
  theme_id?: string;
  slot?: ThemeBase | "in_use";
  theme?: ThemePrefs["theme"];
};

/** The slot a theme choice is for: the one it names, else the slot in use,
 *  which is the active slot once the choice's mode (when it has one) is
 *  applied. Null when it names no theme. */
export function choiceSlot(
  prefs: Pick<ThemePrefs, "theme">,
  choice: ThemeChoice,
  systemDark: boolean,
): ThemeBase | null {
  if (!choice.theme_id) return null;
  if (choice.slot === "light" || choice.slot === "dark") return choice.slot;
  return activeSlot({ theme: choice.theme ?? prefs.theme }, systemDark);
}

/** The theme to show while a choice is previewed: the chosen theme when its
 *  slot is the one in use, else (when the choice changes the mode) the saved
 *  theme of the slot the new mode shows. Null when the choice changes nothing
 *  on screen: its slot is the other one and the mode is the same. */
export function choicePreview(
  prefs: ThemePrefs,
  choice: ThemeChoice,
  systemDark: boolean,
): Theme | null {
  const mode = choice.theme ?? prefs.theme;
  const active = activeSlot({ theme: mode }, systemDark);
  const chosen = choice.theme_id
    ? prefs.themes.find((theme) => theme.id === choice.theme_id)
    : undefined;
  if (chosen && choiceSlot(prefs, choice, systemDark) === active) return chosen;
  if (mode !== prefs.theme) return slotTheme(prefs, active);
  return null;
}

/** The theme to apply: the preview when there is one, else the theme in the
 *  active slot. A slot naming a theme that is gone gets its built-in. */
export function activeTheme(prefs: ThemePrefs, systemDark: boolean, preview?: Theme | null): Theme {
  if (preview) return preview;
  return slotTheme(prefs, activeSlot(prefs, systemDark));
}

/** The theme a slot names; a slot naming a theme that is gone gets its built-in. */
export function slotTheme(prefs: ThemePrefs, slot: ThemeBase): Theme {
  const id = (slot === "dark" ? prefs.dark_theme : prefs.light_theme) || `default-${slot}`;
  return (
    prefs.themes.find((theme) => theme.id === id) ??
    builtinThemes.find((theme) => theme.id === `default-${slot}`)!
  );
}

/** Puts a theme on the root: `data-theme` and `color-scheme` follow its base,
 *  and its allowlisted tokens are set inline, which beats both palette layers
 *  and style.css. Allowlisted tokens it does not set are removed, so the
 *  previous theme's colours do not linger. */
export function applyTheme(root: HTMLElement, theme: Theme) {
  root.dataset.theme = theme.base;
  root.style.colorScheme = theme.base;
  for (const token of THEME_TOKENS) {
    const value = theme.tokens[token];
    if (typeof value === "string" && Object.hasOwn(theme.tokens, token))
      root.style.setProperty(token, value);
    else root.style.removeProperty(token);
  }
}

/** The attributes that show a theme on one element, not the page, for a
 *  preview: `data-theme-scope` makes the palette's recipes apply to the
 *  element (oss/viewer/src/palette.css), `data-theme` picks the base whatever
 *  the page's mode is, and the theme's allowlisted tokens are set inline, as
 *  `applyTheme` sets them on the root. */
export function themeScope(theme: Theme): {
  "data-theme-scope": "";
  "data-theme": ThemeBase;
  style: CSSProperties;
} {
  return {
    "data-theme-scope": "",
    "data-theme": theme.base,
    style: { colorScheme: theme.base, ...allowedTokens(theme) } as CSSProperties,
  };
}

/** The tile protocol's `theme` message (baum.tile/1) for a theme: its base and
 *  its allowlisted tokens. A tile frame replaces its tokens with these. */
export function tileThemeMessage(theme: Theme) {
  return {
    protocol: "baum.tile/1",
    kind: "theme",
    theme: theme.base,
    tokens: allowedTokens(theme),
  };
}

/** The theme's allowlisted tokens that hold a string. */
export function allowedTokens(theme: Theme): Record<string, string> {
  const tokens: Record<string, string> = {};
  for (const token of THEME_TOKENS) {
    const value = Object.hasOwn(theme.tokens, token) ? theme.tokens[token] : undefined;
    if (typeof value === "string") tokens[token] = value;
  }
  return tokens;
}
