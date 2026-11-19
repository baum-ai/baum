import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ReviewFilesContext } from "@baum/viewer/components/review-contexts";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { fixtureSource, fixtureTile } from "../fixture-test-helper";
import { TileHostContext, type TileHost } from "../host";
import { tileFace } from "../registry";
import type { Tile, TileContext } from "../types";
import { framesAllowedHere, NOT_IN_THIS_SHELL } from "./environment";
import { HELLO_TIMEOUT, PROTOCOL, READY_TIMEOUT } from "./protocol";
import { listenedFrames } from "./router";
import { forgetInput, TileFrame, type Face } from "./tile-frame";
import { forgetPreflights, HUNG_WORDS } from "./preflight";

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
  forgetInput();
});

/** What the browser says of user activation: absent, inactive or active. */
function activation(state: "absent" | boolean) {
  if (state === "absent") Reflect.deleteProperty(navigator, "userActivation");
  else
    Object.defineProperty(navigator, "userActivation", {
      configurable: true,
      value: { isActive: state, hasBeenActive: state },
    });
}

afterEach(() => {
  activation("absent");
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  vi.useRealTimers();
});

const source = fixtureSource();
let counter = 0;

/** A fresh copy of the fixture's custom tile, so no frame failure from one
 *  test is remembered in the next. */
function freshTile(): Tile {
  const tile = fixtureTile("route_changes");
  tile.id = `route_changes_${++counter}`;
  return tile;
}

function contextFor(overrides: Partial<TileContext> = {}): TileContext {
  return {
    source,
    review: {} as ReviewDocument,
    open: vi.fn(),
    openChange: vi.fn(),
    openPane: vi.fn(),
    patches: null,
    idPrefix: "",
    labels: new Map(),
    ...overrides,
  };
}

