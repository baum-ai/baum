import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ReviewViewer } from "@baum/viewer/components/review-viewer";
import { type SearchUpdate, UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { fixtureTile } from "../tiles/fixture-test-helper";
import { TileHostContext, type TileHost } from "../tiles/host";
import type { TileContext } from "../tiles/types";
import { tiledDocument } from "../api";
import type { ReviewV2 } from "./document";
import { legacyAnswer1303, REVIEW_1303, review1303 } from "./fixture-1303";
import { reviewOf, type ViewTile } from "./model";
import { useViewPlaces } from "./places";
import { ViewTileCard } from "./view-tile";

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

let search = new URLSearchParams();

function Host({ initial = "", children }: { initial?: string; children: ReactNode }) {
  const [state, setState] = useState(() => new URLSearchParams(initial));
  search = state;
  const update = (change: SearchUpdate) =>
    setState((value) => {
      const next = change(new URLSearchParams(value));
      search = next;
      return next;
    });
  return <UrlStateProvider value={{ search: state, update }}>{children}</UrlStateProvider>;
}

function Page({ document }: { document: ReviewDocument }) {
  const places = useViewPlaces();
  return <ReviewViewer document={document} places={places} />;
}

async function render(node: ReactNode) {
  const container = window.document.createElement("div");
  window.document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(node));
  return container;
}

const page = (initial = "", document = review1303()) =>
  render(
    <Host initial={initial}>
      <Page document={document} />
    </Host>,
  );

const pane = (container: HTMLElement) => container.querySelector<HTMLElement>("#review-section")!;
const tabs = (container: HTMLElement) => [
  ...pane(container).querySelectorAll<HTMLButtonElement>(
    '[role="tablist"][aria-label="Review"] [role="tab"]',
  ),
];
const tab = (container: HTMLElement, name: string) =>
  tabs(container).find((each) => each.textContent === name)!;
const panel = (container: HTMLElement) =>
  pane(container).querySelector<HTMLElement>('[role="tabpanel"]:not([hidden])')!;
const button = (scope: HTMLElement, text: string) =>
  [...scope.querySelectorAll<HTMLButtonElement>("button")].find((each) =>
    each.textContent?.includes(text),
  )!;
const click = (element: HTMLElement) => act(async () => element.click());

describe("the review page's tabs", () => {
  it("draws Overview, How it works and Code, and keeps the tab in the address", async () => {
    const container = await page();
    expect(tabs(container).map((each) => each.textContent)).toEqual([
      "Overview",
      "How it works",
      "Code",
    ]);
    expect(tab(container, "Overview").getAttribute("aria-selected")).toBe("true");
    await click(tab(container, "How it works"));
    // The tab opens on the first real section, never on Where this fits.
    expect(search.get("pane")).toBe("section");
    expect(search.get("section")).toBe("sec_pairing");
    expect(tab(container, "How it works").getAttribute("aria-selected")).toBe("true");
    await click(tab(container, "Code"));
    expect(search.get("pane")).toBe("tile");
    expect(search.get("tile")).toBe("code");
    await click(tab(container, "How it works"));
    // Back to the section read last.
    expect(search.get("section")).toBe("sec_pairing");
  });

  it("opens where the address says, so a reload or a link lands there", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fremote%2Frelay.go",
    );
    expect(tab(container, "Code").getAttribute("aria-selected")).toBe("true");
    expect(panel(container).querySelector("#review-view-file")?.getAttribute("aria-label")).toBe(
      "oss/internal/remote/relay.go",
    );
  });
});

