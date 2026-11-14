import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewFilesContext } from "@baum/viewer/components/review-contexts";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { golden } from "../goldens-test-helper";
import { TileHostContext, type TileHost } from "../host";
import { tileFace } from "../registry";
import type { Tile, TileDocument } from "../tile-document";
import type { TileContext } from "../types";
import { framesAllowedHere } from "./environment";
import { HELLO_TIMEOUT, PROTOCOL, READY_TIMEOUT } from "./protocol";
import { listenedFrames } from "./router";
import { TileFrame, type Face } from "./tile-frame";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

/** jsdom gives a frame a new window as it navigates, where a browser keeps
 *  its WindowProxy: each frame here has one stand-in window for good. */
type FakeWindow = { postMessage: ReturnType<typeof vi.fn> };
const windows = new WeakMap<HTMLIFrameElement, FakeWindow>();
const windowOf = (frame: HTMLIFrameElement) => {
  let win = windows.get(frame);
  if (!win) {
    win = { postMessage: vi.fn() };
    windows.set(frame, win);
  }
  return win;
};

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", {
    configurable: true,
    get(this: HTMLIFrameElement) {
      return windowOf(this);
    },
  });
});

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  vi.useRealTimers();
});

const doc: TileDocument = golden("custom");
let counter = 0;

/** A fresh copy of the golden's custom tile, so no frame failure from one
 *  test is remembered in the next. */
function freshTile(): Tile {
  const tile = structuredClone(doc.tiles.find((each) => each.id === "route_changes")!);
  tile.id = `route_changes_${++counter}`;
  return tile;
}

function contextFor(overrides: Partial<TileContext> = {}): TileContext {
  return {
    doc,
    review: {} as ReviewDocument,
    open: vi.fn(),
    openChange: vi.fn(),
    openPane: vi.fn(),
    historical: false,
    comparing: false,
    draft: false,
    contextPending: false,
    patches: null,
    activeVersions: () => undefined,
    idPrefix: "",
    labels: new Map(),
    compare: null,
    ...overrides,
  };
}

function mount(face: Face, tile = freshTile()) {
  const ctx = contextFor();
  const host: TileHost = {
    draft: false,
    commentCounts: new Map(),
    ask: vi.fn(),
    reread: vi.fn(async () => undefined),
  };
  const files = { has: () => true, open: vi.fn(() => true) };
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <TileHostContext.Provider value={host}>
        <ReviewFilesContext.Provider value={files}>
          <TileFrame tile={tile} ctx={ctx} face={face} />
        </ReviewFilesContext.Provider>
      </TileHostContext.Provider>,
    ),
  );
  const frame = () => container.querySelector("iframe");
  // The frame's window, kept once the frame is gone, as a browser's is.
  const first = windowOf(frame()!);
  const sent: unknown[] = [];
  const spyOn = () => {
    windowOf(frame()!).postMessage.mockImplementation((message: unknown) => {
      sent.push(message);
    });
  };
  spyOn();
  /** Posts `data` to the page as if from `source` (the frame by default). */
  const post = (
    data: unknown,
    { source, origin = "null" }: { source?: unknown; origin?: string } = {},
  ) =>
    act(() => {
      const event = new MessageEvent("message", { data, origin });
      Object.defineProperty(event, "source", {
        value: source === undefined ? (frame()?.contentWindow ?? first) : source,
      });
      window.dispatchEvent(event);
    });
  const start = () => {
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    post({ protocol: PROTOCOL, kind: "ready", metrics: { height: 420 } });
  };
  return { container, frame, sent, post, start, ctx, host, files, tile, spyOn };
}

/** The reader tabs into a frame: the page's window loses focus to it. */
function giveFocus(frame: HTMLIFrameElement) {
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab" }));
    frame.focus();
    window.dispatchEvent(new Event("blur"));
    vi.advanceTimersByTime(1);
  });
}

const state = (container: HTMLElement) =>
  container.querySelector<HTMLElement>("[data-frame-state]")?.dataset.frameState;

