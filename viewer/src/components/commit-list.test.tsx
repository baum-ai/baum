// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { CommitList, type CommitListState, type CommitView } from "./commit-list";
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

function render(state: CommitListState, selected: string | null = null) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <CommitList state={state} selected={selected} actions={{ select, openFile: vi.fn() }} />,
    ),
  );
}

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
        .find((button) => button.textContent === "Wire the retry budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "session", id: "s1" });

    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "Add the retry budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "commit", id: ONE });
  });

  it("shows one selected commit and a way back to the list", () => {
    render({ status: "ready", commits }, ONE);

    const text = container.textContent ?? "";
    expect(text).toContain("1 of 2 commits");
    expect(text).toContain("Add the retry budget");
    expect(text).not.toContain("No session is linked to this commit.");

    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "All commits →")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "commits" });
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