describe("the Overview", () => {
  it("reads in its fixed order", async () => {
    const container = await page();
    const view = panel(container);
    const text = view.textContent ?? "";
    const order = [
      "A paired phone can now reach the daemon",
      "Risk",
      "This adds a network entry point to the daemon",
      "Approve into the mobile branch once you've decided on Always allow.",
      "How a phone reaches the daemon",
      "What was asked",
    ].map((part) => text.indexOf(part));
    expect(order.every((at) => at >= 0)).toBe(true);
    expect(order).toEqual(order.toSorted((a, b) => a - b));
    expect(view.querySelector('[aria-label="Risk"]')?.textContent).toContain("4 of 5");
    // The verdict is part of the risk card, under the score and its reason.
    expect(view.querySelector('[aria-label="Risk"] [aria-label="Before merging"]')).not.toBeNull();
    // No check callouts and no line counts.
    for (const gone of ["Verify", "Fixed later", "Follow-up", "lines"])
      expect(text).not.toContain(gone);
  });

  it("links each verdict point to its section", async () => {
    const container = await page();
    const verdict = panel(container).querySelector<HTMLElement>('[aria-label="Before merging"]')!;
    expect(verdict.querySelectorAll("li")).toHaveLength(2);
    await click(button(verdict, "What a phone can do"));
    expect(search.get("section")).toBe("sec_allowlist");
    expect(tab(container, "How it works").getAttribute("aria-selected")).toBe("true");
  });

  it("says what was asked against what the pull request does, linking the prompt", async () => {
    const container = await page();
    const asked = panel(container).querySelector<HTMLElement>('[aria-label="What was asked"]')!;
    expect(asked.textContent).toContain("Review on the phone");
    expect(asked.textContent).toContain("The relay sees only ciphertext and token hashes.");
    const link = asked.querySelector("a")!;
    const query = new URLSearchParams(link.getAttribute("href")!.slice(1));
    expect([query.get("pane"), query.get("session"), query.get("turn")]).toEqual([
      "session",
      "ses_mobile",
      "1",
    ]);
  });

  it("falls back to the review alone without overview parts or tiles", async () => {
    const bare: ReviewV2 = {
      ...REVIEW_1303,
      summary: { ...REVIEW_1303.summary, verdict: null, risk_why: "" },
      overview: { screenshots: [], diagram: null, intent: [] },
      tiles: [],
    };
    const container = await page("", review1303(bare));
    const view = panel(container);
    expect(view.querySelector('[aria-label="Before merging"]')).toBeNull();
    expect(view.querySelector('[aria-label="Risk"]')?.textContent).toContain(
      "Phones can approve pull requests and answer agents through a third-party relay.",
    );
    expect(view.querySelector('[role="figure"]')).toBeNull();
    expect(view.querySelector('[aria-label="What was asked"]')?.textContent).toContain(
      "allows to review on mobile",
    );
  });
});

describe("How it works", () => {
  it("lists Where this fits first and shows the section read", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    const view = panel(container);
    const list = view.querySelector<HTMLElement>('nav[aria-label="Sections"]')!;
    expect([...list.querySelectorAll("[data-row-title]")].map((each) => each.textContent)).toEqual([
      "Where this fits",
      "Pairing",
      "Relay connection",
      "Encryption",
      "Requests",
      "What a phone can do",
      "Daemon lifecycle",
      "Dependencies",
    ]);
    expect(button(list, "What a phone can do").getAttribute("aria-pressed")).toBe("true");
    const body = view.querySelector<HTMLElement>("article")!;
    expect(body.querySelector("h2")?.textContent).toBe("What a phone can do");
    expect(body.textContent).toContain("lists 37 routes by method and exact path");
    // Its tiles, each titled, then its behaviour and its risks; its code is
    // in its Code view, not under them.
    expect(
      [...body.querySelectorAll('[role="figure"] h3')].map((each) => each.textContent),
    ).toEqual([
      "What a phone can reach",
      "What Always allow changes",
      "Interface changes",
      "The writes a phone may make",
    ]);
    const parts = [...body.querySelectorAll("section[aria-label]")].map((each) =>
      each.getAttribute("aria-label"),
    );
    expect(parts).toEqual(expect.arrayContaining(["What it does", "Risks"]));
    expect(parts).not.toContain("Code");
    expect(body.querySelector('[aria-label="Code changes"]')).toBeNull();
    expect(body.querySelector('[aria-label="What it does"]')?.textContent).toContain(
      "Deny by default",
    );
    expect(body.querySelector('[aria-label="Risks"]')?.textContent).toContain("High risk");
    expect(body.textContent).not.toContain("Decide");
  });

  it("draws Where this fits from the context, its files opening the Code tab", async () => {
    const container = await page("pane=section&section=context");
    const body = panel(container).querySelector<HTMLElement>("article")!;
    expect(body.querySelector("h2")?.textContent).toBe("Where this fits");
    expect(body.textContent).toContain("Phones don't get a new API.");
    expect(body.querySelector('[role="figure"] h3')?.textContent).toBe(
      "Where the transport sits in the daemon",
    );
    // baum.review/2's context names files, not symbols.
    await click(button(body, "remote.go"));
    expect(search.get("tile")).toBe("code");
    expect(search.get("tile_item")).toBe("oss/internal/uiserver/remote.go");
  });

  it("opens a risk's section from a link to the risk", async () => {
    const container = await page("pane=risk&risk_section=sec_allowlist&risk_id=rsk_always_allow");
    const body = panel(container).querySelector<HTMLElement>("article")!;
    expect(body.querySelector("h2")?.textContent).toBe("What a phone can do");
    expect(body.querySelector("#sec_allowlist-rsk_always_allow")).not.toBeNull();
  });
});

