// The window's zoom: the web contents' own page zoom, so it scales every page
// the window shows, bundled or served by the daemon, the same way on X11,
// Wayland, Windows and macOS. Ctrl (Cmd on macOS) with `+`, `-` and `0` changes
// it on any page (`zoomShortcut`, applied by zoom-electron.ts); the desktop
// Settings page offers the levels too. The level is saved in the `window`
// section of `desktop.json`.
import { registerCommand } from "./commands.ts";
import { loadSection, updateSection } from "./settings.ts";

const SECTION = "window";

/** The levels the shortcuts step through and Settings offers. */
export const LEVELS: readonly number[] = [0.67, 0.75, 0.8, 0.9, 1.0, 1.1, 1.25, 1.5, 1.75, 2.0];
export const DEFAULT = 1.0;

/** The level one step from `current` in `direction` (1 in, -1 out, 0 back to
 *  100%), staying within `LEVELS`. */
export function step(current: number, direction: number): number {
  let nearest = 0;
  for (const [i, level] of LEVELS.entries()) {
    if (Math.abs(level - current) < Math.abs((LEVELS[nearest] as number) - current)) nearest = i;
  }
  const at = (i: number) => LEVELS[Math.min(Math.max(i, 0), LEVELS.length - 1)] as number;
  if (direction === 0) return DEFAULT;
  return at(direction > 0 ? nearest + 1 : nearest - 1);
}

/** The saved level; a missing, damaged or unknown one is 100%, so a bad file
 *  never leaves the window unreadable. */
function load(path: string): number {
  const zoom = loadSection(path, SECTION).zoom;
  return typeof zoom === "number" && LEVELS.includes(zoom) ? zoom : DEFAULT;
}

/** The current level and where it is saved; without a path it stays in memory. */
export class ZoomStore {
  private current: number;

  constructor(private readonly path?: string) {
    this.current = path === undefined ? DEFAULT : load(path);
  }

  level(): number {
    return this.current;
  }

  /** Sets and saves a level from `LEVELS`; any other value is refused. */
  set(level: unknown): number {
    return this.update(() => level);
  }

  /** Replaces the level with `next(current)`. */
  update(next: (current: number) => unknown): number {
    const level = next(this.current);
    if (typeof level !== "number" || !LEVELS.includes(level)) {
      throw new Error(`${String(level)} is not a zoom level`);
    }
    this.current = level;
    if (this.path !== undefined) {
      try {
        updateSection(this.path, SECTION, (section) => {
          section.zoom = level;
        });
      } catch (e) {
        console.error(`baum desktop: cannot save the zoom level: ${String(e)}`);
      }
    }
    return level;
  }
}

export type ZoomAction = "in" | "out" | "reset";

export function directionOf(action: ZoomAction): number {
  return action === "in" ? 1 : action === "out" ? -1 : 0;
}

/** The part of an Electron `before-input-event` `Input` the shortcuts read. */
export interface ZoomInput {
  type: string;
  key: string;
  code: string;
  control: boolean;
  meta: boolean;
  alt?: boolean;
}

const KEYS = new Map<string, ZoomAction>([
  ["=", "in"],
  ["+", "in"],
  ["-", "out"],
  ["_", "out"],
  ["0", "reset"],
]);
// Physical keys too, for layouts whose unshifted key gives another character
// (AZERTY's 0 key is "à").
const CODES = new Map<string, ZoomAction>([
  ["Equal", "in"],
  ["NumpadAdd", "in"],
  ["Minus", "out"],
  ["NumpadSubtract", "out"],
  ["Digit0", "reset"],
  ["Numpad0", "reset"],
]);

/** The zoom a key press asks for: Ctrl (Cmd on macOS) with the typed `+`, `=`,
 *  `-` or `0`, or the physical key when the typed character differs. */
export function zoomShortcut(
  input: ZoomInput,
  platform: NodeJS.Platform = process.platform,
): ZoomAction | null {
  if (input.type !== "keyDown" || input.alt) return null;
  const modifier =
    platform === "darwin" ? input.meta && !input.control : input.control && !input.meta;
  if (!modifier) return null;
  return KEYS.get(input.key) ?? CODES.get(input.code) ?? null;
}

export interface ZoomDeps {
  store: ZoomStore;
  /** Applies a level to the web contents of the window a command came from
   *  (`CommandContext.window`). */
  apply: (window: unknown, level: number) => void;
}

/** `zoom_state`, `set_zoom` (Settings' choice of level) and `step_zoom` (one
 *  level in (1), out (-1) or back to 100% (0)). */
export function registerZoomCommands({ store, apply }: ZoomDeps): void {
  registerCommand("zoom_state", () => ({ level: store.level(), levels: LEVELS }));
  registerCommand("set_zoom", (args, context) => {
    const level = store.set(args.level);
    apply(context.window, level);
    return level;
  });
  registerCommand("step_zoom", (args, context) => {
    const { direction } = args;
    if (typeof direction !== "number" || !Number.isInteger(direction)) {
      throw new Error("a zoom step is a whole number");
    }
    const level = store.update((current) => step(current, direction));
    apply(context.window, level);
    return level;
  });
}
