// The tray panel: a small borderless window under the tray icon, drawn like
// a menu but with meters, which a left click on the icon opens on macOS and
// Windows. It shows what the tray menu does: what the daemon is doing,
// anything waiting on the user, what is left of the agents' plans, and
// pause and Quit and stop. It has no Open item: the Inbox's count opens the
// app, and what the daemon is doing opens the jobs list while reviews run or
// wait. Linux tray hosts report no clicks, so Linux has the menu only.
//
// The page is `tray.html` on its own origin. Its role allows exactly the three
// commands here, and only in this window: it reads the tray's last view, sets
// its own height, and runs one of the panel's actions. When it loses focus, it
// hides. This file holds the view model, the commands' logic and the
// placement maths; panel-electron.ts holds the window.
import { registerCommand } from "./commands.ts";
import {
  isAlert,
  type Alert,
  type NeedsYou,
  type Plan,
  pauseLabel,
  type View,
  waitingLabel,
} from "./status.ts";
import { SESSION_PREFIX } from "./tray.ts";

export const WIDTH = 340;
export const DEFAULT_HEIGHT = 360;
export const MIN_HEIGHT = 80;
export const MAX_HEIGHT = 720;
/** Space between the icon and the panel, and the panel and a screen edge. */
export const GAP = 6;
/** A click on the icon while the panel shows first takes its focus, which
 *  hides it; the click that follows must not show it again. */
export const REOPEN_GRACE = 300;

/** What the panel page draws. The keys are what the page reads. */
export interface PanelModel {
  summary: string;
  /** Reviews run or wait, so the summary opens the jobs list (`summary`). */
  jobs: boolean;
  /** The Inbox's row, which opens the app: its count, or that it is empty
   *  once the daemon counted none. `null` while the count is unknown. */
  waiting: string | null;
  /** What the icon is red for, each an action (`alert:…`). */
  attention: Alert[];
  needs: NeedsYou[];
  /** The global automatic switch; `null` while unknown. */
  automatic: boolean | null;
  pause_label: string;
  /** Only plans the daemon could read; empty when there are none. */
  plans: Plan[];
}

/** The Inbox's row. The panel has no Open item, so it shows even when the
 *  Inbox is empty, as mail apps say so; an unknown count shows nothing. */
function inboxLine(view: View): string | null {
  if (view.waiting === 0) return "Your Inbox is empty";
  return waitingLabel(view) ?? null;
}

export function panelModel(view: View, plans: Plan[]): PanelModel {
  return {
    summary: view.summary,
    jobs: view.running + view.queued > 0,
    waiting: inboxLine(view),
    attention: view.attention,
    needs: view.needs,
    automatic: view.automatic ?? null,
    pause_label: pauseLabel(view.automatic),
    plans,
  };
}

/** The panel's height in CSS pixels, fitted to what the page asked for. */
export function fitHeight(height: number): number {
  const h = Number.isFinite(height) ? height : DEFAULT_HEIGHT;
  return Math.min(MAX_HEIGHT, Math.max(MIN_HEIGHT, h));
}

/** Whether the page may run this action: the tray menu's, of which it offers
 *  `open` (the Inbox's count), `summary` (the busy status), pause or resume,
 *  Quit and stop, the alerts and the waiting sessions, and `close` (Escape),
 *  which only hides the panel. Anything else is refused. */
export function allowed(id: string): boolean {
  switch (id) {
    case "open":
    case "summary":
    case "pause":
    case "quit-stop":
    case "close":
      return true;
  }
  return isAlert(id) || (id.startsWith(SESSION_PREFIX) && id.length > SESSION_PREFIX.length);
}

/** What the commands need of the panel's window and the tray. */
export interface PanelHost {
  hide(): void;
  setHeight(height: number): void;
  snapshot(): { view: View; plans: Plan[] } | undefined;
  /** Runs a tray menu id. */
  onMenu(id: string): void;
}

export function trayPanel(host: PanelHost): PanelModel | null {
  const snapshot = host.snapshot();
  return snapshot === undefined ? null : panelModel(snapshot.view, snapshot.plans);
}

export function trayPanelHeight(host: PanelHost, height: unknown): void {
  host.setHeight(fitHeight(typeof height === "number" ? height : Number.NaN));
}