describe("a custom tile's frame", () => {
  it("is sandboxed to scripts alone, from its digest's route", () => {
    const { frame, tile } = mount("glance");
    const iframe = frame()!;
    expect(iframe.getAttribute("sandbox")).toBe("allow-scripts");
    expect(iframe.getAttribute("src")).toBe(
      `/tile-frames/${(tile.definition as { digest: string }).digest.slice(7)}`,
    );
    expect(iframe.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(iframe.getAttribute("allow")).toBe("");
    expect(iframe.title).toBe(tile.title);
    expect(iframe.tabIndex).toBe(-1);
    expect(iframe.hasAttribute("srcdoc")).toBe(false);
  });

  it("answers hello with the tile's data and resolved citations, then shows it", () => {
    const { container, sent, post, tile } = mount("glance");
    expect(state(container)).toBe("loading");
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({
      protocol: PROTOCOL,
      kind: "init",
      face: "glance",
      size: "full",
      theme: "light",
      data: tile.data,
    });
    const refs = (sent[0] as { refs: Record<string, { excerpt?: unknown }> }).refs;
    expect(Object.keys(refs)).toContain('["risk","rsk_retry_storm"]');
    post({ protocol: PROTOCOL, kind: "ready", metrics: { height: 100 } });
    expect(state(container)).toBe("ready");
  });

  it("sends the app's theme as it changes", async () => {
    const { sent, start } = mount("glance");
    start();
    await act(async () => {
      document.documentElement.dataset.theme = "dark";
    });
    expect(sent.at(-1)).toEqual({ protocol: PROTOCOL, kind: "theme", theme: "dark" });
    delete document.documentElement.dataset.theme;
  });

  it("hears no other window and no origin but the opaque one", () => {
    const { container, sent, post } = mount("glance");
    const other = document.createElement("iframe");
    document.body.append(other);
    post(
      { protocol: PROTOCOL, kind: "hello", kit: 1, registered: true },
      { source: windowOf(other) },
    );
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true }, { source: window });
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true }, { source: null });
    post(
      { protocol: PROTOCOL, kind: "hello", kit: 1, registered: true },
      { origin: window.location.origin },
    );
    expect(sent).toHaveLength(0);
    expect(state(container)).toBe("loading");
  });

  it("sizes a detail by its height requests, within bounds", () => {
    const { container, post, start } = mount("detail");
    const box = () => container.querySelector<HTMLElement>("[data-frame-state]")!;
    start();
    expect(box().style.height).toBe("420px");
    post({ protocol: PROTOCOL, kind: "resize", height: 1e9 });
    expect(box().style.height).toBe("20000px");
    post({ protocol: PROTOCOL, kind: "resize", height: 3 });
    expect(box().style.height).toBe("120px");
    post({ protocol: PROTOCOL, kind: "resize", height: "999" });
    expect(box().style.height).toBe("120px");
  });

  it("keeps a glance's box whatever the frame asks", () => {
    const { container, post, start } = mount("glance");
    start();
    post({ protocol: PROTOCOL, kind: "resize", height: 5000 });
    expect(container.querySelector<HTMLElement>("[data-frame-state]")!.style.height).toBe("");
  });

  it("jumps only to a citation it resolved, only while the reader is in the frame", () => {
    const { frame, post, start, files, ctx } = mount("detail");
    start();
    const cited = '["hunk","h_000000000003",[21,25]]';
    post({ protocol: PROTOCOL, kind: "jump", key: cited });
    expect(files.open).not.toHaveBeenCalled();
    giveFocus(frame()!);
    expect(document.activeElement).toBe(frame());
    post({ protocol: PROTOCOL, kind: "jump", key: '["hunk","h_0000000000ff",null]' });
    post({ protocol: PROTOCOL, kind: "jump", key: '["external_url","https://example.com/spec"]' });
    expect(files.open).not.toHaveBeenCalled();
    post({ protocol: PROTOCOL, kind: "jump", key: cited });
    expect(files.open).toHaveBeenCalledWith(expect.any(String), 21);
    post({ protocol: PROTOCOL, kind: "jump", key: '["tile","overview"]' });
    expect(ctx.open).toHaveBeenCalledWith("overview");
  });

  it("takes no jump, ask or key from a glance", () => {
    const { frame, post, start, files, host } = mount("glance");
    start();
    giveFocus(frame()!);
    const keys = vi.fn();
    window.addEventListener("keydown", keys);
    post({ protocol: PROTOCOL, kind: "jump", key: '["hunk","h_000000000003",[21,25]]' });
    post({ protocol: PROTOCOL, kind: "ask" });
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    window.removeEventListener("keydown", keys);
    expect(files.open).not.toHaveBeenCalled();
    expect(host.ask).not.toHaveBeenCalled();
    expect(keys).not.toHaveBeenCalled();
  });

  it("hands the page a whitelisted key from a focused detail", () => {
    const { frame, post, start, host } = mount("detail");
    start();
    const keys: string[] = [];
    const listen = (event: KeyboardEvent) => keys.push(event.key);
    window.addEventListener("keydown", listen);
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    giveFocus(frame()!);
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    post({ protocol: PROTOCOL, kind: "key", key: "Enter", shift: false });
    post({ protocol: PROTOCOL, kind: "ask" });
    window.removeEventListener("keydown", listen);
    expect(keys.filter((key) => key !== "Tab")).toEqual(["Escape"]);
    expect(host.ask).toHaveBeenCalledTimes(1);
  });

  it("takes the keyboard back from a frame the reader did not give it to", () => {
    const { frame, post, start, container } = mount("detail");
    start();
    act(() => frame()!.focus());
    post({ protocol: PROTOCOL, kind: "jump", key: '["tile","overview"]' });
    expect(document.activeElement).not.toBe(frame());
    for (let i = 0; i < 4; i++) {
      act(() => frame()?.focus());
      post({ protocol: PROTOCOL, kind: "ask" });
    }
    expect(container.textContent).toContain("It kept taking the keyboard from the page.");
  });

  it("unmounts a frame that floods the page", () => {
    const { container, post } = mount("glance");
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    for (let i = 0; i < 400; i++) post({ protocol: PROTOCOL, kind: "resize", height: 200 });
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.textContent).toContain("This tile couldn't load.");
  });

  it("unmounts a frame whose document loads again", () => {
    const { container, post, start } = mount("detail");
    start();
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.textContent).toContain("It tried to leave its frame.");
  });

  it("takes an error before the tile drew as its failure, shown as text", () => {
    const { container, post } = mount("detail");
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    post({ protocol: PROTOCOL, kind: "error", code: "script_error", message: "<b>bad</b>" });
    expect(container.textContent).toContain("<b>bad</b>");
    expect(container.querySelector("b")).toBeNull();
  });

  it("gives up on a frame that does not draw in time", () => {
    const { container, post } = mount("glance");
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    act(() => vi.advanceTimersByTime(READY_TIMEOUT + 10));
    expect(container.textContent).toContain("This tile couldn't load.");
  });

  it("reads the tile route again and remounts a silent frame once, then says it couldn't load", async () => {
    const { container, frame, host, spyOn, start } = mount("detail");
    const first = frame();
    await act(async () => {
      vi.advanceTimersByTime(HELLO_TIMEOUT + 10);
    });
    expect(host.reread).toHaveBeenCalledTimes(1);
    expect(frame()).not.toBe(first);
    expect(frame()).not.toBeNull();
    spyOn();
    await act(async () => {
      vi.advanceTimersByTime(HELLO_TIMEOUT + 10);
    });
    expect(host.reread).toHaveBeenCalledTimes(1);
    expect(frame()).toBeNull();
    expect(container.textContent).toContain("This tile couldn't load.");
    expect(container.textContent).toContain("It didn't start.");
    // Retry mounts it afresh.
    act(() => container.querySelector("button")!.click());
    expect(frame()).not.toBeNull();
    spyOn();
    start();
    expect(state(container)).toBe("ready");
  });

  it("fails alone: another tile of the same type still loads", () => {
    const tile = freshTile();
    const broken = mount("glance", tile);
    for (let i = 0; i < 400; i++) broken.post({ protocol: PROTOCOL, kind: "ask" });
    expect(broken.container.textContent).toContain("couldn't load");
    const other = { ...structuredClone(tile), id: `${tile.id}_other` };
    const container = document.createElement("div");
    document.body.append(container);
    const second = createRoot(container);
    act(() => second.render(<TileFrame tile={other} ctx={contextFor()} face="glance" />));
    expect(container.querySelector("iframe")).not.toBeNull();
    act(() => second.unmount());
  });

  it("stops listening when it unmounts", () => {
    const before = listenedFrames();
    mount("glance");
    expect(listenedFrames()).toBe(before + 1);
    act(() => root?.unmount());
    root = null;
    expect(listenedFrames()).toBe(before);
  });
});

