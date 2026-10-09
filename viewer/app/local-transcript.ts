import type {
  SessionCardActivity,
  SessionCardEffects,
  SessionCardMessage,
  SessionCardPrompt,
  SessionCardSession,
  SessionCardToolCall,
} from "../src/components/session-card";

/** One linked session's transcript, as `GET /api/sessions/{id}` answers it:
 *  the session card's session with every prompt it shows, each prompt's
 *  activity, and whether anything was left out to keep it small. The server
 *  has redacted it and written the home directory as `~`. */
export type LocalTranscript = {
  session: SessionCardSession;
  activity: Record<string, SessionCardActivity>;
  /** Some prompts, messages, calls or long texts were left out or cut. */
  truncated: boolean;
  /** How many prompts the session has, shown or not. */
  promptsTotal: number;
  /** The text budget the server applied, in bytes. */
  limitBytes: number;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

const isString = (value: unknown): value is string => typeof value === "string";
const isNumber = (value: unknown): value is number =>
  typeof value === "number" && Number.isFinite(value);
const isBoolean = (value: unknown): value is boolean => typeof value === "boolean";
const isInstant = (value: unknown): value is string | null => value === null || isString(value);

function every(record: Record<string, unknown>, keys: string[], test: (value: unknown) => boolean) {
  return keys.every((key) => test(record[key]));
}

const EFFECT_KEYS = [
  "files_read",
  "files_edited",
  "reads",
  "diff_hunks",
  "added_lines",
  "removed_lines",
  "tool_calls",
  "failed_calls",
];

function parseEffects(value: unknown): SessionCardEffects | null | undefined {
  if (value === null) return null;
  if (!isRecord(value) || !every(value, EFFECT_KEYS, isNumber)) return undefined;
  return value as SessionCardEffects;
}

function parsePrompt(value: unknown): SessionCardPrompt | null {
  if (
    !isRecord(value) ||
    !every(value, ["turn_id", "text"], isString) ||
    !every(value, ["ordinal", "text_length", "tokens", "estimated_tokens"], isNumber) ||
    !isBoolean(value.truncated) ||
    !isInstant(value.started_at)
  ) {
    return null;
  }
  const effects = parseEffects(value.effects);
  if (effects === undefined) return null;
  return {
    turn_id: value.turn_id as string,
    ordinal: value.ordinal as number,
    started_at: value.started_at,
    text: value.text as string,
    text_length: value.text_length as number,
    truncated: value.truncated,
    effects,
    cost_usd: null,
    tokens: value.tokens as number,
    estimated_tokens: value.estimated_tokens as number,
  };
}

function parseSession(value: unknown): SessionCardSession | null {
  if (
    !isRecord(value) ||
    !every(
      value,
      ["run_id", "source", "title", "git_branch", "working_dir", "outcome"],
      isString,
    ) ||
    !every(
      value,
      ["turn_count", "commit_count", "confidence", "tokens", "estimated_tokens"],
      isNumber,
    ) ||
    !every(value, ["prompts_truncated", "cost_per_prompt", "has_inner_trace"], isBoolean) ||
    !every(value, ["started_at", "ended_at"], isInstant) ||
    !Array.isArray(value.models) ||
    !value.models.every(isString) ||
    !Array.isArray(value.prompts)
  ) {
    return null;
  }
  const prompts = value.prompts.map(parsePrompt);
  const totals = parseEffects(value.totals);
  const unattributed = parseEffects(value.unattributed);
  if (prompts.includes(null) || totals === undefined || unattributed === undefined) return null;
  // The cost is left out on purpose: this machine has no prices, and the card
  // hides the Cost figure rather than showing an empty one.
  return {
    run_id: value.run_id as string,
    source: value.source as string,
    title: value.title as string,
    git_branch: value.git_branch as string,
    working_dir: value.working_dir as string,
    models: value.models,
    started_at: value.started_at as string | null,
    ended_at: value.ended_at as string | null,
    outcome: value.outcome as string,
    // The server never sends one; the card shows none.
    user_email: "",
    turn_count: value.turn_count as number,
    commit_count: value.commit_count as number,
    confidence: value.confidence as number,
    prompts: prompts as SessionCardPrompt[],
    prompts_truncated: value.prompts_truncated as boolean,
    totals,
    unattributed,
    tokens: value.tokens as number,
    estimated_tokens: value.estimated_tokens as number,
    cost_per_prompt: value.cost_per_prompt as boolean,
    has_inner_trace: value.has_inner_trace as boolean,
  };
}

function parseMessage(value: unknown): SessionCardMessage | null {
  if (
    !isRecord(value) ||
    !every(value, ["text", "model"], isString) ||
    !isInstant(value.timestamp) ||
    !isNumber(value.text_length) ||
    !isBoolean(value.truncated)
  ) {
    return null;
  }
  return {
    text: value.text as string,
    model: value.model as string,
    timestamp: value.timestamp,
    text_length: value.text_length,
    truncated: value.truncated,
  };
}

function parseToolCall(value: unknown): SessionCardToolCall | null {
  if (
    !isRecord(value) ||
    !every(value, ["id", "name", "verb", "input", "status"], isString) ||
    !every(value, ["input_truncated", "output_truncated"], isBoolean) ||
    !isInstant(value.timestamp) ||
    !(value.output === null || isString(value.output))
  ) {
    return null;
  }
  return {
    id: value.id as string,
    name: value.name as string,
    verb: value.verb as string,
    input: value.input as string,
    input_truncated: value.input_truncated as boolean,
    output: value.output,
    output_truncated: value.output_truncated as boolean,
    status: value.status as string,
    timestamp: value.timestamp,
  };
}

function parseActivity(value: unknown): SessionCardActivity | null {
  if (
    !isRecord(value) ||
    !Array.isArray(value.messages) ||
    !Array.isArray(value.tool_calls) ||
    !Array.isArray(value.invocations) ||
    !every(value, ["messages_truncated", "tool_calls_truncated"], isBoolean) ||
    !isRecord(value.token_usage) ||
    !every(
      value.token_usage,
      ["tokens", "estimated_tokens", "uncached_input_tokens", "output_tokens", "api_calls"],
      isNumber,
    )
  ) {
    return null;
  }
  const messages = value.messages.map(parseMessage);
  const calls = value.tool_calls.map(parseToolCall);
  if (messages.includes(null) || calls.includes(null)) return null;
  const usage = value.token_usage;
  const models = Array.isArray(usage.models) ? usage.models.filter(isString) : [];
  return {
    messages: messages as SessionCardMessage[],
    messages_truncated: value.messages_truncated as boolean,
    invocations: value.invocations,
    token_usage: {
      tokens: usage.tokens as number,
      estimated_tokens: usage.estimated_tokens as number,
      uncached_input_tokens: usage.uncached_input_tokens as number,
      output_tokens: usage.output_tokens as number,
      api_calls: usage.api_calls as number,
      models,
    },
    tool_calls: calls as SessionCardToolCall[],
    tool_calls_truncated: value.tool_calls_truncated as boolean,
  };
}

/** The transcript in a response body, or null when it is not one: the wrong
 *  shape or a field of the wrong type anywhere in it. Fields it does not
 *  know are ignored. */
export function parseTranscript(body: unknown): LocalTranscript | null {
  if (!isRecord(body) || !isRecord(body.activity) || !isBoolean(body.truncated)) return null;
  const session = parseSession(body.session);
  if (!session) return null;
  const activity: Record<string, SessionCardActivity> = {};
  for (const [turn, value] of Object.entries(body.activity)) {
    const parsed = parseActivity(value);
    if (!parsed) return null;
    activity[turn] = parsed;
  }
  const raw = body.session as Record<string, unknown>;
  return {
    session,
    activity,
    truncated: body.truncated,
    promptsTotal: isNumber(raw.prompts_total) ? raw.prompts_total : session.prompts.length,
    limitBytes: isNumber(body.limit_bytes) ? body.limit_bytes : 0,
  };
}

/** Why a transcript did not arrive: the server does not link this session
 *  (it is not one of the pull request's, or the sessions changed), or the
 *  request failed. */
export class TranscriptError extends Error {
  readonly reason: "not-linked" | "failed";

  constructor(reason: "not-linked" | "failed") {
    super(reason === "not-linked" ? "This session is not linked here." : "The request failed.");
    this.reason = reason;
  }
}

/** Reads `/api/sessions/{id}`. It rejects with a TranscriptError, or with the
 *  abort that ends it. */
export async function fetchTranscript(id: string, signal: AbortSignal): Promise<LocalTranscript> {
  let response: Response;
  try {
    response = await fetch(`/api/sessions/${encodeURIComponent(id)}`, {
      cache: "no-store",
      signal,
    });
  } catch (error) {
    if (signal.aborted) throw error;
    throw new TranscriptError("failed");
  }
  if (response.status === 404) throw new TranscriptError("not-linked");
  if (!response.ok) throw new TranscriptError("failed");
  let body: unknown;
  try {
    body = await response.json();
  } catch (error) {
    if (signal.aborted) throw error;
    throw new TranscriptError("failed");
  }
  const transcript = parseTranscript(body);
  if (!transcript) throw new TranscriptError("failed");
  return transcript;
}
