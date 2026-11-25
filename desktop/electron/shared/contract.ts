// What the main process and the preload agree on: the one IPC channel every
// command goes through, the shape of its answer, and how the main process
// hands the preload the shell's declaration. The preload runs sandboxed, so
// this module may import nothing.

/** Every command a page calls travels over this one channel; the guard in
 *  bridge.ts decides whether it runs. */
export const INVOKE_CHANNEL = "baum:invoke";

/** The answer to an invoke. A refused or failed command carries only its
 *  message, which the preload rejects with as a string, as Tauri's bridge
 *  did, so the page's error handling sees the same value in both shells. */
export type InvokeResult = { ok: true; value: unknown } | { ok: false; error: string };

/** What the shell tells the page in its main window, before the page's own
 *  scripts run: `window.__BAUM_DESKTOP__` (see main/declaration.ts). */
export interface Declaration {
  /** The page may embed sandboxed frames of untrusted content served from
   *  the daemon's origin. */
  tileFrames: boolean;
  /** The page can open the tile check window. */
  checkWindow: boolean;
  /** The page opens links to other sites with the `open_link` command. */
  openLinks: boolean;
}

/** The command line switch that carries the declaration into the renderer,
 *  through `webPreferences.additionalArguments`. */
export const DECLARATION_SWITCH = "--baum-desktop-declaration=";

export function declarationArgument(declaration: Declaration): string {
  const { tileFrames, checkWindow, openLinks } = declaration;
  return `${DECLARATION_SWITCH}${JSON.stringify({ tileFrames, checkWindow, openLinks })}`;
}

/** The declaration on a renderer's command line, or null when there is none
 *  or it is not exactly three booleans (a window without one, such as the
 *  tray panel, declares nothing). */
export function parseDeclaration(argv: readonly string[]): Declaration | null {
  const found = argv.filter((a) => a.startsWith(DECLARATION_SWITCH));
  if (found.length !== 1) return null;
  let value: unknown;
  try {
    value = JSON.parse((found[0] as string).slice(DECLARATION_SWITCH.length));
  } catch {
    return null;
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const keys = ["tileFrames", "checkWindow", "openLinks"] as const;
  if (Object.keys(record).length !== keys.length) return null;
  if (!keys.every((k) => typeof record[k] === "boolean")) return null;
  return {
    tileFrames: record.tileFrames as boolean,
    checkWindow: record.checkWindow as boolean,
    openLinks: record.openLinks as boolean,
  };
}

/** The channel the preload asks, synchronously and before the page's first
 *  paint, for the theme the native screens apply (main/theme.ts). */
export const THEME_CHANNEL = "baum:theme";

/** What a page applies before its first paint, as `applyTheme` in the app
 *  does: the `data-theme` and `color-scheme` base and the inline tokens. */
export interface ThemeBootstrap {
  base: "light" | "dark";
  tokens: Record<string, string>;
}

/** The main process's answer on `THEME_CHANNEL`, or null when it is not a
 *  theme (a page the app does not own is answered null). */
export function parseThemeBootstrap(value: unknown): ThemeBootstrap | null {
  if (typeof value !== "object" || value === null) return null;
  const { base, tokens } = value as Record<string, unknown>;
  if (base !== "light" && base !== "dark") return null;
  if (typeof tokens !== "object" || tokens === null || Array.isArray(tokens)) return null;
  const out: Record<string, string> = {};
  for (const [name, token] of Object.entries(tokens)) {
    if (typeof token === "string") out[name] = token;
  }
  return { base, tokens: out };
}

/** The parts of the document element a bootstrap sets. */
export interface ThemeRoot {
  dataset: Record<string, string | undefined>;
  style: { colorScheme: string; setProperty(name: string, value: string): void };
}

/** Applies a bootstrap to the document element. The values are set as data
 *  through the DOM, never parsed as script or markup. */
export function applyThemeBootstrap(root: ThemeRoot, bootstrap: ThemeBootstrap): void {
  root.dataset.theme = bootstrap.base;
  root.style.colorScheme = bootstrap.base;
  for (const [name, value] of Object.entries(bootstrap.tokens)) root.style.setProperty(name, value);
}
