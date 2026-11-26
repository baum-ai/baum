import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ReviewViewer, type ReviewSteps } from "@baum/viewer/components/review-viewer";
import { type SearchUpdate, UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { fixtureTile } from "../tiles/fixture-test-helper";
import { TileHostContext, type TileHost } from "../tiles/host";
import type { TileContext } from "../tiles/types";
import { parseReviewV2, type ReviewV2 } from "./document";
import { REVIEW_1303, review1303 } from "./fixture-1303";
import { setFileGrouping } from "./file-grouping";
import { reviewOf, type ViewTile } from "./model";
import { steppableViews, viewKeyStep } from "./section-views";
import { useViewPlaces } from "./places";
import { drawnAs, imageItems, ViewTileCard } from "./view-tile";
import { foldedLines } from "../tiles/builtins/code-excerpt";
import { ColumnsDiagram, comparedText, wordDiff } from "./native-tiles";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import { reviewDocumentFor } from "../../../viewer/test/review-document";

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
  // Forget a grouping held for the page while storage was refused.
  setFileGrouping("type");
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

let steps: { current: ReviewSteps | null } = { current: null };

function Page({ document }: { document: ReviewDocument }) {
  const places = useViewPlaces();
  steps = { current: null };
  return <ReviewViewer document={document} places={places} steps={steps} />;
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
const rail = (container: HTMLElement) => container.querySelector<HTMLElement>("#review-rail")!;
/** The rail's rows that open the review pane, in order. */
const railRows = (container: HTMLElement) => [
  ...rail(container).querySelectorAll<HTMLButtonElement>('button[aria-controls~="review-section"]'),
];
const rowLabel = (row: HTMLElement) =>
  row.querySelector("span[title]")?.getAttribute("title") ?? row.textContent ?? "";
const railRow = (container: HTMLElement, name: string) =>
  railRows(container).find((each) => rowLabel(each) === name)!;
/** The review pane's body: the place the address names. */
const panel = (container: HTMLElement) => pane(container);
const button = (scope: HTMLElement, text: string) =>
  [...scope.querySelectorAll<HTMLButtonElement>("button")].find((each) =>
    each.textContent?.includes(text),
  )!;
const click = (element: HTMLElement) => act(async () => element.click());

describe("the review page's rail", () => {
  it("lists Overview, How it works with its sections, and Files changed, and keeps the place in the address", async () => {
    const container = await page();
    expect(railRows(container).map(rowLabel)).toEqual([
      "Overview",
      "How it works",
      "Where this fits",
      "Pairing",
      "Relay connection",
      "Encryption",
      "Requests",
      "What a phone can do",
      "Daemon lifecycle",
      "Dependencies",
      "Files changed",
    ]);
    // No tabs above the pane.
    expect(pane(container).querySelector('[role="tablist"][aria-label="Review"]')).toBeNull();
    expect(railRow(container, "Overview").getAttribute("aria-pressed")).toBe("true");
    await click(railRow(container, "How it works"));
    // The group opens its first real section, never Where this fits.
    expect(search.get("pane")).toBe("section");
    expect(search.get("section")).toBe("sec_pairing");
    expect(railRow(container, "Pairing").getAttribute("aria-pressed")).toBe("true");
    expect(railRow(container, "How it works").getAttribute("aria-pressed")).toBe("false");
    await click(railRow(container, "Files changed"));
    expect(search.get("pane")).toBe("tile");
    expect(search.get("tile")).toBe("code");
    expect(railRow(container, "Files changed").getAttribute("aria-pressed")).toBe("true");
    await click(railRow(container, "Requests"));
    expect(search.get("section")).toBe("sec_requests");
  });

  it("counts the changed files on its row, with no disclosure on it", async () => {
    const container = await page();
    const row = railRow(container, "Files changed");
    expect(row.getAttribute("aria-expanded")).toBeNull();
    expect(row.textContent).toMatch(/\d+$/);
    expect(rail(container).querySelector("#review-files")).toBeNull();
  });

  it("opens where the address says, so a reload or a link lands there", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fremote%2Frelay.go",
    );
    expect(railRow(container, "Files changed").getAttribute("aria-pressed")).toBe("true");
    expect(panel(container).querySelector("#review-view-file")?.getAttribute("aria-label")).toBe(
      "oss/internal/remote/relay.go",
    );
  });
});

