// @vitest-environment jsdom
import { act, type ReactNode, useEffect, useLayoutEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import { type SearchUpdate, UrlStateProvider } from "../navigation";
import type { Block, ReviewDocument } from "../review-document";
import type { Selection } from "../review-selection";
import { SummaryIcon } from "./icons";
import { ReviewSummary } from "./review-summary";
import {
  type ReviewPlaceContext,
  type ReviewPlaces,
  ReviewViewer,
  type ReviewStep,
  type ReviewViewerProps,
} from "./review-viewer";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= (query: string) =>
    ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }) as unknown as MediaQueryList;
  Element.prototype.scrollIntoView ??= () => {};
  HTMLElement.prototype.hidePopover ??= () => {};
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  localStorage.clear();
});

const current = example as unknown as ReviewDocument;

function earlier(): ReviewDocument {
  const document = structuredClone(current);
  document.review.version = 2;
  document.review.previous_version = 1;
  return document;
}

function Host({ initial = "", children }: { initial?: string; children: ReactNode }) {
  const [search, setSearch] = useState(() => new URLSearchParams(initial));
  const update = (change: SearchUpdate) => setSearch((value) => change(value));
  return (
    <UrlStateProvider value={{ search, update }}>
      {children}
      <output data-testid="search">{search.toString()}</output>
    </UrlStateProvider>
  );
}

async function render(node: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(node));
  return container;
}

const ORDER = ["home", "tile-a", "tile-b"];

const rowFor = (id: string): Selection =>
  id === "home" ? { kind: "summary" } : { kind: "tile", id };
const keyOf = (selection: Selection) =>
  selection.kind === "tile" ? selection.id : selection.kind === "summary" ? "home" : "other";
const strip = (container: HTMLElement) =>
  container.querySelector<HTMLButtonElement>('button[aria-label="Expand sections"]');

/** A host whose places are Home and two tiles, held in the rail's first rows. */
function dummyPlaces(overrides: Partial<ReviewPlaces> = {}, seen?: ReviewPlaceContext[]) {
  const places: ReviewPlaces = {
    renderRail: (context) => (
      <div data-testid="host-rail">
        {ORDER.map((id) => (
          <button
            key={id}
            type="button"
            aria-pressed={keyOf(context.selection) === id}
            aria-controls="review-section"
            onClick={() => context.select(rowFor(id))}
          >
            Host row {id}
          </button>
        ))}
      </div>
    ),
    railRows: (context) =>
      ORDER.map((id) => ({
        key: id,
        label: `Host row ${id}`,
        go: () => context.select(rowFor(id)),
      })),
    steps: (selection, context) => {
      const at = ORDER.indexOf(keyOf(selection));
      if (at < 0) return null;
      const step = (index: number) =>
        ORDER[index]
          ? { label: `Place ${ORDER[index]}`, go: () => context.select(rowFor(ORDER[index]!)) }
          : null;
      return {
        label: `Place ${ORDER[at]}`,
        icon: SummaryIcon,
        position: `${at + 1} of ${ORDER.length}`,
        previous: step(at - 1),
        next: step(at + 1),
        railKey: ORDER[at]!,
      };
    },
    renderPane: (snapshot, historical, context) => {
      seen?.push(context);
      return (
        <div data-testid="host-pane">
          {context.selection.kind}:{JSON.stringify(context.selection)}@v{snapshot.review.version}
          {historical ? " historical" : " current"}
        </div>
      );
    },
    ...overrides,
  };
  return places;
}

function viewer(props: Partial<ReviewViewerProps> = {}) {
  return <ReviewViewer document={current} {...props} />;
}

const paneLabel = (container: HTMLElement) =>
  container.querySelector("#review-section")?.getAttribute("aria-label");

