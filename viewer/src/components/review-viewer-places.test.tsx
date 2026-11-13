// @vitest-environment jsdom
import { act, type ReactNode, useState } from "react";
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
    ["pane=tile&tile=tile-a&item=rsk_x", "tile"],
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

  it("lets the host start a comparison from its context", async () => {
    const loadVersion = vi.fn(async () => ({ status: "ready" as const, document: earlier() }));
    const seen: ReviewPlaceContext[] = [];
    const container = await render(
      <Host>{viewer({ places: dummyPlaces({}, seen), versions: [2], loadVersion })}</Host>,
    );
    await act(async () => seen.at(-1)!.compareWith(2));
    expect(loadVersion).toHaveBeenCalledWith(2);
    expect(container.textContent).toContain("Historical · Review v2");
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
        ? "pane=tile&tile=sec_retry&item=dec_snapshot"
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
      "pane=tile&tile=tile-b&item=x",
    );
  });
});
