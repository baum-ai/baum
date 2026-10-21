// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import type { ReviewContext, ReviewDocument } from "../review-document";
import { ReviewFilesProvider } from "./code-file-link";
import { ReviewSummary } from "./review-summary";

const HEAD = "d6d7a116b9babb73a1e9be4db63b94f35952ba6b";
const MERGE_BASE = "48682784a10b4ecf3a94f47a2f8f68b846cfc01b";

function documentWith(context: ReviewContext | undefined): ReviewDocument {
  const document = structuredClone(example) as unknown as ReviewDocument;
  document.summary.context = context;
  const removed = structuredClone(document.index.hunks[0]);
  if (!removed) throw new Error("the example has no hunks");
  document.index.hunks.push({
    ...removed,
    id: "h_removed",
    path: "queue/legacy.go",
    change: "removed",
  });
  return document;
}

let root: Root | null = null;
let container: HTMLElement | null = null;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  container?.remove();
  root = null;
  container = null;
});

function render(document: ReviewDocument, open?: (path: string) => boolean) {
  container = window.document.createElement("div");
  window.document.body.append(container);
  root = createRoot(container);
  const files = open ? { has: (path: string) => path === "queue/worker.go", open } : null;
  act(() =>
    root?.render(
      <ReviewFilesProvider files={files}>
        <ReviewSummary document={document} onSelectSection={() => {}} />
      </ReviewFilesProvider>,
    ),
  );
  return container;
}

const context = (view: HTMLElement) => view.querySelector('section[aria-label="Context"]');

describe("the summary's context", () => {
  it("renders nothing for a document generated before the context existed", () => {
    const view = render(documentWith(undefined));
    expect(context(view)).toBeNull();
    expect(view.textContent).not.toContain("Where this change sits");
  });

  it("starts collapsed under the overview and expands when its summary is clicked", () => {
    const view = render(documentWith(example.summary.context as ReviewContext));
    const section = context(view);
    const details = section?.querySelector("details");
    const summary = details?.querySelector("summary");
    expect(summary?.textContent).toBe("ContextWhere this change sits in the codebase");
    expect(details?.open).toBe(false);

    // It follows the overview and leads the intent and risk parts.
    const order = [...view.querySelectorAll("section[aria-label]")].map((each) =>
      each.getAttribute("aria-label"),
    );
    expect(order[0]).toBe("Context");

    act(() => summary?.click());
    expect(details?.open).toBe(true);
    expect([...(section?.querySelectorAll("h4") ?? [])].map((each) => each.textContent)).toEqual([
      "Area",
      "How it's reached",
      "Contracts",
      "Related code",
    ]);
    expect(section?.textContent).toContain("runs taskline's background jobs");
  });

  it("shows only the parts the document has, in order", () => {
    const view = render(
      documentWith({
        area: { text: "The area.", refs: [] },
        related: { text: "Related.", refs: [] },
      }),
    );
    const section = context(view);
    expect([...(section?.querySelectorAll("h4") ?? [])].map((each) => each.textContent)).toEqual([
      "Area",
      "Related code",
    ]);
    expect(section?.querySelector("ul")).toBeNull();
  });

  it("links each reference: a changed file to the file view, others to GitHub", () => {
    const open = vi.fn(() => true);
    const view = render(
      documentWith({
        area: {
          text: "The area.",
          refs: [
            { path: "queue/worker.go", symbol: "Worker.Run" },
            { path: "queue/a b#c.go", symbol: null },
            { path: "queue/legacy.go", symbol: "Legacy" },
            { path: "queue", symbol: null },
          ],
        },
      }),
      open,
    );
    const chips = [...(context(view)?.querySelectorAll('ul[aria-label="Area files"] li') ?? [])];
    expect(chips.map((each) => each.textContent)).toEqual([
      "queue/worker.go · Worker.Run",
      "queue/a b#c.go",
      "queue/legacy.go · Legacy",
      "queue",
    ]);

    const changed = chips[0]?.querySelector("button");
    expect(changed?.getAttribute("title")).toBe("Worker.Run in queue/worker.go");
    expect(chips[0]?.querySelector("a")).toBeNull();
    act(() => changed?.click());
    expect(open).toHaveBeenCalledWith("queue/worker.go", undefined);

    const hrefs = chips.slice(1).map((each) => each.querySelector("a")?.getAttribute("href"));
    expect(hrefs).toEqual([
      `https://github.com/example-org/taskline/blob/${HEAD}/queue/a%20b%23c.go`,
      `https://github.com/example-org/taskline/blob/${MERGE_BASE}/queue/legacy.go`,
      `https://github.com/example-org/taskline/blob/${HEAD}/queue`,
    ]);
    for (const chip of chips.slice(1))
      expect(chip.querySelector("a")?.getAttribute("target")).toBe("_blank");
  });
});