describe("ReviewViewer with host places", () => {
  it("draws the host's rows in place of Summary and Changes and keeps the other rows", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces(),
          decisions: { count: 2, renderPane: () => <p>Decisions pane</p> },
          sessions: { state: { status: "ready", items: [] }, renderPane: () => <p>Sessions</p> },
          commits: { state: { status: "ready", items: [] }, renderPane: () => <p>Commits</p> },
          files: {
            files: [],
            loading: false,
            failed: false,
            retry() {},
            renderTree: () => null,
            renderFiles: () => null,
          },
        })}
      </Host>,
    );
    const rail = container.querySelector("#review-rail")!;
    expect(rail.querySelector('[data-testid="host-rail"]')?.textContent).toContain("Host row home");
    const labels = [...rail.querySelectorAll("button")].map((button) => button.textContent);
    expect(labels.some((label) => label === "Summary")).toBe(false);
    expect(labels.some((label) => label?.startsWith("Changes"))).toBe(false);
    expect(rail.querySelector("#review-changes")).toBeNull();
    for (const kept of ["Files changed", "Decisions", "Sessions", "Commits"])
      expect(labels.some((label) => label?.startsWith(kept))).toBe(true);
  });

  it("steps by the host's model and names the place from it", async () => {
    const seen: ReviewPlaceContext[] = [];
    const container = await render(
      <Host initial="pane=tile&tile=tile-a">{viewer({ places: dummyPlaces({}, seen) })}</Host>,
    );
    const step = (name: "Previous" | "Next") =>
      container.querySelector<HTMLButtonElement>(`button[aria-label^="${name}"]`)!;
    expect(paneLabel(container)).toBe("Place tile-a");
    expect(container.querySelector("[data-review-page]")?.textContent).toContain("2 of 3");
    expect(step("Previous").getAttribute("aria-label")).toBe("Previous: Place home");
    expect(step("Next").getAttribute("aria-label")).toBe("Next: Place tile-b");
    await act(async () => step("Next").click());
    expect(paneLabel(container)).toBe("Place tile-b");
    expect(step("Next").disabled).toBe(true);
    await act(async () => step("Previous").click());
    await act(async () => step("Previous").click());
    expect(paneLabel(container)).toBe("Place home");
    expect(step("Previous").disabled).toBe(true);
  });

  it("hands the host's keys the stepping model, with the rail rows kept in order", async () => {
    const steps = { current: null } as NonNullable<ReviewViewerProps["steps"]>;
    await render(
      <Host initial="pane=tile&tile=tile-a">{viewer({ places: dummyPlaces(), steps })}</Host>,
    );
    expect(steps.current?.previous?.label).toBe("Place home");
    expect(steps.current?.next?.label).toBe("Place tile-b");
    expect(steps.current?.rail?.previous?.label).toBe("Host row home");
    expect(steps.current?.rail?.next?.label).toBe("Host row tile-b");
  });

  it.each([
    ["pane=summary", "summary"],
    ["pane=changes", "changes"],
    ["pane=risks", "risks"],
    ["pane=section&section=sec_retry", "section"],
    ["pane=risk&risk_section=sec_retry&risk_id=rsk_x", "risk"],
    ["pane=tile&tile=tile-a&tile_item=rsk_x", "tile"],
  ])("routes %s to the host's pane", async (initial, kind) => {
    const container = await render(
      <Host initial={initial}>{viewer({ places: dummyPlaces() })}</Host>,
    );
    const pane = container.querySelector('[data-testid="host-pane"]');
    expect(pane?.textContent).toContain(`${kind}:`);
    expect(container.querySelector("#review-section")?.contains(pane!)).toBe(true);
  });

  it("keeps the viewer's own panes for the places the host does not own", async () => {
    const container = await render(
      <Host initial="pane=decisions">
        {viewer({
          places: dummyPlaces(),
          decisions: { renderPane: () => <p>Decisions pane</p> },
        })}
      </Host>,
    );
    expect(container.querySelector('[data-testid="host-pane"]')).toBeNull();
    expect(container.querySelector("#review-section")?.textContent).toContain("Decisions pane");
  });

  it("calls the host's pane inside the version history, alone and compared", async () => {
    const loadVersion = vi.fn(async () => ({ status: "ready" as const, document: earlier() }));
    const alone = await render(
      <Host initial="pane=tile&tile=tile-a&review=2">
        {viewer({ places: dummyPlaces(), versions: [2], loadVersion })}
      </Host>,
    );
    const old = alone.querySelector('[aria-label="Historical view v2"]')!;
    expect(old.querySelector('[data-testid="host-pane"]')?.textContent).toContain("@v2 historical");
    expect(alone.querySelectorAll('[data-testid="host-pane"]')).toHaveLength(1);
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();

    const seen: ReviewPlaceContext[] = [];
    const compared = await render(
      <Host initial="pane=tile&tile=tile-a&review=2&compare=1">
        {viewer({ places: dummyPlaces({}, seen), versions: [2], loadVersion })}
      </Host>,
    );
    const panes = [...compared.querySelectorAll('[data-testid="host-pane"]')].map(
      (pane) => pane.textContent,
    );
    expect(panes.some((text) => text?.endsWith("@v3 current"))).toBe(true);
    expect(panes.some((text) => text?.endsWith("@v2 historical"))).toBe(true);
    const context = seen.at(-1)!;
    expect(context.comparing).toBe(true);
    expect(context.historical).toBe(true);
    expect(context.hunkPatches).toBeNull();
    expect(context.version).toBeUndefined();
    expect(context.historicalDocument?.review.version).toBe(2);
    expect(context.loadVersion).toBe(loadVersion);
  });

  it.each([
    ["redirects", true],
    ["leaves to the viewer, when the host declines,", false],
  ])("%s a reference chip in prose", async (_name, redirect) => {
    const snapshot = structuredClone(current);
    const section = snapshot.sections.find((item) => item.id === "sec_retry")!;
    const decisions = section.blocks.find((block) => block.kind === "decisions") as Extract<
      Block,
      { kind: "decisions" }
    >;
    decisions.items[0]!.id = "dec_snapshot";
    snapshot.summary.text = "Inspect dec_snapshot.";
    snapshot.summary.recommendation = null;
    const jump = vi.fn((id: string, item?: string): Selection | null =>
      redirect ? { kind: "tile", id, item } : null,
    );
    const container = await render(
      <Host>
        {viewer({
          document: snapshot,
          places: dummyPlaces({
            jump,
            renderPane: (shown) => <ReviewSummary document={shown} onSelectSection={() => {}} />,
          }),
        })}
      </Host>,
    );
    const chip = container.querySelector<HTMLButtonElement>('[title="Decision dec_snapshot"]')!;
    await act(async () => chip.click());
    expect(jump).toHaveBeenCalledWith("sec_retry", "dec_snapshot");
    expect(container.querySelector('[data-testid="search"]')?.textContent).toBe(
      redirect
        ? "pane=tile&tile=sec_retry&tile_item=dec_snapshot"
        : "pane=section&section=sec_retry&decision=dec_snapshot",
    );
  });
});

