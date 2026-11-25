// Where the app keeps its files. They are the directories the Tauri app used,
// under its identifier, so a copy that updated from it keeps its desktop.json
// and stable baum: the configuration directory (Electron's userData) and the
// local data directory.
import { isAbsolute, join } from "node:path";

export const IDENTIFIER = "com.usebaum.desktop";

export interface Platform {
  platform: NodeJS.Platform;
  env: Record<string, string | undefined>;
  home: string;
}

/** An XDG variable counts only when it names an absolute path, as the dirs
 *  crate the Tauri app used read them. */
function xdg(env: Platform["env"], name: string, fallback: string): string {
  const value = env[name];
  return value && isAbsolute(value) ? value : fallback;
}

function windowsDir(env: Platform["env"], name: string, home: string, fallback: string): string {
  return env[name] || join(home, "AppData", fallback);
}

/** The configuration directory the Tauri app used: desktop.json lives here. */
export function configDir({ platform, env, home }: Platform): string {
  switch (platform) {
    case "darwin":
      return join(home, "Library", "Application Support", IDENTIFIER);
    case "win32":
      return join(windowsDir(env, "APPDATA", home, "Roaming"), IDENTIFIER);
    default:
      return join(xdg(env, "XDG_CONFIG_HOME", join(home, ".config")), IDENTIFIER);
  }
}

/** The local data directory the Tauri app used: the stable copy of baum lives
 *  under it. */
export function localDataDir({ platform, env, home }: Platform): string {
  switch (platform) {
    case "darwin":
      return join(home, "Library", "Application Support", IDENTIFIER);
    case "win32":
      return join(windowsDir(env, "LOCALAPPDATA", home, "Local"), IDENTIFIER);
    default:
      return join(xdg(env, "XDG_DATA_HOME", join(home, ".local", "share")), IDENTIFIER);
  }
}

/** Chromium's own files (cookies, caches, local storage) go in a folder of
 *  their own, so they never mix with desktop.json. */
export function sessionDataDir(config: string): string {
  return join(config, "chromium");
}