const views = (container: HTMLElement) => [
  ...pane(container).querySelectorAll<HTMLButtonElement>(
    '[role="tablist"][aria-label="Section views"] [role="tab"]',
  ),
];
const viewTab = (container: HTMLElement, label: string) =>
  views(container).find((each) => each.textContent?.startsWith(label))!;
const viewPanel = (container: HTMLElement) =>
  pane(container).querySelector<HTMLElement>(
    'article [role="tabpanel"][id^="section-views-panel"]:not([hidden])',
  )!;

describe("a section's views", () => {
  it("shows every view with its count, disabled when the section has none", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    expect(
      views(container).map((each) => [
        each.textContent,
        each.disabled,
        each.getAttribute("aria-selected"),
      ]),
    ).toEqual([
      ["Overview", false, "true"],
      ["Code 1", false, "false"],
      ["Tests 1", false, "false"],
      ["Session 0", true, "false"],
      ["Docs 1", false, "false"],
    ]);
    // Where this fits has no views.
    await click(button(pane(container), "Where this fits"));
    expect(views(container)).toEqual([]);
  });

  it("keeps the view in the address, and opens another section on its Overview", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    await click(viewTab(container, "Tests"));
    expect(search.get("section_view")).toBe("tests");
    expect(viewTab(container, "Tests").getAttribute("aria-selected")).toBe("true");
    expect(
      viewPanel(container).querySelector('[aria-label$=" diff"]')?.getAttribute("aria-label"),
    ).toBe("oss/internal/uiserver/remote_test.go diff");
    await click(button(pane(container), "Relay connection"));
    expect(search.get("section")).toBe("sec_relay");
    expect(search.has("section_view")).toBe(false);
    expect(viewTab(container, "Overview").getAttribute("aria-selected")).toBe("true");
  });

  it("opens on the view the address names, and on the Overview when it has nothing", async () => {
    const container = await page("pane=section&section=sec_allowlist&section_view=docs");
    expect(viewTab(container, "Docs").getAttribute("aria-selected")).toBe("true");
    expect(
      viewPanel(container).querySelector('[aria-label$=" diff"]')?.getAttribute("aria-label"),
    ).toBe("oss/SECURITY.md diff");
    act(() => root?.unmount());
    const empty = await page("pane=section&section=sec_allowlist&section_view=session");
    expect(viewTab(empty, "Overview").getAttribute("aria-selected")).toBe("true");
  });

  it("draws the section's code with its own notes, generated files folded at the end", async () => {
    const container = await page("pane=section&section=sec_allowlist&section_view=code");
    const code = viewPanel(container);
    expect(
      [...code.querySelectorAll('[aria-label$=" diff"]')].map((each) =>
        each.getAttribute("aria-label"),
      ),
    ).toEqual(["oss/internal/uiserver/remote.go diff"]);
    const notes = [...code.querySelectorAll<HTMLElement>('ul[aria-label="Notes"] li')];
    expect(notes.map((note) => note.dataset.note)).toEqual(["step", "risk", "step"]);
    // A note leads back to the section's Overview, at the risk it names.
    await click(button(notes[1]!, "In Overview"));
    expect([search.get("pane"), search.get("risk_id")]).toEqual(["risk", "rsk_always_allow"]);
    act(() => root?.unmount());
    const deps = await page("pane=section&section=sec_deps&section_view=code");
    const generated = viewPanel(deps);
    // Generated files open when they are all the section's code.
    expect(button(generated, "Generated").getAttribute("aria-expanded")).toBe("true");
    expect(generated.querySelector('[aria-label="oss/go.sum diff"]')).not.toBeNull();
  });

  it("names the tests the section's test hunks add, above their diffs", async () => {
    const document = review1303();
    const hunk = document.index.hunks.find((each) => each.id === "h_remote_test")!;
    hunk.diff!.lines = [
      ...hunk.diff!.lines,
      { kind: "add", text: "func TestRemoteRoutesCoverLocalAPI(t *testing.T) {", new_line: 21 },
      { kind: "add", text: "}", new_line: 22 },
    ];
    const container = await page("pane=section&section=sec_allowlist&section_view=tests", document);
    const names = viewPanel(container).querySelector('[aria-label="Tests added or changed"]');
    expect(names?.textContent).toContain("TestRemoteRoutesCoverLocalAPI");
    // The fixture's own hunk adds no test, so it lists none.
    act(() => root?.unmount());
    const plain = await page("pane=section&section=sec_allowlist&section_view=tests");
    expect(viewPanel(plain).querySelector('[aria-label="Tests added or changed"]')).toBeNull();
    expect(viewPanel(plain).querySelector('[aria-label$=" diff"]')).not.toBeNull();
  });

  it("quotes the prompts and events the section cites, linking to their turns", async () => {
    const container = await page("pane=section&section=sec_relay");
    expect(viewTab(container, "Session").textContent).toBe("Session 2");
    await click(viewTab(container, "Session"));
    expect(search.get("section_view")).toBe("session");
    const quotes = [...viewPanel(container).querySelectorAll<HTMLElement>("[data-quote]")];
    expect(quotes.map((quote) => quote.dataset.quote)).toEqual(["prompt", "event"]);
    expect(quotes[0]?.querySelector("blockquote")?.textContent).toBe(
      "use an encrypted relay that we run",
    );
    expect(quotes[0]?.textContent).toContain("Person");
    expect(quotes[1]?.textContent).toContain("Claude");
    const links = quotes.map((quote) => new URLSearchParams(quote.querySelector("a")!.search));
    expect(links.map((link) => [link.get("session"), link.get("turn")])).toEqual([
      ["ses_mobile", "2"],
      ["ses_mobile", "2"],
    ]);
  });
});

