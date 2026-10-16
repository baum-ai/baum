// @vitest-environment jsdom
import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import { type SearchUpdate, UrlStateProvider } from "../navigation";
import type { LoadReviewVersion, ReviewDocument } from "../review-document";
import { ReviewViewer } from "./review-viewer";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  // jsdom lays nothing out and observes nothing; the viewer only needs the
  // observers to exist.
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
  // Nor does it match media: every query reads as false, a narrow window.
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

/** The same review one version earlier, before the double-ack fix was added. */
function earlier(): ReviewDocument {
  const document = structuredClone(current);
  document.review.version = 2;
  document.review.previous_version = 1;
  document.sections = document.sections.filter((section) => section.id !== "sec_ackfix");
  return document;
}

/** The URL state a host keeps in its router, held here in component state. */
function Host({
  initial = "",
  onUpdate,
  children,
}: {
  initial?: string;
  onUpdate?: () => void;
  children: ReactNode;
}) {
  const [search, setSearch] = useState(() => new URLSearchParams(initial));
  const update = (change: SearchUpdate) => {
    onUpdate?.();
    setSearch((value) => change(value));
  };
  return <UrlStateProvider value={{ search, update }}>{children}</UrlStateProvider>;
}

async function render(node: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root!.render(node));
  return container;
}

function rows(container: HTMLElement, list: string) {
  return [...container.querySelectorAll(`#${list} button`)].map((row) => row.textContent);
}

/** A rail row's text without the review score badge that follows its title. */
function titles(labels: (string | null)[]) {
  return labels.map((label) => label?.replace(/\d+Review score.*$/, ""));
}

