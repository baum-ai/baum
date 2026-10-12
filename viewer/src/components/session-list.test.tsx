// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { sessionsSlot } from "./list-slots";
import { SessionList, type SessionListState, type SessionView } from "./session-list";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const sessions: SessionView[] = [
  {
    id: "s1",
    vendor: "claude_code",
    title: "Wire the retry budget",
    branch: "feature",
    startedAt: "2026-07-01T10:00:00Z",
    endedAt: "2026-07-01T10:30:00Z",
    outcome: "committed",
    editedFiles: 2,
    commits: [{ id: "1111111111111111111111111111111111111111", subject: "Add the budget" }],
  },
  { id: "s2", vendor: "codex", title: null, commits: [] },
];

let root: Root | null = null;
let container: HTMLElement;
const select = vi.fn();

function render(state: SessionListState, selected: string | null = null) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <SessionList state={state} selected={selected} actions={{ select, openFile: vi.fn() }} />,
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

describe("SessionList", () => {
  it("lists every session with its agent, title, branch, outcome and commits", () => {
    render({ status: "ready", sessions });

    const text = container.textContent ?? "";
    expect(text).toContain("2 sessions");
    expect(text).toContain("Claude Code");
    expect(text).toContain("Wire the retry budget");
    expect(text).toContain("feature");
    expect(text).toContain("Committed");
    expect(text).toContain("30m");
    expect(text).toContain("2 files edited");
    expect(text).toContain("1 linked commit");
    expect(text).toContain("Add the budget");
    // A session with no title is named by its agent.
    expect(text).toContain("Codex");
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("opens a session from the list and a commit from a session", () => {
    render({ status: "ready", sessions });

    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "Wire the retry budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "session", id: "s1" });

    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "Add the budget")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({
      kind: "commit",
      id: "1111111111111111111111111111111111111111",
    });
  });

  it("shows one selected session and a way back to the list", () => {
    render({ status: "ready", sessions }, "s1");

    const text = container.textContent ?? "";
    expect(text).toContain("1 of 2 sessions");
    expect(text).toContain("Wire the retry budget");
    expect(text).not.toContain("Codex");

    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "All sessions →")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "sessions" });
  });

  it("says nothing is linked when the list is empty", () => {
    render({ status: "ready", sessions: [] });

    expect(container.textContent).toContain("No agent session is linked to this pull request.");
    expect(container.textContent).toContain("No agent session is linked to this");
    expect(container.querySelector("ol")).toBeNull();
  });

  it("shows the note as plain text under the heading", () => {
    render({ status: "ready", sessions, note: "Showing the recorded ones — <i>why</i>" });

    expect(container.textContent).toContain("Showing the recorded ones — <i>why</i>");
    expect(container.querySelector("i")).toBeNull();
  });

  it("explains the wait while loading", () => {
    render({ status: "loading" });

    const status = container.querySelector('[role="status"]');
    expect(status?.textContent).toContain("Reading the sessions on this machine");
  });

  it("fails in an alert", () => {
    render({ status: "failed" });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "The sessions could not be loaded.",
    );
  });
});

describe("sessionsSlot", () => {
  it("lists the sessions in the rail by title, else by agent", () => {
    const slot = sessionsSlot({ status: "ready", sessions });

    expect(slot.state).toEqual({
      status: "ready",
      items: [
        { id: "s1", label: "Wire the retry budget" },
        { id: "s2", label: "Codex" },
      ],
    });
  });

  it("carries the loading and failed states through", () => {
    expect(sessionsSlot({ status: "loading" }).state).toEqual({ status: "loading" });
    expect(sessionsSlot({ status: "failed" }).state).toEqual({ status: "failed" });
  });
});
