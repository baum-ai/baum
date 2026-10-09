// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import {
  SessionCard,
  type SessionCardActivity,
  type SessionCardPrompt,
  type SessionCardSession,
  type SessionCardSource,
} from "./session-card";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

const effects = {
  files_read: 3,
  files_edited: 1,
  reads: 5,
  diff_hunks: 2,
  added_lines: 10,
  removed_lines: 4,
  tool_calls: 6,
  failed_calls: 1,
};

function prompt(ordinal: number, text: string, overrides: Partial<SessionCardPrompt> = {}) {
  return {
    turn_id: `turn-${ordinal}`,
    ordinal,
    started_at: "2026-07-01T10:00:00Z",
    text,
    text_length: text.length,
    truncated: false,
    effects,
    cost_usd: null,
    tokens: 1200,
    estimated_tokens: 0,
    ...overrides,
  } satisfies SessionCardPrompt;
}

function session(overrides: Partial<SessionCardSession> = {}): SessionCardSession {
  return {
    run_id: "run-1",
    source: "claude_code",
    title: "Wire the retry budget",
    git_branch: "feature/retry",
    working_dir: "~/src/widgets",
    models: ["claude-opus"],
    started_at: "2026-07-01T10:00:00Z",
    ended_at: "2026-07-01T10:30:00Z",
    outcome: "committed",
    user_email: "",
    turn_count: 2,
    commit_count: 1,
    confidence: 0.95,
    prompts: [prompt(1, "Add a retry budget"), prompt(2, "Now test it")],
    prompts_truncated: false,
    totals: effects,
    unattributed: null,
    tokens: 2400,
    estimated_tokens: 0,
    cost_usd: 1.25,
    cost_per_prompt: true,
    has_inner_trace: true,
    ...overrides,
  };
}

const activity: SessionCardActivity = {
  messages: [
    { text: "Reading the client.", timestamp: "2026-07-01T10:01:00Z", model: "claude-opus" },
  ],
  messages_truncated: false,
  invocations: [{}],
  token_usage: {
    tokens: 1200,
    estimated_tokens: 0,
    uncached_input_tokens: 800,
    output_tokens: 400,
    api_calls: 1,
    models: ["claude-opus"],
  },
  tool_calls: [
    {
      id: "call-1",
      name: "Read",
      verb: "read",
      input: '{"file_path":"client.go"}',
      input_truncated: false,
      output: "package client",
      output_truncated: true,
      status: "ok",
      timestamp: null,
    },
  ],
  tool_calls_truncated: false,
};

let root: Root | null = null;
let container: HTMLElement;

function render(element: ReturnType<typeof createElement>) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(element));
}

async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function button(name: string): HTMLButtonElement {
  const found = [...container.querySelectorAll("button")].find((b) =>
    (b.textContent ?? "").includes(name),
  );
  if (!found) throw new Error(`no button "${name}"`);
  return found;
}

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
});

describe("SessionCard", () => {
  it("shows a restricted card with no usage", () => {
    const html = renderToStaticMarkup(
      createElement(SessionCard, {
        session: session({
          restricted: true,
          title: "",
          prompts: [],
          totals: null,
          tokens: 0,
          cost_usd: null,
          has_inner_trace: false,
        }),
        defaultOpen: true,
      }),
    );
    expect(html).toContain("Details restricted");
    expect(html).toContain(
      "shown only when you can read every repository it belongs to and all of them are connected",
    );
    for (const hidden of ["Tokens", "Cost", "Files read", "0 prompts", "$"]) {
      expect(html).not.toContain(hidden);
    }
  });

  it("lists the prompts with their effects and the session's figures", () => {
    render(
      createElement(SessionCard, {
        session: session(),
        defaultOpen: true,
        modelLabel: (model: string) => model.toUpperCase(),
      }),
    );
    const text = container.textContent ?? "";
    expect(text).toContain("Wire the retry budget");
    expect(text).toContain("2 prompts");
    expect(text).toContain("1 commit");
    expect(text).toContain("CLAUDE-OPUS");
    expect(text).toContain("Add a retry budget");
    expect(text).toContain("Now test it");
    expect(text).toContain("read 3 files");
    expect(text).toContain("5 reads");
    expect(text).toContain("+10/-4");
    expect(text).toContain("$1.25");
    expect(text).toContain("~/src/widgets");
    // Without a loader there is no activity to open.
    expect(text).not.toContain("Agent activity");
  });

  it("hides the Cost figure when the host has no prices", () => {
    const local = session({ cost_per_prompt: true });
    delete local.cost_usd;
    render(createElement(SessionCard, { session: local, defaultOpen: true }));
    const text = container.textContent ?? "";
    expect(text).toContain("Tokens");
    expect(text).not.toContain("Cost");
    expect(text).not.toContain("no price");
  });

  it("loads a session that arrived without its prompts when it opens", async () => {
    const loadSession = vi.fn(async () => session());
    render(
      createElement(SessionCard, {
        session: session({ prompts: [] }),
        source: { loadSession },
        defaultOpen: false,
      }),
    );
    expect(loadSession).not.toHaveBeenCalled();
    act(() => button("Session").click());
    expect(container.textContent).toContain("Loading this session…");
    await settle();
    expect(loadSession).toHaveBeenCalledWith("run-1");
    expect(container.textContent).toContain("Add a retry budget");
  });

  it("says when the session could not be loaded", async () => {
    render(
      createElement(SessionCard, {
        session: session({ prompts: [] }),
        source: { loadSession: () => Promise.reject(new Error("no")) },
        defaultOpen: true,
      }),
    );
    await settle();
    expect(container.textContent).toContain("This session could not be loaded.");
  });

  it("opens a prompt's agent activity with its messages, tool calls and output", async () => {
    const loadTurnActivity = vi.fn(async () => activity);
    const source: SessionCardSource = { loadTurnActivity };
    render(createElement(SessionCard, { session: session(), source, defaultOpen: true }));
    act(() => button("Agent activity").click());
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledWith("run-1", "turn-1");
    const text = container.textContent ?? "";
    expect(text).toContain("Reading the client.");
    expect(text).toContain("read · 1 call");
    expect(text).toContain('{"file_path":"client.go"}');
    expect(text).toContain("Output");
    expect(text).toContain("package client\n…");
  });

  it("fetches a prompt's full text when it is expanded", async () => {
    const preview = "x".repeat(300);
    const loadPromptText = vi.fn(async () => ({ text: `${preview} and the rest` }));
    render(
      createElement(SessionCard, {
        session: session({
          prompts: [prompt(1, preview, { truncated: true, text_length: 5000 })],
        }),
        source: { loadPromptText },
        defaultOpen: true,
      }),
    );
    expect(container.textContent).toContain("Showing the first 300 of 5K characters.");
    act(() => button("Show all").click());
    await settle();
    expect(loadPromptText).toHaveBeenCalledWith("run-1", "turn-1");
    expect(container.textContent).toContain("and the rest");
  });

  it("marks and scrolls to the turn it is sent to", () => {
    render(createElement(SessionCard, { session: session(), defaultOpen: true, focusTurn: 2 }));
    const current = container.querySelector('[aria-current="step"]');
    expect(current?.textContent).toContain("Now test it");
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
  });

  it("says when prompts were left out", () => {
    render(
      createElement(SessionCard, {
        session: session({ prompts_truncated: true }),
        defaultOpen: true,
      }),
    );
    expect(container.textContent).toContain("This session has more prompts than are shown here.");
  });
});