describe("ReviewViewer without host slots", () => {
  it("renders the example document on its own", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );

    expect(container.querySelector("h1")?.textContent).toBe(
      "#212 Add retry budgets and a dead-letter queue",
    );
    expect(container.textContent).toContain("Review v3 · Current");
    expect(container.querySelector("header")?.textContent).not.toContain("Review v3");
    const category = container.querySelector("h1")?.nextElementSibling;
    expect(category?.textContent).toBe("Feature");
    expect(container.querySelector("#review-section")?.textContent).not.toContain("Feature");
    // The Summary opens by default and leads with the review's own summary.
    expect(container.querySelector("#review-section")?.getAttribute("aria-label")).toBe("Summary");
    expect(container.querySelector("#review-section")?.textContent).toContain(
      current.summary.text.slice(0, 60),
    );
    // Sections carry review scores, so the rail lists them from the highest
    // score down, with the section that has no score (Obsolete) last.
    expect(titles(rows(container, "review-changes"))).toEqual([
      "Operator-only dead-letter endpoints",
      "Retry policy and backoff",
      "Dead-letter store and migration",
      "Second ack is ignored",
      "Docs and dependency bump",
      "Mechanical changes",
      "Fixed retry delay",
    ]);
    // The rail lists the critical and high risks, each named with its section.
    const risks = rows(container, "review-risks");
    expect(risks.length).toBeGreaterThan(0);
    expect(risks.some((label) => label?.includes("Operator-only dead-letter endpoints"))).toBe(
      true,
    );
    // Without a resolver nothing is drawn for an attachment.
    expect(container.querySelectorAll("img")).toHaveLength(0);
    // One version only: no version menu.
    expect(container.querySelector('[aria-label="Review version"]')).toBeNull();
    // Every hosted feature is absent rather than empty or failed.
    const groups = [
      ...container.querySelectorAll('nav[aria-label="Review sections"] button[aria-expanded]'),
    ].map((row) => row.textContent);
    expect(groups).toEqual(["Changes", "Critical risks"]);
    const tabs = [...container.querySelectorAll('[role="tab"]')].map((tab) => tab.textContent);
    expect(tabs).toEqual(["Details", "Activity"]);
    // Without a conversation nothing is named after one.
    expect(container.querySelector('section[aria-label="Side pane"]')).not.toBeNull();
    expect(container.textContent).not.toContain("General");
    const named = [...container.querySelectorAll("[aria-label]")].map((element) =>
      element.getAttribute("aria-label"),
    );
    expect([container.textContent, ...named].join(" ")).not.toMatch(/[Cc]onversation/);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("keeps the review's own order for a document without review scores", async () => {
    const unscored = structuredClone(current);
    for (const section of unscored.sections) delete section.derived.review_score;
    const container = await render(
      <Host>
        <ReviewViewer document={unscored} />
      </Host>,
    );
    expect(rows(container, "review-changes")).toEqual([
      "Retry policy and backoff",
      "Fixed retry delay",
      "Operator-only dead-letter endpoints",
      "Dead-letter store and migration",
      "Docs and dependency bump",
      "Mechanical changes",
      "Second ack is ignored",
    ]);
  });

  it("opens a section from the rail", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    const row = [...container.querySelectorAll("#review-changes button")].find((button) =>
      button.textContent?.startsWith("Operator-only dead-letter endpoints"),
    ) as HTMLButtonElement;
    await act(async () => row.click());
    expect(container.querySelector("#review-section")?.getAttribute("aria-label")).toBe(
      "Operator-only dead-letter endpoints",
    );
  });

  it("steps through the Summary and then each section in the rail's order", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    // Each row's title, without the score badge beside it.
    const rail = [...container.querySelectorAll("#review-changes button[aria-pressed]")].map(
      (button) => button.querySelector("[title]")?.getAttribute("title"),
    );
    const step = (name: "Previous" | "Next") =>
      container.querySelector<HTMLButtonElement>(`button[aria-label^="${name}"]`)!;
    const paneName = () => container.querySelector("#review-section")?.getAttribute("aria-label");
    const sheet = container.querySelector("#review-rail")!;
    const opener = container.querySelector<HTMLButtonElement>('[aria-controls="review-rail"]')!;

    expect(paneName()).toBe("Summary");
    expect(step("Previous").disabled).toBe(true);
    expect(step("Next").getAttribute("aria-label")).toBe(`Next: ${rail[0]}`);
    await act(async () => step("Next").click());
    expect(paneName()).toBe(rail[0]);
    expect(opener.textContent).toContain(`1 of ${rail.length}`);
    // The pane's own foot offers the same step on.
    const foot = container.querySelector('nav[aria-label="Next in review"]')!;
    expect(foot.textContent).toContain(rail[1]);
    await act(async () => step("Next").click());
    expect(paneName()).toBe(rail[1]);
    await act(async () => step("Previous").click());
    expect(paneName()).toBe(rail[0]);

    // The sheet closes once a place is picked from it, and on Escape.
    await act(async () => opener.click());
    expect(sheet.className).toContain("block");
    expect(opener.getAttribute("aria-expanded")).toBe("true");
    const row = [...sheet.querySelectorAll("#review-changes button")].find(
      (button) => button.querySelector("[title]")?.getAttribute("title") === rail[2],
    ) as HTMLButtonElement;
    await act(async () => row.click());
    expect(paneName()).toBe(rail[2]);
    expect(opener.getAttribute("aria-expanded")).toBe("false");
    await act(async () => opener.click());
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(opener.getAttribute("aria-expanded")).toBe("false");

    // The side pane is a sheet of its own, opened from the same bar, and only
    // one sheet is open at a time.
    const aside = container.querySelector("#review-aside")!;
    const asideOpener = container.querySelector<HTMLButtonElement>(
      '[aria-controls="review-aside"]',
    )!;
    // The example's CI is failing and a thread is open, which the button
    // flags from the bar.
    expect(asideOpener.getAttribute("aria-label")).toBe(
      "Details: CI checks failing, review threads unresolved",
    );
    expect(aside.className).toMatch(/(^| )hidden( |$)/);
    await act(async () => asideOpener.click());
    expect(aside.className).not.toMatch(/(^| )hidden( |$)/);
    expect(asideOpener.getAttribute("aria-expanded")).toBe("true");
    await act(async () => opener.click());
    expect(asideOpener.getAttribute("aria-expanded")).toBe("false");
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    // Going somewhere else closes it, as a pick from the rail does.
    await act(async () => asideOpener.click());
    await act(async () => step("Next").click());
    expect(asideOpener.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("ReviewViewer with an earlier version", () => {
  it("compares the current version with one loaded through loadVersion", async () => {
    const loadVersion = vi.fn<LoadReviewVersion>(async () => ({
      status: "ready",
      document: earlier(),
    }));
    const shown = vi.fn<(versions: number[]) => void>();
    const container = await render(
      <Host>
        <ReviewViewer
          document={current}
          versions={[2]}
          loadVersion={loadVersion}
          onVersionsShown={shown}
        />
      </Host>,
    );
    expect(container.querySelector('[aria-label="Review version"]')?.textContent).toContain(
      "Review v3 · Current",
    );
    expect(shown).toHaveBeenLastCalledWith([3]);

    const compare = container.querySelector('[aria-label="Compare v2"]') as HTMLInputElement;
    await act(async () => compare.click());

    expect(loadVersion).toHaveBeenCalledWith(2);
    expect(container.textContent).toContain("Comparing current v3 with v2");
    expect(container.textContent).toContain("1 new section");
    expect(container.textContent).toContain("Historical · Review v2");
    // The section only the current version has is marked new in the rail.
    const added = [...container.querySelectorAll("#review-changes button")].find((button) =>
      button.textContent?.startsWith("Second ack is ignored"),
    );
    expect(added?.textContent).toContain("New");
    expect(shown).toHaveBeenLastCalledWith([3, 2]);
  });

  it("reads an earlier version alone from the URL", async () => {
    const container = await render(
      <Host initial="review=2">
        <ReviewViewer
          document={current}
          versions={[2]}
          loadVersion={async () => ({ status: "ready", document: earlier() })}
        />
      </Host>,
    );
    expect(container.textContent).toContain("Viewing historical review v2 · Read only");
    // The rail follows the version being read, which has no double-ack fix.
    expect(rows(container, "review-changes")).not.toContain("Second ack is ignored");
  });
});

describe("ReviewViewer with host slots", () => {
  it("draws what the host supplies beside the document", async () => {
    const container = await render(
      <Host initial="pane=section&section=sec_api">
        <ReviewViewer
          document={current}
          header={{ approval: <span>Approved</span>, statistics: <p>12 files</p> }}
          checks={{
            renderSection: ({ id }) => <input type="checkbox" aria-label={`Read ${id}`} />,
            renderRisk: () => null,
            sectionRead: (id) => id === "sec_retry",
            riskRead: () => false,
          }}
          renderSectionAside={(id) => <p>Approvers of {id}</p>}
          files={{
            files: [{ id: "f1", path: "a.go", viewed: true }],
            loading: false,
            failed: false,
            retry: () => {},
            renderTree: ({ files }) => <p>{files.length} file</p>,
            renderFiles: ({ files }) => <p>Diffs of {files.length} file</p>,
          }}
          sessions={{ state: { status: "loading" }, renderPane: () => null }}
          conversation={(place) => <p>Discussing {place.viewedSectionId}</p>}
        />
      </Host>,
    );
    expect(container.textContent).toContain("Approved");
    expect(container.textContent).toContain("12 files");
    expect(container.textContent).toContain("Approvers of sec_api");
    expect(container.textContent).toContain("Discussing sec_api");
    expect(container.querySelector('[aria-label="Read sec_ackfix"]')).not.toBeNull();
    expect(container.querySelector('[aria-label="Changes, 1 of 6 read"]')).not.toBeNull();
    const groups = [
      ...container.querySelectorAll('nav[aria-label="Review sections"] button[aria-expanded]'),
    ].map((row) => row.textContent);
    // Files writes its total beside its read progress; Changes leaves it to
    // the progress alone.
    expect(groups).toEqual(["Changes", "Critical risks", "Files changed1", "Sessions"]);
    const tabs = [...container.querySelectorAll('[role="tab"]')].map((tab) => tab.textContent);
    expect(tabs).toEqual(["Chat", "Details", "Activity"]);
    expect(container.querySelector('section[aria-label="Conversation"]')).not.toBeNull();
    expect(container.textContent).toContain("Summary / General");
  });
});

/** Labels naming the version and section they were drawn for. */
const labels = (section: { id: string }, document: ReviewDocument) => (
  <a href={`/labels/${section.id}`}>
    v{document.review.version} {section.id}
  </a>
);

const head = (container: HTMLElement) => container.querySelector("header")?.textContent ?? "";

describe("ReviewViewer's review time estimate", () => {
  const withMinutes = (review_minutes: number | null | undefined) =>
    ({ ...current, summary: { ...current.summary, review_minutes } }) as ReviewDocument;

  it("states the estimate in the head when the document has one", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={withMinutes(7)} header={{ pullRequest: { repository: "a/b" } }} />
      </Host>,
    );
    expect(head(container)).toContain("~7 min to review");
    expect(
      container.querySelector(
        'header [title="Estimated time to read this review: about 7 minutes"]',
      ),
    ).not.toBeNull();
  });

  it("speaks of one minute in the singular", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={withMinutes(1)} />
      </Host>,
    );
    expect(head(container)).toContain("~1 min to review");
    expect(
      container.querySelector(
        'header [title="Estimated time to read this review: about 1 minute"]',
      ),
    ).not.toBeNull();
  });

  it("says nothing when the estimate is null, absent or not a whole number from 1 to 240", async () => {
    for (const minutes of [
      null,
      undefined,
      0,
      0.4,
      1.4,
      240.4,
      -3,
      241,
      5000,
      Number.NaN,
      Infinity,
    ]) {
      const container = await render(
        <Host>
          <ReviewViewer document={withMinutes(minutes)} />
        </Host>,
      );
      expect(head(container)).not.toContain("min to review");
    }
  });

  it("leaves it out of a review headed for something other than one pull request", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={withMinutes(7)} header={{ heading: "Split the importer" }} />
      </Host>,
    );
    expect(head(container)).not.toContain("min to review");
  });
});