describe("ReviewViewer places and the rail's fold", () => {
  it("starts folded when the host says so, unfolds on request and follows the host again", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({ railFolded: (selection) => selection.kind === "summary" }),
        })}
      </Host>,
    );
    const body = () => container.querySelector("#review-rail > div.contents");
    expect(strip(container)).not.toBeNull();
    expect(body()?.className).toContain("wide:hidden");

    await act(async () => strip(container)!.click());
    expect(strip(container)).toBeNull();
    expect(body()?.className).not.toContain("wide:hidden");

    // Folding it again, then moving to a place the host starts open, opens it.
    await act(async () =>
      container.querySelector<HTMLButtonElement>('button[aria-label="Collapse sections"]')!.click(),
    );
    expect(strip(container)).not.toBeNull();
    await act(async () =>
      [...container.querySelectorAll<HTMLButtonElement>('[data-testid="host-rail"] button')]
        .find((button) => button.textContent?.includes("tile-a"))!
        .click(),
    );
    expect(strip(container)).toBeNull();
    expect(container.querySelector('[aria-label="Collapse sections"]')).not.toBeNull();
  });

  it("never folds the rail, and draws no fold button, without places", async () => {
    const container = await render(<Host>{viewer()}</Host>);
    expect(container.querySelector('[aria-label="Expand sections"]')).toBeNull();
    expect(container.querySelector('[aria-label="Collapse sections"]')).toBeNull();
    expect(container.querySelector("#review-rail > div.contents")).toBeNull();
  });
});

