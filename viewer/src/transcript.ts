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
};

/** Writes a dollar figure, given whether it is a lower bound; null for no
 *  price. A host without prices passes none, and no cost is shown. */
export type CostFormat = (cost: number | null, floor: boolean) => string | null;

/** Where a card and its transcript get what the session did not arrive with.
 *  Each loader is optional, and leaving one out turns its feature off:
 *  without `loadSession` a card shows the session it was given, without
 *  `loadPromptText` a prompt preview stays a preview, and without
 *  `loadTurnActivity` there is no agent activity to open. A source the host
 *  rebuilds on every render loads again, so keep it stable. */
export type TranscriptSource = {
  loadSession?: (runId: string) => Promise<TranscriptSession>;
  loadPromptText?: (runId: string, turnId: string) => Promise<{ text: string }>;
  loadTurnActivity?: (runId: string, turnId: string) => Promise<TranscriptActivity>;
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
  | { kind: "tools"; calls: TranscriptToolCall[] };

/** The turn as it happened: the agent's messages with its tool calls between
 *  them, in time order, each run of calls folded into one row. */
export function timeline(activity: TranscriptActivity): TimelineEntry[] {
  const events = [
    ...activity.messages.map((message, index) => ({
      at: message.timestamp,
      index,
      entry: { kind: "message", message } as const,
    })),
    ...activity.tool_calls.map((call, index) => ({
      at: call.timestamp,
      index: activity.messages.length + index,
      entry: { kind: "call", call } as const,
    })),
  ];
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
    if (entry.kind === "message") {
      entries.push(entry);
      continue;
    }
    const last = entries.at(-1);
    if (last?.kind === "tools") last.calls.push(entry.call);
    else entries.push({ kind: "tools", calls: [entry.call] });
  }
  return entries;
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
