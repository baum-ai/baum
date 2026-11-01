// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import type { ReviewContext, ReviewDocument } from "../review-document";
import { ReviewFilesProvider, ReviewSourcesProvider, type ReviewSources } from "./code-file-link";
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

function render(
  document: ReviewDocument,
  open?: (path: string) => boolean,
  openRepositoryFile?: ReviewSources["openRepositoryFile"],
) {
  container = window.document.createElement("div");
  window.document.body.append(container);
  root = createRoot(container);
  const files = open ? { has: (path: string) => path === "queue/worker.go", open } : null;
  const sources: ReviewSources | null = openRepositoryFile
    ? { opens: () => false, open: () => false, openRepositoryFile }
    : null;
  act(() =>
    root?.render(
      <ReviewFilesProvider files={files}>
        <ReviewSourcesProvider sources={sources}>
          <ReviewSummary document={document} onSelectSection={() => {}} />
        </ReviewSourcesProvider>
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
    expect([...(section?.querySelectorAll("dt") ?? [])].map((each) => each.textContent)).toEqual([
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
    expect([...(section?.querySelectorAll("dt") ?? [])].map((each) => each.textContent)).toEqual([
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
      // The symbol first, then the file's name; the folder is in the title.
      "Worker.Run·worker.go",
      "a b#c.go",
      "Legacy·legacy.go",
      "queue/",
    ]);

    const changed = chips[0]?.querySelector("button");
    expect(changed?.getAttribute("title")).toBe("Worker.Run in queue/worker.go");
    expect(chips[0]?.querySelector("a")).toBeNull();
    act(() => changed?.click());
    expect(open).toHaveBeenCalledWith("queue/worker.go");

    const hrefs = chips.slice(1).map((each) => each.querySelector("a")?.getAttribute("href"));
    expect(hrefs).toEqual([
      `https://github.com/example-org/taskline/blob/${HEAD}/queue/a%20b%23c.go`,
      `https://github.com/example-org/taskline/blob/${MERGE_BASE}/queue/legacy.go`,
      `https://github.com/example-org/taskline/blob/${HEAD}/queue`,
    ]);
    for (const chip of chips.slice(1))
      expect(chip.querySelector("a")?.getAttribute("target")).toBe("_blank");
  });

  it("lays each part out as a label beside its paragraph and chips", () => {
    const view = render(
      documentWith({
        area: {
          text: "The area.",
          refs: [{ path: "internal/queue/worker/pool.go", symbol: "Pool" }],
        },
      }),
    );
    const row = context(view)?.querySelector("dl > div");
    expect(row?.querySelector("dt")?.textContent).toBe("Area");
    const content = row?.querySelector("dd");
    expect(content?.querySelector("p")?.textContent).toBe("The area.");
    const chip = content?.querySelector("ul li a");
    // The chip leads with the symbol, as code, then the file's name; the
    // full path is its title, and the folder is not written on it.
    expect(chip?.getAttribute("title")).toBe("Pool in internal/queue/worker/pool.go");
    expect(chip?.querySelector("code")?.textContent).toBe("Pool");
    expect(chip?.textContent).toBe("Pool·pool.go");
  });

  it("opens every file in the host's dialog, changed or not, without the leaving mark", () => {
    const open = vi.fn(() => true);
    const openRepositoryFile = vi.fn(() => true);
    const view = render(
      documentWith({
        area: {
          text: "The area.",
          refs: [
            { path: "queue/worker.go", symbol: "Worker.Run" },
            { path: "queue/retry.go", symbol: "Backoff" },
            { path: "queue", symbol: null },
          ],
        },
      }),
      open,
      openRepositoryFile,
    );
    const chips = [...(context(view)?.querySelectorAll('ul[aria-label="Area files"] li') ?? [])];

    // A changed file opens as the file in the host's dialog too, not its diff.
    act(() => chips[0]?.querySelector("button")?.click());
    expect(openRepositoryFile).toHaveBeenCalledWith({
      path: "queue/worker.go",
      symbol: "Worker.Run",
    });
    expect(open).not.toHaveBeenCalled();

    // A file the change does not touch is a button for the host, with no ↗.
    const unchanged = chips[1]?.querySelector("button");
    expect(unchanged).not.toBeNull();
    expect(chips[1]?.querySelector("a")).toBeNull();
    expect(chips[1]?.querySelector("svg")).toBeNull();
    act(() => unchanged?.click());
    expect(openRepositoryFile).toHaveBeenCalledWith({ path: "queue/retry.go", symbol: "Backoff" });

    // A directory stays its GitHub link.
    const directory = chips[2]?.querySelector("a");
    expect(directory?.getAttribute("href")).toBe(
      `https://github.com/example-org/taskline/blob/${HEAD}/queue`,
    );
    expect(directory?.getAttribute("target")).toBe("_blank");
    expect(chips[2]?.querySelector("button")).toBeNull();
  });
});
