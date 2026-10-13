import pluralize from "pluralize";

import { formatCompact, formatCount } from "./components/format";

/** A session as its card and transcript draw it, and the pure parts of
 *  drawing it, which `components/session-card.tsx` and
 *  `components/transcript.tsx` render. Plain data: the host decides where it
 *  comes from (the hosted service's API, or `baum view`'s local transcript),
 *  and a host's own types need only carry these fields. */

/** What one prompt, or the whole session, caused. */
export type TranscriptEffects = {
  files_read: number;
  files_edited: number;
  /** `reads` minus `files_read` is how much re-reading the prompt did. */
  reads: number;
  diff_hunks: number;
  added_lines: number;
  removed_lines: number;
  tool_calls: number;
  failed_calls: number;
};

/** One text reply, cut to a preview like a prompt. The whole text is in the
 *  turn's activity. */
export type TranscriptReply = {
  text: string;
  text_length: number;
  truncated: boolean;
  timestamp: string | null;
  /** Empty when the invocation did not record a model. */
  model: string;
};

export type TranscriptPrompt = {
  turn_id: string;
  ordinal: number;
  started_at: string | null;
  text: string;
  /** The true length, so the card says what it is not showing. */
  text_length: number;
  /** `text` is a preview of a longer prompt. */
  truncated: boolean;
  /** Null when the session recorded no inner trace: unknown, not zero. */
  effects: TranscriptEffects | null;
  cost_usd: number | null;
  /** Part of the prompt's usage has no price: `cost_usd` is a lower bound,
   *  or null when none of it is priced. */
  cost_is_floor: boolean;
  tokens: number;
  estimated_tokens: number;
  /** The agent's text replies to this prompt, oldest first. Empty unless the
   *  session's `replies_included` says they were sent. */
  replies: TranscriptReply[];
  /** Earlier text replies of this prompt left out of `replies`. */
  replies_omitted: number;
};

export type TranscriptSession = {
  run_id: string;
  /** Attribution is visible but the session's content is not. */
  restricted?: boolean;
  /** The agent's vendor slug, such as `claude_code`. */
  source: string;
  title: string;
  git_branch: string;
  working_dir: string;
  models: string[];
  started_at: string | null;
  ended_at: string | null;
  outcome: string;
  /** Who typed the prompts; empty where it is not to be shown. */
  user_email: string;
  /** The session's human prompts, which can exceed `prompts.length`. */
  turn_count: number;
  commit_count: number;
  /** The link's confidence; below 0.7 the card says so. */
  confidence: number;
  /** Empty when the host loads them as the card opens (`loadSession`). */
  prompts: TranscriptPrompt[];
  prompts_truncated: boolean;
  /** The prompts carry the agent's replies. */
  replies_included: boolean;
  totals: TranscriptEffects | null;
  /** Activity tied to no prompt. */
  unattributed: TranscriptEffects | null;
  tokens: number;
  estimated_tokens: number;
  cost_usd: number | null;
  /** Part of the session's usage has no price. */
  cost_is_floor: boolean;
  /** False when usage was reported once for the whole session, in which case
   *  every prompt's cost is null rather than zero. */
  cost_per_prompt: boolean;
  /** False when only the outcome and usage were recorded. */
  has_inner_trace: boolean;
};

export type TranscriptMessage = {
  text: string;
  timestamp: string | null;
  model: string;
  /** The true length, when the host cut `text`. */
  text_length?: number;
  truncated?: boolean;
};

export type TranscriptToolCall = {
  id: string;
  name: string;
  /** Our tool category, such as `read` or `spawn_subagent`. */
  verb: string;
  input: string;
  input_truncated: boolean;
  /** The tool's output, when the host has it. */
  output?: string | null;
  output_truncated?: boolean;
  status: string;
  timestamp: string | null;
};

export type TranscriptActivity = {
  messages: TranscriptMessage[];
  messages_truncated: boolean;
  /** The model calls; the transcript only asks whether there were any. */
  invocations: readonly unknown[];
  token_usage: {
    tokens: number;
    estimated_tokens: number;
    uncached_input_tokens: number;
    output_tokens: number;
    api_calls: number;
    models?: string[] | null;
  };
  tool_calls: TranscriptToolCall[];
  tool_calls_truncated: boolean;
  /** The subagents the turn spawned, each drawn as a card where its spawn
   *  call happened; none when the host records none. */
  subagents?: TranscriptSubagent[];
  /** For a prompt, the inputs after it and up to the next prompt shown — a
   *  task notification, a subagent's report handed back, a slash command —
   *  each with what the agent did about it. */
  followups?: TranscriptTurn[];
  followups_truncated?: boolean;
};