describe("ReviewViewer for a review of more than one pull request", () => {
  it("titles the head with the host's heading and none of the document's pull request", async () => {
    const container = await render(
      <Host>
        <ReviewViewer
          document={current}
          header={{ heading: "Split the importer", pullRequest: { repository: "acme/widgets" } }}
        />
      </Host>,
    );
    expect(container.querySelector("h1")?.textContent).toBe("Split the importer");
    // The head only: a quoted prompt's "#2" and a "12:40 PM" read as "#212".
    expect(container.querySelector("header")?.textContent).not.toContain("#212");
    expect(container.querySelector('a[aria-label="View on GitHub"]')).toBeNull();
    expect(container.textContent).toContain("Review v3 · Current");
  });

  it("draws the host's section labels in the section and the Changes overview, on any version", async () => {
    const section = await render(
      <Host initial="pane=section&section=sec_api">
        <ReviewViewer document={current} renderSectionLabels={labels} />
      </Host>,
    );
    expect(section.querySelector("#review-section a[href='/labels/sec_api']")?.textContent).toBe(
      "v3 sec_api",
    );
    act(() => root?.unmount());
    root = null;

    const changes = await render(
      <Host initial="pane=changes&review=2">
        <ReviewViewer
          document={current}
          versions={[2]}
          loadVersion={async () => ({ status: "ready", document: earlier() })}
          renderSectionLabels={labels}
        />
      </Host>,
    );
    const links = [...changes.querySelectorAll("#review-section a[href^='/labels/']")];
    expect(links.length).toBe(earlier().sections.length);
    expect(links.every((link) => link.textContent?.startsWith("v2 "))).toBe(true);
  });
});

