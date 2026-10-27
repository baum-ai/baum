// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import type { DecisionDetail } from "../decision-log";
import { DecisionDetailBody, type OpenSessionSource, type SessionTurn } from "./decision-log";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const detail: DecisionDetail = {
  session: {
    id: "s1",
    person: null,
    vendor: "claude_code",
    branch: "main",
    started_at: null,
    ended_at: null,
  },
  decisions: [
    {
      id: "d1",
      turn: 3,
      turns: [3],
      decided_at: "2026-10-06T14:00:00Z",
      who: "human",
      category: "data_model",
      summary: "Store decisions per session",
      rejected_alternative: null,
      superseded: false,
      files: [],
    },
    {
      id: "d2",
      turn: 5,
      turns: [5],
      decided_at: "2026-10-06T15:00:00Z",
      who: "agent",
      category: "interface",
      summary: "Page with an opaque cursor",
      rejected_alternative: null,
      superseded: false,
      files: [],
    },
  ],
  turns: [],
  prompts_withheld: false,
  pull_requests: [],
};

let root: Root | null = null;
let container: HTMLElement;

/** A host that loads a session as its id and draws it with the turn. */
function host(load: OpenSessionSource<string>["load"] = async (id) => id) {
  return {
    load: vi.fn(load),
    render: (session: string, at: SessionTurn) => (
      <p>
        session {session} at turn {at.turn}
      </p>
    ),
  };
}

/** What the detail shows: a decision of a loaded detail, or a decision no
 *  longer available, known only by its session and turn. The route changes
 *  either as the address opens another decision. */
type View = { ready: string } | { missing: SessionTurn };

function draw(source: OpenSessionSource<string>, view: View) {
  act(() =>
    root!.render(
      <DecisionDetailBody
        state={"ready" in view ? { status: "ready", detail } : { status: "missing" }}
        decisionId={"ready" in view ? view.ready : "gone"}
        at={"missing" in view ? view.missing : undefined}
        pullRequestHref={(number) => `/pulls/${number}`}
        openSession={source}
        onFile={() => {}}
        onRetry={() => {}}
      />,
    ),
  );
}

function render(source: OpenSessionSource<string>, view: View = { ready: "d1" }) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  draw(source, view);
}

function toggle() {
  const button = [...container.querySelectorAll("button")].find((each) =>
    /session/i.test(each.textContent ?? ""),
  )!;
  act(() => button.click());
}

/** Lets settled promises reach React. */
async function flush() {
  await act(async () => {
    await Promise.resolve();
  });
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  container.remove();
});

describe("a decision's session", () => {
  it("asks the host once, on first open, and keeps what it loaded", async () => {
    const source = host();
    render(source);
    expect(source.load).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Open session at turn 3");

    toggle();
    await flush();
    expect(source.load).toHaveBeenCalledTimes(1);
    expect(source.load.mock.calls[0]![0]).toBe("s1");
    expect(container.textContent).toContain("session s1 at turn 3");

    toggle();
    toggle();
    await flush();
    expect(source.load).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain("session s1 at turn 3");
  });

  it("follows another missing decision's turn open, and loads another session", async () => {
    const source = host();
    render(source, { missing: { sessionId: "s1", turn: 3 } });
    toggle();
    await flush();
    expect(container.textContent).toContain("session s1 at turn 3");

    draw(source, { missing: { sessionId: "s1", turn: 5 } });
    await flush();
    expect(container.textContent).toContain("Hide session");
    expect(container.textContent).toContain("session s1 at turn 5");
    expect(source.load).toHaveBeenCalledTimes(1);

    draw(source, { missing: { sessionId: "s2", turn: 1 } });
    await flush();
    expect(container.textContent).toContain("Hide session");
    expect(container.textContent).toContain("session s2 at turn 1");
    expect(source.load).toHaveBeenCalledTimes(2);
    expect(source.load.mock.calls[1]![0]).toBe("s2");
  });

  it("says a session that could not be loaded", async () => {
    render(
      host(async () => {
        throw new Error("refused");
      }),
    );
    toggle();
    await flush();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "This session could not be loaded.",
    );
  });

  it("does not mark a load the reader closed on as failed", async () => {
    const signals: AbortSignal[] = [];
    const source = host(
      (_id, signal) =>
        new Promise((_resolve, reject) => {
          signals.push(signal);
          signal.addEventListener("abort", () => reject(new Error("aborted")));
        }),
    );
    render(source);
    toggle();
    toggle();
    await flush();
    expect(signals[0]?.aborted).toBe(true);
    expect(container.querySelector('[role="alert"]')).toBeNull();

    toggle();
    await flush();
    expect(source.load).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("Loading this session…");
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it("follows another decision of the same session in a loaded detail", async () => {
    const source = host();
    render(source, { ready: "d1" });
    toggle();
    await flush();
    expect(container.textContent).toContain("session s1 at turn 3");

    draw(source, { ready: "d2" });
    await flush();
    expect(container.textContent).toContain("Hide session");
    expect(container.textContent).toContain("session s1 at turn 5");
    expect(source.load).toHaveBeenCalledTimes(1);
  });

  it("drops a load in flight for another session, and never shows its answer", async () => {
    const pending: { signal: AbortSignal; resolve: (session: string) => void }[] = [];
    const source = host(
      (id, signal) =>
        new Promise((resolve) => {
          pending.push({ signal, resolve: () => resolve(id) });
        }),
    );
    render(source, { missing: { sessionId: "s1", turn: 3 } });
    toggle();
    await flush();
    expect(source.load).toHaveBeenCalledTimes(1);

    draw(source, { missing: { sessionId: "s2", turn: 1 } });
    await flush();
    expect(pending[0]!.signal.aborted).toBe(true);
    expect(source.load).toHaveBeenCalledTimes(2);
    expect(source.load.mock.calls[1]![0]).toBe("s2");

    // The first session answers late, after the reset: it must not land.
    pending[0]!.resolve("s1");
    await flush();
    expect(container.textContent).not.toContain("session s1");
    expect(container.textContent).toContain("Loading this session…");
    expect(container.querySelector('[role="alert"]')).toBeNull();

    pending[1]!.resolve("s2");
    await flush();
    expect(container.textContent).toContain("session s2 at turn 1");
  });
});