/** One subagent a turn spawned. Its totals cover it and every subagent it
 *  spawned in turn, and are all zero when no transcript of its own was
 *  linked, which an empty `session_id` says. */
export type TranscriptSubagent = {
  /** The spawn call's id among the activity's tool calls. */
  tool_call_id: string;
  /** The spawn tool's name: `spawn_agent`, `Task`, `Agent`. */
  name: string;
  /** What it was asked to do, in a few words; empty when the spawn named
   *  nothing. */
  task: string;
  input: string;
  input_truncated: boolean;
  status: string;
  timestamp: string | null;
  session_id: string;
  started_at: string | null;
  ended_at: string | null;
  /** What it reported back to the parent; empty when nothing was recorded. */
  response: string;
  response_length: number;
  response_truncated: boolean;
  tokens: number;
  estimated_tokens: number;
  /** Null when none of the usage is priced. */
  cost_usd: number | null;
  cost_is_floor: boolean;
  models: string[];
  tool_calls: number;
  failed_calls: number;
};

/** An input other than a prompt shown in the list: the instruction a
 *  subagent was given, or what woke the agent after a prompt. */
export type TranscriptInput = {
  turn_id: string;
  ordinal: number;
  text: string;
  text_length: number;
  truncated: boolean;
  started_at: string | null;
  origin: string;
  kind: string;
  /** For a follow-up that is a subagent's report, message or notification,
   *  the task it was spawned for, when the host knows it. */
  subagent_task?: string;
};

/** An input and what the agent did about it. */
export type TranscriptTurn = { input: TranscriptInput; activity: TranscriptActivity };

/** One subagent's own conversation, an entry per input. */
export type TranscriptSubagentActivity = { turns: TranscriptTurn[] };

/** Writes a dollar figure, given whether it is a lower bound; null for no
 *  price. A host without prices passes none, and no cost is shown. */
export type CostFormat = (cost: number | null, floor: boolean) => string | null;

/** Where a card and its transcript get what the session did not arrive with.
 *  Each loader is optional, and leaving one out turns its feature off:
 *  without `loadSession` a card shows the session it was given, without
 *  `loadPromptText` a prompt preview stays a preview, and without
 *  `loadTurnActivity` there is no agent activity to open, and without
 *  `loadSubagentActivity` a subagent card shows its response and totals but
 *  does not open onto its conversation. `loadSubagentActivity` is given the
 *  root session's run id; a rejection whose `status` is 404 reads as nothing
 *  captured rather than as a failure to retry. A source the host rebuilds on
 *  every render loads again, so keep it stable. */
export type TranscriptSource = {
  loadSession?: (runId: string) => Promise<TranscriptSession>;
  loadPromptText?: (runId: string, turnId: string) => Promise<{ text: string }>;
  loadTurnActivity?: (runId: string, turnId: string) => Promise<TranscriptActivity>;
  loadSubagentActivity?: (runId: string, sessionId: string) => Promise<TranscriptSubagentActivity>;
};

export type ActivityState = TranscriptActivity | "loading" | "failed" | null;

/** A reply's whole text once the turn's activity has arrived. A reply is a
 *  prefix of its message, so the message is the one at the same instant that
 *  starts with it. */
export function fullReplyText(reply: TranscriptReply, activity: ActivityState): string {
  if (!reply.truncated || activity === null || typeof activity === "string") return reply.text;
  const match = activity.messages.find(
    (message) => message.timestamp === reply.timestamp && message.text.startsWith(reply.text),
  );
  return match?.text ?? reply.text;
}

/** What a turn did, as one line. A fact the trace did not capture is left
 *  out rather than rendered as a dash; without `formatCost` no cost is
 *  shown at all. */
export function effectFacts(
  effects: TranscriptEffects | null,
  tokens: number,
  estimatedTokens: number,
  cost: number | null,
  costIsFloor = false,
  formatCost?: CostFormat,
): string | null {
  const costFact = () => {
    if (!formatCost) return null;
    // Part of the usage unpriced makes the figure a lower bound, and all of
    // it unpriced leaves no figure at all; neither may read as complete.
    return cost != null ? formatCost(cost, costIsFloor) : costIsFloor && "no price";
  };
  return join([
    effects && effects.files_read > 0 && `read ${pluralize("file", effects.files_read, true)}`,
    // Re-reading the same file is the exploration signal, and it disappears if
    // only the distinct count is shown.
    effects && effects.reads > effects.files_read && `${formatCount(effects.reads)} reads`,
    effects && effects.files_edited > 0 && `edited ${formatCount(effects.files_edited)}`,
    effects && effects.diff_hunks > 0 && pluralize("diff hunk", effects.diff_hunks, true),
    effects &&
      effects.added_lines + effects.removed_lines > 0 &&
      `+${formatCount(effects.added_lines)}/−${formatCount(effects.removed_lines)}`,
    effects && effects.tool_calls > 0 && pluralize("tool call", effects.tool_calls, true),
    effects && effects.failed_calls > 0 && `${formatCount(effects.failed_calls)} failed`,
    tokens > 0 && formatTokenUsage(tokens, estimatedTokens),
    costFact(),
  ]);
}

