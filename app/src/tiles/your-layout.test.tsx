import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../toast";
import { flipMoves } from "./flip";
import {
  announcement,
  isUndoKey,
  layoutKey,
  publishLayoutEvent,
  useTileLayout,
  type LayoutControls,
  type LayoutSummary,
} from "./your-layout";

const requests: { path: string; body?: unknown }[] = [];
let answers: ((path: string, body?: unknown) => unknown)[] = [];

vi.mock("../api", () => ({
  request: async (path: string, body?: unknown) => {
    requests.push({ path, body });
    const answer = answers.shift();
    if (!answer) throw new Error("unexpected request " + path);
    const value = answer(path, body);
    if (value instanceof Error) throw value;
    return value;
  },
}));

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

const summary = (
  revision: number,
  batches: number,
  last: LayoutSummary["last"],
): LayoutSummary => ({
  revision,
  edited: batches > 0 && !last?.reset,
  can_undo: batches > 0,
  last,
  batches,
});
const moved: LayoutSummary["last"] = {
  id: "edt_000000000001",
  by: "chat",
  summary: ["Moved Risks to the top"],
  tiles: ["risks"],
};

describe("what a change to the layout says", () => {
  it("names a new batch, an undo and a reset, and nothing for a read that found no change", () => {
    const none = summary(0, 0, null);
    const one = summary(1, 1, moved);
    expect(announcement(none, one)).toEqual({
      title: "Moved Risks to the top",
      undo: true,
      tiles: ["risks"],
    });
    expect(announcement(one, one)).toBeNull();
    expect(announcement(null, one)).toBeNull();
    expect(announcement(one, summary(2, 0, null))).toEqual({
      title: "Undid: Moved Risks to the top",
      undo: false,
      tiles: ["risks"],
    });
    const reset = summary(3, 2, { id: "edt_000000000002", by: "page", reset: true, summary: [] });
    expect(announcement(one, reset)).toEqual({
      title: "Reset the layout to the generated one",
      undo: true,
      tiles: [],
    });
    const many = summary(4, 2, {
      id: "edt_000000000003",
      by: "chat",
      summary: ["A", "B", "C", "D", "E"],
      tiles: [],
    });
    expect(announcement(one, many)).toMatchObject({ title: "A", detail: "B · C · D · …" });
  });

  it("takes Mod+Z outside text fields only", () => {
    const key = (init: KeyboardEventInit, target?: Element) => {
      const event = new KeyboardEvent("keydown", { key: "z", ...init });
      if (target) Object.defineProperty(event, "target", { value: target });
      return event;
    };
    expect(isUndoKey(key({ metaKey: true }), true)).toBe(true);
    expect(isUndoKey(key({ ctrlKey: true }), false)).toBe(true);
    expect(isUndoKey(key({ ctrlKey: true }), true)).toBe(false);
    expect(isUndoKey(key({ metaKey: true, shiftKey: true }), true)).toBe(false);
    expect(isUndoKey(key({ metaKey: true }, document.createElement("textarea")), true)).toBe(false);
    expect(layoutKey("Acme/Demo", 7)).toBe("acme/demo#7");
  });
});

describe("the cards' moves", () => {
  it("slide a card from its old box and fade a new one in", () => {
    const box = (x: number, y: number, width = 100) => ({ x, y, width, height: 50 });
    const moves = flipMoves(
      new Map([
        ["risks", box(110, 0)],
        ["overview", box(0, 0)],
        ["evidence", box(0, 60)],
      ]),
      new Map([
        ["risks", box(0, 0)],
        ["overview", box(110, 0)],
        ["evidence", box(0, 60, 210)],
        ["context", box(0, 120)],
      ]),
    );
    expect(moves).toEqual([
      { id: "risks", kind: "move", dx: 110, dy: 0, resized: false },
      { id: "overview", kind: "move", dx: -110, dy: 0, resized: false },
      { id: "evidence", kind: "move", dx: 0, dy: 0, resized: true },
      { id: "context", kind: "enter", dx: 0, dy: 0, resized: false },
    ]);
  });
});

describe("the page's layout", () => {
  let root: Root | null = null;
  let controls: LayoutControls | null = null;
  const reread = vi.fn(async () => undefined);
  function Page({ enabled = true }: { enabled?: boolean }) {
    controls = useTileLayout({ clone: "c1", repository: "acme/demo", number: 7, enabled, reread });
    return null;
  }
  const flush = async () => {
    for (let i = 0; i < 5; i++) await act(async () => await Promise.resolve());
  };
  beforeEach(() => {
    requests.length = 0;
    answers = [];
    reread.mockClear();
    controls = null;
  });
  afterEach(() => {
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();
  });
  const mount = async () => {
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    act(() =>
      root!.render(
        <ToastProvider>
          <Page />
        </ToastProvider>,
      ),
    );
    await flush();
  };

  it("reads the tiles again on its event, says what changed with Undo, and undoes with Mod+Z", async () => {
    answers = [() => summary(1, 1, moved)];
    await mount();
    expect(controls).toMatchObject({ edited: true, canUndo: true, revision: 1 });
    expect(reread).not.toHaveBeenCalled();
    // Another pull request's event, and one already seen, read nothing.
    publishLayoutEvent(JSON.stringify({ pr: "acme/demo#8", revision: 5 }));
    publishLayoutEvent(JSON.stringify({ pr: "acme/demo#7", revision: 1 }));
    await flush();
    expect(requests).toHaveLength(1);
    const hidden = {
      id: "edt_000000000002",
      by: "chat" as const,
      summary: ["Removed Context"],
      tiles: ["context"],
    };
    answers = [() => summary(2, 2, hidden)];
    publishLayoutEvent(JSON.stringify({ pr: "acme/demo#7", revision: 2 }));
    await flush();
    expect(reread).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("Removed Context");
    expect(controls?.marked?.tiles.has("context")).toBe(true);
    // Mod+Z undoes the newest batch, naming it.
    answers = [() => summary(3, 1, moved)];
    act(() => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "z", ctrlKey: true, metaKey: true }),
      );
      window.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "z",
          ...(/Mac/.test(navigator.platform) ? { metaKey: true } : { ctrlKey: true }),
        }),
      );
    });
    await flush();
    expect(requests.at(-1)).toEqual({
      path: "api/tiles/layout",
      body: { clone: "c1", pr: 7, action: "undo", batch: "edt_000000000002" },
    });
    expect(document.body.textContent).toContain("Undid: Removed Context");
    expect(reread).toHaveBeenCalledTimes(2);
  });

  it("shows a hidden tile and resets, and says why when the daemon refuses", async () => {
    answers = [() => summary(1, 1, moved)];
    await mount();
    answers = [
      () => new Error("The layout changed meanwhile; read it again"),
      () => summary(4, 2, moved),
    ];
    act(() => controls!.reset());
    await flush();
    expect(requests[1]).toEqual({
      path: "api/tiles/layout",
      body: { clone: "c1", pr: 7, action: "reset" },
    });
    expect(document.body.textContent).toContain("The layout changed meanwhile");
    expect(controls?.revision).toBe(4);
    answers = [
      () =>
        summary(5, 3, {
          id: "edt_000000000005",
          by: "page",
          summary: ["Brought back Context"],
          tiles: ["context"],
        }),
    ];
    act(() => controls!.show("context"));
    await flush();
    expect(requests.at(-1)?.body).toEqual({
      clone: "c1",
      pr: 7,
      action: "edit",
      ops: [{ op: "show", tile: "context" }],
    });
    expect(document.body.textContent).toContain("Brought back Context");
  });
});