describe("Code", () => {
  it("lists the files grouped, generated ones folded", async () => {
    const container = await page("pane=tile&tile=code");
    const list = panel(container).querySelector<HTMLElement>('nav[aria-label="Changed files"]')!;
    expect(list.textContent).toContain("Code");
    expect(list.textContent).toContain("Tests");
    expect(list.textContent).toContain("Docs");
    expect(list.textContent).not.toContain("go.sum");
    await click(button(list, "Generated"));
    expect(list.textContent).toContain("go.sum");
    // It opens on the first file that is not generated.
    expect(panel(container).querySelector("#review-view-file")?.getAttribute("aria-label")).toBe(
      "oss/internal/uiserver/remote.go",
    );
  });

  it("pins notes to the hunks they cite, each linking to its section", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go",
    );
    const file = panel(container).querySelector<HTMLElement>("#review-view-file")!;
    // The diff's header names the file once, with where it is explained.
    expect(file.querySelector("h2")).toBeNull();
    expect(file.querySelector("article header")?.textContent).toContain("Explained in");
    const notes = [...file.querySelectorAll<HTMLElement>('ul[aria-label="Notes"] li')];
    expect(notes.map((note) => note.dataset.note)).toEqual(["step", "risk", "step"]);
    expect(notes[0]?.textContent).toContain("Deny by default");
    await click(button(notes[1]!, "What a phone can do"));
    expect([search.get("pane"), search.get("risk_id")]).toEqual(["risk", "rsk_always_allow"]);
  });

  it("says when a hunk's code isn't in the review", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fremote%2Frelay.go",
    );
    expect(panel(container).textContent).toContain("1 change in this file isn't in the review.");
  });

  it("folds a generated file until asked", async () => {
    const container = await page("pane=tile&tile=code&tile_item=oss%2Fgo.sum");
    const file = panel(container).querySelector<HTMLElement>("#review-view-file")!;
    expect(file.textContent).toContain("This file is generated, so it's folded.");
    expect(file.querySelector("table")).toBeNull();
    await click(button(file, "Show the diff"));
    expect(file.querySelector("table")).not.toBeNull();
  });
});

// ---------- the native tiles ----------

const CHROME = review1303();
const ctx = { review: CHROME } as unknown as TileContext;
const tileOf = (id: string) => reviewOf(CHROME).tiles.find((each) => each.id === id)!;
const card = (tile: ViewTile) =>
  render(
    <Host>
      <ViewTileCard tile={tile} review={CHROME} ctx={ctx} />
    </Host>,
  );