describe("custom tiles on the page", () => {
  it("are drawn by their definition, unavailable when made with a newer kit", () => {
    const ready = doc.tiles.find((each) => each.id === "route_changes")!;
    expect(tileFace(ready)).toMatchObject({ kind: "ready", type: { type: "custom" } });
    const newer = doc.tiles.find((each) => each.id === "routes_newer_kit")!;
    expect(tileFace(newer)).toMatchObject({
      kind: "unavailable",
      why: "This tile was made with a newer version of baum. Update baum to see it.",
    });
    const unresolved = doc.tiles.find((each) => each.id === "routes_unresolved")!;
    expect(tileFace(unresolved)).toMatchObject({
      kind: "unavailable",
      why: unresolved.problems![0],
    });
    const noDigest = { ...ready, definition: { ...(ready.definition as object), digest: "nope" } };
    expect(tileFace(noDigest).kind).toBe("unavailable");
  });

  it("mount no frame in the desktop app on Windows", () => {
    const fake = (userAgent: string, desktop: boolean) =>
      ({
        navigator: { userAgent, platform: "" },
        ...(desktop ? { __TAURI_INTERNALS__: {} } : {}),
      }) as unknown as Window;
    const windows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Edg/130.0";
    expect(framesAllowedHere(fake(windows, true))).toBe(false);
    expect(framesAllowedHere(fake(windows, false))).toBe(true);
    expect(framesAllowedHere(fake("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", true))).toBe(
      true,
    );
    expect(framesAllowedHere(fake("Mozilla/5.0 (X11; Linux x86_64)", true))).toBe(true);
  });
});