export type TimelineEntry =
  | { kind: "message"; message: TranscriptMessage }
  | { kind: "tools"; calls: TranscriptToolCall[] }
  | { kind: "subagent"; subagent: TranscriptSubagent };

type TimelineEvent = {
  at: string | null;
  index: number;
  entry: Exclude<TimelineEntry, { kind: "tools" }> | { kind: "call"; call: TranscriptToolCall };
};

/** The turn as it happened: the agent's messages with its tool calls between
 *  them, in time order, each run of calls folded into one row. A call that
 *  spawned a subagent stands on its own as that subagent, splitting the run
 *  around it; a subagent whose call is not among the listed ones, such as one
 *  past the cap, takes its place by its own time. */
export function timeline(activity: TranscriptActivity): TimelineEntry[] {
  const subagents = activity.subagents ?? [];
  const spawned = new Map(subagents.map((subagent) => [subagent.tool_call_id, subagent]));
  const placed = new Set<TranscriptSubagent>();
  const events: TimelineEvent[] = [
    ...activity.messages.map((message, index) => ({
      at: message.timestamp,
      index,
      entry: { kind: "message", message } as const,
    })),
    ...activity.tool_calls.map((call, index) => {
      const subagent = spawned.get(call.id);
      if (subagent) placed.add(subagent);
      return {
        at: call.timestamp,
        index: activity.messages.length + index,
        entry: subagent
          ? ({ kind: "subagent", subagent } as const)
          : ({ kind: "call", call } as const),
      };
    }),
  ];
  for (const subagent of subagents) {
    if (placed.has(subagent)) continue;
    events.push({
      at: subagent.timestamp,
      index: events.length,
      entry: { kind: "subagent", subagent },
    });
  }
  // Undated events keep their place after the dated ones; ties keep the
  // order each list came in.
  events.sort((a, b) => {
    if (a.at === b.at) return a.index - b.index;
    if (a.at === null) return 1;
    if (b.at === null) return -1;
    return Date.parse(a.at) - Date.parse(b.at) || a.index - b.index;
  });
  const entries: TimelineEntry[] = [];
  for (const { entry } of events) {
    if (entry.kind !== "call") {
      entries.push(entry);
      continue;
    }
    const last = entries.at(-1);
    if (last?.kind === "tools") last.calls.push(entry.call);
    else entries.push({ kind: "tools", calls: [entry.call] });
  }
  return entries;
}

/** Whether a subagent's conversation, as fetched, ends in its whole response:
 *  the last turn's last message starts with the response (as capped), and none
 *  of that turn's messages were left out. */
export function endsWithResponse(
  subagent: TranscriptSubagent,
  detail: TranscriptSubagentActivity,
): boolean {
  const last = detail.turns.at(-1)?.activity;
  if (!subagent.response || !last || last.messages_truncated) return false;
  // The response is trimmed; the conversation's text is as recorded.
  return last.messages.at(-1)?.text.trimStart().startsWith(subagent.response) ?? false;
}

/** The badge for a spawn call's status, which is the call's and not how the
 *  subagent's work turned out; empty where a badge would say nothing, for a
 *  spawn that went through or whose status is unknown. */
export function spawnStatusLabel(status: string): string {
  switch (status) {
    case "":
    case "ok":
    case "unknown":
      return "";
    case "error":
      return "failed";
    case "no_result":
      return "no result";
    default:
      return status.replaceAll("_", " ");
  }
}

/** What a subagent card says in place of a response it has none of. Codex
 *  keeps a child's report only in the parent's full record, so its absence
 *  there is how that record was kept, not a failure. */
export function missingResponseNote(vendor: string): string {
  return vendor === "codex"
    ? "The Codex record holds no report from this subagent."
    : "No response was recorded.";
}

/** Whether a spawn's input is one whose message the agent recorded redacted,
 *  as Codex does for the instructions it gives a subagent. An input the host
 *  cut short is read up to where it stops. */