/** Whether the side pane is folded to its strip, once wide. */
const asideFolded = (container: HTMLElement) =>
  container.querySelector("#review-aside")?.className.includes("wide:items-center") ?? false;
const pressAside = (container: HTMLElement, label: string) =>
  container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!.click();
const goTo = (container: HTMLElement, row: string) =>
  [...container.querySelectorAll<HTMLButtonElement>('[data-testid="host-rail"] button')]
    .find((button) => button.textContent?.includes(row))!
    .click();

describe("ReviewViewer places and an earlier version's label", () => {
  it("keeps the label for assistive technology alone when the host's version bar says it", async () => {
    const loadVersion = vi.fn(async () => ({ status: "ready" as const, document: earlier() }));
    const quiet = await render(
      <Host initial="pane=tile&tile=tile-a&review=2">
        {viewer({ places: dummyPlaces({ quietHistory: true }), versions: [2], loadVersion })}
      </Host>,
    );
    const label = [...quiet.querySelectorAll("p")].find((p) =>
      p.textContent?.startsWith("Historical · Review v2"),
    );
    expect(label?.className).toContain("sr-only");
    act(() => root?.unmount());
    root = null;
    document.body.replaceChildren();
    const plain = await render(
      <Host initial="pane=tile&tile=tile-a&review=2">
        {viewer({ places: dummyPlaces(), versions: [2], loadVersion })}
      </Host>,
    );
    const shown = [...plain.querySelectorAll("p")].find((p) =>
      p.textContent?.startsWith("Historical · Review v2"),
    );
    expect(shown?.className).not.toContain("sr-only");
  });
});

describe("ReviewViewer places and the side pane's fold", () => {
  it("starts folded where the host says so and as the reader left it elsewhere", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({ sidePaneFolded: (selection) => selection.kind === "summary" }),
        })}
      </Host>,
    );
    expect(asideFolded(container)).toBe(true);
    // The reader unfolds it; that holds while the host's answer does.
    await act(async () => pressAside(container, "Expand side pane"));
    expect(asideFolded(container)).toBe(false);
    // A place the host does not fold shows the reader's own choice: open.
    await act(async () => goTo(container, "tile-a"));
    expect(asideFolded(container)).toBe(false);
    // Back on the landing place the host's fold applies again.
    await act(async () => goTo(container, "home"));
    expect(asideFolded(container)).toBe(true);
  });

  it("leaves the side pane to the reader without an answer", async () => {
    const container = await render(<Host>{viewer({ places: dummyPlaces() })}</Host>);
    expect(asideFolded(container)).toBe(false);
    await act(async () => pressAside(container, "Collapse side pane"));
    expect(asideFolded(container)).toBe(true);
  });
});

describe("ReviewViewer without places", () => {
  it("reads a tile selection as the Summary, without writing the URL", async () => {
    const container = await render(<Host initial="pane=tile&tile=overview">{viewer()}</Host>);
    expect(paneLabel(container)).toBe("Summary");
    expect(container.querySelector('[data-testid="search"]')?.textContent).toBe(
      "pane=tile&tile=overview",
    );
  });

  it("writes a tile selection to the URL when the host selects one", async () => {
    let select: ReviewPlaceContext["select"] | undefined;
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({
            renderPane: (_snapshot, _historical, context) => {
              select = context.select;
              return <p>pane</p>;
            },
          }),
        })}
      </Host>,
    );
    await act(async () => select!({ kind: "tile", id: "tile-b", item: "x" }));
    expect(container.querySelector('[data-testid="search"]')?.textContent).toBe(
      "pane=tile&tile=tile-b&tile_item=x",
    );
  });
});

const stepTo = (step: ReviewStep | null | undefined) => step?.go();