const hostChecks = (document: ReviewDocument) => ({
  content: <p>Host checks for v{document.review.version}</p>,
  failing: false,
});

describe("ReviewViewer's side pane for a host", () => {
  it("leaves out the Activity tab, and a stored Activity choice opens Details", async () => {
    localStorage.setItem("baum.review.conversation-tab", "activity");
    const container = await render(
      <Host>
        <ReviewViewer document={current} hideActivity />
      </Host>,
    );
    expect(container.querySelector("#conversation-tab-activity")).toBeNull();
    expect(container.querySelector("#conversation-panel-activity")).toBeNull();
    expect(
      container.querySelector("#conversation-tab-details")?.getAttribute("aria-selected"),
    ).toBe("true");
    act(() => root?.unmount());
    root = null;

    const single = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    expect(single.querySelector("#conversation-tab-activity")).not.toBeNull();
  });

  it("draws the host's checks at the head of Activity, or none, in place of the document's", async () => {
    localStorage.setItem("baum.review.conversation-tab", "activity");
    const container = await render(
      <Host>
        <ReviewViewer document={current} detailsChecks={hostChecks} />
      </Host>,
    );
    const activity = container.querySelector("#conversation-panel-activity");
    expect(activity?.textContent).toContain("Host checks for v3");
    expect(activity?.textContent).not.toContain("on this head");
    act(() => root?.unmount());
    root = null;

    // Without the Activity tab they lead Details instead.
    localStorage.setItem("baum.review.conversation-tab", "details");
    const stacked = await render(
      <Host>
        <ReviewViewer document={current} detailsChecks={hostChecks} hideActivity />
      </Host>,
    );
    expect(stacked.querySelector("#conversation-panel-details")?.textContent).toContain(
      "Host checks for v3",
    );
    act(() => root?.unmount());
    root = null;

    const hidden = await render(
      <Host>
        <ReviewViewer
          document={current}
          detailsChecks={() => ({ content: null, failing: false })}
          hideActivity
        />
      </Host>,
    );
    const part = [...hidden.querySelectorAll("#conversation-panel-details *")].some(
      (element) => element.children.length === 0 && element.textContent === "Checks",
    );
    expect(part).toBe(false);
    expect(hidden.querySelector("#conversation-panel-details")?.textContent).toContain("Author");
  });
});