describe("the rail's plain rows", () => {
  function WithGroups({ document }: { document: ReviewDocument }) {
    const places = useViewPlaces();
    return (
      <ReviewViewer
        document={document}
        places={places}
        sessions={{
          state: {
            status: "ready",
            items: [{ id: "ses_a", label: "Session a", indicator: "live" }],
          },
          indicator: "live",
          renderPane: (id) => <p>Session pane {id ?? "all"}</p>,
        }}
        commits={{
          state: { status: "ready", items: [{ id: "c0ffee", label: "Commit a" }] },
          renderPane: (id) => <p>Commit pane {id ?? "all"}</p>,
        }}
        decisions={{ count: 3, renderPane: () => <p>Decisions pane</p> }}
      />
    );
  }
  const grouped = (initial = "") =>
    render(
      <Host initial={initial}>
        <WithGroups document={review1303()} />
      </Host>,
    );

  it("lists Files changed, Decisions, Sessions and Commits after the sections, none folding open", async () => {
    const container = await grouped();
    expect(railRows(container).map(rowLabel).slice(-4)).toEqual([
      "Files changed",
      "Decisions",
      "Sessions",
      "Commits",
    ]);
    for (const name of ["Files changed", "Decisions", "Sessions", "Commits"])
      expect(railRow(container, name).getAttribute("aria-expanded")).toBeNull();
    expect(railRow(container, "Sessions").textContent).toBe("Sessions1");
    expect(rail(container).querySelector('[role="img"][aria-label="In progress"]')).not.toBeNull();
  });

  it("opens a pane from a row and leaves the Overview unmarked there", async () => {
    const container = await grouped();
    await click(railRow(container, "Sessions"));
    expect(search.get("pane")).toBe("sessions");
    expect(pane(container).textContent).toContain("Session pane all");
    expect(railRow(container, "Sessions").getAttribute("aria-pressed")).toBe("true");
    expect(railRow(container, "Overview").getAttribute("aria-pressed")).toBe("false");
    await click(railRow(container, "Commits"));
    expect(pane(container).textContent).toContain("Commit pane all");
    expect(railRow(container, "Commits").getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps the group's row marked on one of its items", async () => {
    const container = await grouped("pane=session&session=ses_a");
    expect(railRow(container, "Sessions").getAttribute("aria-pressed")).toBe("true");
    expect(railRow(container, "Overview").getAttribute("aria-pressed")).toBe("false");
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
    expect(railRow(container, "What a phone can do").getAttribute("aria-pressed")).toBe("true");
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
      overview: { screenshots: [], videos: [], diagram: null, intent: [] },
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
    // The sections are the rail's, not a list inside the pane.
    expect(view.querySelector('nav[aria-label="Sections"]')).toBeNull();
    expect(railRows(container).map(rowLabel)).toContain("Where this fits");
    expect(railRow(container, "What a phone can do").getAttribute("aria-pressed")).toBe("true");
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
    expect(parts).toContain("Risks");
    expect(parts).not.toContain("What it does");
    expect(parts).not.toContain("Code");
    // The only diff is the code tile's excerpt.
    expect(
      [...body.querySelectorAll('[aria-label="Code changes"]')].map(
        (diff) => diff.closest<HTMLElement>("[data-tile]")?.dataset.tile,
      ),
    ).toEqual(["code_remote_routes"]);
    expect(body.querySelector('[data-block="points"]')?.textContent).toContain("Deny by default");
    expect(body.querySelector('[aria-label="Risks"]')?.textContent).toContain("High");
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

/** A key pressed on `target`, as a real key reaches the page. */
const press = (key: string, target: EventTarget = document.body) =>
  act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });

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
    await click(railRow(container, "Where this fits"));
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
    await click(railRow(container, "Relay connection"));
    expect(search.get("section")).toBe("sec_relay");
    expect(search.has("section_view")).toBe(false);
    expect(viewTab(container, "Overview").getAttribute("aria-selected")).toBe("true");
  });

  it("steps through the views with h and l, over the empty ones, unless the sidebar has the keys", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    const selected = () =>
      views(container).find((each) => each.getAttribute("aria-selected") === "true")?.textContent;
    await press("l");
    expect(selected()).toBe("Code 1");
    await press("l");
    expect(selected()).toBe("Tests 1");
    // Session has nothing, so l passes over it.
    await press("l");
    expect(selected()).toBe("Docs 1");
    await press("l");
    expect(selected()).toBe("Overview");
    await press("h");
    expect(selected()).toBe("Docs 1");
    // The right-hand sidebar keeps h and l for its own tabs.
    const aside = container.querySelector<HTMLElement>("#review-aside");
    expect(aside).not.toBeNull();
    await press("h", aside!);
    expect(selected()).toBe("Docs 1");
  });

  it("steps the side column's views without the Overview, round from either end", () => {
    const counts = { code: 2, tests: 0, session: 1, docs: 3 };
    const side = steppableViews(counts, false);
    expect(side).toEqual(["code", "session", "docs"]);
    expect(viewKeyStep("l", "docs", side)).toBe("code");
    expect(viewKeyStep("h", "code", side)).toBe("docs");
    expect(viewKeyStep("l", "code", side)).toBe("session");
    expect(viewKeyStep("j", "code", side)).toBeNull();
    expect(steppableViews(counts, true)[0]).toBe("overview");
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

  it("draws the section's code with nothing under its hunks, generated files folded at the end", async () => {
    const container = await page("pane=section&section=sec_allowlist&section_view=code");
    const code = viewPanel(container);
    expect(
      [...code.querySelectorAll('[aria-label$=" diff"]')].map((each) =>
        each.getAttribute("aria-label"),
      ),
    ).toEqual(["oss/internal/uiserver/remote.go diff"]);
    // The section's steps and risks are in its Overview, not repeated under the code.
    expect(code.textContent).not.toContain("Always allow");
    act(() => root?.unmount());
    const deps = await page("pane=section&section=sec_deps&section_view=code");
    const generated = viewPanel(deps);
    // Generated files open when they are all the section's code.
    expect(button(generated, "Generated").getAttribute("aria-expanded")).toBe("true");
    expect(generated.querySelector('[aria-label="oss/go.sum diff"]')).not.toBeNull();
  });

  it("heads each test file's diff with its tests and helpers, a row opening its line", async () => {
    const document = review1303();
    const hunk = document.index.hunks.find((each) => each.id === "h_remote_test")!;
    hunk.diff!.lines = [
      ...hunk.diff!.lines,
      { kind: "add", text: "func TestRemoteRoutesCoverLocalAPI(t *testing.T) {", new_line: 21 },
      { kind: "add", text: "}", new_line: 22 },
    ];
    const path = "oss/internal/uiserver/remote_test.go";
    const container = await page("pane=section&section=sec_allowlist&section_view=tests", document);
    const panel = viewPanel(container);
    expect(panel.querySelector('[aria-label="Test changes"]')?.textContent).toBe("Tests: 1 added");
    const file = panel.querySelector<HTMLElement>(`[aria-label="${path} diff"]`)!;
    // The list sits in the file's own diff, above its code.
    const tests = file.querySelector<HTMLElement>(`[aria-label="Tests in ${path}"]`)!;
    expect(tests.textContent).toContain("TestRemoteRoutesCoverLocalAPI");
    expect(tests.compareDocumentPosition(file.querySelector("table")!)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    // What the file changes beside its tests is listed under them.
    expect(file.querySelector(`[aria-label="Helpers in ${path}"]`)?.textContent).toContain(
      "imports",
    );
    const original = Element.prototype.scrollIntoView;
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    try {
      await click(button(tests, "TestRemoteRoutesCoverLocalAPI"));
      const row = scroll.mock.contexts[0] as HTMLElement;
      expect(row.closest(`[aria-label="${path} diff"]`)).not.toBeNull();
      expect(row.dataset.newLine).toBe("21");
    } finally {
      Element.prototype.scrollIntoView = original;
    }
  });

  it("folds a file's long list of tests to its first few", async () => {
    const document = review1303();
    const hunk = document.index.hunks.find((each) => each.id === "h_remote_test")!;
    hunk.diff!.lines = [
      ...hunk.diff!.lines,
      ...Array.from({ length: 10 }, (_, at) => [
        { kind: "add" as const, text: `func TestCase${at}(t *testing.T) {`, new_line: 21 + at * 2 },
        { kind: "add" as const, text: "}", new_line: 22 + at * 2 },
      ]).flat(),
    ];
    const path = "oss/internal/uiserver/remote_test.go";
    const container = await page("pane=section&section=sec_allowlist&section_view=tests", document);
    const rows = () =>
      viewPanel(container).querySelectorAll(`[aria-label="Tests in ${path}"] li`).length;
    expect(rows()).toBe(5);
    await click(button(viewPanel(container), "Show 5 more tests"));
    expect(rows()).toBe(10);
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
  const switcher = (container: HTMLElement) =>
    panel(container).querySelector<HTMLElement>('[role="group"][aria-label="Group files by"]')!;
  const filesNav = (container: HTMLElement) =>
    panel(container).querySelector<HTMLElement>('nav[aria-label="Changed files"]')!;
  const headings = (container: HTMLElement) =>
    [...filesNav(container).querySelectorAll<HTMLElement>("p")].map((each) => each.textContent);

  it("groups by type, folder or feature, from a switch above the list", async () => {
    const container = await page("pane=tile&tile=code");
    expect(
      [...switcher(container).querySelectorAll("button")].map((each) => [
        each.textContent,
        each.getAttribute("aria-pressed"),
      ]),
    ).toEqual([
      ["Type", "true"],
      ["Folder", "false"],
      ["Feature", "false"],
    ]);
    expect(headings(container)).toEqual(["Code", "Tests", "Docs"]);

    await click(button(switcher(container), "Folder"));
    expect(button(switcher(container), "Folder").getAttribute("aria-pressed")).toBe("true");
    const folders = headings(container);
    expect(folders.slice(0, 2)).toEqual(["oss", "internal"]);
    expect(folders).toContain("remote");
    expect(folders).not.toContain("Code");
    // Under a folder, files are listed by name alone.
    expect(filesNav(container).textContent).toContain("relay.go");
    expect(
      filesNav(container).querySelector('[title="oss/internal/remote/relay.go"]'),
    ).not.toBeNull();

    await click(button(switcher(container), "Feature"));
    const features = headings(container);
    expect(features.slice(0, 2)).toEqual(["Pairing", "Relay connection"]);
    expect(features.at(-1)).toBe("Dependencies");
    expect(features).not.toContain("Not in a section");
  });

  it("puts a file no section explains under Not in a section, last", async () => {
    const without: ReviewV2 = {
      ...REVIEW_1303,
      sections: REVIEW_1303.sections.map((each) =>
        each.id === "sec_deps" ? { ...each, hunks: [] } : each,
      ),
    };
    localStorage.setItem("baum.review.fileGrouping", "feature");
    const container = await page("pane=tile&tile=code", review1303(without));
    const features = headings(container);
    expect(features.at(-1)).toBe("Not in a section");
    expect(features).not.toContain("Dependencies");
    const loose = [...filesNav(container).querySelectorAll("li")].filter(
      (each) => each.title === "oss/go.sum",
    );
    expect(loose).toHaveLength(1);
  });

  it("lists a file under every section that explains it, marking each", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fremote%2Frelay.go",
    );
    await click(button(switcher(container), "Feature"));
    const rows = [...filesNav(container).querySelectorAll<HTMLElement>("li")].filter(
      (each) => each.title === "oss/internal/remote/relay.go",
    );
    expect(rows.length).toBeGreaterThanOrEqual(1);
    for (const row of rows)
      expect(row.querySelector("button")?.getAttribute("aria-pressed")).toBe("true");
  });

  it("remembers the grouping for the next page", async () => {
    const container = await page("pane=tile&tile=code");
    await click(button(switcher(container), "Folder"));
    expect(localStorage.getItem("baum.review.fileGrouping")).toBe("folder");
    act(() => root?.unmount());
    const again = await page("pane=tile&tile=code");
    expect(button(switcher(again), "Folder").getAttribute("aria-pressed")).toBe("true");
  });

  it("keeps working where storage is refused", async () => {
    const getItem = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    try {
      const container = await page("pane=tile&tile=code");
      expect(button(switcher(container), "Type").getAttribute("aria-pressed")).toBe("true");
      await click(button(switcher(container), "Folder"));
      expect(button(switcher(container), "Folder").getAttribute("aria-pressed")).toBe("true");
    } finally {
      getItem.mockRestore();
      setItem.mockRestore();
    }
  });

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

  it("names where a file is explained in its header, with nothing under its hunks", async () => {
    const container = await page(
      "pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go",
    );
    const file = panel(container).querySelector<HTMLElement>("#review-view-file")!;
    // The diff's header names the file once, with where it is explained.
    expect(file.querySelector("h2")).toBeNull();
    expect(file.querySelector("article header")?.textContent).toContain("Explained in");
    expect(file.textContent).not.toContain("Deny by default");
    expect(file.textContent).not.toContain("Always allow");
    await click(button(file, "What a phone can do"));
    expect([search.get("pane"), search.get("section")]).toEqual(["section", "sec_allowlist"]);
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

  it("lines a flow's steps up in columns and draws a branch under the steps after its own", async () => {
    const steps = ["Read", "Choose", "Export", "Run", "Save"].map((title) => ({ title }));
    const container = await card({
      ...tileOf("dg_pairing"),
      data: {
        source: [],
        lanes: [
          { label: "Capture", steps },
          { label: "No source", from: 2, steps: [{ title: "No clone" }, { title: "Skipped" }] },
          { label: "Late", from: 5, steps: [{ title: "Too late" }, { title: "Dropped" }] },
          { label: "Lost", from: 9, steps: [{ title: "Nowhere" }] },
        ],
      },
    });
    const flow = container.querySelector<HTMLElement>(".flow")!;
    expect(flow.style.getPropertyValue("--flow-columns")).toBe("5");
    // Without a measured width the flow is the list going down.
    expect([flow.dataset.across, flow.dataset.labels]).toEqual([undefined, undefined]);
    const columns = (lane: Element) =>
      [...lane.querySelectorAll<HTMLElement>("li")].map((li) =>
        li.style.getPropertyValue("--flow-column"),
      );
    const lanes = [...container.querySelectorAll("[data-lane]")];
    // The first lane from 1, a branch after step 2 from 3, a branch after the
    // last step moved back to fit, a branch from a step that is not there as
    // a lane of its own.
    expect(lanes.map(columns)).toEqual([["1", "2", "3", "4", "5"], ["3", "4"], ["4", "5"], ["1"]]);
    expect(lanes[1]?.textContent).toContain("After Choose");
    expect(lanes[1]?.querySelector('[data-join="branch"]')).not.toBeNull();
    expect(lanes[3]?.querySelector('[data-join="branch"]')).toBeNull();
  });

  it("gives a branch longer than the first lane a column for every step", async () => {
    const container = await card({
      ...tileOf("dg_pairing"),
      data: {
        source: [],
        lanes: [
          { label: "Newer", steps: [{ title: "Ask" }, { title: "Split" }, { title: "Keep" }] },
          {
            label: "Older",
            from: 1,
            steps: [
              { title: "Refused" },
              { title: "Ask again" },
              { title: "Split" },
              { title: "Keep" },
            ],
          },
        ],
      },
    });
    const flow = container.querySelector<HTMLElement>(".flow")!;
    expect(flow.style.getPropertyValue("--flow-columns")).toBe("4");
    const lanes = [...container.querySelectorAll("[data-lane]")].map((lane) =>
      [...lane.querySelectorAll<HTMLElement>("li")].map((li) =>
        li.style.getPropertyValue("--flow-column"),
      ),
    );
    expect(lanes).toEqual([
      ["1", "2", "3"],
      ["1", "2", "3", "4"],
    ]);
  });

  it("draws columns under their labels", async () => {
    const container = await card(tileOf("dg_allowlist"));
    const columns = [...container.querySelectorAll<HTMLElement>("section[aria-label]")];
    expect(columns.map((each) => each.getAttribute("aria-label"))).toEqual([
      "A phone can",
      "A phone can't",
    ]);
    expect(columns[1]?.querySelectorAll("li")).toHaveLength(3);
    expect(columns[1]?.querySelector("h4")?.textContent).toBe("A phone can't3 items");
  });

  it("marks the items columns share and points from how it was to how it is", async () => {
    const container = await render(
      <ColumnsDiagram
        data={{
          source: [],
          columns: [
            {
              label: "Before",
              tone: "muted",
              items: [{ title: "Retry" }, { title: "Opens a dialog" }],
            },
            {
              label: "After",
              tone: "good",
              items: [{ title: " retry" }, { title: "Plays in place" }],
            },
          ],
        }}
      />,
    );
    const same = [...container.querySelectorAll("li[data-same]")].map((li) => li.textContent);
    expect(same).toEqual(["Retry", " retry"]);
    expect(container.textContent).toContain("also in another column");
    expect(container.textContent).toContain("→");
  });

  it("lines compared rows up, marks the words that differ and folds the rest on request", async () => {
    const container = await render(
      <ColumnsDiagram
        data={{
          source: [],
          columns: [
            { label: "Still image", tone: "muted" },
            { label: "Clip", tone: "new" },
          ],
          rows: [
            [
              { title: "Failure says Image unavailable" },
              { title: "Failure says Video unavailable" },
            ],
            [{ title: "Retry takes focus" }, { title: "Retry takes focus" }],
            [null, { title: "Plays in place" }],
            [{ title: "Opens full size" }, null],
          ],
        }}
      />,
    );
    const rows = () => [...container.querySelectorAll<HTMLElement>("ol > li")];
    expect(rows()).toHaveLength(4);
    expect(rows().map((row) => row.dataset.same === "true")).toEqual([false, true, false, false]);
    const marks = [...rows()[0]!.querySelectorAll<HTMLElement>("[data-mark]")].map((mark) => [
      mark.dataset.mark,
      mark.textContent,
    ]);
    expect(marks).toEqual([
      ["removed", "Image"],
      ["added", "Video"],
    ]);
    expect(
      [...container.querySelectorAll("[data-change]")].map((cell) => cell.textContent?.slice(0, 7)),
    ).toEqual(["Changed", "NewPlay", "Removed"]);
    const toggle = container.querySelector<HTMLElement>('[role="switch"], input[type="checkbox"]')!;
    await act(async () => toggle.click());
    expect(rows()).toHaveLength(3);
    expect(container.textContent).not.toContain("Retry takes focus");
  });

  it("marks words only between texts mostly alike", () => {
    const marked = (was: string, now: string) =>
      wordDiff(was, now)
        .now.filter((piece) => piece.mark)
        .map((piece) => piece.tokens.join(""));
    expect(marked("Long table folded", "Long table unfolded")).toEqual(["unfolded"]);
    expect(marked("Retry reloads the image", "Retry reloads the clip now")).toEqual(["clip now"]);
    expect(marked("A dialog shows the image larger", "The clip plays in its own box")).toEqual([]);
  });

  it("draws compared text plain, with code, keeping identifiers and operators", async () => {
    const container = await render(
      <ColumnsDiagram
        data={{
          source: [],
          columns: [{ label: "Off" }, { label: "On" }],
          rows: [
            [{ title: "Reads *cached rows* fast" }, { title: "Reads *cached data* `fast`" }],
            [{ title: "limit 5 > max" }, { title: "limit 10 > max" }],
          ],
        }}
      />,
    );
    const cells = (row: number) =>
      [
        ...container
          .querySelectorAll("ol > li")
          [row]!.querySelectorAll("[data-change], [class*='rounded-md']"),
      ].map((cell) => cell.querySelector(".font-medium")?.textContent);
    expect(cells(0)).toEqual(["Reads cached rows fast", "Reads cached data fast"]);
    expect(container.querySelector("ins code")?.textContent).toBe("fast");
    expect(cells(1)).toEqual(["limit 5 > max", "limit 10 > max"]);
    expect(container.querySelector("em")).toBeNull();
  });

  it("takes italics out of compared text but not identifiers or globs", () => {
    expect(comparedText("a *cached* _hot_ row")).toBe("a cached hot row");
    expect(comparedText("__init__ snake_case *.go `*x*`")).toBe("__init__ snake_case *.go `*x*`");
    expect(comparedText("*a*, *b*. (_c_)")).toBe("a, b. (c)");
    for (const kept of [
      "src/*/foo/*.ts",
      "*.go,*.ts",
      "(*.go|*.ts)",
      "https://x.com/_next_/static",
      "a*b*c",
      "_x_y",
      "2 * 3 * 4",
    ])
      expect(comparedText(kept)).toBe(kept);
  });

  it("leaves the Changed pill out when every compared row differs", async () => {
    const container = await render(
      <ColumnsDiagram
        data={{
          source: [],
          columns: [{ label: "Off" }, { label: "On", tone: "new" }],
          rows: [
            [{ title: "Long table folded" }, { title: "Long table unfolded" }],
            [null, { title: "Rows fade" }],
          ],
        }}
      />,
    );
    expect(
      [...container.querySelectorAll("[data-change]")].map((cell) => cell.textContent),
    ).toEqual(["NewRows fade"]);
    expect(container.querySelector('[role="switch"]')).toBeNull();
  });

  it("draws no arrow or shared mark between unrelated lists", async () => {
    const container = await render(
      <ColumnsDiagram
        data={{
          source: [],
          columns: [
            { label: "Accepted", tone: "good", items: [{ title: "MP4" }] },
            { label: "Refused", tone: "bad", items: [{ title: "GIF" }] },
          ],
        }}
      />,
    );
    expect(container.querySelector("li[data-same]")).toBeNull();
    expect(container.textContent).not.toContain("→");
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
    // Numbered, wide and narrow, and a message to itself is a loop on its lifeline.
    expect(lists[0]?.querySelector("li")?.textContent).toContain("Phone to Daemon: 1");
    expect(lists[1]?.querySelector("li")?.textContent?.startsWith("1")).toBe(true);
    expect(container.querySelector("[data-self]")?.textContent).toContain("No keys");
    // A legend of the marks it uses: green, red, a check within one actor.
    const legend = container.querySelector('[aria-label="Legend"]')?.textContent ?? "";
    expect(legend).toContain("works or is allowed");
    expect(legend).toContain("fails or is refused");
    expect(legend).toContain("a check within one actor");
    expect(legend).not.toContain("a reply");
  });

  it("marks what a sequence's pull request added or changed, and can show only that", async () => {
    const tile = tileOf("dg_encryption");
    const data = tile.data as { steps: Record<string, unknown>[] };
    const steps = data.steps.map((step, at) =>
      at === 1
        ? { ...step, tone: undefined, change: "changed", was: "Plaintext and a token" }
        : at === 0
          ? step // tone "new": read as added
          : { ...step, tone: undefined },
    );
    const container = await card({ ...tile, data: { ...data, steps } });
    // Each message is drawn twice: wide and narrow.
    const rows = (change: string) => container.querySelectorAll(`li[data-change="${change}"]`);
    expect(rows("added")).toHaveLength(2);
    expect(rows("changed")).toHaveLength(2);
    expect(rows("changed")[0]?.querySelector("del")?.textContent).toBe("Plaintext and a token");
    // The pills explain themselves: the legend leaves them out.
    expect(container.textContent).not.toContain("by this pull request");
    const only = container.querySelector<HTMLInputElement>('input[role="switch"]');
    expect(only?.checked).toBe(false);
    await click(only as HTMLElement);
    expect(only?.checked).toBe(true);
    const quiet = container.querySelectorAll("li[data-faded]");
    // Three untouched messages, wide and narrow.
    expect(quiet).toHaveLength(6);
  });

  it("draws a sequence's reply dashed", async () => {
    const tile = tileOf("dg_encryption");
    const data = tile.data as { steps: { reply?: boolean }[] };
    const steps = data.steps.map((step, at) => (at === 4 ? { ...step, reply: true } : step));
    const container = await card({ ...tile, data: { ...data, steps } });
    const replies = container.querySelectorAll("[data-reply]");
    expect(replies).toHaveLength(1);
    expect(replies[0]?.textContent).toContain("Encrypted answers");
    expect(replies[0]?.querySelector(".border-dashed")).not.toBeNull();
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
    expect(container.querySelectorAll("tbody tr")).toHaveLength(8);
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

  it("draws a library tile named by its own type in its frame, and old exhibits as their kind", () => {
    const library = {
      ...tileOf("lib_api_change"),
      type: "api_change" as ViewTile["type"],
      definition: { name: "api_change" },
    };
    expect(drawnAs(library).type).toBe("custom");
    const exhibit = { ...tileOf("dg_relay_runs"), type: "v1_table" as ViewTile["type"] };
    expect(drawnAs(exhibit).type).toBe("table");
    expect(drawnAs(tileOf("dg_shape")).type).toBe("flow");
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
    // Set apart from the section's own text, on its wash behind a rule.
    expect(container.querySelector("[data-prose-callout]")?.textContent).toBe(
      "Phones can't reach a laptop behind NAT.",
    );
  });

  it("draws an added images tile from the images the index lists", async () => {
    const id = "att_0123456789abcdef0123456789abcdef";
    const review = {
      ...CHROME,
      index: {
        ...CHROME.index,
        attachments: [
          ...(CHROME.index.attachments ?? []),
          {
            id,
            position: 99,
            content_type: "image/png",
            width: 40,
            height: 20,
            alt: "The switch, on",
          },
        ],
      },
    } as ReviewDocument;
    const tile: ViewTile = {
      ...tileOf("dg_shape"),
      id: "added_images_0a1b2c3d4e",
      type: "images",
      title: "Review automatically",
      state: "ready",
      added: true,
      data: { items: [{ attachment: id, caption: "The switch, on" }] },
    };
    const container = await render(
      <Host>
        <AttachmentUrlProvider resolve={(each) => `api/attachments/${each}`}>
          <ViewTileCard tile={tile} review={review} ctx={ctx} />
        </AttachmentUrlProvider>
      </Host>,
    );
    expect(container.querySelector("h3")?.textContent).toBe("Review automatically");
    const image = container.querySelector("img");
    expect(image?.getAttribute("src")).toBe(`api/attachments/${id}`);
    expect(container.querySelector("figcaption")?.textContent).toBe("The switch, on");
    // The tile's own title heads it: the images add no heading of their own.
    expect(container.querySelectorAll("h3")).toHaveLength(1);
    expect(imageItems({ items: [{ attachment: 3 }] })).toBeNull();
  });
});

// ---------- tiles the person added, and those being made ----------

describe("the code tile", () => {
  const code = (data: Record<string, unknown>): ViewTile =>
    parseReviewV2({
      ...REVIEW_1303,
      tiles: [
        { id: "code_x", type: "code", owner: "sec_allowlist", title: "X", state: "ready", data },
      ],
    })!.tiles[0]!;
  const newSide = (first: number, last: number) =>
    CHROME.index.hunks
      .find((hunk) => hunk.id === "h_routes")!
      .diff!.lines.flatMap((line) =>
        line.kind !== "remove" && line.new_line >= first && line.new_line <= last
          ? [line.text]
          : [],
      )
      .join("\n");
  const rows = (container: HTMLElement) => [...container.querySelectorAll("tbody tr")].slice(1);

  it("draws its excerpt as the hunk's diff, with the old side, numbers and the lines that matter", async () => {
    const container = await card(
      code({
        hunk: "h_shutdown",
        lines: [995, 998],
        focus: [997],
        text: "\t\t\t// The phone…\n\t\t\t// providers…\n\t\t\tShutdown: func…\n\t\t\t\tphones.Close()",
      }),
    );
    expect(container.querySelector("header")?.textContent).toContain("oss/internal/cli/daemon.go");
    const drawn = rows(container);
    // The removed line the new ones replace, then the four lines.
    expect(drawn.map((row) => row.querySelectorAll("td")[2]?.textContent)).toEqual([
      "−",
      "+",
      "+",
      "+Highlighted line",
      "+",
    ]);
    // The header's ranges are the excerpt's.
    expect(container.querySelector("tbody tr")?.textContent).toBe("@@ -993,1 +995,4 @@");
    expect(drawn[0]?.textContent).toContain("Shutdown:     queue.Shutdown,");
    expect(drawn[3]?.querySelectorAll("td")[1]?.textContent).toBe("997");
    expect(drawn.filter((row) => row.hasAttribute("data-focus"))).toEqual([drawn[3]]);
    expect(drawn.filter((row) => row.classList.contains("opacity-35"))).toHaveLength(4);
  });

  it("folds to show every focus line that fits", () => {
    const lines = Array.from({ length: 33 }, (_, at) => ({
      kind: "add" as const,
      newLine: at + 1,
      text: "",
      focus: at === 19 || at === 30,
    }));
    const shown = foldedLines(lines)!;
    expect(shown.map((line) => line.newLine)).toEqual(
      Array.from({ length: 12 }, (_, at) => at + 20),
    );
    // Focus lines too far apart: the window keeps its lead-in to the first.
    const apart = lines.map((line, at) => ({ ...line, focus: at === 19 || at === 32 }));
    expect(foldedLines(apart)![0]?.newLine).toBe(18);
    expect(foldedLines(lines.slice(0, 14))).toBeNull();
  });

  it("dims nothing when no line is named", async () => {
    const container = await card(
      code({ hunk: "h_routes", lines: [61, 64], text: newSide(61, 64) }),
    );
    // Only additions: the header has no old range.
    expect(container.querySelector("tbody tr")?.textContent).toBe("@@ +61,4 @@");
    expect(rows(container)).toHaveLength(4);
    expect(container.querySelector("tbody tr.opacity-35")).toBeNull();
  });

  it("folds a long excerpt at the lines that matter, and opens it", async () => {
    const container = await card(
      code({ hunk: "h_routes", lines: [17, 49], focus: [45], text: newSide(17, 49) }),
    );
    expect(rows(container)).toHaveLength(12);
    expect(rows(container)[0]?.querySelectorAll("td")[1]?.textContent).toBe("38");
    const more = button(container, "Show all 33 lines");
    await click(more);
    expect(rows(container)).toHaveLength(33);
    await click(button(container, "Show fewer lines"));
    expect(rows(container)).toHaveLength(12);
  });

  it("opens its file in Files changed from the path", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    const tile = container.querySelector<HTMLElement>('[data-tile="code_remote_routes"]')!;
    await click(button(tile, "oss/internal/uiserver/remote.go"));
    expect(search.get("tile")).toBe("code");
    expect(search.get("tile_item")).toBe("oss/internal/uiserver/remote.go");
  });
});

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

// ---------- a baum.review/1 version ----------

const v1 = () => structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;

describe("a baum.review/1 version in the review shell", () => {
  it("keeps the rail, drawing the Summary and each section with the viewer's own pages", async () => {
    const document = v1();
    const container = await page("", document);
    const labels = railRows(container).map(rowLabel);
    expect(labels[0]).toBe("Overview");
    expect(labels).toContain("How it works");
    expect(labels).toContain("Files changed");
    // No "Where this fits" place: a v1 context is part of its Summary.
    expect(labels).not.toContain("Where this fits");
    // The v1 Summary.
    const summary = pane(container).textContent ?? "";
    expect(summary).toContain(document.summary.text.slice(0, 20));
    // A section's evidence, in the viewer's own section page.
    const section = document.sections[0]!;
    await click(
      railRows(container).find(
        (row) => rowLabel(row) === section.title && row !== railRows(container)[0],
      )!,
    );
    expect(search.get("section")).toBe(section.id);
    expect(pane(container).textContent).toContain(section.evidence[0]!.claim);
    // A section the agent review has a finding on shows it.
    const behavior = document.sections.find((each) => each.id === "behavior")!;
    await click(railRows(container).find((row) => rowLabel(row) === behavior.title)!);
    expect(search.get("section")).toBe("behavior");
    expect(pane(container).textContent).toContain("Guard the repository read");
    // Files changed is the app's own list and diffs.
    await click(railRows(container).find((row) => rowLabel(row) === "Files changed")!);
    expect(pane(container).querySelector('nav[aria-label="Changed files"]')).not.toBeNull();
    expect(pane(container).querySelector("#review-view-file")).not.toBeNull();
  });

  it("shows a baum.review/2 version's section body, not the viewer's section page", async () => {
    const container = await page("pane=section&section=sec_pairing", review1303());
    expect(pane(container).querySelector('[role="tablist"]')).not.toBeNull();
    expect(railRows(container).map(rowLabel)).toContain("Where this fits");
  });
});

// ---------- a section's body ----------

const withBody = (): ReviewDocument => {
  const review: ReviewV2 = {
    ...REVIEW_1303,
    sections: REVIEW_1303.sections.map((section) =>
      section.id === "sec_pairing"
        ? {
            ...section,
            gist: "A one-time code pairs up to 16 phones.",
            body: [
              { type: "prose", text: "Pairing is **short-lived**, a `NewOffer` per phone." },
              {
                type: "points",
                title: "Choices to agree with",
                items: [
                  {
                    text: "A code lasts **5 minutes**.",
                    more: "The secret expires, and at most 4 are live.",
                    cites: [{ type: "hunk", id: "h_pairing" }],
                  },
                  { text: "A plain line with nothing behind it.", cites: [] },
                ],
              },
              { type: "tile", tile: "dg_pairing" },
              { type: "note", text: "Aside about *nothing* else." },
              { type: "points", items: [{ text: "Untitled point.", cites: [] }] },
            ],
          }
        : section,
    ),
  };
  return review1303(review);
};

describe("a section's body", () => {
  const bodyOf = async () => {
    const container = await page("pane=section&section=sec_pairing", withBody());
    return panel(container).querySelector<HTMLElement>("[data-section-body]")!;
  };

  it("draws the blocks in order between the gist and the risks", async () => {
    const container = await page("pane=section&section=sec_pairing", withBody());
    const article = panel(container).querySelector<HTMLElement>("article")!;
    const body = article.querySelector<HTMLElement>("[data-section-body]")!;
    const blocks = [...body.children].map(
      (each) => (each as HTMLElement).dataset.block ?? (each as HTMLElement).dataset.tile,
    );
    expect(blocks).toEqual(["prose", "points", "dg_pairing", "note", "points"]);
    const text = article.textContent ?? "";
    const order = [
      "A one-time code pairs up to 16 phones.",
      "Pairing is short-lived",
      "Choices to agree with",
      "Aside about",
      "Risks",
    ].map((part) => text.indexOf(part));
    expect(order.every((at) => at >= 0)).toBe(true);
    expect(order).toEqual(order.toSorted((a, b) => a - b));
    // The keys stop at each block holding no block of its own: a points
    // block's rows rather than the list.
    const stops = [...body.querySelectorAll<HTMLElement>("[data-review-block]")].filter(
      (block) => !block.querySelector("[data-review-block]"),
    );
    const points = [...body.querySelectorAll('[data-block="points"] [data-point]')];
    expect(points.length).toBeGreaterThan(0);
    expect(stops).toHaveLength(3 + points.length);
    expect(points.every((point) => stops.includes(point as HTMLElement))).toBe(true);
  });

  it("draws prose and a note as inline markdown", async () => {
    const body = await bodyOf();
    expect(body.querySelector('[data-block="prose"] strong')?.textContent).toBe("short-lived");
    expect(body.querySelector('[data-block="prose"] code')?.textContent).toBe("NewOffer");
    expect(body.querySelector('[data-block="note"]')?.textContent).toBe(
      "Aside about nothing else.",
    );
  });

  it("labels a titled block and a tile bare, and leaves an untitled one without a label", async () => {
    const body = await bodyOf();
    const [titled, untitled] = [...body.querySelectorAll<HTMLElement>('[data-block="points"]')];
    expect(titled?.querySelector("h3")?.textContent).toBe("Choices to agree with");
    expect(untitled?.querySelector("h3")).toBeNull();
    const tile = body.querySelector<HTMLElement>('[role="figure"]')!;
    expect(tile.querySelector("h3")?.textContent).toBe("How a phone is paired");
    // No card: no border, background or padding of its own.
    expect(tile.className).not.toMatch(/\bborder\b|\bbg-surface-card\b|\bp-4\b/);
  });

  it("opens a point to its detail and the files it cites", async () => {
    const body = await bodyOf();
    const rows = [...body.querySelectorAll<HTMLElement>("[data-point]")];
    expect(rows).toHaveLength(3);
    const toggle = rows[0]!.querySelector("button")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(rows[0]!.textContent).not.toContain("The secret expires");
    await click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(rows[0]!.textContent).toContain("The secret expires, and at most 4 are live.");
    expect(rows[0]!.textContent).toContain("pairing");
    await click(toggle);
    expect(rows[0]!.textContent).not.toContain("The secret expires");
    // A point with nothing behind it is not a button.
    expect(rows[1]!.querySelector("button")).toBeNull();
  });

  it("leaves the points of a block without tones unmarked, and a note without a kind plain", async () => {
    const body = await bodyOf();
    expect(body.querySelectorAll("[data-point] [data-mark]")).toHaveLength(0);
    expect(body.querySelector("[data-point][data-tone]")).toBeNull();
    expect(body.querySelector("[data-kind]")).toBeNull();
  });

  it("marks each point of a block with tones, and draws a note's kind with its icon", async () => {
    const container = await page("pane=section&section=sec_encryption");
    const body = panel(container).querySelector<HTMLElement>("[data-section-body]")!;
    const rows = [...body.querySelectorAll<HTMLElement>("[data-point]")];
    expect(rows.map((row) => row.dataset.tone ?? "plain")).toEqual(["good", "bad", "muted"]);
    // Every line leads with a marker, and a toned one says what it means.
    for (const row of rows) expect(row.querySelectorAll("[data-mark]")).toHaveLength(1);
    expect(rows[1]!.querySelector(".sr-only")?.textContent).toBe("fails or is refused: ");
    const notes = [...body.querySelectorAll<HTMLElement>('[data-block="note"] [data-kind]')];
    expect(notes.map((note) => note.dataset.kind)).toEqual(["decision", "caveat", "follow_up"]);
    expect(notes[0]!.querySelector("svg")).not.toBeNull();
    expect(notes[0]!.textContent).toBe(
      "Decision. Noise IK rather than TLS to the relay, so the relay never holds a key.",
    );
  });

  it("draws a section's prose and points blocks", async () => {
    const container = await page("pane=section&section=sec_allowlist");
    const body = panel(container).querySelector<HTMLElement>("[data-section-body]")!;
    expect(
      [...body.querySelectorAll<HTMLElement>("[data-block]")].map((each) => each.dataset.block),
    ).toEqual(["prose", "points"]);
    expect(body.querySelector('[data-block="points"] strong')?.textContent).toBe(
      "Deny by default.",
    );
  });

  it("shows a table cell's old value struck through before the new one", async () => {
    const table = tileOf("dg_relay_runs");
    const data = table.data as { columns: string[]; rows: unknown[][] };
    const container = await card({
      ...table,
      data: {
        ...data,
        rows: [["run 1", { text: "5 minutes", was: "10 minutes" }, { text: "plain" }]],
      },
    });
    const cell = container.querySelector("tbody td [data-changed]")!;
    expect(cell.querySelector("del")?.textContent).toContain("10 minutes");
    expect(cell.querySelector("strong")?.textContent).toContain("5 minutes");
    expect(container.querySelectorAll("tbody td")[2]?.querySelector("del")).toBeNull();
  });

  it("marks a table's changed rows with a pill and can show only them", async () => {
    const table = tileOf("dg_relay_runs");
    const container = await card({
      ...table,
      data: {
        ...(table.data as object),
        rows: [
          ["run 1", { text: "5 minutes", was: "10 minutes" }, "ok"],
          ["run 2", "1 minute", "ok"],
          ["run 3", "2 minutes", "ok"],
        ],
      },
    });
    const rows = [...container.querySelectorAll<HTMLElement>("tbody tr")];
    expect(rows[0]?.textContent).toContain("Changed");
    expect(rows[1]?.textContent).not.toContain("Changed");
    const only = container.querySelector<HTMLInputElement>('input[role="switch"]');
    expect(only?.checked).toBe(false);
    await click(only as HTMLElement);
    expect(only?.checked).toBe(true);
    expect(container.querySelectorAll("tbody tr.opacity-35")).toHaveLength(2);
  });

  it("unfolds a long table when it shows only changes, so a changed row below the fold shows", async () => {
    const table = tileOf("dg_relay_runs");
    const rows: unknown[][] = Array.from({ length: 12 }, (_, at) => [`run ${at}`, "relay", "ok"]);
    rows[10] = ["run 10", { text: "moved", was: "relay" }, "ok"];
    const container = await card({ ...table, data: { ...(table.data as object), rows } });
    expect(container.querySelectorAll("tbody tr")).toHaveLength(8);
    await click(container.querySelector<HTMLElement>('input[role="switch"]')!);
    expect(container.querySelectorAll("tbody tr")).toHaveLength(12);
    expect(container.querySelectorAll("tbody tr:not(.opacity-35)")).toHaveLength(1);
  });

  it("marks a changed row in a one-column table", async () => {
    const table = tileOf("dg_relay_runs");
    const container = await card({
      ...table,
      data: { columns: ["Step"], rows: [[{ text: "Pair", was: "Scan" }], ["Offer"]], source: [] },
    });
    expect(container.querySelectorAll("tbody tr")[0]?.textContent).toContain("Changed");
    const numbers = await card({
      ...table,
      data: { columns: ["Limit"], rows: [[{ text: "10", was: "5" }], ["20"]], source: [] },
    });
    expect(numbers.querySelectorAll("tbody tr")[0]?.textContent).toContain("Changed");
  });

  it("keeps a number column right-aligned when a cell holds a dash", async () => {
    const table = tileOf("dg_relay_runs");
    const container = await card({
      ...table,
      data: {
        ...(table.data as object),
        rows: [
          ["a", "250 milliseconds", "x"],
          ["b", "—", "y"],
          ["c", "1,000 req/s", "z"],
        ],
      },
    });
    const cells = [...container.querySelectorAll<HTMLElement>("tbody tr")[0]!.children];
    expect(cells[1]?.className).toContain("text-right");
  });

  it("right-aligns a table's number columns and tints its repeated values", async () => {
    const table = tileOf("dg_relay_runs");
    const container = await card({
      ...table,
      data: {
        ...(table.data as object),
        rows: [
          ["phone", "65,535 bytes", "Closed"],
          ["daemon", "4 KiB", "Closed"],
          ["hello", "10 seconds", "Rejected"],
          ["budget", "$2.00", "Closed"],
        ],
      },
    });
    const cells = [...container.querySelectorAll<HTMLElement>("tbody tr")[0]!.children];
    expect(cells[1]?.className).toContain("text-right");
    expect(cells[1]?.className).toContain("tabular-nums");
    const tints = [...container.querySelectorAll<HTMLElement>(".table-tint")];
    expect(tints.map((each) => each.dataset.tint)).toEqual(["0", "0", "1", "0"]);
  });

  it("makes a flow's steps with a detail clickable and shows the clicked step's detail", async () => {
    const flow = tileOf("dg_pairing");
    const container = await card({
      ...flow,
      data: {
        lanes: [
          {
            steps: [
              { title: "Offer", more: "A secret is made." },
              { title: "Scan" },
              { title: "Pair", more: "The phone is stored by its key." },
            ],
          },
        ],
        source: [],
      },
    });
    const boxes = [...container.querySelectorAll<HTMLButtonElement>(".flow button")];
    expect(boxes.map((each) => each.textContent)).toEqual(["Offer", "Pair"]);
    expect(container.querySelectorAll(".flow li")[1]?.querySelector("button")).toBeNull();
    expect(boxes.map((each) => each.getAttribute("aria-pressed"))).toEqual(["false", "false"]);
    const more = container.querySelector<HTMLElement>("[data-flow-more]")!;
    expect(more.textContent).toContain("Click a step");
    await click(boxes[1]!);
    expect(boxes[1]!.getAttribute("aria-pressed")).toBe("true");
    expect(more.textContent).toContain("Pair");
    expect(more.textContent).toContain("The phone is stored by its key.");
    await click(boxes[0]!);
    expect(boxes.map((each) => each.getAttribute("aria-pressed"))).toEqual(["true", "false"]);
    expect(more.textContent).toContain("A secret is made.");
    await click(boxes[0]!);
    expect(more.textContent).not.toContain("A secret is made.");
  });
  it("marks a flow's new steps with a pill and can show only them", async () => {
    const flow = tileOf("dg_pairing");
    const container = await card({
      ...flow,
      data: {
        lanes: [
          {
            steps: [
              { title: "Offer", tone: "new" },
              { title: "Scan", tone: "good" },
              { title: "Pair" },
            ],
          },
        ],
        source: [],
      },
    });
    const steps = [...container.querySelectorAll<HTMLElement>(".flow li")];
    expect(steps[0]?.textContent).toContain("New");
    expect(steps[1]?.textContent).not.toContain("New");
    const legend = container.querySelector('[aria-label="Legend"]')?.textContent ?? "";
    expect(legend).toContain("works or is allowed");
    expect(legend).not.toContain("by this pull request");
    const only = container.querySelector<HTMLInputElement>('input[role="switch"]');
    expect(only?.checked).toBe(false);
    await click(only as HTMLElement);
    expect(only?.checked).toBe(true);
    expect(container.querySelectorAll(".flow li[data-faded]")).toHaveLength(2);
  });
});

describe("j and k on Files changed", () => {
  const nav = (container: HTMLElement) =>
    pane(container).querySelector<HTMLElement>('nav[aria-label="Changed files"]')!;
  /** The files in the order the list shows them, a file under several
   *  headings at its first. */
  const listed = (container: HTMLElement) => [
    ...new Set(
      [...nav(container).querySelectorAll<HTMLElement>("li[title]")].map(
        (row) => row.getAttribute("title")!,
      ),
    ),
  ];
  const openFile = (container: HTMLElement) =>
    pane(container).querySelector("#review-view-file")?.getAttribute("aria-label");
  const key = (direction: "next" | "previous") =>
    act(async () => {
      steps.current?.item?.[direction]?.go();
    });

  it.each(["Type", "Folder", "Feature"])(
    "steps through the files in %s order and stops at the ends",
    async (mode) => {
      const container = await page("pane=tile&tile=code");
      await click(button(nav(container).querySelector<HTMLElement>('[role="group"]')!, mode));
      // Generated files are in the order too, and open with their group.
      const generated = [...nav(container).querySelectorAll<HTMLElement>("button")].find((each) =>
        each.textContent?.startsWith("Generated"),
      );
      if (generated) await click(generated);
      const order = listed(container);
      expect(order.length).toBeGreaterThan(3);
      // Open the first file as a click on its row does.
      await click(nav(container).querySelector<HTMLElement>(`li[title="${order[0]}"] button`)!);
      expect(openFile(container)).toBe(order[0]);
      await key("previous");
      expect(openFile(container)).toBe(order[0]);
      for (const path of order.slice(1)) {
        await key("next");
        expect(openFile(container)).toBe(path);
        // Its row is marked.
        expect(
          nav(container).querySelector(`li[title="${path}"] button`)?.getAttribute("aria-pressed"),
        ).toBe("true");
      }
      await key("next");
      expect(openFile(container)).toBe(order.at(-1));
      await key("previous");
      expect(openFile(container)).toBe(order.at(-2));
    },
  );

  it("follows a grouping switched on the page", async () => {
    const container = await page("pane=tile&tile=code");
    const type = listed(container);
    await click(nav(container).querySelector<HTMLElement>(`li[title="${type[0]}"] button`)!);
    await click(button(nav(container).querySelector<HTMLElement>('[role="group"]')!, "Folder"));
    const folder = listed(container);
    expect(folder).not.toEqual(type);
    await click(nav(container).querySelector<HTMLElement>(`li[title="${folder[1]}"] button`)!);
    await key("next");
    expect(openFile(container)).toBe(folder[2]);
  });

  it("keeps marking blocks on a section", async () => {
    await page("pane=section&section=sec_pairing");
    expect(steps.current?.item?.next?.label).toBe("Next item");
    expect(steps.current?.item?.previous?.label).toBe("Previous item");
  });
});
