// The notification settings the app keeps in its config directory: which
// kinds are on (all, by default) and what the operating system answered when
// the app first asked to notify. Nothing here is secret.
import { join } from "node:path";
import { FILE, loadSection, updateSection } from "../settings.ts";
import { KINDS, type Kind } from "./toast.ts";

const SECTION = "notifications";

/** What the operating system lets the app do. */
export type Permission =
  // Not asked yet: the app asks when it first has something to show.
  "unknown" | "granted" | "denied";

export type Kinds = Record<Kind, boolean>;

export interface Settings {
  kinds: Kinds;
  permission: Permission;
}

export function defaultSettings(): Settings {
  const kinds = Object.fromEntries(KINDS.map((kind) => [kind, true])) as Kinds;
  return { kinds, permission: "unknown" };
}

/** A kind the file does not name, or names with a non-boolean, is on; kinds
 *  this version does not know are ignored. */
function parse(section: Record<string, unknown>): Settings {
  const settings = defaultSettings();
  const kinds = section.kinds;
  if (typeof kinds === "object" && kinds !== null && !Array.isArray(kinds)) {
    for (const kind of KINDS) {
      const value = (kinds as Record<string, unknown>)[kind];
      if (typeof value === "boolean") settings.kinds[kind] = value;
    }
  }
  const permission = section.permission;
  if (permission === "unknown" || permission === "granted" || permission === "denied") {
    settings.permission = permission;
  }
  return settings;
}

/** Where the settings live; no path keeps them in memory only. */
export class Store {
  private constructor(private readonly path: string | undefined) {}

  static inDir(dir: string): Store {
    return new Store(join(dir, FILE));
  }

  static inMemory(): Store {
    return new Store(undefined);
  }

  /** The saved settings; a missing or unreadable file is the defaults, so a
   *  damaged file never keeps the app from notifying. */
  load(): Settings {
    if (this.path === undefined) return defaultSettings();
    return parse(loadSection(this.path, SECTION));
  }

  /** Throws when the file is damaged, leaving it as it was. */
  save(settings: Settings): void {
    if (this.path === undefined) return;
    updateSection(this.path, SECTION, (section) => {
      section.kinds = { ...settings.kinds };
      section.permission = settings.permission;
    });
  }
}
