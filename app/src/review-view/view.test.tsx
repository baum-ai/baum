import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ReviewViewer } from "@baum/viewer/components/review-viewer";
import { type SearchUpdate, UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import type { TileContext } from "../tiles/types";
import { REVIEW_1303, review1303, VIEW_1303 } from "./fixture-1303";
import { emptyView, type ReviewView, type ViewTile } from "./model";
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
  ...pane(container).querySelectorAll<HTMLButtonElement>('[role="tab"]'),
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
    expect(panel(container).querySelector("h2")?.textContent).toBe("oss/internal/remote/relay.go");
  });
});

describe("the Overview", () => {
  it("reads in its fixed order", async () => {
    const container = await page();
    const view = panel(container);
    const text = view.textContent ?? "";
    const order = [
      "A paired phone can now reach the daemon",
      "Approve into the mobile branch once you've decided on Always allow.",
      "Risk",
      "This adds a network entry point to the daemon",
      "How a phone reaches the daemon",
      "What was asked",
    ].map((part) => text.indexOf(part));
    expect(order.every((at) => at >= 0)).toBe(true);
    expect(order).toEqual(order.toSorted((a, b) => a - b));
    expect(view.querySelector('[aria-label="Risk"]')?.textContent).toContain("4 of 5");
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

  it("falls back to the review alone without a view", async () => {
    const container = await page("", review1303(null));
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
    // Its tiles, each titled, then its behaviour, its risks and its code.
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
    expect(parts).toEqual(expect.arrayContaining(["What it does", "Risks", "Code"]));
    expect(body.querySelector('[aria-label="What it does"]')?.textContent).toContain(
      "Deny by default",
    );
    expect(body.querySelector('[aria-label="Risks"]')?.textContent).toContain("High risk");
    // The code is folded.
    expect(body.querySelector('[aria-label="Code"] details')?.hasAttribute("open")).toBe(false);
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
    await click(button(body, "RemoteHandler"));
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
    expect(panel(container).querySelector("h2")?.textContent).toBe(
      "oss/internal/uiserver/remote.go",
    );
  });

  it("pins notes to the hunks they cite, each linking to its section", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go",
    );
    const file = panel(container).querySelector<HTMLElement>("#review-view-file")!;
    expect(file.textContent).toContain("Explained in");
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

const ctx = { review: REVIEW_1303 } as unknown as TileContext;
const tileOf = (id: string) => VIEW_1303.tiles.find((each) => each.id === id)!;
const card = (tile: ViewTile) =>
  render(
    <Host>
      <ViewTileCard tile={tile} review={REVIEW_1303} ctx={ctx} />
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

  it("draws an exhibit as the generic tiles do", async () => {
    const container = await card({
      id: "ex_note",
      type: "prose",
      title: "Why a relay",
      state: "ready",
      data: { text: "Phones can't reach a laptop behind NAT.", anchors: [] },
    });
    expect(container.querySelector("h3")?.textContent).toBe("Why a relay");
    expect(container.textContent).toContain("Phones can't reach a laptop behind NAT.");
  });
});

it("keeps an empty view's sections listed with no tiles", () => {
  const view: ReviewView = emptyView(REVIEW_1303);
  expect(view.sections.every((section) => section.tiles.length === 0)).toBe(true);
});
