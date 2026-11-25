import { afterEach, describe, expect, it } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import {
  type Area,
  allowed,
  fitHeight,
  GAP,
  type PanelHost,
  panelModel,
  panelPosition,
  place,
  ReopenGuard,
  registerPanelCommands,
  scaled,
  screenOf,
  trayAction,
} from "./panel.ts";
import { defaultView } from "./status.ts";

const SCREEN: Area = { x: 0, y: 0, width: 1440, height: 900 };

describe("panel placement", () => {
  it("hangs the panel below a menu bar icon, centred", () => {
    const icon = { x: 1000, y: 0, width: 24, height: 24 };
    expect(place(icon, [340, 400], SCREEN, 6)).toEqual([842, 30]);
  });

  it("stands the panel on a taskbar icon", () => {
    const icon = { x: 1200, y: 860, width: 32, height: 40 };
    expect(place(icon, [340, 400], SCREEN, 6)).toEqual([1046, 454]);
  });

  it("keeps the panel on the icon's screen", () => {
    const corner = { x: 1430, y: 0, width: 10, height: 24 };
    expect(place(corner, [340, 400], SCREEN, 6)).toEqual([1094, 30]);
    const second = { x: 1440, y: 0, width: 1920, height: 1080 };
    const icon = { x: 1450, y: 0, width: 24, height: 24 };
    expect(place(icon, [340, 400], second, 6)).toEqual([1446, 30]);
  });

  it("finds the point's screen in points", () => {
    // A 2x built-in screen of 1512x982 points and an external 1x screen
    // to its right, as macOS reports them: points times each one's scale.
    const builtIn = { x: 0, y: 0, width: 3024, height: 1964 };
    const external = { x: 1512, y: 0, width: 2560, height: 1440 };
    // In those pixels the screens overlap; in points they do not.
    const points = [scaled(builtIn, 0.5), external];
    expect(screenOf([1180, 12], points)).toBe(0);
    expect(screenOf([2000, 12], points)).toBe(1);
  });

  it("sends a point on no screen to the nearest, and none without screens", () => {
    expect(screenOf([1500, -30], [SCREEN])).toBe(0);
    const right = { ...SCREEN, x: 1440 };
    expect(screenOf([3000, 10], [SCREEN, right])).toBe(1);
    expect(screenOf([10, 10], [])).toBeUndefined();
  });

  it("places a panel in points under a retina menu bar icon", () => {
    // The icon at 1180 points on a 2x screen, reported in pixels.
    const icon = { x: 2360, y: 0, width: 48, height: 48 };
    const work = { x: 0, y: 74, width: 3024, height: 1890 };
    expect(place(scaled(icon, 0.5), [340, 400], scaled(work, 0.5), GAP)).toEqual([1022, 43]);
  });

  it("picks the screen under the cursor on macOS and under the icon elsewhere", () => {
    const screens = [
      { bounds: SCREEN, work: { ...SCREEN, y: 24, height: 876 }, scale: 2 },
      {
        bounds: { ...SCREEN, x: 1440 },
        work: { ...SCREEN, x: 1440, y: 24, height: 876 },
        scale: 1,
      },
    ];
    const icon = { x: 1430, y: 0, width: 24, height: 24 };
    const input = { icon, screens, panel: [340, 400] as [number, number] };
    // The icon's centre is on the second screen, the cursor on the first.
    const cursor = { x: 1420, y: 10 };
    expect(panelPosition({ ...input, cursor, platform: "darwin" })?.[0]).toBe(1094);
    expect(panelPosition({ ...input, cursor, platform: "win32" })?.[0]).toBe(1446);
    expect(panelPosition({ ...input, screens: [], platform: "win32" })).toBeUndefined();
  });

  it("does not show the panel again for the click that hid it", () => {
    const guard = new ReopenGuard();
    expect(guard.blocks(1_000)).toBe(false);
    guard.hidden(1_000);
    expect(guard.blocks(1_200)).toBe(true);
    expect(guard.blocks(1_300)).toBe(false);
  });
});

describe("panel commands", () => {
  it("reaches only the panel's actions", () => {
    for (const id of [
      "open",
      "summary",
      "pause",
      "quit-stop",
      "close",
      "session:claude-code:a",
      "alert:job:6e280a68",
      "alert:jobs",
      "alert:diagnostics",
      "alert:daemon",
    ]) {
      expect(allowed(id), id).toBe(true);
    }
    for (const id of [
      "quit",
      "login",
      "setup",
      "session:",
      "alert:job:",
      "alert:other",
      "notifications:review_finished",
      "",
    ]) {
      expect(allowed(id), id).toBe(false);
    }
  });

  it("shows the view's lines and only readable plans", () => {
    const view = defaultView();
    const shown = panelModel(view, []);
    expect(shown.summary).toBe(view.summary);
    expect(shown.pause_label).toBe("Pause automatic reviews");
    expect(shown.jobs).toBe(false);
    // An unknown count shows no Inbox row; zero says the Inbox is empty.
    expect(shown.waiting).toBeNull();
    const inbox = (waiting: number | undefined) => panelModel({ ...view, waiting }, []).waiting;
    expect(inbox(0)).toBe("Your Inbox is empty");
    expect(inbox(12)).toBe("12 pull requests in your Inbox");
    expect(panelModel({ ...view, running: 1 }, []).jobs).toBe(true);
    expect(shown.plans).toEqual([]);
    const json = JSON.parse(JSON.stringify(shown));
    expect(json.plans).toEqual([]);
    expect(json.automatic).toBeNull();
  });

  it("fits the window to the page's height", () => {
    expect(fitHeight(10)).toBe(80);
    expect(fitHeight(400)).toBe(400);
    expect(fitHeight(5000)).toBe(720);
    expect(fitHeight(Number.NaN)).toBe(360);
    expect(fitHeight(Number.POSITIVE_INFINITY)).toBe(360);
  });
});

describe("registered commands", () => {
  afterEach(clearCommands);

  function host() {
    const calls: string[] = [];
    const panel: PanelHost = {
      hide: () => calls.push("hide"),
      setHeight: (h) => calls.push(`height ${h}`),
      snapshot: () => ({ view: defaultView(), plans: [] }),
      onMenu: (id) => calls.push(`menu ${id}`),
    };
    return { panel, calls };
  }

  const context = { window: undefined, role: "tray-panel" as const, origin: "app://bundle" };

  it("hides the panel and runs the action, refusing any other id", async () => {
    const { panel, calls } = host();
    registerPanelCommands(panel);
    await dispatch("tray_action", { id: "pause" }, context);
    await dispatch("tray_action", { id: "close" }, context);
    expect(calls).toEqual(["hide", "menu pause", "hide"]);
    await expect(dispatch("tray_action", { id: "quit" }, context)).rejects.toThrow(
      'the panel has no action "quit"',
    );
    expect(() => trayAction(panel, 5)).toThrow();
    expect(calls).toHaveLength(3);
  });

  it("answers the view and fits the height", async () => {
    const { panel, calls } = host();
    registerPanelCommands(panel);
    const model = (await dispatch("tray_panel", {}, context)) as { summary: string };
    expect(model.summary).toBe("Connecting to baum…");
    await dispatch("tray_panel_height", { height: 900 }, context);
    await dispatch("tray_panel_height", { height: "tall" }, context);
    expect(calls).toEqual(["height 720", "height 360"]);
  });
});
