// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import type { DecisionDetail } from "../decision-log";
import { DecisionDetailBody, type OpenSessionSource } from "./decision-log";

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

function render(source: OpenSessionSource) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
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
  it("asks the host once, on first open, and keeps what it drew", async () => {
    const source = vi.fn<OpenSessionSource>(async (at) => <p>session {at.sessionId}</p>);
    render(source);
    expect(source).not.toHaveBeenCalled();
    expect(container.textContent).toContain("Open session at turn 3");

    toggle();
    await flush();
    expect(source).toHaveBeenCalledTimes(1);
    expect(source.mock.calls[0]![0]).toEqual({ sessionId: "s1", turn: 3 });
    expect(container.textContent).toContain("session s1");

    toggle();
    toggle();
    await flush();
    expect(source).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain("session s1");
  });

  it("says a session that could not be loaded", async () => {
    render(async () => {
      throw new Error("refused");
    });
    toggle();
    await flush();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "This session could not be loaded.",
    );
  });

  it("does not mark a load the reader closed on as failed", async () => {
    const signals: AbortSignal[] = [];
    const source = vi.fn<OpenSessionSource>(
      (_at, signal) =>
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
    expect(source).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("Loading this session…");
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });
});