export function redactedSpawnMessage(input: string): boolean {
  try {
    const parsed: unknown = JSON.parse(input);
    return (parsed as { message?: unknown } | null)?.message === "REDACTED";
  } catch {
    return /^\s*\{(?:\s*"[^"\\]*"\s*:\s*"[^"\\]*"\s*,)*\s*"message"\s*:\s*"REDACTED"/.test(input);
  }
}

const HAND_BACK = "Another Claude session sent a message:";

/** The wrappers Claude Code writes into an input that carry nothing anybody
 *  typed: a slash command's echo and output, the caveat before them, the
 *  IDE's open-file and selection context, and the interrupt marker. The same
 *  set the platform strips before deciding what is a prompt. */
const BOILERPLATE = [
  /<command-name>[\s\S]*?<\/command-name>/g,
  /<command-message>[\s\S]*?<\/command-message>/g,
  /<command-args>[\s\S]*?<\/command-args>/g,
  /<local-command-stdout>[\s\S]*?<\/local-command-stdout>/g,
  /<local-command-caveat>[\s\S]*?<\/local-command-caveat>/g,
  /<ide_opened_file>[\s\S]*?<\/ide_opened_file>/g,
  /<ide_selection>[\s\S]*?<\/ide_selection>/g,
  /\[Request interrupted by user[^\]]*\]/g,
];

function isBoilerplate(text: string): boolean {
  return BOILERPLATE.reduce((rest, pattern) => rest.replace(pattern, ""), text).trim() === "";
}

/** A hand-back's frame, at the start of a line; an agent's message without
 *  it is one it sent of its own accord. */
const HAND_BACK_FRAME = /^\[Subagent hand-back\]/m;

/** What woke the agent with a follow-up input, from how the input reads: a
 *  background task's notification, a subagent's report handed back or a
 *  message it sent, a slash command or its output, an interruption, editor
 *  context, or else who sent it. A subagent is named by the task it was
 *  spawned for when the host knows it, never by its agent id. `wrapper`
 *  marks an input that carries nothing anybody typed, which the agent need
 *  not answer. */
export function followupLabel(input: { text: string; origin: string; subagent_task?: string }): {
  label: string;
  wrapper: boolean;
} {
  const text = input.text.trimStart();
  const task = input.subagent_task?.trim();
  if (!text) return { label: "Empty input", wrapper: true };
  if (text.startsWith("<task-notification>"))
    return { label: task ? `Notification from “${task}”` : "Task notification", wrapper: false };
  const message = text.startsWith(HAND_BACK) ? text.slice(HAND_BACK.length).trimStart() : text;
  if (message.startsWith('<agent-message from="')) {
    const report = HAND_BACK_FRAME.test(message);
    const label = task
      ? `${report ? "Report" : "Message"} from “${task}”`
      : report
        ? "Subagent report"
        : "Agent message";
    return { label, wrapper: false };
  }
  if (isBoilerplate(text)) {
    const command = /<command-name>\s*([\s\S]*?)\s*<\/command-name>/.exec(text)?.[1];
    if (command) return { label: `Slash command ${command}`, wrapper: true };
    if (/^<command-(message|args)>/.test(text)) return { label: "Slash command", wrapper: true };
    if (text.startsWith("<local-command-stdout>"))
      return { label: "Command output", wrapper: true };
    if (text.startsWith("<local-command-caveat>")) return { label: "Command note", wrapper: true };
    if (text.startsWith("[Request interrupted")) return { label: "Interrupted", wrapper: true };
    return { label: "Editor context", wrapper: true };
  }
  // A subagent's message whose text names nobody; the host knows its sender.
  if (task) return { label: `Message from “${task}”`, wrapper: false };
  const label =
    input.origin === "agent" ? "Agent input" : input.origin === "human" ? "Input" : "System input";
  return { label, wrapper: false };
}

export function formatTokenUsage(tokens: number, estimatedTokens: number): string {
  const detail = estimatedTokenDetail(tokens, estimatedTokens);
  return `${formatCompact(tokens)} tokens${detail ? ` (${detail})` : ""}`;
}

export function estimatedTokenDetail(tokens: number, estimatedTokens: number): string | undefined {
  if (estimatedTokens <= 0) return undefined;
  return estimatedTokens >= tokens ? "estimated" : `${formatCompact(estimatedTokens)} estimated`;
}

export function join(parts: (string | false | null | undefined)[]): string | null {
  const kept = parts.filter(Boolean) as string[];
  return kept.length > 0 ? kept.join(" · ") : null;
}
