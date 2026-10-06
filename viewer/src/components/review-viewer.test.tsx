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

/** The same review one version earlier, before the commit picker was added. */
function earlier(): ReviewDocument {
  const document = structuredClone(current);
  document.review.version = 2;
  document.review.previous_version = 1;
  document.sections = document.sections.filter((section) => section.id !== "sec_picker");
  return document;
}

/** The URL state a host keeps in its router, held here in component state. */
function Host({ initial = "", children }: { initial?: string; children: ReactNode }) {
  const [search, setSearch] = useState(() => new URLSearchParams(initial));
  const update = (change: SearchUpdate) => setSearch((value) => change(value));
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

describe("ReviewViewer without host slots", () => {
  it("renders the example document on its own", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );

    expect(container.querySelector("h1")?.textContent).toBe(
      "#191 Add commit-side session review and atomic corrections",
    );
    expect(container.textContent).toContain("Review v3 · Current");
    // The Summary opens by default and leads with the review's own summary.
    expect(container.querySelector("#review-section")?.getAttribute("aria-label")).toBe("Summary");
    expect(container.querySelector("#review-section")?.textContent).toContain(
      current.summary.text.slice(0, 60),
    );
    // No section carries a review score, so the review's own order holds,
    // with the mechanical section last.
    expect(rows(container, "review-changes")).toEqual([
      "Candidate reading",
      "Atomic review save",
      "Evidence snapshot",
      "Writer fencing",
      "Commit picker",
      "Mechanical changes",
    ]);
    // The rail lists the critical and high risks, each named with its section.
    const risks = rows(container, "review-risks");
    expect(risks.length).toBeGreaterThan(0);
    expect(risks.some((label) => label?.includes("Atomic review save"))).toBe(true);
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
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("opens a section from the rail", async () => {
    const container = await render(
      <Host>
        <ReviewViewer document={current} />
      </Host>,
    );
    const row = [...container.querySelectorAll("#review-changes button")].find(
      (button) => button.textContent === "Writer fencing",
    ) as HTMLButtonElement;
    await act(async () => row.click());
    expect(container.querySelector("#review-section")?.getAttribute("aria-label")).toBe(
      "Writer fencing",
    );
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
      "Current · v3",
    );
    expect(shown).toHaveBeenLastCalledWith([3]);

    const compare = container.querySelector('[aria-label="Compare v2"]') as HTMLInputElement;
    await act(async () => compare.click());

    expect(loadVersion).toHaveBeenCalledWith(2);
    expect(container.textContent).toContain("Comparing current v3 with v2");
    expect(container.textContent).toContain("1 new section");
    expect(container.textContent).toContain("Historical · Review v2");
    // The section only the current version has is marked new in the rail.
    const picker = [...container.querySelectorAll("#review-changes button")].find((button) =>
      button.textContent?.startsWith("Commit picker"),
    );
    expect(picker?.textContent).toContain("New");
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
    // The rail follows the version being read, which has no commit picker.
    expect(rows(container, "review-changes")).not.toContain("Commit picker");
  });
});

describe("ReviewViewer with host slots", () => {
  it("draws what the host supplies beside the document", async () => {
    const container = await render(
      <Host initial="pane=section&section=sec_fencing">
        <ReviewViewer
          document={current}
          header={{ approval: <span>Approved</span>, statistics: <p>12 files</p> }}
          checks={{
            renderSection: ({ id }) => <input type="checkbox" aria-label={`Read ${id}`} />,
            renderRisk: () => null,
            sectionRead: (id) => id === "sec_candidates",
            riskRead: () => false,
          }}
          renderSectionAside={(id) => <p>Approvers of {id}</p>}
          files={{
            files: [{ id: "f1", path: "a.go", viewed: true }],
            loading: false,
            failed: false,
            retry: () => {},
            renderTree: ({ files }) => <p>{files.length} file</p>,
            renderFile: ({ file }) => <p>Diff of {file.path}</p>,
          }}
          sessions={{ state: { status: "loading" }, renderPane: () => null }}
          conversation={(place) => <p>Discussing {place.viewedSectionId}</p>}
        />
      </Host>,
    );
    expect(container.textContent).toContain("Approved");
    expect(container.textContent).toContain("12 files");
    expect(container.textContent).toContain("Approvers of sec_fencing");
    expect(container.textContent).toContain("Discussing sec_fencing");
    expect(container.querySelector('[aria-label="Read sec_picker"]')).not.toBeNull();
    expect(container.querySelector('[aria-label="Changes, 1 of 6 read"]')).not.toBeNull();
    const groups = [
      ...container.querySelectorAll('nav[aria-label="Review sections"] button[aria-expanded]'),
    ].map((row) => row.textContent);
    expect(groups).toEqual(["Changes", "Critical risks", "Files changed", "Sessions"]);
    const tabs = [...container.querySelectorAll('[role="tab"]')].map((tab) => tab.textContent);
    expect(tabs).toEqual(["Chat", "Details", "Activity"]);
  });
});
