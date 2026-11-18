import { act, type ReactNode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ReviewViewer } from "@baum/viewer/components/review-viewer";
import { type SearchUpdate, UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { review1303 } from "./fixture-1303";
import { useViewPlaces } from "./places";

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

import { reviewDocumentFor } from "../../../viewer/test/review-document";

const v1 = () => structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;

const page = (initial = "", document: ReviewDocument = v1()) =>
  render(
    <Host initial={initial}>
      <Page document={document} />
    </Host>,
  );
const pane = (container: HTMLElement) => container.querySelector<HTMLElement>("#review-section")!;
const rail = (container: HTMLElement) => container.querySelector<HTMLElement>("#review-rail")!;
const railRows = (container: HTMLElement) => [
  ...rail(container).querySelectorAll<HTMLButtonElement>('button[aria-controls~="review-section"]'),
];
const rowLabel = (row: HTMLElement) =>
  row.querySelector("span[title]")?.getAttribute("title") ?? row.textContent ?? "";
const click = (element: HTMLElement) => act(async () => element.click());

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
