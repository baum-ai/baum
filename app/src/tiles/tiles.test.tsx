import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { UrlStateProvider, type SearchUpdate } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../../schema/review-example.json" with { type: "json" };
import baseReview from "../../../tiles/testdata/base.review.json" with { type: "json" };
import { TileBoundary } from "./card";
import { TileDetail } from "./detail";
import { TileLanding } from "./landing";
import { blocksOf, landingOf, narrowFills } from "./layout";
import { TileHostContext } from "./host";
import { tileFace } from "./registry";
import { golden } from "./goldens-test-helper";
import type { Tile, TileDocument } from "./tile-document";
import type { TileContext } from "./types";

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
  window.matchMedia ??= (query: string) =>
    ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }) as unknown as MediaQueryList;
  Element.prototype.scrollIntoView ??= () => {};
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

let search = new URLSearchParams();

function render(node: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const update = (change: SearchUpdate) => {
    search = change(new URLSearchParams(search));
  };
  act(() => root!.render(<UrlStateProvider value={{ search, update }}>{node}</UrlStateProvider>));
  return container;
}

/** The example review and its tiles, as the fixture daemon serves them. */
const pairs = {
  example: () => ({ doc: golden("example"), review: example as unknown as ReviewDocument }),
  base: () => ({ doc: golden("base"), review: baseReview as unknown as ReviewDocument }),
};

function contextFor(
  doc: TileDocument,
  review: ReviewDocument,
  overrides: Partial<TileContext> = {},
): TileContext {
  return {
    doc,
    review,
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
    labels: new Map([["rsk_retry_storm", "Retries can pile up"]]),
    compare: null,
    ...overrides,
  };
}

const tileOf = (doc: TileDocument, id: string) => doc.tiles.find((tile) => tile.id === id)!;

describe("every built-in tile", () => {
  for (const [name, pair] of Object.entries(pairs))
    it(`draws its glance and its detail on the ${name} review`, () => {
      const { doc, review } = pair();
      const ctx = contextFor(doc, review);
      // Every tile on the page: the rest shown, every group open.
      search = new URLSearchParams("more=1&expand=changes,obsolete");
      for (const tile of doc.tiles) {
        expect(tileFace(tile).kind, tile.id).toBe("ready");
        const landing = render(
          <TileLanding ctx={ctx} generation={null} memory={{ current: null }} />,
        );
        const card = landing.querySelector(`[data-tile="${tile.id}"]`);
        expect(card?.textContent, tile.id).toContain(tile.title);
        act(() => root!.unmount());
        const detail = render(<TileDetail tile={tile} ctx={ctx} back={() => {}} />);
        expect(detail.textContent?.length, tile.id).toBeGreaterThan(20);
        expect(detail.textContent, tile.id).not.toContain("couldn't be shown");
        act(() => root!.unmount());
      }
      search = new URLSearchParams();
    });
});

describe("the Change tile", () => {
  it("says what the change does, its size, its risks and its evidence", () => {
    const { doc, review } = pairs.base();
    const page = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    const card = page.querySelector('[data-tile="sec_retry_policy"]')!;
    expect(card.textContent).toContain("Deliver retries a failed request up to five times.");
    expect(card.textContent).toContain("2 files");
    expect(card.textContent).toContain("2 behaviours");
    expect(card.textContent).toContain("1 missing");
    expect(card.querySelector('[aria-label="Holds a high or critical risk"]')).not.toBeNull();
  });

  it("says its details are being written, or offers to write them again", async () => {
    const doc = golden("details-states");
    const retry = vi.fn().mockResolvedValue(undefined);
    const ctx = contextFor(doc, baseReview as unknown as ReviewDocument, { onRetryDetails: retry });
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    expect(page.querySelector('[data-tile="sec_retry_policy"]')?.textContent).toContain(
      "Writing details…",
    );
    const failed = page.querySelector('[data-tile="sec_config_flag"]')!;
    expect(failed.textContent).toContain("Details unavailable");
    const button = [...failed.querySelectorAll("button")].find((b) => b.textContent === "Retry")!;
    await act(async () => button.click());
    expect(retry).toHaveBeenCalledWith("sec_config_flag");
    // The strip counts both.
    expect(page.textContent).toContain("1 change couldn't be written");
  });

  it("offers no retry on an earlier version", () => {
    const doc = golden("details-states");
    const ctx = contextFor(doc, baseReview as unknown as ReviewDocument, {
      onRetryDetails: vi.fn(),
      historical: true,
    });
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    const failed = page.querySelector('[data-tile="sec_config_flag"]')!;
    expect([...failed.querySelectorAll("button")].some((b) => b.textContent === "Retry")).toBe(
      false,
    );
  });
});

