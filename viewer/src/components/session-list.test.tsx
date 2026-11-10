// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { sessionsSlot } from "./list-slots";
import {
  type HostSessionView,
  SessionList,
  type SessionListState,
  type SessionView,
} from "./session-list";

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

/** A host's session row with nothing to draw, for the slot's rail. */
const row = (id: string, indicator?: "live" | "attention"): HostSessionView => ({
  id,
  label: `Running ${id}`,
  indicator,
  renderCard: () => null,
  renderPane: () => null,
});

const transcript = (session: SessionView) => <p data-testid="extra">{session.id} transcript</p>;

let root: Root | null = null;
let container: HTMLElement;
const select = vi.fn();

function render(
  state: SessionListState,
  selected: string | null = null,
  renderSession?: (session: SessionView) => ReactNode,
) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root!.render(
      <SessionList
        state={state}
        selected={selected}
        actions={{ select, openFile: vi.fn() }}
        renderSession={renderSession}
      />,
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

  it("adds the host's content under the one session opened, and only then", () => {
    render({ status: "ready", sessions }, null, transcript);
    expect(container.querySelector('[data-testid="extra"]')).toBeNull();
    act(() => root?.unmount());

    render({ status: "ready", sessions }, "s2", transcript);
    expect(container.querySelector('[data-testid="extra"]')?.textContent).toBe("s2 transcript");
    act(() => root?.unmount());

    render({ status: "ready", sessions }, "unknown", transcript);
    expect(container.querySelector('[data-testid="extra"]')).toBeNull();
    act(() => root?.unmount());

    // With the host's view of it, the session heads the pane, its linked
    // commits stay, and no card repeats what that view says.
    render({ status: "ready", sessions }, "s1", transcript);
    expect(container.querySelector("h2, h3")?.textContent).toBe("Wire the retry budget");
    expect(container.querySelector("#session-s1")).toBeNull();
    expect(container.querySelector('[aria-label="Linked commits"]')?.textContent).toContain(
      "Add the budget",
    );
    expect(container.querySelector('[data-testid="extra"]')?.textContent).toBe("s1 transcript");
  });

  it("says once that there are no sessions when the list is empty", () => {
    render({ status: "ready", sessions: [] });

    // The heading carries no count and no second "nothing here".
    expect(container.textContent).not.toContain("linked to this");
    expect(container.textContent).not.toContain("0 sessions");
    expect(container.textContent).toContain("No agent sessions");
    expect(container.textContent).toContain(
      "No agent session on this machine matches a commit of this pull request.",
    );
    expect(container.querySelector("ol")).toBeNull();
  });

  it("lists the host's sessions first, once each, and opens one in the host's pane", () => {
    const hosted: HostSessionView[] = [
      {
        id: "s1",
        label: "Running now",
        indicator: "live",
        renderCard: (open) => (
          <button type="button" onClick={open}>
            Running card
          </button>
        ),
        renderPane: () => <p data-testid="hosted-pane">running transcript</p>,
      },
    ];
    const mount = (state: SessionListState, selected: string | null = null) => {
      container = document.createElement("div");
      document.body.append(container);
      root = createRoot(container);
      act(() =>
        root!.render(
          <SessionList
            state={state}
            selected={selected}
            actions={{ select, openFile: vi.fn() }}
            hosted={hosted}
            empty="Shown while they work."
          />,
        ),
      );
    };

    // s1 is both running and linked: one row, the host's.
    mount({ status: "ready", sessions });
    let text = container.textContent ?? "";
    expect(text).toContain("2 sessions");
    expect(text).toContain("Running card");
    expect(text).not.toContain("Wire the retry budget");
    expect(text).toContain("Codex");
    expect(container.querySelectorAll("li[id^='session-']")).toHaveLength(2);
    act(() =>
      [...container.querySelectorAll("button")]
        .find((button) => button.textContent === "Running card")!
        .click(),
    );
    expect(select).toHaveBeenLastCalledWith({ kind: "session", id: "s1" });
    act(() => root?.unmount());

    // Nothing linked: the running session alone, no empty state.
    mount({ status: "ready", sessions: [] });
    text = container.textContent ?? "";
    expect(text).toContain("1 session");
    expect(text).not.toContain("No agent sessions");
    act(() => root?.unmount());

    // Still loading the linked ones: the running session shows meanwhile.
    mount({ status: "loading" });
    expect(container.textContent).toContain("Running card");
    expect(container.querySelector('[aria-label="Loading sessions"]')).not.toBeNull();
    act(() => root?.unmount());

    mount({ status: "ready", sessions }, "s1");
    expect(container.querySelector("h2, h3")?.textContent).toBe("Running now");
    expect(container.querySelector('[data-testid="hosted-pane"]')).not.toBeNull();
    act(() => root?.unmount());

    // A session no longer listed leaves the whole list in view.
    mount({ status: "ready", sessions }, "gone");
    expect(container.querySelectorAll("li[id^='session-']")).toHaveLength(2);
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

  it("lists and counts the host's sessions first, once each, and marks the group", () => {
    const slot = sessionsSlot({ status: "ready", sessions }, { hosted: [row("s1", "live")] });
    expect(slot.state).toEqual({
      status: "ready",
      items: [
        { id: "s1", label: "Running s1", indicator: "live" },
        { id: "s2", label: "Codex" },
      ],
    });
    expect(slot.indicator).toBe("live");
    expect(
      sessionsSlot(
        { status: "ready", sessions: [] },
        { hosted: [row("a", "live"), row("b", "attention")] },
      ).indicator,
    ).toBe("attention");
    expect(sessionsSlot({ status: "ready", sessions }, { hosted: [row("x")] }).indicator).toBe(
      undefined,
    );
  });

  it("carries the loading and failed states through", () => {
    expect(sessionsSlot({ status: "loading" }).state).toEqual({ status: "loading" });
    expect(sessionsSlot({ status: "failed" }).state).toEqual({ status: "failed" });
  });
});