describe("the native tiles", () => {
  it("draws a flow's lanes, their labels and the joins between steps", async () => {
    const container = await card(tileOf("dg_shape"));
    expect(container.querySelector("h3")?.textContent).toBe("How a phone reaches the daemon");
    const lanes = [...container.querySelectorAll<HTMLElement>("[data-lane]")];
    expect(lanes).toHaveLength(2);
    expect(lanes[0]?.textContent).toContain("Phone");
    expect(lanes[0]?.querySelectorAll("li")).toHaveLength(5);
    expect(
      [...lanes[0]!.querySelectorAll<HTMLElement>("[data-join]")].map((each) => each.dataset.join),
    ).toEqual(["both", "both", "arrow", "arrow"]);
    // "none" lists the step beside the one before it, with no arrow.
    expect(lanes[1]?.querySelectorAll("[data-join]")).toHaveLength(2);
    expect(container.textContent).toContain(
      "The daemon dials out to the relay. Nothing on the computer listens for the phone.",
    );
  });

  it("draws a single unlabelled lane as a plain flow", async () => {
    const container = await card(tileOf("dg_pairing"));
    expect(container.querySelectorAll("[data-lane]")).toHaveLength(1);
    expect(container.querySelector("[data-lane] > span")).toBeNull();
  });

  it("draws columns under their labels", async () => {
    const container = await card(tileOf("dg_allowlist"));
    const columns = [...container.querySelectorAll<HTMLElement>("section[aria-label]")];
    expect(columns.map((each) => each.getAttribute("aria-label"))).toEqual([
      "A phone can",
      "A phone can't",
    ]);
    expect(columns[1]?.querySelectorAll("li")).toHaveLength(3);
  });

  it("draws a sequence as arrows between actors and as a list for a narrow pane", async () => {
    const container = await card(tileOf("dg_encryption"));
    expect(container.textContent).toContain("Phone");
    expect(container.textContent).toContain("Daemon");
    const lists = container.querySelectorAll("ol");
    // Wide (arrows) and narrow (list), one shown by the room the tile has.
    expect(lists).toHaveLength(2);
    expect(lists[1]?.querySelectorAll("li")).toHaveLength(5);
    expect(lists[1]?.textContent).toContain("Phone → Daemon");
    expect(container.querySelector('[data-tone="good"]')?.textContent).toContain(
      "Encrypted answers",
    );
  });

  it("draws a table", async () => {
    const container = await card(tileOf("dg_relay_runs"));
    expect([...container.querySelectorAll("th")].map((each) => each.textContent)).toEqual([
      "Run",
      "Relay",
      "Result",
    ]);
    expect(container.querySelectorAll("tbody tr")).toHaveLength(3);
  });

  it("folds a long table to its first rows until it is opened", async () => {
    const table = tileOf("dg_relay_runs");
    const data = table.data as { columns: string[]; rows: string[][] };
    const rows = Array.from({ length: 12 }, (_, at) => [`run ${at}`, "relay", "ok"]);
    const container = await card({ ...table, data: { ...data, rows } });
    expect(container.querySelectorAll("tbody tr")).toHaveLength(6);
    await click(button(container, "Show all 12 rows"));
    expect(container.querySelectorAll("tbody tr")).toHaveLength(12);
  });

  it("folds a tile's citations into one line that opens them", async () => {
    const container = await card(tileOf("dg_shape"));
    const cites = button(container, "1 file");
    expect(cites.getAttribute("aria-expanded")).toBe("false");
    expect(container.textContent).not.toContain("oss/internal/uiserver/remote.go");
    await click(cites);
    expect(container.textContent).toContain("remote.go");
  });

  it("says when a tile is being written, failed or can't be read, under its title", async () => {
    const pending = await card(tileOf("lib_api_change"));
    expect(pending.querySelector("h3")?.textContent).toBe("Interface changes");
    expect(pending.textContent).toContain("Being written");
    act(() => root?.unmount());
    const failed = await card(tileOf("dg_stop"));
    expect(failed.querySelector("h3")?.textContent).toBe("How the daemon stops");
    expect(failed.textContent).toContain("This tile couldn't be written.");
    act(() => root?.unmount());
    const broken = await card({ ...tileOf("dg_shape"), title: "", data: { lanes: "no" } });
    expect(broken.querySelector("h3")?.textContent).toBe("Untitled diagram");
    expect(broken.textContent).toContain("This tile's content couldn't be read.");
  });

  it("says a custom tile made with a newer kit is, in the page's words", async () => {
    const container = await card(fixtureTile("routes_newer_kit") as ViewTile);
    expect(container.textContent).toContain(
      "This tile was made with a newer version of baum. Update baum to see it.",
    );
    expect(container.querySelector("iframe")).toBeNull();
  });

  it("draws an exhibit as the generic tiles do", async () => {
    const container = await card({
      id: "ex_note",
      type: "prose",
      owner: "sec_relay",
      cites: [],
      title: "Why a relay",
      state: "ready",
      data: { text: "Phones can't reach a laptop behind NAT.", anchors: [] },
    });
    expect(container.querySelector("h3")?.textContent).toBe("Why a relay");
    expect(container.textContent).toContain("Phones can't reach a laptop behind NAT.");
  });
});

