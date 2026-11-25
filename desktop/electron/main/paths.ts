// Where the app keeps its files. They stay where the Tauri shell kept them,
// under its identifier, so the Electron shell finds the same desktop.json and
// the same stable copy of baum: the configuration directory (Electron's
// userData) and the local data directory.
import { isAbsolute, join } from "node:path";

export const IDENTIFIER = "com.usebaum.desktop";

export interface Platform {
  platform: NodeJS.Platform;
  env: Record<string, string | undefined>;
  home: string;
}

/** An XDG variable counts only when it names an absolute path, as the dirs
 *  crate Tauri uses reads them. */
function xdg(env: Platform["env"], name: string, fallback: string): string {
  const value = env[name];
  return value && isAbsolute(value) ? value : fallback;
}

function windowsDir(env: Platform["env"], name: string, home: string, fallback: string): string {
  return env[name] || join(home, "AppData", fallback);
}

/** Tauri's `app_config_dir`: desktop.json lives here. */
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

/** Tauri's `app_local_data_dir`: the stable copy of baum lives under it. */
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