describe("ReviewViewer and its host's renders", () => {
  it("reads nothing again when the host passes a new loader and version list", async () => {
    const calls: number[] = [];
    const view = () => (
      <Host>
        <ReviewViewer
          document={current}
          versions={[2]}
          loadVersion={async (version) => {
            calls.push(version);
            return { status: "ready", document: earlier() };
          }}
        />
      </Host>
    );
    const container = await render(view());
    const compare = container.querySelector('[aria-label="Compare v2"]') as HTMLInputElement;
    await act(async () => compare.click());
    expect(container.textContent).toContain("Historical · Review v2 · Generated against");
    const before = calls.length;

    await act(async () => root!.render(view()));

    expect(calls).toHaveLength(before);
    expect(container.textContent).not.toContain("Loading review v2");
    expect(container.textContent).toContain("Historical · Review v2 · Generated against");
  });

  it("shows the Summary for a group the host has not supplied, without writing the URL", async () => {
    const update = vi.fn();
    const container = await render(
      <Host initial="pane=sessions" onUpdate={update}>
        <ReviewViewer document={current} />
      </Host>,
    );
    expect(container.querySelector("#review-section")?.getAttribute("aria-label")).toBe("Summary");
    expect(container.querySelector("#review-section")?.textContent).toContain(
      current.summary.text.slice(0, 60),
    );
    expect(update).not.toHaveBeenCalled();
  });
});

describe("ReviewViewer's pane dividers", () => {
  it("previews a drag on the grid and chooses the width once, when the pointer is released", async () => {
    Element.prototype.setPointerCapture ??= () => {};
    Element.prototype.releasePointerCapture ??= () => {};
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    const divider = container.querySelector<HTMLElement>(
      '[role="separator"][aria-label="Resize the side pane"]',
    )!;
    const grid = container.querySelector<HTMLElement>("[style*='--conversation-width']")!;
    const stored = vi.spyOn(Storage.prototype, "setItem");
    const pointer = (type: string, clientX = 0) =>
      act(async () => {
        divider.dispatchEvent(new MouseEvent(type, { bubbles: true, clientX }));
      });
    await pointer("pointerdown");
    // jsdom lays nothing out, so the pane's right edge is 0 and a width is
    // the pointer's distance to the left of it.
    await pointer("pointermove", -420);
    await pointer("pointermove", -450);
    await pointer("pointermove", -500);
    expect(grid.style.getPropertyValue("--conversation-width")).toBe("500px");
    expect(stored).not.toHaveBeenCalled();
    await pointer("pointerup");
    expect(stored.mock.calls.filter(([key]) => key === "baum.review.conversation-width")).toEqual([
      ["baum.review.conversation-width", "500"],
    ]);
    expect(grid.style.getPropertyValue("--conversation-width")).toBe("500px");
    stored.mockRestore();
  });

  it("keeps the grid and the divider's value in step when a drag ends without a pointerup or at the starting width", async () => {
    Element.prototype.setPointerCapture ??= () => {};
    Element.prototype.releasePointerCapture ??= () => {};
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    const divider = container.querySelector<HTMLElement>(
      '[role="separator"][aria-label="Resize the side pane"]',
    )!;
    const grid = container.querySelector<HTMLElement>("[style*='--conversation-width']")!;
    const pointer = (type: string, clientX = 0) =>
      act(async () => {
        divider.dispatchEvent(new MouseEvent(type, { bubbles: true, clientX }));
      });
    const inStep = () =>
      expect(grid.style.getPropertyValue("--conversation-width")).toBe(
        `${divider.getAttribute("aria-valuenow")}px`,
      );
    const start = Number(divider.getAttribute("aria-valuenow"));

    // Lost capture commits what the drag showed.
    await pointer("pointerdown");
    await pointer("pointermove", -500);
    await pointer("lostpointercapture");
    expect(divider.getAttribute("aria-valuenow")).toBe("500");
    inStep();

    // A drag back to the width it started from settles on that width.
    await pointer("pointerdown");
    await pointer("pointermove", -300);
    await pointer("pointermove", -start);
    await pointer("pointerup");
    expect(divider.getAttribute("aria-valuenow")).toBe(String(start));
    inStep();

    // A cancelled drag shows the chosen width again.
    await pointer("pointerdown");
    await pointer("pointermove", -600);
    await pointer("pointercancel");
    expect(divider.getAttribute("aria-valuenow")).toBe(String(start));
    inStep();
  });
});