const key = (name: string, shiftKey = false) =>
  act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: name, shiftKey, bubbles: true }));
  });

const decisionChip = '[title="Decision dec_snapshot"]';

/** The review with a decision of sec_retry named, and a summary that cites it. */
function citing(base: ReviewDocument): ReviewDocument {
  const snapshot = structuredClone(base);
  const section = snapshot.sections.find((item) => item.id === "sec_retry")!;
  const decisions = section.blocks.find((block) => block.kind === "decisions") as Extract<
    Block,
    { kind: "decisions" }
  >;
  decisions.items[0]!.id = "dec_snapshot";
  snapshot.summary.text = "Inspect dec_snapshot.";
  snapshot.summary.recommendation = null;
  return snapshot;
}

const search = (container: HTMLElement) =>
  container.querySelector('[data-testid="search"]')?.textContent;

describe("ReviewViewer places: the rail's fold", () => {
  it("shows the host's default in the first commit after its answer changes", async () => {
    const commits: boolean[] = [];
    function Probe() {
      useLayoutEffect(() => {
        commits.push(Boolean(strip(document.body)));
      });
      return null;
    }
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({
            railFolded: (selection) => selection.kind === "summary",
            renderRail: (context) => (
              <>
                <Probe />
                {ORDER.map((id) => (
                  <button key={id} type="button" onClick={() => context.select(rowFor(id))}>
                    Host row {id}
                  </button>
                ))}
              </>
            ),
          }),
        })}
      </Host>,
    );
    const go = (id: string) =>
      act(async () =>
        [...container.querySelectorAll<HTMLButtonElement>("#review-rail button")]
          .find((button) => button.textContent === `Host row ${id}`)!
          .click(),
      );
    await act(async () => strip(container)!.click());
    expect(strip(container)).toBeNull();
    // The answer changes to open: the first commit already shows it open.
    commits.length = 0;
    await go("tile-a");
    expect(commits[0]).toBe(false);
    // The reader folds it; the answer changes back to folded, and the
    // reader's old unfold is gone: folded in the very first commit.
    await act(async () =>
      container.querySelector<HTMLButtonElement>('[aria-label="Collapse sections"]')!.click(),
    );
    commits.length = 0;
    await go("home");
    expect(commits[0]).toBe(true);
    // Back at an answer the reader once unfolded for, the default applies again.
    await act(async () => strip(container)!.click());
    await go("tile-a");
    commits.length = 0;
    await go("home");
    expect(commits[0]).toBe(true);
  });

  it("labels the fold buttons and moves focus to the counterpart", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({ railFolded: (selection) => selection.kind === "summary" }),
        })}
      </Host>,
    );
    const expand = strip(container)!;
    expect(expand.getAttribute("aria-expanded")).toBe("false");
    expect(expand.getAttribute("aria-controls")).toBe("review-rail");
    expect(expand.hasAttribute("aria-keyshortcuts")).toBe(false);
    await act(async () => expand.click());
    const collapse = container.querySelector<HTMLButtonElement>(
      '[aria-label="Collapse sections"]',
    )!;
    expect(collapse.getAttribute("aria-expanded")).toBe("true");
    expect(collapse.getAttribute("aria-controls")).toBe("review-rail");
    expect(document.activeElement).toBe(collapse);
    await act(async () => collapse.click());
    expect(document.activeElement).toBe(strip(container));
  });
});

