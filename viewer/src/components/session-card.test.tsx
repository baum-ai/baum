// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import type {
  TranscriptActivity,
  TranscriptPrompt,
  TranscriptReply,
  TranscriptSession,
  TranscriptSource,
} from "../transcript";
import { SessionCard } from "./session-card";

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

const reply = (text: string, overrides: Partial<TranscriptReply> = {}): TranscriptReply => ({
  text,
  text_length: text.length,
  truncated: false,
  timestamp: "2026-07-01T10:05:00Z",
  model: "claude-opus",
  ...overrides,
});

function prompt(ordinal: number, text: string, overrides: Partial<TranscriptPrompt> = {}) {
  return {
    turn_id: `turn-${ordinal}`,
    ordinal,
    started_at: "2026-07-01T10:00:00Z",
    text,
    text_length: text.length,
    truncated: false,
    effects,
    cost_usd: 0.5,
    cost_is_floor: false,
    tokens: 1200,
    estimated_tokens: 0,
    replies: ordinal === 1 ? [reply("The budget is wired.")] : [],
    replies_omitted: 0,
    ...overrides,
  } satisfies TranscriptPrompt;
}

function session(overrides: Partial<TranscriptSession> = {}): TranscriptSession {
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
    replies_included: true,
    totals: effects,
    unattributed: null,
    tokens: 2400,
    estimated_tokens: 0,
    cost_usd: 1.25,
    cost_is_floor: false,
    cost_per_prompt: true,
    has_inner_trace: true,
    ...overrides,
  };
}

const activity: TranscriptActivity = {
  messages: [
    { text: "Reading the client.", timestamp: "2026-07-01T10:01:00Z", model: "claude-opus" },
    {
      text: "The budget is wired, and the rest of the reply.",
      timestamp: "2026-07-01T10:05:00Z",
      model: "claude-opus",
    },
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
      timestamp: "2026-07-01T10:02:00Z",
    },
  ],
  tool_calls_truncated: false,
};

