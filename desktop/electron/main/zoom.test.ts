import { EventEmitter } from "node:events";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { WebContents } from "electron";
import { beforeEach, describe, expect, it } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import { FILE } from "./settings.ts";
import {
  DEFAULT,
  LEVELS,
  registerZoomCommands,
  step,
  ZoomStore,
  type ZoomInput,
  zoomShortcut,
} from "./zoom.ts";
import { announceZoom, installZoom } from "./zoom-electron.ts";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "baum-desktop-zoom-"));
}

describe("zoom levels", () => {
  it("steps stay within the levels", () => {
    expect(step(1.0, 1)).toBe(1.1);
    expect(step(1.0, -1)).toBe(0.9);
    expect(step(2.0, 1)).toBe(2.0);
    expect(step(0.67, -1)).toBe(0.67);
    expect(step(1.75, 0)).toBe(1.0);
    // A level between steps moves from the nearest one.
    expect(step(1.3, 1)).toBe(1.5);
  });

  it("only offered levels are accepted", () => {
    const zoom = new ZoomStore();
    expect(() => zoom.set(3.0)).toThrow();
    expect(() => zoom.set(Number.NaN)).toThrow();
    expect(() => zoom.set("1.25")).toThrow();
    expect(zoom.level()).toBe(DEFAULT);
    expect(zoom.set(1.25)).toBe(1.25);
    expect(zoom.level()).toBe(1.25);
  });

  it("the level survives a restart", () => {
    const path = join(scratch(), FILE);
    new ZoomStore(path).set(1.5);
    expect(new ZoomStore(path).level()).toBe(1.5);
  });

  it("a damaged or unknown level is the default", () => {
    for (const content of ["{", '{"window":{"zoom":7}}', '{"window":{"zoom":"1.5"}}']) {
      const dir = scratch();
      mkdirSync(dir, { recursive: true });
      writeFileSync(join(dir, FILE), content);
      expect(new ZoomStore(join(dir, FILE)).level()).toBe(DEFAULT);
    }
  });

  it("an unsavable level still applies and a damaged section is not overwritten", () => {
    const dir = scratch();
    writeFileSync(join(dir, FILE), '{"window":5}');
    const store = new ZoomStore(join(dir, FILE));
    expect(store.set(1.5)).toBe(1.5);
    expect(new ZoomStore(join(dir, FILE)).level()).toBe(DEFAULT);
  });
});

describe("zoom shortcuts", () => {
  const press = (over: Partial<ZoomInput>): ZoomInput => ({
    type: "keyDown",
    key: "",
    code: "",
    control: false,
    meta: false,
    ...over,
  });

  it("maps the typed character with Ctrl, or Cmd on macOS", () => {
    for (const [key, action] of [
      ["+", "in"],
      ["=", "in"],
      ["-", "out"],
      ["_", "out"],
      ["0", "reset"],
    ] as const) {
      expect(zoomShortcut(press({ key, control: true }), "linux")).toBe(action);
      expect(zoomShortcut(press({ key, control: true }), "win32")).toBe(action);
      expect(zoomShortcut(press({ key, meta: true }), "darwin")).toBe(action);
    }
  });

  it("falls back to the physical key for layouts that type something else", () => {
    expect(zoomShortcut(press({ key: "à", code: "Digit0", control: true }), "linux")).toBe("reset");
    expect(zoomShortcut(press({ key: "&", code: "Equal", control: true }), "linux")).toBe("in");
    expect(zoomShortcut(press({ key: "6", code: "Minus", control: true }), "linux")).toBe("out");
    expect(zoomShortcut(press({ key: "+", code: "NumpadAdd", control: true }), "linux")).toBe("in");
    expect(zoomShortcut(press({ key: "0", code: "Numpad0", control: true }), "linux")).toBe(
      "reset",
    );
    // The typed character wins over the physical key.
    expect(zoomShortcut(press({ key: "-", code: "Equal", control: true }), "linux")).toBe("out");
  });

  it("needs the platform's modifier alone", () => {
    expect(zoomShortcut(press({ key: "+" }), "linux")).toBeNull();
    expect(zoomShortcut(press({ key: "+", meta: true }), "linux")).toBeNull();
    expect(zoomShortcut(press({ key: "+", control: true, meta: true }), "linux")).toBeNull();
    expect(zoomShortcut(press({ key: "+", control: true }), "darwin")).toBeNull();
    expect(zoomShortcut(press({ key: "+", control: true, meta: true }), "darwin")).toBeNull();
    expect(zoomShortcut(press({ key: "+", control: true, alt: true }), "linux")).toBeNull();
  });

  it("ignores key releases and other keys", () => {
    expect(zoomShortcut(press({ type: "keyUp", key: "+", control: true }), "linux")).toBeNull();
    expect(zoomShortcut(press({ key: "a", code: "KeyA", control: true }), "linux")).toBeNull();
    expect(zoomShortcut(press({ key: "constructor", control: true }), "linux")).toBeNull();
  });
});