describe("the Risks tile", () => {
  it("shows every critical and high risk in full, and the tiers' counts", () => {
    const { doc, review } = pairs.example();
    const page = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    const card = page.querySelector('[data-tile="risks"]')!;
    for (const risk of doc.risks.filter(
      (each) => (each.tier === "critical" || each.tier === "high") && each.state !== "mitigated",
    ))
      expect(card.textContent).toContain(risk.text.slice(0, 40));
    expect(card.textContent).toContain("1 critical");
  });

  it("says plainly when there is no open risk", () => {
    const doc = structuredClone(golden("base"));
    (tileOf(doc, "risks").data as { ranked: string[] }).ranked = [];
    doc.risks = doc.risks.map((risk) => ({ ...risk, state: "mitigated" }));
    const page = render(
      <TileLanding
        ctx={contextFor(doc, baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(page.querySelector('[data-tile="risks"]')?.textContent).toContain("No open risks");
  });
});

describe("the landing page's markers", () => {
  it("name each card's type and size and each must-read risk, for tools that measure the page", () => {
    const { doc, review } = pairs.example();
    const page = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    for (const card of page.querySelectorAll<HTMLElement>("article[data-tile]")) {
      const entry = doc.layout.entries.find((each) => each.tile === card.dataset.tile)!;
      expect(card.dataset.tileType).toBe(tileOf(doc, entry.tile).type);
      expect(card.dataset.tileSize).toBe(entry.size);
    }
    const urgent = doc.risks.filter(
      (each) => (each.tier === "critical" || each.tier === "high") && each.state !== "mitigated",
    );
    expect(urgent.length).toBeGreaterThan(0);
    for (const risk of urgent)
      expect(page.querySelector(`[data-risk="${risk.id}"]`)?.textContent).toContain(
        risk.text.slice(0, 20),
      );
  });
});

describe("the table tile", () => {
  it("shows a fifth row rather than counting one more", () => {
    const { doc, review } = pairs.base();
    const rows = (n: number) => Array.from({ length: n }, (_, i) => [`r${i}`, `${i}`]);
    const table = (n: number): Tile =>
      ({
        id: "table_rows",
        type: "table",
        title: "Rows",
        state: "ready",
        data: { columns: ["name", "value"], rows: rows(n), source: [] },
      }) as unknown as Tile;
    const face = tileFace(table(5));
    if (face.kind !== "ready") throw new Error(`the table tile is ${face.kind}`);
    const Glance = face.type.Glance;
    const five = render(<Glance tile={table(5)} ctx={contextFor(doc, review)} />);
    expect(five.textContent).toContain("r4");
    expect(five.textContent).not.toMatch(/more rows/);
    act(() => root!.unmount());
    const six = render(<Glance tile={table(6)} ctx={contextFor(doc, review)} />);
    expect(six.textContent).toContain("2 more rows");
  });
});

describe("the landing grid's blocks", () => {
  it("draws a group as a block, so the tile after it is not under its heading", () => {
    const doc = structuredClone(golden("base"));
    const page = render(
      <TileLanding
        ctx={contextFor(doc, baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    const block = page.querySelector('[data-tile-group-block="changes"]')!;
    const members = doc.layout.entries.filter((entry) => entry.group === "changes");
    expect(block.querySelectorAll("article[data-tile]").length).toBe(members.length);
    expect(block.querySelector('[data-tile="evidence"]')).toBeNull();
    expect(page.querySelector('[data-tile="evidence"]')).not.toBeNull();
  });

  it("finds the cards that span both narrow columns block by block", () => {
    const tile = (id: string, size: "full" | "half" | "third", group?: string) => ({
      kind: "tile" as const,
      tile: { id } as Tile,
      entry: { tile: id, size, ...(group ? { group } : {}) },
    });
    const group = { id: "changes", title: "Changes" };
    const blocks = blocksOf([
      tile("overview", "half"),
      tile("risks", "half"),
      { kind: "heading", group, count: 3 },
      tile("a", "third", "changes"),
      tile("b", "third", "changes"),
      tile("c", "third", "changes"),
      tile("evidence", "third"),
      tile("context", "full"),
    ]);
    expect(blocks.map((block) => block.group?.id ?? "")).toEqual(["", "changes", ""]);
    // c is alone in its narrow row at the end of the group; evidence ends
    // its row only because the full card after it does not fit, which is
    // not a block's end.
    expect([...narrowFills(blocks)]).toEqual(["c"]);
  });

  it("never folds a single member of a group that shows some", () => {
    const doc = structuredClone(golden("example"));
    const group = doc.layout.groups.find((each) => each.id === "changes")!;
    const members = doc.layout.entries.filter((entry) => entry.group === "changes").length;
    group.collapse_after = members - 1;
    doc.layout.landing = doc.layout.entries.length;
    const folds = () =>
      landingOf(doc, { more: false, groups: new Set() }).items.some(
        (item) => item.kind === "more" && item.group.id === "changes",
      );
    expect(folds()).toBe(false);
    group.collapse_after = members - 2;
    expect(folds()).toBe(true);
  });
});

describe("tiles that cannot be drawn", () => {
  it("are drawn unavailable, whatever their type", () => {
    const doc = structuredClone(golden("base"));
    const custom: Tile = {
      id: "custom_one",
      type: "custom",
      title: "A custom tile",
      data: {},
      state: "unavailable",
      problems: ["Custom tiles aren't supported in this version"],
    };
    const unknown: Tile = {
      id: "future_one",
      type: "future",
      title: "A future tile",
      data: {},
      state: "ready",
    };
    const broken: Tile = { ...tileOf(doc, "risks"), id: "risks_broken", data: { ranked: 4 } };
    doc.tiles.push(custom, unknown, broken);
    doc.layout.entries.splice(
      2,
      0,
      ...[custom, unknown, broken].map((tile) => ({ tile: tile.id, size: "third" as const })),
    );
    doc.layout.landing += 3;
    const page = render(
      <TileLanding
        ctx={contextFor(doc, baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(page.querySelector('[data-tile="custom_one"]')?.textContent).toContain(
      "Custom tiles aren't supported in this version",
    );
    expect(page.querySelector('[data-tile="future_one"]')?.textContent).toContain(
      "This version can't show this kind of tile.",
    );
    expect(page.querySelector('[data-tile="risks_broken"]')?.textContent).toContain(
      "This tile's content couldn't be read.",
    );
  });

  it("keep a tile that throws from taking the page with it", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    const Throws = () => {
      throw Error("broken");
    };
    const page = render(
      <TileBoundary fallback={<p>fallback</p>}>
        <Throws />
      </TileBoundary>,
    );
    expect(page.textContent).toBe("fallback");
    spy.mockRestore();
  });

  it("show a pending tile without data as being written", () => {
    const doc = structuredClone(golden("base"));
    const context = tileOf(doc, "intent");
    context.state = "pending";
    context.data = {};
    expect(tileFace(context).kind).toBe("pending");
    const page = render(
      <TileLanding
        ctx={contextFor(doc, baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(page.querySelector('[data-tile="intent"]')?.textContent).toContain("Writing details…");
  });
});

describe("the landing page", () => {
  it("leads with the strip, then the grid in the layout's order", () => {
    const { doc, review } = pairs.example();
    const page = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    const strip = page.querySelector('[aria-label="Review at a glance"]')!;
    // No recommendation and a current version: the risk score alone.
    expect(strip.querySelector('[aria-label="Recommendation"]')).toBeNull();
    expect(strip.querySelector('[aria-label="This version"]')).toBeNull();
    expect(strip.querySelector('[aria-label="Risk score"]')?.textContent).toContain("5/5");
    const order = [...page.querySelectorAll("[data-tile]")].map((card) =>
      card.getAttribute("data-tile"),
    );
    expect(order.slice(0, 6)).toEqual([
      "overview",
      "risks",
      "sec_api",
      "sec_retry",
      "sec_deadletter",
      "sec_ackfix",
    ]);
    // Five changes are all shown; the tiles past the landing area wait
    // behind the page's own control.
    expect(page.textContent).not.toMatch(/more change/);
    expect(page.textContent).toContain(
      `Show ${doc.layout.entries.length - doc.layout.landing - 1} more tiles`,
    );
  });

  it("names the recommendation and leads to what it cites", () => {
    const doc = golden("recommendation-set");
    const ctx = contextFor(doc, baseReview as unknown as ReviewDocument);
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    const strip = page.querySelector('[aria-label="Recommendation"]')!;
    expect(strip.textContent).toMatch(/Approve|Look first|Request changes/);
  });

  it("shows the must-read risks when the Risks tile does not", () => {
    const doc = structuredClone(golden("critical-risks"));
    tileOf(doc, "risks").state = "pending";
    const page = render(
      <TileLanding
        ctx={contextFor(doc, baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(page.textContent).toContain("Read before approving");
    const ready = render(
      <TileLanding
        ctx={contextFor(golden("critical-risks"), baseReview as unknown as ReviewDocument)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(ready.textContent).not.toContain("Read before approving");
  });

  it("opens a tile from its card", () => {
    const { doc, review } = pairs.base();
    const ctx = contextFor(doc, review);
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    act(() => page.querySelector<HTMLElement>('[data-tile="evidence"] [data-block-open]')!.click());
    expect(ctx.open).toHaveBeenCalledWith("evidence");
  });

  it("shows New and Changed against a compared version", () => {
    const { doc, review } = pairs.base();
    const ctx = contextFor(doc, review, {
      comparing: true,
      compare: { added: new Set(["media"]), changed: new Set(["sec_config_flag"]) },
    });
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    expect(page.querySelector('[data-tile="media"]')?.textContent).toContain("New");
    expect(page.querySelector('[data-tile="sec_config_flag"]')?.textContent).toContain("Changed");
  });
});

describe("the band", () => {
  it("always names missing or failed evidence and leads to the first", () => {
    const { doc, review } = pairs.base();
    const ctx = contextFor(doc, review);
    const page = render(<TileLanding ctx={ctx} generation={null} memory={{ current: null }} />);
    const band = page.querySelector('[aria-label="Review at a glance"]')!;
    const link = [...band.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("evidence"),
    )!;
    expect(link.textContent).toBe("1 missing evidence");
    act(() => link.click());
    expect(ctx.open).toHaveBeenCalledWith("evidence", "ev_load_run");
  });
});

describe("a Change card's warning", () => {
  it("names a must-read risk the Risks card shows in full, and says it otherwise", () => {
    const { doc, review } = pairs.base();
    const shown = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    expect(shown.querySelector('[data-tile="sec_retry_policy"]')?.textContent).toContain(
      "High risk · Availability",
    );
    act(() => root!.unmount());
    const pending = structuredClone(doc);
    tileOf(pending, "risks").state = "pending";
    const said = render(
      <TileLanding
        ctx={contextFor(pending, review)}
        generation={null}
        memory={{ current: null }}
      />,
    );
    expect(said.querySelector('[data-tile="sec_retry_policy"]')?.textContent).toContain(
      "Every worker retries at the same moments",
    );
  });
});

describe("the Overview card", () => {
  it("leaves the title to the page, naming it only for assistive technology", () => {
    const { doc, review } = pairs.base();
    const page = render(
      <TileLanding ctx={contextFor(doc, review)} generation={null} memory={{ current: null }} />,
    );
    const card = page.querySelector('[data-tile="overview"]')!;
    expect(card.querySelector("h4")?.className).toContain("sr-only");
    expect(card.querySelector("[data-block-open]")?.getAttribute("aria-labelledby")).toBe(
      card.querySelector("h4")?.id,
    );
  });
});

describe("a card's menu", () => {
  it("opens, comments on, asks about and opens in the full review its tile", async () => {
    const { doc, review } = pairs.base();
    const ctx = contextFor(doc, review);
    const host = {
      draft: false,
      commentCounts: new Map(),
      comment: vi.fn(),
      ask: vi.fn(),
      fullReview: () => "/s/test/?view=full&pane=section&section=sec_retry_policy",
    };
    const page = render(
      <TileHostContext.Provider value={host}>
        <TileLanding ctx={ctx} generation={null} memory={{ current: null }} />
      </TileHostContext.Provider>,
    );
    const choose = async (label: string) => {
      const button = page.querySelector<HTMLButtonElement>(
        '[data-tile="sec_retry_policy"] button[aria-haspopup="menu"]',
      )!;
      await act(async () => button.click());
      const item = [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')].find((each) =>
        each.textContent?.startsWith(label),
      );
      expect(item, label).toBeDefined();
      await act(async () => item!.click());
    };
    await choose("Open");
    expect(ctx.open).toHaveBeenCalledWith("sec_retry_policy");
    await choose("Comment");
    expect(host.comment).toHaveBeenCalledWith("sec_retry_policy");
    await choose("Ask about this tile");
    expect(host.ask).toHaveBeenCalledWith(expect.objectContaining({ id: "sec_retry_policy" }));
    await choose("Open in Full review");
    expect(location.search).toBe("?view=full&pane=section&section=sec_retry_policy");
  });
});