const usd = (cost: number | null, floor: boolean) =>
  cost == null ? null : `${floor ? "at least " : ""}$${cost.toFixed(2)}`;

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
    (b.getAttribute("aria-label") ?? b.textContent ?? "").includes(name),
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
  it("keeps identifying metadata in the session toggle's accessible text", () => {
    render(createElement(SessionCard, { session: session({ title: "" }), defaultOpen: false }));
    const header = document.querySelector<HTMLButtonElement>("[data-session-id] > button")!;
    expect(header.hasAttribute("aria-label")).toBe(false);
    for (const text of [
      "Coding session",
      "Claude Code",
      "claude-opus",
      "feature/retry",
      "30m",
      "2 prompts",
      "1 commit",
    ]) {
      expect(header.textContent).toContain(text);
    }
  });

  it("shows a restricted card with no usage, and the host's reason", () => {
    const restricted = session({
      restricted: true,
      title: "",
      prompts: [],
      totals: null,
      tokens: 0,
      cost_usd: null,
      has_inner_trace: false,
    });
    expect(
      renderToStaticMarkup(createElement(SessionCard, { session: restricted, defaultOpen: true })),
    ).toContain("prompts, metadata, usage, and activity are not shown here.");
    const html = renderToStaticMarkup(
      createElement(SessionCard, {
        session: restricted,
        defaultOpen: true,
        formatCost: usd,
        restrictedNote: "Only readers of every repository see it.",
      }),
    );
    expect(html).toContain("Details restricted");
    expect(html).toContain("Only readers of every repository see it.");
    for (const hidden of ["Tokens", "Cost", "Files read", "0 prompts", "$"]) {
      expect(html).not.toContain(hidden);
    }
  });

  it("shows the prompts and replies as a conversation, with each turn's effects", () => {
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
    expect(container.querySelectorAll('[data-message="human"]')).toHaveLength(2);
    expect(text).toContain("Add a retry budget");
    expect(text).toContain("The budget is wired.");
    expect(text).toContain("No text reply was recorded.");
    expect(text).toContain("read 3 files");
    expect(text).toContain("5 reads");
    expect(text).toContain("+10/−4");
    expect(text).not.toContain("~/src/widgets");
    expect(container.querySelector('[title="Branch: feature/retry"]')).not.toBeNull();
    expect([...container.querySelectorAll("dt")].map((label) => label.textContent)).not.toContain(
      "Commits",
    );
    // Without a loader there is no activity to open, and without a cost
    // format no cost anywhere.
    expect(text).not.toContain("Agent activity");
    expect(text).not.toContain("Cost");
    expect(text).not.toContain("$");
  });

  it("shows the session's and each prompt's cost in the host's format", () => {
    render(
      createElement(SessionCard, {
        session: session({ cost_is_floor: true }),
        defaultOpen: true,
        formatCost: usd,
      }),
    );
    const text = container.textContent ?? "";
    expect(text).toContain("Cost");
    expect(text).toContain("at least $1.25");
    expect(text).toContain("lower bound · some usage has no price");
    expect(text).toContain("$0.50");
  });

  it("draws the host's picture beside a person's prompt, and none without one", () => {
    const renderAvatar = vi.fn((author: string) => createElement("img", { alt: author }));
    render(
      createElement(SessionCard, {
        session: session({ user_email: "ada@example.com" }),
        defaultOpen: true,
        renderAvatar,
      }),
    );
    expect(renderAvatar).toHaveBeenCalledWith("ada@example.com");
    expect(container.querySelectorAll('img[alt="ada@example.com"]')).toHaveLength(2);
    act(() => root!.unmount());
    render(createElement(SessionCard, { session: session(), defaultOpen: true }));
    expect(container.querySelector("img")).toBeNull();
  });

  it("loads a session that arrived without its prompts when it opens", async () => {
    const loadSession = vi.fn(async () => session());
    render(
      createElement(SessionCard, {
        session: session({ prompts: [], replies_included: false }),
        source: { loadSession },
        defaultOpen: false,
      }),
    );
    expect(loadSession).not.toHaveBeenCalled();
    act(() => button("Session").click());
    expect(container.textContent).toContain("Loading this session…");
    await settle();
    expect(loadSession).toHaveBeenCalledWith("run-1");
    expect(container.textContent).toContain("The budget is wired.");
  });

  it("shows a report's prompts at once and their replies once the session arrives", async () => {
    let resolve!: (value: TranscriptSession) => void;
    const arrival = new Promise<TranscriptSession>((done) => {
      resolve = done;
    });
    const loadSession = vi.fn(() => arrival);
    const report = session({
      replies_included: false,
      prompts: [prompt(1, "Add a retry budget", { replies: [] })],
    });
    render(
      createElement(SessionCard, { session: report, source: { loadSession }, defaultOpen: true }),
    );
    expect(container.textContent).toContain("Add a retry budget");
    expect(container.textContent).toContain("Loading the agent's replies…");
    await act(async () => resolve(session()));
    expect(container.textContent).toContain("The budget is wired.");
    expect(container.textContent).not.toContain("Loading the agent's replies…");
  });

  it("says when the session could not be loaded", async () => {
    render(
      createElement(SessionCard, {
        session: session({ prompts: [], replies_included: false }),
        source: { loadSession: () => Promise.reject(new Error("no")) },
        defaultOpen: true,
      }),
    );
    await settle();
    expect(container.textContent).toContain("This session could not be loaded.");
  });

  it("opens a prompt's agent activity with its messages, tool calls and output", async () => {
    const loadTurnActivity = vi.fn(async () => activity);
    const source: TranscriptSource = { loadTurnActivity };
    render(createElement(SessionCard, { session: session(), source, defaultOpen: true }));
    act(() => button("Agent activity").click());
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledWith("run-1", "turn-1");
    const text = container.textContent ?? "";
    expect(text).toContain("Reading the client.");
    expect(text).toContain("1 model call");
    expect(text).toContain("1 tool call · read");
    const activityRegion = container.querySelector(
      '[role="region"][aria-label="Agent activity for prompt 1"]',
    )!;
    expect(activityRegion.querySelector("details")?.open).toBe(true);
    expect(activityRegion.querySelector("summary")?.className).not.toContain("mx-auto");
    expect(text).toContain('{"file_path":"client.go"}');
    expect(text).toContain("Output");
    expect(text).toContain("package client\n…");
  });

  it("loads an opened turn's activity again for a new revision, keeping the card open", async () => {
    const grown: TranscriptActivity = {
      ...activity,
      messages: [
        ...activity.messages,
        { text: "Running the tests.", timestamp: "2026-07-01T10:06:00Z", model: "claude-opus" },
      ],
    };
    const loadTurnActivity = vi.fn(async () => activity);
    const card = (revision: string | undefined, load = loadTurnActivity) =>
      createElement(SessionCard, {
        session: session(),
        source: { loadTurnActivity: load, revision },
        defaultOpen: true,
      });
    render(card("v1"));
    act(() => button("Agent activity").click());
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledTimes(1);
    // The same revision loads nothing more.
    act(() => root!.render(card("v1")));
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledTimes(1);
    const again = vi.fn(async () => grown);
    act(() => root!.render(card("v2", again)));
    await settle();
    expect(again).toHaveBeenCalledWith("run-1", "turn-1");
    expect(container.textContent).toContain("Running the tests.");
    expect(button("Agent activity").getAttribute("aria-expanded")).toBe("true");
    // A turn whose activity was never opened is not fetched.
    expect(again).toHaveBeenCalledTimes(1);
  });

  it("loads a turn's activity once without a revision", async () => {
    const loadTurnActivity = vi.fn(async () => activity);
    const card = () =>
      createElement(SessionCard, {
        session: session(),
        source: { loadTurnActivity },
        defaultOpen: true,
      });
    render(card());
    act(() => button("Agent activity").click());
    await settle();
    act(() => root!.render(card()));
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledTimes(1);
  });

  it("opens activity below its toggle and explains when no visible output was recorded", async () => {
    const loadTurnActivity = vi.fn(async () => ({
      ...activity,
      messages: [],
      tool_calls: [],
      subagents: [],
      followups: [],
      invocations: [{}],
    }));
    render(
      createElement(SessionCard, {
        session: session({ cost_per_prompt: false }),
        source: { loadTurnActivity },
        defaultOpen: true,
      }),
    );
    const toggle = button("Agent activity");
    const replyElement = [...container.querySelectorAll("p")].find(
      (node) => node.textContent === "The budget is wired.",
    );
    expect(replyElement).toBeDefined();
    act(() => toggle.click());
    await settle();
    const region = container.querySelector(
      '[role="region"][aria-label="Agent activity for prompt 1"]',
    )!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(container.textContent).toContain("The budget is wired.");
    expect(replyElement!.isConnected).toBe(true);
    expect(toggle.compareDocumentPosition(region) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(region.textContent).toContain("No agent output was captured for this prompt.");
    act(() => toggle.click());
    expect(
      container.querySelector('[role="region"][aria-label="Agent activity for prompt 1"]'),
    ).toBeNull();
    expect(container.textContent).toContain("The budget is wired.");
  });

  it("swaps a reply's preview for its whole message from the activity", async () => {
    const loadTurnActivity = vi.fn(async () => activity);
    render(
      createElement(SessionCard, {
        session: session({
          prompts: [
            prompt(1, "Add a retry budget", {
              replies: [reply("The budget is wired", { truncated: true, text_length: 47 })],
            }),
          ],
        }),
        source: { loadTurnActivity },
        defaultOpen: true,
      }),
    );
    act(() => button("Show all").click());
    await settle();
    expect(loadTurnActivity).toHaveBeenCalledWith("run-1", "turn-1");
    expect(container.textContent).toContain("The budget is wired, and the rest of the reply.");
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
    expect(container.textContent).toContain("5K characters");
    act(() => button("Show all").click());
    await settle();
    expect(loadPromptText).toHaveBeenCalledWith("run-1", "turn-1");
    expect(container.textContent).toContain("and the rest");
  });

  it("keeps a prompt's preview and says so without a loader", () => {
    const preview = "x".repeat(300);
    render(
      createElement(SessionCard, {
        session: session({
          prompts: [prompt(1, preview, { truncated: true, text_length: 5000 })],
        }),
        defaultOpen: true,
      }),
    );
    act(() => button("Show all").click());
    expect(container.textContent).toContain("Showing the first 300 of 5K characters.");
  });

  it("marks and scrolls to the turn it is sent to", () => {
    render(createElement(SessionCard, { session: session(), defaultOpen: true, focusTurn: 2 }));
    const current = container.querySelector('[aria-current="step"]');
    expect(current?.textContent).toContain("Now test it");
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({
      block: "center",
      behavior: "instant",
    });
    expect(current?.className).not.toContain("ring-");
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