function mount(face: Face, tile = freshTile()) {
  const ctx = contextFor();
  const host: TileHost = {
    draft: false,
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

/** Lets a frame's coalesced height be applied. */
const nextFrame = () => act(() => vi.advanceTimersByTime(20));

/** The reader tabs into a frame: the Tab lands on its element, and the
 *  page's window loses focus to it. */
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
    nextFrame();
    expect(box().style.height).toBe("20000px");
    post({ protocol: PROTOCOL, kind: "resize", height: 3 });
    nextFrame();
    expect(box().style.height).toBe("120px");
    post({ protocol: PROTOCOL, kind: "resize", height: "999" });
    nextFrame();
    expect(box().style.height).toBe("120px");
  });

  it("holds the height a ready reports to the bounds, and takes ready once", () => {
    const { container, post } = mount("detail");
    const box = () => container.querySelector<HTMLElement>("[data-frame-state]")!;
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    post({ protocol: PROTOCOL, kind: "ready", metrics: { height: 1e9 } });
    expect(box().style.height).toBe("20000px");
    // An unasked ready is a resize under the same guard, never a new start.
    post({ protocol: PROTOCOL, kind: "ready", metrics: { height: 300 } });
    nextFrame();
    expect(box().style.height).toBe("300px");
    for (let i = 0; i < 12; i++) {
      post({ protocol: PROTOCOL, kind: "ready", metrics: { height: i % 2 ? 400 : 900 } });
      nextFrame();
    }
    // It bounced: the height stopped following.
    const frozen = box().style.height;
    post({ protocol: PROTOCOL, kind: "resize", height: 5000 });
    nextFrame();
    expect(box().style.height).toBe(frozen);
  });

  it("applies at most one height an animation frame", () => {
    const { container, post, start } = mount("detail");
    const box = () => container.querySelector<HTMLElement>("[data-frame-state]")!;
    start();
    post({ protocol: PROTOCOL, kind: "resize", height: 500 });
    post({ protocol: PROTOCOL, kind: "resize", height: 520 });
    expect(box().style.height).toBe("420px");
    nextFrame();
    expect(box().style.height).toBe("520px");
  });

  it("keeps a glance's box whatever the frame asks", () => {
    const { container, post, start } = mount("glance");
    start();
    post({ protocol: PROTOCOL, kind: "resize", height: 5000 });
    expect(container.querySelector<HTMLElement>("[data-frame-state]")!.style.height).toBe("");
  });

  it("jumps only to a citation it resolved, only while the reader is in the frame", () => {
    activation(true);
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
    // One a second at most.
    post({ protocol: PROTOCOL, kind: "jump", key: '["tile","overview"]' });
    expect(ctx.open).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1000));
    post({ protocol: PROTOCOL, kind: "jump", key: '["tile","overview"]' });
    expect(ctx.open).toHaveBeenCalledWith("overview");
  });

  it("takes no jump or question without a user activation, failing closed", () => {
    for (const state of ["absent", false] as const) {
      activation(state);
      const { frame, post, start, files, host } = mount("detail");
      start();
      giveFocus(frame()!);
      expect(document.activeElement).toBe(frame());
      post({ protocol: PROTOCOL, kind: "jump", key: '["hunk","h_000000000003",[21,25]]' });
      post({ protocol: PROTOCOL, kind: "ask" });
      expect(files.open).not.toHaveBeenCalled();
      expect(host.ask).not.toHaveBeenCalled();
      act(() => root?.unmount());
      root = null;
    }
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
    activation(true);
    const { frame, post, start, host } = mount("detail");
    start();
    const keys: string[] = [];
    const seen: KeyboardEvent[] = [];
    const listen = (event: KeyboardEvent) => {
      keys.push(event.key);
      seen.push(event);
    };
    window.addEventListener("keydown", listen);
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    giveFocus(frame()!);
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    post({ protocol: PROTOCOL, kind: "key", key: "Enter", shift: false });
    post({ protocol: PROTOCOL, kind: "ask" });
    window.removeEventListener("keydown", listen);
    expect(keys.filter((key) => key !== "Tab")).toEqual(["Escape"]);
    // The event carries the key's name and Shift, and nothing else the frame
    // could choose: no other modifier, code, repeat or composition.
    const forwarded = seen.find((event) => event.key === "Escape")!;
    expect(forwarded.target).toBe(frame());
    expect([
      forwarded.ctrlKey,
      forwarded.metaKey,
      forwarded.altKey,
      forwarded.shiftKey,
      forwarded.repeat,
      forwarded.isComposing,
      forwarded.code,
    ]).toEqual([false, false, false, false, false, false, ""]);
    expect(host.ask).toHaveBeenCalledTimes(1);
  });

  it("does not take the pointer resting on a detail while the reader types elsewhere as a click", () => {
    activation(true);
    const { frame, post, start } = mount("detail");
    start();
    const composer = document.createElement("textarea");
    document.body.append(composer);
    composer.focus();
    act(() => {
      frame()!.dispatchEvent(new Event("pointerenter"));
      composer.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
      frame()!.focus();
      window.dispatchEvent(new Event("blur"));
      vi.advanceTimersByTime(1);
    });
    expect(document.activeElement).toBe(composer);
    post({ protocol: PROTOCOL, kind: "key", key: "Escape", shift: false });
    // A click in it with nothing typed just before is the reader's.
    act(() => vi.advanceTimersByTime(1000));
    act(() => {
      frame()!.focus();
      window.dispatchEvent(new Event("blur"));
      vi.advanceTimersByTime(1);
    });
    expect(document.activeElement).toBe(frame());
    // Without an activation, the pointer over it is not a click.
    composer.focus();
    activation(false);
    act(() => vi.advanceTimersByTime(1000));
    act(() => {
      frame()!.focus();
      window.dispatchEvent(new Event("blur"));
      vi.advanceTimersByTime(1);
    });
    expect(document.activeElement).not.toBe(frame());
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

  it("takes an error before the tile drew as its failure, its words quoted apart", () => {
    const { container, post } = mount("detail");
    post({ protocol: PROTOCOL, kind: "hello", kit: 1, registered: true });
    post({
      protocol: PROTOCOL,
      kind: "error",
      code: "script_error",
      message: "<b>Your session expired: sign in again</b>",
    });
    // The host's own reason; the tile's words only behind Details, quoted.
    expect(container.querySelector("[role=alert] > div > p")?.textContent).toBe(
      "This tile couldn't load.",
    );
    expect(container.textContent).toContain("It failed while drawing.");
    const quoted = container.querySelector("details blockquote");
    expect(quoted?.textContent).toBe("<b>Your session expired: sign in again</b>");
    expect(container.querySelector("details")?.open).toBe(false);
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

  it("reads the tile routes once for frames silent together", async () => {
    const reread = vi.fn(
      () => new Promise<undefined>((resolve) => setTimeout(() => resolve(undefined), 100)),
    );
    const host: TileHost = { draft: false, reread };
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() =>
      root!.render(
        <TileHostContext.Provider value={host}>
          <TileFrame tile={freshTile()} ctx={contextFor()} face="glance" />
          <TileFrame tile={freshTile()} ctx={contextFor()} face="glance" />
          <TileFrame tile={freshTile()} ctx={contextFor()} face="detail" />
        </TileHostContext.Provider>,
      ),
    );
    await act(async () => {
      vi.advanceTimersByTime(HELLO_TIMEOUT + 10);
    });
    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    expect(reread).toHaveBeenCalledTimes(1);
    expect(container.querySelectorAll("iframe")).toHaveLength(3);
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
    const ready = fixtureTile("route_changes");
    expect(tileFace(ready)).toMatchObject({ kind: "ready", type: { type: "custom" } });
    const newer = fixtureTile("routes_newer_kit");
    expect(tileFace(newer)).toMatchObject({
      kind: "unavailable",
      why: "This tile was made with a newer version of baum. Update baum to see it.",
    });
    const unresolved = fixtureTile("routes_unresolved");
    expect(tileFace(unresolved)).toMatchObject({
      kind: "unavailable",
      why: unresolved.problems![0],
    });
    expect(tileFace({ ...ready, state: "pending", data: {} }).kind).toBe("pending");
    const noDigest = { ...ready, definition: { ...(ready.definition as object), digest: "nope" } };
    expect(tileFace(noDigest).kind).toBe("unavailable");
  });

  it("mount frames in a browser, and in a desktop shell only when it says it is safe", () => {
    const fake = (fields: Record<string, unknown>) =>
      ({ navigator: { userAgent: "", platform: "" }, ...fields }) as unknown as Window;
    const shell = { __TAURI_INTERNALS__: {} };
    // A browser, declaration or not (a page cannot be made a shell).
    expect(framesAllowedHere(fake({}))).toBe(true);
    expect(framesAllowedHere(fake({ __BAUM_DESKTOP__: { tileFrames: false } }))).toBe(true);
    // A shell that declares it.
    expect(framesAllowedHere(fake({ ...shell, __BAUM_DESKTOP__: { tileFrames: true } }))).toBe(
      true,
    );
    // A shell that declares nothing, or anything else.
    for (const declared of [
      undefined,
      null,
      true,
      "tileFrames",
      {},
      { tileFrames: false },
      { tileFrames: "true" },
      { tileFrames: 1 },
      Object.create({ tileFrames: true }),
    ])
      expect(
        framesAllowedHere(fake({ ...shell, __BAUM_DESKTOP__: declared })),
        String(declared),
      ).toBe(false);
  });

  it("say so in a shell that has not declared it", () => {
    const win = window as Window & { __TAURI_INTERNALS__?: unknown };
    win.__TAURI_INTERNALS__ = {};
    try {
      const { container } = (() => {
        const container = document.createElement("div");
        document.body.append(container);
        root = createRoot(container);
        act(() => root!.render(<TileFrame tile={freshTile()} ctx={contextFor()} face="glance" />));
        return { container };
      })();
      expect(container.textContent).toBe(NOT_IN_THIS_SHELL);
      expect(container.querySelector("iframe")).toBeNull();
    } finally {
      delete win.__TAURI_INTERNALS__;
    }
  });
});

describe("the first-show preflight", () => {
  type Shell = { __TAURI_INTERNALS__?: unknown; __BAUM_DESKTOP__?: unknown };
  afterEach(() => {
    delete (window as Shell).__TAURI_INTERNALS__;
    delete (window as Shell).__BAUM_DESKTOP__;
    vi.unstubAllGlobals();
    forgetPreflights();
  });

  /** Renders a glance where the shell has a check window, the daemon
   *  answering the preflight with `verdict`. */
  async function preflighted(verdict: string) {
    (window as Shell).__TAURI_INTERNALS__ = { invoke: vi.fn() };
    (window as Shell).__BAUM_DESKTOP__ = { tileFrames: true, checkWindow: true };
    const fetched = vi.fn(async (_url: string, _init?: RequestInit) => ({
      ok: true,
      json: async () => ({ verdict }),
    }));
    vi.stubGlobal("fetch", fetched);
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    const host: TileHost = { draft: false };
    act(() =>
      root!.render(
        <TileHostContext.Provider value={host}>
          <TileFrame tile={freshTile()} ctx={contextFor()} face="glance" />
        </TileHostContext.Provider>,
      ),
    );
    const before = container.querySelector("iframe");
    const state = container.querySelector<HTMLElement>("[data-custom-tile]")?.dataset.frameState;
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    return { container, before, state, fetched };
  }

  it("waits for the check, then mounts a tile that drew", async () => {
    const { container, before, state, fetched } = await preflighted("pass");
    expect(before).toBeNull();
    expect(state).toBe("checking");
    expect(container.querySelector("iframe")).not.toBeNull();
    const [url, init] = fetched.mock.calls[0]!;
    expect(url).toContain("api/tile-checks/preflight");
    expect(JSON.parse(String(init?.body))).toMatchObject({
      digest: expect.stringMatching(/^sha256:/),
      size: expect.any(String),
    });
  });

  it("keeps a tile that hung down", async () => {
    const { container } = await preflighted("hang");
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.textContent).toContain(HUNG_WORDS);
  });

  it("asks for one tile at a time", async () => {
    // Each request stays open until its tile is drawn, and the check window
    // shares the page's few connections to the daemon.
    (window as Shell).__TAURI_INTERNALS__ = { invoke: vi.fn() };
    (window as Shell).__BAUM_DESKTOP__ = { tileFrames: true, checkWindow: true };
    const pending: (() => void)[] = [];
    const fetched = vi.fn(
      () =>
        new Promise((resolve) =>
          pending.push(() => resolve({ ok: true, json: async () => ({ verdict: "pass" }) })),
        ),
    );
    vi.stubGlobal("fetch", fetched);
    const first = freshTile();
    const second = freshTile();
    second.data = { ...(second.data as object), other: true };
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    const host: TileHost = { draft: false };
    const settle = () =>
      act(async () => {
        for (let i = 0; i < 5; i++) await Promise.resolve();
      });
    act(() =>
      root!.render(
        <TileHostContext.Provider value={host}>
          <TileFrame tile={first} ctx={contextFor()} face="glance" />
          <TileFrame tile={second} ctx={contextFor()} face="glance" />
        </TileHostContext.Provider>,
      ),
    );
    await settle();
    expect(fetched).toHaveBeenCalledTimes(1);
    pending[0]!();
    await settle();
    expect(fetched).toHaveBeenCalledTimes(2);
    pending[1]!();
    await settle();
    expect(container.querySelectorAll("iframe")).toHaveLength(2);
  });

  it("does not ask where there is no check window", () => {
    const fetched = vi.fn();
    vi.stubGlobal("fetch", fetched);
    mount("glance");
    expect(fetched).not.toHaveBeenCalled();
  });
});
