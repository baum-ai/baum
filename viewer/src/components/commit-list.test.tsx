// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import {
  type CommitDetailView,
  CommitList,
  type CommitListState,
  type CommitLoaders,
  type CommitView,
} from "./commit-list";
import { commitsSlot } from "./list-slots";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const ONE = "1111111111111111111111111111111111111111";
const TWO = "2222222222222222222222222222222222222222";

const commits: CommitView[] = [
  {
    id: ONE,
    subject: "Add the retry budget",
    author: "Grace Hopper",
    committedAt: "2026-07-01T10:30:00Z",
    links: [
      { session: "s1", label: "Wire the retry budget", method: "commit_ref", confidence: 0.95 },
      { session: "s2", label: "Codex", method: "files_overlap", confidence: 0.4 },
    ],
  },
  { id: TWO, subject: "", links: [] },
];

let root: Root | null = null;
let container: HTMLElement;
const select = vi.fn();

function render(state: CommitListState, selected: string | null = null, loaders?: CommitLoaders) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <CommitList
        state={state}
        selected={selected}
        actions={{ select, openFile: vi.fn() }}
        loaders={loaders}
      />,
    ),
  );
}

/** Lets the loaders' promises settle and React draw what they answered. */
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function retryButton() {
  const found = [...container.querySelectorAll("button")].find((b) => b.textContent === "Retry");
  if (!found) throw new Error("no Retry button");
  return found;
}

function button(label: string) {
  const found = container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!found) throw new Error(`no button labelled ${label}`);
  return found;
}

const detail: CommitDetailView = {
  body: "Retries were unbounded.\n\n<b>Not markup.</b>",
  files: [
    { path: "src/retry.ts", status: "modified", additions: 3, deletions: 1 },
    { path: "logo.png", status: "added", additions: null, deletions: null },
    {
      path: "src/budget.ts",
      oldPath: "src/limit.ts",
      status: "renamed",
      additions: 2,
      deletions: 2,
    },
  ],
};

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  select.mockReset();
});

