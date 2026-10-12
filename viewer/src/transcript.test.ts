import { describe, expect, it } from "vitest";

import {
  effectFacts,
  fullReplyText,
  timeline,
  type TranscriptActivity as TurnActivity,
  type TranscriptMessage as TurnMessage,
  type TranscriptReply as AgentReply,
  type TranscriptToolCall as TurnToolCall,
} from "./transcript";

const message = (text: string, timestamp: string | null): TurnMessage => ({
  text,
  timestamp,
  model: "gpt-5-codex",
});

const call = (id: string, verb: string, timestamp: string | null): TurnToolCall => ({
  id,
  name: verb,
  verb,
  input: "{}",
  input_truncated: false,
  status: "ok",
  timestamp,
});

const activity = (messages: TurnMessage[], toolCalls: TurnToolCall[]): TurnActivity => ({
  messages,
  messages_truncated: false,
  invocations: [],
  token_usage: {
    tokens: 0,
    estimated_tokens: 0,
    uncached_input_tokens: 0,
    output_tokens: 0,
    api_calls: 0,
    models: [],
  },
  tool_calls: toolCalls,
  tool_calls_truncated: false,
});

describe("timeline", () => {
  it("puts tool calls between the messages they happened between, one run per gap", () => {
    const entries = timeline(
      activity(
        [message("Looking", "2026-07-28T10:00:00Z"), message("Done", "2026-07-28T10:05:00Z")],
        [
          call("read", "read", "2026-07-28T10:01:00Z"),
          call("edit", "edit", "2026-07-28T10:02:00Z"),
          call("late", "execute", "2026-07-28T10:06:00Z"),
        ],
      ),
    );
    expect(
      entries.map((entry) =>
        entry.kind === "message" ? entry.message.text : entry.calls.map((c) => c.id),
      ),
    ).toEqual(["Looking", ["read", "edit"], "Done", ["late"]]);
  });

  it("keeps undated events after the dated ones, in the order they came", () => {
    const entries = timeline(
      activity(
        [message("undated", null), message("first", "2026-07-28T10:00:00Z")],
        [call("unknown", "read", null)],
      ),
    );
    expect(
      entries.map((entry) => (entry.kind === "message" ? entry.message.text : entry.calls[0]!.id)),
    ).toEqual(["first", "undated", "unknown"]);
  });
});

describe("fullReplyText", () => {
  const preview: AgentReply = {
    text: "The start",
    text_length: 40,
    truncated: true,
    timestamp: "2026-07-28T10:04:00Z",
    model: "gpt-5-codex",
  };

  it("swaps a preview for its message once the activity has it", () => {
    const loaded = activity(
      [
        message("The start of something else", "2026-07-28T10:03:00Z"),
        message("The start, and the rest of the reply", "2026-07-28T10:04:00Z"),
      ],
      [],
    );
    expect(fullReplyText(preview, loaded)).toBe("The start, and the rest of the reply");
  });

  it("keeps the preview while the activity is missing or lacks the message", () => {
    expect(fullReplyText(preview, "loading")).toBe("The start");
    expect(fullReplyText(preview, activity([], []))).toBe("The start");
  });
});

const format = (cost: number | null, floor: boolean) =>
  cost == null ? null : `${floor ? "at least " : ""}$${cost}`;

describe("effectFacts", () => {
  it("leaves out what was not recorded and keeps what was", () => {
    expect(
      effectFacts(
        {
          files_read: 2,
          files_edited: 1,
          reads: 5,
          diff_hunks: 0,
          added_lines: 3,
          removed_lines: 1,
          tool_calls: 4,
          failed_calls: 0,
        },
        1200,
        1200,
        null,
      ),
    ).toBe("read 2 files · 5 reads · edited 1 · +3/−1 · 4 tool calls · 1.2K tokens (estimated)");
    expect(effectFacts(null, 0, 0, null)).toBeNull();
  });

  it("never shows a prompt's partly or wholly unpriced cost as complete", () => {
    // The host writes the figure, and tests its own format with its card.
    expect(effectFacts(null, 0, 0, 0.5, false, format)).toBe("$0.5");
    expect(effectFacts(null, 0, 0, 0.5, true, format)).toBe("at least $0.5");
    expect(effectFacts(null, 0, 0, null, true, format)).toBe("no price");
  });

  it("shows no cost at all for a host without prices", () => {
    expect(effectFacts(null, 0, 0, 0.5, true)).toBeNull();
    expect(effectFacts(null, 0, 0, null, true)).toBeNull();
  });
});