// ---------- tiles the person added, and those being made ----------

describe("a tile the person added", () => {
  const added: ViewTile = {
    id: "ex_added",
    type: "prose",
    owner: "sec_relay",
    cites: [],
    title: "Retry notes",
    state: "ready",
    data: { text: "Retries double their delay.", anchors: [] },
    added: true,
    from: 3,
    filling: true,
  };
  const hosted = (tile: ViewTile, host: TileHost) =>
    render(
      <Host>
        <TileHostContext.Provider value={host}>
          <ViewTileCard tile={tile} review={CHROME} ctx={ctx} />
        </TileHostContext.Provider>
      </Host>,
    );
  const menuItems = () =>
    [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] [role="menuitem"]')].map(
      (each) => each.textContent,
    );

  it("says where its data is from, and offers Remove in its menu", async () => {
    const removeAdded = vi.fn();
    const container = await hosted(added, { draft: false, removeAdded });
    expect(container.querySelector("[data-added-note]")?.textContent).toBe(
      "From version 3; filling for this version",
    );
    await click(
      container.querySelector<HTMLButtonElement>('[aria-label="Actions for Retry notes"]')!,
    );
    expect(menuItems()).toContain("Remove");
    const remove = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(
      (each) => each.textContent === "Remove",
    )!;
    await click(remove);
    expect(removeAdded).toHaveBeenCalledWith(expect.objectContaining({ id: "ex_added" }));
  });

  it("is the only kind of tile that can be removed", async () => {
    const removeAdded = vi.fn();
    const container = await hosted(
      { ...added, added: undefined, from: undefined, filling: undefined },
      { draft: false, removeAdded, ask: vi.fn() },
    );
    expect(container.querySelector("[data-added-note]")).toBeNull();
    await click(
      container.querySelector<HTMLButtonElement>('[aria-label="Actions for Retry notes"]')!,
    );
    expect(menuItems()).toEqual(["Ask about this tile"]);
  });

  it("has no menu where there is nothing to do with it", async () => {
    const container = await hosted(added, { draft: false });
    expect(container.querySelector('[aria-label="Actions for Retry notes"]')).toBeNull();
  });
});

describe("pending cards", () => {
  const drafts = [
    { tile: "draft_1", title: "Route table", phase: "writing" as const, section: "sec_allowlist" },
    { tile: "draft_2", title: "Where it runs", phase: "checking" as const, section: "context" },
    { tile: "draft_3", title: "Somewhere", phase: "writing" as const, section: "sec_gone" },
  ];
  const withDrafts = () => review1303({ ...REVIEW_1303, drafts });
  const cards = (container: HTMLElement) =>
    [...panel(container).querySelectorAll<HTMLElement>("[data-tile-draft]")].map(
      (each) => each.dataset.tileDraft,
    );

  it("stand on their section, and one for a section not listed on the first listed", async () => {
    const section = await page("pane=section&section=sec_allowlist", withDrafts());
    expect(cards(section)).toEqual(["draft_1"]);
    act(() => root?.unmount());
    const context = await page("pane=section&section=context", withDrafts());
    expect(cards(context)).toEqual(["draft_2", "draft_3"]);
  });
});

// TEMPORARY, with from-v1.ts: until the daemon answers baum.review/2.
describe("a baum.review/1 answer with its view", () => {
  it("draws the same page as the baum.review/2 document", async () => {
    const legacy = tiledDocument(legacyAnswer1303() as Parameters<typeof tiledDocument>[0]);
    expect(legacy.schema).toBe("baum.review/1");
    const overview = await page("", legacy);
    const drawn = panel(overview).textContent;
    act(() => root?.unmount());
    expect(panel(await page()).textContent).toBe(drawn);
    act(() => root?.unmount());
    const section = await page("pane=section&section=sec_allowlist", legacy);
    expect(
      [...panel(section).querySelectorAll<HTMLElement>("[data-tile]")].map(
        (each) => each.dataset.tile,
      ),
    ).toEqual(["dg_allowlist", "dg_always_allow", "lib_api_change", "code_remote_routes"]);
  });
});
