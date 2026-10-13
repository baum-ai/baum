import { describe, expect, it } from "vitest";

import {
  effectFacts,
  endsWithResponse,
  followupLabel,
  fullReplyText,
  missingResponseNote,
  redactedSpawnMessage,
  spawnStatusLabel,
  timeline,
  type TranscriptActivity as TurnActivity,
  type TranscriptMessage as TurnMessage,
  type TranscriptReply as AgentReply,
  type TranscriptSubagent as TurnSubagent,
  type TranscriptSubagentActivity as SubagentActivity,
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

const subagent = (toolCallId: string, timestamp: string | null): TurnSubagent => ({
  tool_call_id: toolCallId,
  name: "spawn_agent",
  task: `task of ${toolCallId}`,
  input: "{}",
  input_truncated: false,
  status: "ok",
  timestamp,
  session_id: "",
  started_at: null,
  ended_at: null,
  response: "",
  response_length: 0,
  response_truncated: false,
  tokens: 0,
  estimated_tokens: 0,
  cost_usd: null,
  cost_is_floor: false,
  models: [],
  tool_calls: 0,
  failed_calls: 0,
});

const activity = (
  messages: TurnMessage[],
  toolCalls: TurnToolCall[],
  subagents: TurnSubagent[] = [],
): TurnActivity => ({
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
  subagents,
});

const shape = (entries: ReturnType<typeof timeline>) =>
  entries.map((entry) =>
    entry.kind === "message"
      ? entry.message.text
      : entry.kind === "subagent"
        ? `subagent ${entry.subagent.tool_call_id}`
        : entry.calls.map((c) => c.id),
  );

const label = (text: string, origin = "system") => followupLabel({ text, origin });
const named = (text: string, subagent_task?: string) =>
  followupLabel({ text, origin: "system", subagent_task }).label;

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
    expect(shape(entries)).toEqual(["Looking", ["read", "edit"], "Done", ["late"]]);
  });

  it("keeps undated events after the dated ones, in the order they came", () => {
    const entries = timeline(
      activity(
        [message("undated", null), message("first", "2026-07-28T10:00:00Z")],
        [call("unknown", "read", null)],
      ),
    );
    expect(shape(entries)).toEqual(["first", "undated", ["unknown"]]);
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

describe("timeline with subagents", () => {
  it("puts a subagent where its spawn call was, splitting the run around it", () => {
    const entries = timeline(
      activity(
        [message("Done", "2026-07-28T10:05:00Z")],
        [
          call("read", "read", "2026-07-28T10:01:00Z"),
          call("spawn", "spawn_subagent", "2026-07-28T10:02:00Z"),
          call("edit", "edit", "2026-07-28T10:03:00Z"),
        ],
        [subagent("spawn", "2026-07-28T10:02:00Z")],
      ),
    );
    expect(shape(entries)).toEqual([["read"], "subagent spawn", ["edit"], "Done"]);
  });

  it("places a subagent whose call is not listed by its own time", () => {
    const entries = timeline(
      activity(
        [message("Looking", "2026-07-28T10:00:00Z"), message("Done", "2026-07-28T10:05:00Z")],
        [
          call("read", "read", "2026-07-28T10:01:00Z"),
          call("edit", "edit", "2026-07-28T10:03:00Z"),
        ],
        [subagent("beyond-cap", "2026-07-28T10:02:00Z"), subagent("undated", null)],
      ),
    );
    expect(shape(entries)).toEqual([
      "Looking",
      ["read"],
      "subagent beyond-cap",
      ["edit"],
      "Done",
      "subagent undated",
    ]);
  });

  it("keeps consecutive spawns as separate subagents", () => {
    const entries = timeline(
      activity(
        [],
        [
          call("a", "spawn_subagent", "2026-07-28T10:01:00Z"),
          call("b", "spawn_subagent", "2026-07-28T10:01:00Z"),
        ],
        [subagent("b", "2026-07-28T10:01:00Z"), subagent("a", "2026-07-28T10:01:00Z")],
      ),
    );
    expect(shape(entries)).toEqual(["subagent a", "subagent b"]);
  });

  it("matches a spawn by its call's id, whatever verb the call was given", () => {
    const entries = timeline(
      activity(
        [],
        [
          call("read", "read", "2026-07-28T10:01:00Z"),
          call("codex-spawn", "other", "2026-07-28T10:02:00Z"),
          call("unrelated", "other", "2026-07-28T10:03:00Z"),
        ],
        [subagent("codex-spawn", "2026-07-28T10:02:00Z")],
      ),
    );
    expect(shape(entries)).toEqual([["read"], "subagent codex-spawn", ["unrelated"]]);
  });

  it("reads a host that records no subagents as having none", () => {
    const { subagents: _, ...bare } = activity([], [call("read", "read", null)]);
    expect(shape(timeline(bare))).toEqual([["read"]]);
  });
});

describe("endsWithResponse", () => {
  const replied = { ...subagent("spawn", null), response: "Found one problem" };
  const detail = (messages: TurnMessage[], truncated = false): SubagentActivity => ({
    turns: [
      {
        input: {
          turn_id: "input",
          ordinal: 1,
          text: "Review",
          text_length: 6,
          truncated: false,
          started_at: null,
          origin: "agent",
          kind: "prompt",
        },
        activity: { ...activity(messages, []), messages_truncated: truncated },
      },
    ],
  });

  it("holds when the last message carries the whole response", () => {
    expect(
      endsWithResponse(replied, detail([message("Found one problem, in filters.ts", null)])),
    ).toBe(true);
  });

  it("holds when the last message leads with whitespace the trimmed response lacks", () => {
    expect(
      endsWithResponse(replied, detail([message("\n\nFound one problem, in filters.ts", null)])),
    ).toBe(true);
  });

  it("does not hold when the last message differs, was cut off or there is none", () => {
    expect(endsWithResponse(replied, detail([message("Something else", null)]))).toBe(false);
    expect(endsWithResponse(replied, detail([message("Found one problem", null)], true))).toBe(
      false,
    );
    expect(endsWithResponse(replied, { turns: [] })).toBe(false);
    expect(endsWithResponse({ ...replied, response: "" }, detail([message("Found", null)]))).toBe(
      false,
    );
  });
});

describe("followupLabel", () => {
  it("names what woke the agent from how the input begins, else by who sent it", () => {
    expect(label("\n<task-notification>\n<task-id>a1</task-id>")).toEqual({
      label: "Task notification",
      wrapper: false,
    });
    expect(
      label(
        'Another Claude session sent a message:\n<agent-message from="a1b2">\n[Subagent hand-back] The report follows:\n  Done\n</agent-message>',
      ),
    ).toEqual({ label: "Subagent report", wrapper: false });
    expect(label('<agent-message from="a1b2">\nStatus: still going.\n</agent-message>').label).toBe(
      "Agent message",
    );
    expect(label('<agent-message from="">Done</agent-message>').label).toBe("Agent message");
    expect(label("Carry on", "agent").label).toBe("Agent input");
    expect(label("Session resumed").label).toBe("System input");
    expect(label("What changed?", "human").label).toBe("Input");
  });

  it("names a subagent by the task it was spawned for, never by its agent id", () => {
    const handBack =
      '<agent-message from="ac144ce280eaabd7b">\n[Subagent hand-back] The report follows:\n  Done\n</agent-message>';
    expect(named(handBack, "Review step 1 platform API")).toBe(
      "Report from “Review step 1 platform API”",
    );
    expect(named(`Another Claude session sent a message:\n${handBack}`, "Fix it")).toBe(
      "Report from “Fix it”",
    );
    expect(named('<agent-message from="a1">\nStatus: going.\n</agent-message>', "Fix it")).toBe(
      "Message from “Fix it”",
    );
    expect(named("<task-notification><task-id>a1</task-id></task-notification>", "Fix it")).toBe(
      "Notification from “Fix it”",
    );
    expect(named(handBack, "  ")).toBe("Subagent report");
    expect(named(handBack)).not.toContain("ac144ce280eaabd7b");
    // A task does not turn other inputs into reports.
    expect(named("Session resumed", "Fix it")).toBe("System input");
  });

  it("matches a report only where the input begins with it", () => {
    expect(label('Why did <agent-message from="x"> show up in the log?').label).toBe(
      "System input",
    );
    expect(label("Another Claude session sent a message: hello").label).toBe("System input");
    expect(label('<task-notification> is the tag; see <agent-message from="x">').label).toBe(
      "Task notification",
    );
  });

  it("names inputs that carry nothing anybody typed, which ask nothing of the agent", () => {
    expect(
      label(
        "<command-message>model</command-message>\n<command-name>/model</command-name>\n<command-args></command-args>",
      ),
    ).toEqual({ label: "Slash command /model", wrapper: true });
    expect(label("<command-name>/clear</command-name>").label).toBe("Slash command /clear");
    expect(label("<command-args>opus</command-args>").label).toBe("Slash command");
    expect(label("<local-command-stdout>Set model to opus</local-command-stdout>")).toEqual({
      label: "Command output",
      wrapper: true,
    });
    expect(
      label("<local-command-caveat>Caveat: the messages below…</local-command-caveat>"),
    ).toEqual({ label: "Command note", wrapper: true });
    expect(label("[Request interrupted by user for tool use]")).toEqual({
      label: "Interrupted",
      wrapper: true,
    });
    expect(label("<ide_opened_file>The user opened app.ts</ide_opened_file>")).toEqual({
      label: "Editor context",
      wrapper: true,
    });
    expect(label("  \n ")).toEqual({ label: "Empty input", wrapper: true });
  });

  it("does not treat a wrapper with words beside it as a wrapper", () => {
    expect(label("<command-name>/model</command-name> and then fix the bug", "human")).toEqual({
      label: "Input",
      wrapper: false,
    });
  });
});

describe("a subagent card without a response", () => {
  it("says a Codex record kept no report, and others that none was recorded", () => {
    expect(missingResponseNote("codex")).toBe(
      "The Codex record holds no report from this subagent.",
    );
    expect(missingResponseNote("claude_code")).toBe("No response was recorded.");
  });

  it("tells a redacted spawn message from one that only mentions it", () => {
    expect(redactedSpawnMessage('{"message": "REDACTED", "task_name": "review"}')).toBe(true);
    expect(redactedSpawnMessage('{"task_name":"review","message":"REDACTED","fork')).toBe(true);
    expect(redactedSpawnMessage('{"message": "Check REDACTED fields"}')).toBe(false);
    expect(redactedSpawnMessage('{"prompt": "say \\"message\\": \\"REDACTED\\""}')).toBe(false);
  });
});

describe("spawnStatusLabel", () => {
  it("badges only a spawn status that says something", () => {
    expect(spawnStatusLabel("ok")).toBe("");
    expect(spawnStatusLabel("unknown")).toBe("");
    expect(spawnStatusLabel("")).toBe("");
    expect(spawnStatusLabel("error")).toBe("failed");
    expect(spawnStatusLabel("no_result")).toBe("no result");
    expect(spawnStatusLabel("interrupted")).toBe("interrupted");
  });
});