describe("ReviewViewer places: the pane and the context", () => {
  it("keeps the pane mounted between items of one tile, and remounts it for another tile", async () => {
    const mounts = vi.fn();
    function Pane() {
      useEffect(() => mounts(), []);
      return <p>pane</p>;
    }
    let select: ReviewPlaceContext["select"] | undefined;
    await render(
      <Host initial="pane=tile&tile=tile-a&tile_item=x">
        {viewer({
          places: dummyPlaces({
            renderPane: (_snapshot, _historical, context) => {
              select = context.select;
              return <Pane />;
            },
          }),
        })}
      </Host>,
    );
    expect(mounts).toHaveBeenCalledTimes(1);
    await act(async () => select!({ kind: "tile", id: "tile-a", item: "y" }));
    await act(async () => select!({ kind: "tile", id: "tile-a" }));
    expect(mounts).toHaveBeenCalledTimes(1);
    await act(async () => select!({ kind: "tile", id: "tile-b" }));
    expect(mounts).toHaveBeenCalledTimes(2);
  });

  it("keeps the context's identity across a render that changes nothing it carries", async () => {
    const seen: ReviewPlaceContext[] = [];
    let bump: (() => void) | undefined;
    function Wrapper() {
      const [, setTick] = useState(0);
      bump = () => setTick((tick) => tick + 1);
      return viewer({ places: dummyPlaces({}, seen) });
    }
    await render(
      <Host>
        <Wrapper />
      </Host>,
    );
    const before = seen.length;
    await act(async () => bump!());
    expect(seen.length).toBeGreaterThan(before);
    expect(seen.at(-1)).toBe(seen[before - 1]);
  });

  it("opens a file and reports the generation", async () => {
    const seen: ReviewPlaceContext[] = [];
    const container = await render(
      <Host>
        {viewer({
          header: { generation: { state: "pending" } },
          places: dummyPlaces({}, seen),
          comments: { count: 0, render: () => <p>Comments body</p> },
          files: {
            files: [{ id: "f1", path: "a.go", viewed: false }],
            loading: false,
            failed: false,
            retry() {},
            renderTree: () => null,
            renderFiles: () => <p>Files pane</p>,
          },
        })}
      </Host>,
    );
    const context = () => seen.at(-1)!;
    expect(context().generation?.state).toBe("pending");
    await act(async () => context().openFile("f1"));
    expect(search(container)).toBe("pane=file&file=f1");
    expect(container.querySelector("#review-section")?.textContent).toContain("Files pane");
  });

  it("follows the section the host's steps name in the side pane and the section aside", async () => {
    const asides = vi.fn((id: string) => <p data-testid="aside">{id}</p>);
    const conversation = vi.fn((context: { sectionId: string | null }) => (
      <p data-testid="conversation">{String(context.sectionId)}</p>
    ));
    const container = await render(
      <Host initial="pane=tile&tile=tile-a">
        {viewer({
          renderSectionAside: asides,
          conversation,
          places: dummyPlaces({
            steps: (selection, context) => {
              const base = dummyPlaces().steps(selection, context);
              return base ? { ...base, sectionId: "sec_retry" } : null;
            },
          }),
        })}
      </Host>,
    );
    expect(container.querySelector('[data-testid="conversation"]')?.textContent).toBe("sec_retry");
    expect(container.querySelector('[data-testid="aside"]')?.textContent).toBe("sec_retry");
  });

  it("keeps a tile selection as it is, and does not write the URL", async () => {
    const container = await render(
      <Host initial="pane=tile&tile=unknown">{viewer({ places: dummyPlaces() })}</Host>,
    );
    expect(container.querySelector('[data-testid="host-pane"]')?.textContent).toContain(
      '{"kind":"tile","id":"unknown"}',
    );
    expect(search(container)).toBe("pane=tile&tile=unknown");
  });

  it("round-trips a tile id that needs encoding", async () => {
    const id = "a/b c&d";
    let select: ReviewPlaceContext["select"] | undefined;
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({
            renderPane: (_snapshot, _historical, context) => {
              select = context.select;
              return (
                <p data-testid="host-pane">
                  {context.selection.kind === "tile"
                    ? `${context.selection.id}|${context.selection.item}`
                    : ""}
                </p>
              );
            },
          }),
        })}
      </Host>,
    );
    await act(async () => select!({ kind: "tile", id, item: "i/1 2" }));
    expect(container.querySelector('[data-testid="host-pane"]')?.textContent).toBe(`${id}|i/1 2`);
    expect(new URLSearchParams(search(container)!).get("tile")).toBe(id);
  });
});