export function trayAction(host: PanelHost, id: unknown): void {
  if (typeof id !== "string" || !allowed(id)) {
    throw new Error(`the panel has no action ${JSON.stringify(id)}`);
  }
  host.hide();
  if (id !== "close") host.onMenu(id);
}

/** Registers the panel's three commands. */
export function registerPanelCommands(host: PanelHost): void {
  registerCommand("tray_panel", () => trayPanel(host));
  registerCommand("tray_panel_height", (args) => trayPanelHeight(host, args.height));
  registerCommand("tray_action", (args) => trayAction(host, args.id));
}

/** Remembers when the panel last hid, so the click that hid it by taking its
 *  focus does not show it again. */
export class ReopenGuard {
  private hiddenAt: number | undefined;

  hidden(at: number): void {
    this.hiddenAt = at;
  }

  blocks(at: number): boolean {
    return this.hiddenAt !== undefined && at - this.hiddenAt < REOPEN_GRACE;
  }
}

/** A rectangle, in pixels or points. */
export interface Area {
  x: number;
  y: number;
  width: number;
  height: number;
}

export function scaled(area: Area, by: number): Area {
  return {
    x: area.x * by,
    y: area.y * by,
    width: area.width * by,
    height: area.height * by,
  };
}

/** A monitor: its whole rectangle, the part windows may use, and its scale. */
export interface Screen {
  bounds: Area;
  work: Area;
  scale: number;
}

/** The screen a point is on: the one containing it, else the nearest, so the
 *  panel never stays where it last was. `undefined` only without screens. */
export function screenOf(
  [cx, cy]: [number, number],
  monitors: readonly Area[],
): number | undefined {
  const distance = (m: Area) => {
    const dx = Math.max(m.x - cx, cx - (m.x + m.width), 0);
    const dy = Math.max(m.y - cy, cy - (m.y + m.height), 0);
    return dx * dx + dy * dy;
  };
  let best: number | undefined;
  let bestDistance = Number.POSITIVE_INFINITY;
  monitors.forEach((m, i) => {
    const d = distance(m);
    if (d < bestDistance) {
      best = i;
      bestDistance = d;
    }
  });
  return best;
}

/** Where the panel goes: centred on the icon and kept on its screen, below
 *  an icon in the top half (the macOS menu bar, a top panel) and above one in
 *  the bottom half (the Windows taskbar). */
export function place(
  icon: Area,
  panel: [number, number],
  screen: Area,
  gap: number,
): [number, number] {
  const [width, height] = panel;
  const left = screen.x + gap;
  const right = Math.max(screen.x + screen.width - width - gap, left);
  const x = clamp(icon.x + icon.width / 2 - width / 2, left, right);
  const top = screen.y + gap;
  const bottom = Math.max(screen.y + screen.height - height - gap, top);
  const below = icon.y + icon.height / 2 < screen.y + screen.height / 2;
  const y = below ? icon.y + icon.height + gap : icon.y - height - gap;
  return [x, clamp(y, top, bottom)];
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

export interface PlacementInput {
  /** The tray icon's rectangle. */
  icon: Area;
  /** The cursor, which is on the icon at the click. */
  cursor?: { x: number; y: number } | undefined;
  screens: readonly Screen[];
  /** The panel's size. */
  panel: [number, number];
  platform: string;
}

/** The panel's position, in the one coordinate space the icon, the cursor and
 *  the screens share (Electron reports all of them in points, so no scale
 *  conversion is needed). On macOS the screen is the one under the cursor,
 *  elsewhere the one under the icon's centre. `undefined` without screens. */
export function panelPosition(input: PlacementInput): [number, number] | undefined {
  const { icon, cursor, screens, panel } = input;
  const centre: [number, number] =
    input.platform === "darwin" && cursor !== undefined
      ? [cursor.x, cursor.y]
      : [icon.x + icon.width / 2, icon.y + icon.height / 2];
  const index = screenOf(
    centre,
    screens.map((s) => s.bounds),
  );
  const screen = index === undefined ? undefined : screens[index];
  if (screen === undefined) return undefined;
  return place(icon, panel, screen.work, GAP);
}
