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

/** The detail as the route draws it for an opened decision that its page
 *  does not list: `at` changes as the address opens another decision. */
function draw(source: OpenSessionSource<string>, at?: SessionTurn) {
  act(() =>
    root!.render(
      <DecisionDetailBody
        state={{ status: "missing" }}
        decisionId="d1"
        at={at}
        pullRequestHref={(number) => `/pulls/${number}`}
        openSession={source}
        onFile={() => {}}
        onRetry={() => {}}
      />,
    ),
  );
}

function render(source: OpenSessionSource<string>, at?: SessionTurn) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  if (at) {
    draw(source, at);
    return;
  }
  act(() =>
    root!.render(
      <DecisionDetailBody
        state={{ status: "ready", detail }}
        decisionId="d1"
        pullRequestHref={(number) => `/pulls/${number}`}
        openSession={source}
        onFile={() => {}}
        onRetry={() => {}}
      />,
    ),
  );
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

  it("follows another turn of the same session open, and loads another session", async () => {
    const source = host();
    render(source, { sessionId: "s1", turn: 3 });
    toggle();
    await flush();
    expect(container.textContent).toContain("session s1 at turn 3");

    draw(source, { sessionId: "s1", turn: 5 });
    await flush();
    expect(container.textContent).toContain("Hide session");
    expect(container.textContent).toContain("session s1 at turn 5");
    expect(source.load).toHaveBeenCalledTimes(1);

    draw(source, { sessionId: "s2", turn: 1 });
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
});