describe("ReviewViewer places: keys and chips in earlier versions", () => {
  it("steps across the host's rows and on into the viewer's own, with real keys", async () => {
    const steps = { current: null } as NonNullable<ReviewViewerProps["steps"]>;
    function Keys() {
      useEffect(() => {
        const on = (event: KeyboardEvent) => {
          const live = steps.current;
          if (event.key === "J") stepTo(live?.rail?.next);
          if (event.key === "K") stepTo(live?.rail?.previous);
          if (event.key === "ArrowRight") stepTo(live?.next);
          if (event.key === "ArrowLeft") stepTo(live?.previous);
        };
        window.addEventListener("keydown", on);
        return () => window.removeEventListener("keydown", on);
      }, []);
      return null;
    }
    const container = await render(
      <Host>
        <Keys />
        {viewer({
          steps,
          places: dummyPlaces(),
          decisions: { renderPane: () => <p>Decisions pane</p> },
        })}
      </Host>,
    );
    const pane = () => container.querySelector("#review-section")?.getAttribute("aria-label");
    await key("J", true);
    expect(pane()).toBe("Place tile-a");
    await key("ArrowRight");
    expect(pane()).toBe("Place tile-b");
    // Past the last host row the rail goes on to the viewer's own Decisions row.
    await key("J", true);
    expect(pane()).toBe("Decisions");
    await key("K", true);
    expect(pane()).toBe("Place tile-b");
    await key("ArrowLeft");
    await key("ArrowLeft");
    expect(pane()).toBe("Place home");
    await key("ArrowLeft");
    expect(pane()).toBe("Place home");
  });

  it("hands the item keys to the host's item steps, and keeps its own without them", async () => {
    const steps = { current: null } as NonNullable<ReviewViewerProps["steps"]>;
    const went = vi.fn();
    const item = {
      previous: null,
      next: { label: "Next file", go: went },
    };
    const container = await render(
      <Host initial="pane=tile&tile=tile-a">
        {viewer({
          steps,
          places: dummyPlaces({
            steps: (selection, context) => {
              const base = dummyPlaces().steps(selection, context);
              return base ? { ...base, item } : null;
            },
          }),
        })}
      </Host>,
    );
    expect(container.querySelector("#review-section")).not.toBeNull();
    expect(steps.current?.item?.next).toBe(item.next);
    expect(steps.current?.item?.previous).toBeNull();
    steps.current?.item?.next?.go();
    expect(went).toHaveBeenCalledOnce();
  });

  it("marks blocks with the item keys when the host's steps have no item steps", async () => {
    const steps = { current: null } as NonNullable<ReviewViewerProps["steps"]>;
    await render(
      <Host initial="pane=tile&tile=tile-a">{viewer({ steps, places: dummyPlaces() })}</Host>,
    );
    expect(steps.current?.item?.next?.label).toBe("Next item");
  });

  it.each([
    ["alone", "review=2"],
    ["compared", "review=2&compare=1"],
  ])("redirects a chip in an earlier version read %s", async (_name, initial) => {
    const latest = citing(current);
    const old = citing(earlier());
    const jump = vi.fn((id: string, item?: string): Selection | null => ({
      kind: "tile",
      id,
      item,
    }));
    const container = await render(
      <Host initial={initial}>
        {viewer({
          document: latest,
          versions: [2],
          loadVersion: async () => ({ status: "ready", document: old }),
          places: dummyPlaces({
            jump,
            renderPane: (shown) => <ReviewSummary document={shown} onSelectSection={() => {}} />,
          }),
        })}
      </Host>,
    );
    const pane = container.querySelector('[aria-label^="Historical"]')!;
    await act(async () => pane.querySelector<HTMLButtonElement>(decisionChip)!.click());
    expect(jump).toHaveBeenCalledWith("sec_retry", "dec_snapshot");
    expect(search(container)).toContain("pane=tile&tile=sec_retry&tile_item=dec_snapshot");
  });
});