describe("zoom commands", () => {
  beforeEach(clearCommands);
  const settings = {
    window: "main",
    role: "daemon-pages",
    origin: "http://127.0.0.1:4100",
  } as const;

  it("report, set and step the level, applying it to the calling window", async () => {
    const applied: [unknown, number][] = [];
    registerZoomCommands({
      store: new ZoomStore(),
      apply: (window, level) => applied.push([window, level]),
    });
    expect(await dispatch("zoom_state", {}, settings)).toEqual({ level: 1, levels: LEVELS });
    expect(await dispatch("set_zoom", { level: 1.25 }, settings)).toBe(1.25);
    expect(await dispatch("step_zoom", { direction: 1 }, settings)).toBe(1.5);
    expect(await dispatch("step_zoom", { direction: 0 }, settings)).toBe(1);
    expect(applied).toEqual([
      ["main", 1.25],
      ["main", 1.5],
      ["main", 1],
    ]);
    await expect(dispatch("set_zoom", { level: 3 }, settings)).rejects.toThrow(/not a zoom level/);
    await expect(dispatch("step_zoom", { direction: "up" }, settings)).rejects.toThrow();
    await expect(dispatch("step_zoom", { direction: 0.5 }, settings)).rejects.toThrow();
    expect(applied).toHaveLength(3);
  });
});

describe("zoom in Electron", () => {
  function fakeContents() {
    const emitter = new EventEmitter();
    const calls = {
      zoomFactors: [] as number[],
      limits: [] as [number, number][],
      scripts: [] as string[],
    };
    const contents = Object.assign(emitter, {
      isDestroyed: () => false,
      setZoomFactor: (f: number) => calls.zoomFactors.push(f),
      setVisualZoomLevelLimits: (a: number, b: number) => calls.limits.push([a, b]),
      executeJavaScript: async (script: string) => {
        calls.scripts.push(script);
      },
    }) as unknown as WebContents;
    return { contents, emitter, calls };
  }

  it("applies the saved level, turns Chromium's zoom off and handles the shortcuts", () => {
    const { contents, emitter, calls } = fakeContents();
    const store = new ZoomStore();
    store.set(1.25);
    installZoom(contents, store, "linux");
    expect(calls.limits).toEqual([[1, 1]]);
    expect(calls.zoomFactors).toEqual([1.25]);

    // A navigation may reset the zoom; Chromium's own zoom is put back.
    emitter.emit("did-start-loading");
    emitter.emit("zoom-changed", {}, "in");
    expect(calls.zoomFactors).toEqual([1.25, 1.25, 1.25]);

    let prevented = false;
    emitter.emit(
      "before-input-event",
      { preventDefault: () => (prevented = true) },
      { type: "keyDown", key: "+", code: "Equal", control: true, meta: false, alt: false },
    );
    expect(prevented).toBe(true);
    expect(store.level()).toBe(1.5);
    expect(calls.zoomFactors.at(-1)).toBe(1.5);
    expect(calls.scripts).toEqual([
      'window.dispatchEvent(new CustomEvent("baum:zoom", {detail: 1.5}))',
    ]);

    prevented = false;
    emitter.emit(
      "before-input-event",
      { preventDefault: () => (prevented = true) },
      { type: "keyDown", key: "a", code: "KeyA", control: true, meta: false, alt: false },
    );
    expect(prevented).toBe(false);
    expect(store.level()).toBe(1.5);
  });

  it("announces the level as the event detail", () => {
    const { contents, calls } = fakeContents();
    announceZoom(contents, 0.67);
    expect(calls.scripts[0]).toContain("{detail: 0.67}");
  });
});