describe("CommitList", () => {
  it("lists every commit with its id, author, and the sessions it links to", () => {
    render({ status: "ready", commits });

    const text = container.textContent ?? "";
    expect(text).toContain("2 commits");
    expect(text).toContain("Add the retry budget");
    expect(text).toContain("1111111");
    expect(text).toContain("Grace Hopper");
    expect(text).toContain("Wire the retry budget");
    expect(text).toContain("Named in the session");
    expect(text).toContain("95% confidence");
    expect(text).toContain("Edited the same files");
    expect(text).toContain("40% confidence");
    expect(text).toContain("No session is linked to this commit.");
    expect(text).toContain("(no subject)");
  });

  it("says a link from the commit's Entire checkpoint was recorded by Entire", () => {
    render({
      status: "ready",
      commits: [
        {
          id: ONE,
          subject: "x",
          links: [{ session: "s1", label: "A", method: "entire_checkpoint", confidence: 1 }],
        },
      ],
    });

    const text = container.textContent ?? "";
    expect(text).toContain("Recorded by Entire");
    expect(text).toContain("100% confidence");
    expect(text).not.toContain("Entire checkpoint");
  });

  it("keeps a confidence outside 0 to 1 within a percentage", () => {
    render({
      status: "ready",
      commits: [
        {
          id: ONE,
          subject: "x",
          links: [
            { session: "s1", label: "A", method: "constructor", confidence: 5 },
            { session: "s2", label: "B", method: "commit_ref", confidence: -1 },
          ],
        },
      ],
    });

    const text = container.textContent ?? "";
    expect(text).toContain("100% confidence");
    expect(text).toContain("0% confidence");
    expect(text).not.toContain("500%");
    expect(text).toContain("Constructor");
  });

  it("opens a linked session, and a commit from the list", () => {
    render({ status: "ready", commits });

    act(() =>
      [...container.querySelectorAll("button")]
        .find((each) => each.textContent === "Wire the retry budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "session", id: "s1" });

    act(() =>
      [...container.querySelectorAll("button")]
        .find((each) => each.textContent === "Add the retry budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "commit", id: ONE });
  });

  it("leads a commit opened alone with its own subject, under no list header", () => {
    render({ status: "ready", commits }, ONE);

    const text = container.textContent ?? "";
    expect(text).toContain("Add the retry budget");
    expect(text).not.toContain("1 of 2 commits");
    expect(text).not.toContain("All commits →");
    expect(text).not.toContain("No session is linked to this commit.");
  });

  it("says so when there are no commits", () => {
    render({ status: "ready", commits: [] });

    expect(container.textContent).toContain("No commit was found for this pull request.");
    expect(container.querySelector("ol")).toBeNull();
  });

  it("shows a skeleton while loading", () => {
    render({ status: "loading" });

    expect(container.querySelector('[role="status"]')).not.toBeNull();
  });

  it("fails in an alert", () => {
    render({ status: "failed" });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "The commits could not be loaded.",
    );
  });
});

describe("CommitList with the host's loaders", () => {
  it("shows a commit's body, totals and files, and opens a file's diff in place", async () => {
    const patch = "@@ -1,2 +1,2 @@\n keep\n-old budget\n+new budget\n";
    const loadCommit = vi.fn(() => Promise.resolve(detail));
    const loadPatch = vi.fn((_oid: string, path: string) =>
      Promise.resolve(path === "logo.png" ? null : patch),
    );
    render({ status: "ready", commits }, ONE, { loadCommit, loadPatch });

    expect(container.querySelector('[role="status"]')?.getAttribute("aria-label")).toBe(
      "Loading the commit",
    );
    await settle();

    expect(loadCommit).toHaveBeenCalledWith(ONE);
    const text = container.textContent ?? "";
    expect(text).toContain("+5 −3 across 3 files");
    expect(text).toContain("Retries were unbounded.");
    // The body is text, never markup.
    expect(text).toContain("<b>Not markup.</b>");
    expect(container.querySelector("b")).toBeNull();
    expect(text).toContain("Files changed");
    expect(text).toContain("from src/limit.ts");
    expect(text).toContain("binary");
    expect(text).toContain("Renamed");
    expect(loadPatch).not.toHaveBeenCalled();

    act(() => button("src/retry.ts code").click());
    expect(loadPatch).toHaveBeenLastCalledWith(ONE, "src/retry.ts");
    await settle();
    const diff = container.querySelector('[aria-label="src/retry.ts diff"]');
    expect(diff).not.toBeNull();
    expect(diff?.textContent).toContain("new budget");
    expect(diff?.textContent).toContain("old budget");

    act(() => button("logo.png code").click());
    await settle();
    expect(loadPatch).toHaveBeenLastCalledWith(ONE, "logo.png");
    expect(container.querySelector('[aria-label="logo.png diff"]')).toBeNull();
    expect(container.textContent).toContain("No diff to show.");
  });

  it("offers a retry when the commit or a file cannot be read", async () => {
    const loadCommit = vi
      .fn<(oid: string) => Promise<CommitDetailView | null>>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue(detail);
    const loadPatch = vi
      .fn<(oid: string, path: string) => Promise<string | null>>()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue("@@ -1 +1 @@\n-a\n+b\n");
    render({ status: "ready", commits }, ONE, { loadCommit, loadPatch });
    await settle();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "This commit could not be read.",
    );
    act(() => retryButton().click());
    await settle();
    expect(loadCommit).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("Files changed");

    act(() => button("src/retry.ts code").click());
    await settle();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain("could not be read");
    act(() => retryButton().click());
    await settle();
    expect(loadPatch).toHaveBeenCalledTimes(2);
    expect(container.querySelector('[aria-label="src/retry.ts diff"]')).not.toBeNull();
  });

  it("says so when this machine cannot read the commit", async () => {
    render({ status: "ready", commits }, ONE, { loadCommit: () => Promise.resolve(null) });
    await settle();

    expect(container.textContent).toContain("could not be read from the clone on this machine");
    expect(container.textContent).not.toContain("Files changed");
  });

  it("lists files without diffs when the host reads no patches", async () => {
    render({ status: "ready", commits }, ONE, { loadCommit: () => Promise.resolve(detail) });
    await settle();

    expect(container.textContent).toContain("src/retry.ts");
    expect(container.querySelector('button[aria-label="src/retry.ts code"]')).toBeNull();
  });

  it("shows no detail without loaders", () => {
    render({ status: "ready", commits }, ONE);

    expect(container.textContent).not.toContain("Files changed");
    expect(container.querySelector('[role="status"]')).toBeNull();
  });

  it("shows a commit's totals in the list once they were read, without asking", async () => {
    const loadCommit = vi.fn(() => Promise.resolve(detail));
    render({ status: "ready", commits }, ONE, { loadCommit });
    await settle();
    act(() => root?.unmount());
    document.body.replaceChildren();

    render({ status: "ready", commits }, null, { loadCommit });
    expect(loadCommit).toHaveBeenCalledTimes(1);
    const cards = container.querySelectorAll("li[id^='commit-']");
    expect(cards[0]?.textContent).toContain("+5 −3 · 3 files");
    expect(cards[1]?.textContent).not.toContain("files");
  });
});

describe("commitsSlot", () => {
  it("lists the commits in the rail by subject, else by short id", () => {
    expect(commitsSlot({ status: "ready", commits }).state).toEqual({
      status: "ready",
      items: [
        { id: ONE, label: "Add the retry budget" },
        { id: TWO, label: "2222222" },
      ],
    });
  });

  it("carries the loading and failed states through", () => {
    expect(commitsSlot({ status: "loading" }).state).toEqual({ status: "loading" });
    expect(commitsSlot({ status: "failed" }).state).toEqual({ status: "failed" });
  });
});