/** A host's Decisions, Sessions and Commits, one item each. */
const groups = () => ({
  decisions: {
    renderPane: (_actions: unknown, open?: string) => <p>Decisions pane {open ?? "all"}</p>,
  },
  sessions: {
    state: { status: "ready" as const, items: [{ id: "ses_a", label: "Session a" }] },
    renderPane: (id: string | null) => <p>Session pane {id ?? "all"}</p>,
  },
  commits: {
    state: { status: "ready" as const, items: [{ id: "c0ffee", label: "Commit a" }] },
    renderPane: (id: string | null) => <p>Commit pane {id ?? "all"}</p>,
  },
});
const asideTabs = (container: HTMLElement) =>
  [...container.querySelectorAll('#review-aside [role="tab"]')].map((tab) => tab.textContent);

describe("ReviewViewer places with a rail", () => {
  it("keeps the rail and the side pane's tabs to Details and Activity", async () => {
    const container = await render(<Host>{viewer({ places: dummyPlaces(), ...groups() })}</Host>);
    expect(container.querySelector("#review-rail")).not.toBeNull();
    expect(asideTabs(container)).toEqual(["Details", "Activity"]);
  });
});

const row = (container: HTMLElement, label: string) =>
  [...container.querySelectorAll<HTMLButtonElement>("#review-rail button")].find((each) =>
    each.textContent?.startsWith(label),
  )!;

describe("ReviewViewer places with a flat rail", () => {
  const filesSlot = {
    files: [],
    loading: false,
    failed: false,
    retry() {},
    renderTree: () => <p>File tree</p>,
    renderFiles: () => null,
  };
  it("draws Decisions, Sessions and Commits as plain rows that open their panes", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({ flatRail: true }),
          ...groups(),
          files: filesSlot,
        })}
      </Host>,
    );
    for (const label of ["Decisions", "Sessions", "Commits"]) {
      expect(row(container, label).getAttribute("aria-expanded")).toBeNull();
    }
    expect(row(container, "Sessions").textContent).toBe("Sessions1");
    expect(row(container, "Commits").textContent).toBe("Commits1");
    // No items are nested under a row.
    expect(container.querySelector("#review-sessions, #review-commits")).toBeNull();
    expect(
      [...container.querySelectorAll("#review-rail button")].some(
        (each) => each.textContent === "Session a" || each.textContent === "Commit a",
      ),
    ).toBe(false);
    await act(async () => row(container, "Sessions").click());
    expect(search(container)).toContain("pane=sessions");
    expect(container.querySelector("#review-section")?.textContent).toContain("Session pane all");
    expect(row(container, "Sessions").getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps the group's row current on one of its items", async () => {
    const container = await render(
      <Host initial="pane=session&session=ses_a">
        {viewer({ places: dummyPlaces({ flatRail: true }), ...groups() })}
      </Host>,
    );
    expect(row(container, "Sessions").getAttribute("aria-pressed")).toBe("true");
    expect(row(container, "Commits").getAttribute("aria-pressed")).toBe("false");
  });

  it("leaves the Files changed row to a host that draws it", async () => {
    const container = await render(
      <Host>
        {viewer({
          places: dummyPlaces({ flatRail: true, ownFiles: true }),
          ...groups(),
          files: filesSlot,
        })}
      </Host>,
    );
    expect(row(container, "Files changed")).toBeUndefined();
  });

  it("steps the keys through the host's rows, then Decisions, Sessions and Commits", async () => {
    const steps = { current: null } as NonNullable<ReviewViewerProps["steps"]>;
    await render(
      <Host initial="pane=tile&tile=tile-b">
        {viewer({
          places: dummyPlaces({ flatRail: true, ownFiles: true }),
          ...groups(),
          files: filesSlot,
          steps,
        })}
      </Host>,
    );
    expect(steps.current?.rail?.previous?.label).toBe("Host row tile-a");
    expect(steps.current?.rail?.next?.label).toBe("Decisions");
  });

  it("keeps the disclosures without the flag", async () => {
    const container = await render(
      <Host>{viewer({ places: dummyPlaces(), ...groups(), files: filesSlot })}</Host>,
    );
    for (const label of ["Files changed", "Sessions", "Commits"])
      expect(row(container, label).getAttribute("aria-expanded")).not.toBeNull();
  });
});
