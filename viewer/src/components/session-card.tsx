import pluralize from "pluralize";
import { type ReactNode, useEffect, useRef, useState } from "react";

import { HarnessName, ModelName } from "./brand-logo";
import { cn, FOCUS_RING } from "./classnames";
import { CARD_LABEL } from "./editorial";
import {
  durationSeconds,
  formatCompact,
  formatCount,
  formatDate,
  formatDuration,
  formatShare,
  formatUSD,
} from "./format";
import { ChevronDownIcon } from "./icons";
import { PromptBlock } from "./prompt-block";
import { Empty } from "./states";
import { Badge, type BadgeTone, Card, Spinner } from "./ui";

/** A session's transcript as the card draws it: its prompts in order, what
 *  each caused, and on request the agent's messages, model calls and tool
 *  calls for one prompt. Plain data: the host decides where it comes from
 *  (the hosted service's API, or `baum view`'s local transcript). */

/** What one prompt, or the whole session, caused. */
export type SessionCardEffects = {
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

export type SessionCardPrompt = {
  turn_id: string;
  ordinal: number;
  started_at: string | null;
  text: string;
  /** The true length, so the card says what it is not showing. */
  text_length: number;
  /** `text` is a preview of a longer prompt. */
  truncated: boolean;
  /** Null when the session recorded no inner trace: unknown, not zero. */
  effects: SessionCardEffects | null;
  cost_usd: number | null;
  tokens: number;
  estimated_tokens: number;
};

export type SessionCardSession = {
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
  /** Empty where it is not to be shown. */
  user_email: string;
  turn_count: number;
  commit_count: number;
  /** The link's confidence; below 0.7 the card says so. */
  confidence: number;
  /** Empty when the host loads them as the card opens (`loadSession`). */
  prompts: SessionCardPrompt[];
  prompts_truncated: boolean;
  totals: SessionCardEffects | null;
  /** Activity tied to no prompt. */
  unattributed: SessionCardEffects | null;
  tokens: number;
  estimated_tokens: number;
  /** Null when there is no price for the models; left out when the host has
   *  no prices at all, which hides the Cost figure. */
  cost_usd?: number | null;
  /** False when usage was reported once for the whole session. */
  cost_per_prompt: boolean;
  /** False when only the outcome and usage were recorded. */
  has_inner_trace: boolean;
};

export type SessionCardMessage = {
  text: string;
  timestamp: string | null;
  model: string;
  /** The true length, when `text` was cut. */
  text_length?: number;
  truncated?: boolean;
};

export type SessionCardToolCall = {
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

export type SessionCardActivity = {
  messages: SessionCardMessage[];
  messages_truncated: boolean;
  /** The model calls; the card only asks whether there were any. */
  invocations: readonly unknown[];
  token_usage: {
    tokens: number;
    estimated_tokens: number;
    uncached_input_tokens: number;
    output_tokens: number;
    api_calls: number;
    models?: string[];
  };
  tool_calls: SessionCardToolCall[];
  tool_calls_truncated: boolean;
};

/** Where the card gets what its session did not arrive with. Each loader is
 *  optional: without `loadSession` a card shows the prompts it was given,
 *  without `loadPromptText` a prompt preview stays a preview, and without
 *  `loadTurnActivity` there is no "Agent activity" to open. */
export type SessionCardSource = {
  loadSession?: (runId: string) => Promise<SessionCardSession>;
  loadPromptText?: (runId: string, turnId: string) => Promise<{ text: string }>;
  loadTurnActivity?: (runId: string, turnId: string) => Promise<SessionCardActivity>;
};

const OUTCOME_TONES: Record<string, BadgeTone> = {
  committed: "positive",
  abandoned: "neutral",
  interrupted: "caution",
  unknown: "neutral",
};

/** The rule down a session card's left edge: the outcome badge's colour,
 *  drawn where a reader scanning a stack of cards will see it first. A session
 *  matched to commits is committed whatever its recorded outcome says. */
function outcomeRule(session: SessionCardSession): string {
  if (session.commit_count > 0 || session.outcome === "committed")
    return "border-l-(--status-positive)";
  if (session.outcome === "interrupted") return "border-l-(--status-caution)";
  return "border-l-(--border-strong)";
}

/** A card that answers the pointer: it lifts a pixel and takes a shadow, the
 *  one place in a flat page where depth says "this opens". */
const LIFT =
  "border-l-2 transition-[transform,box-shadow,border-color] duration-200 ease-out hover:-translate-y-px hover:shadow-(--shadow-raised) motion-reduce:transition-none motion-reduce:hover:translate-y-0";

/** `meta` is a strip below the header, always visible. The header is a button,
 *  so anything the reader can click — a commit or pull request link — has to
 *  live outside it.
 *
 *  A session that arrived without its prompts came from a report that omits
 *  them; opening the card fetches the whole session once and keeps it.
 *
 *  `expand` opens the card from outside — the review rail naming this session
 *  — without taking the toggle away from the reader: it opens on the rising
 *  edge and leaves the card alone otherwise.
 *
 *  `focusTurn` marks the prompt of that turn and scrolls it into view once the
 *  card shows it, for a reader sent to one moment of the session.
 *
 *  `source` loads what the session did not arrive with, and `modelLabel`
 *  names a model (the model id by default). A source the host rebuilds on
 *  every render loads again, so keep it stable. `restrictedNote` says why a
 *  restricted session's details are withheld, in the host's own terms. */
export function SessionCard({
  session,
  source,
  modelLabel = (model) => model,
  restrictedNote = "This session's prompts, metadata, usage, and activity are not shown here.",
  defaultOpen,
  expand = false,
  meta,
  focusTurn,
}: {
  session: SessionCardSession;
  source?: SessionCardSource;
  modelLabel?: (model: string) => string;
  restrictedNote?: ReactNode;
  defaultOpen: boolean;
  expand?: boolean;
  meta?: ReactNode;
  focusTurn?: number;
}) {
  const [open, setOpen] = useState(defaultOpen || expand);
  const [detail, setDetail] = useState<SessionCardSession | "loading" | "failed" | null>(null);
  const duration = durationSeconds(session.started_at, session.ended_at);
  const full = detail && typeof detail !== "string" ? detail : session;
  // The whole session is fetched the first time the card opens, whichever
  // way it opened. The list omits prompts, so an open card without them is
  // still waiting on this.
  const loadSession = source?.loadSession;
  const needsDetail =
    open &&
    detail === null &&
    session.prompts.length === 0 &&
    !session.restricted &&
    loadSession !== undefined;
  useEffect(() => {
    if (!needsDetail || !loadSession) return;
    setDetail("loading");
    loadSession(session.run_id)
      .then(setDetail)
      .catch(() => setDetail("failed"));
  }, [needsDetail, session.run_id, loadSession]);
  useEffect(() => {
    if (expand) setOpen(true);
  }, [expand]);

  if (session.restricted) {
    return (
      <Card
        size="panel"
        className={cn("p-0", LIFT, "border-l-(--border-strong)")}
        data-session-id={session.run_id}
      >
        <div className="flex items-center gap-3 px-5 py-3.5">
          <span className={cn(CARD_LABEL, "flex-none")}>Session</span>
          <span className="min-w-0 flex-1 text-base text-(--text-secondary)">
            <HarnessName vendor={session.source} className="align-middle" /> attribution
          </span>
          <Badge size="compact">Details restricted</Badge>
        </div>
        <p className="border-t border-(--border-subtle) px-5 py-4 text-base text-(--text-tertiary)">
          {restrictedNote}
        </p>
      </Card>
    );
  }

  const toggle = () => setOpen((value) => !value);

  return (
    <Card
      size="panel"
      className={cn("p-0", LIFT, outcomeRule(session))}
      data-session-id={session.run_id}
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={toggle}
        className={cn(
          "flex w-full cursor-pointer items-center gap-3 rounded-t-(--radius-lg) px-5 py-3.5 text-left transition-colors duration-150 hover:bg-(--surface-hover)",
          (open || Boolean(meta)) && "border-b border-(--border-subtle)",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          className={cn(
            "flex-none text-(--text-tertiary) transition-transform",
            !open && "-rotate-90",
          )}
        />
        <span className={cn(CARD_LABEL, "flex-none")}>Session</span>
        <span className="min-w-0 flex-1 truncate text-base text-(--text-secondary)">
          {session.title ? `${session.title} · ` : ""}
          <HarnessName vendor={session.source} className="align-middle" /> ·{" "}
          {session.started_at ? `${formatDate(session.started_at)} · ` : ""}
          {formatDuration(duration)} · {pluralize("prompt", session.turn_count, true)}
          {session.user_email ? ` · ${session.user_email}` : ""}
        </span>
        <span className="flex flex-none items-center gap-2">
          {!session.has_inner_trace ? (
            <Badge
              size="compact"
              title="Only this session's outcome and usage were recorded, not an inner trace"
            >
              No inner trace
            </Badge>
          ) : null}
          {session.confidence > 0 && session.confidence < 0.7 ? (
            <Badge size="compact" tone="caution">
              {formatShare(session.confidence)} confident
            </Badge>
          ) : null}
          {/* How many commits this session was matched to. The outcome it
              replaces said "committed" and no more, which a non-zero count
              already says; with nothing matched the outcome is the only thing
              left that distinguishes an abandoned session from a linked one. */}
          {session.commit_count > 0 ? (
            <Badge size="compact" tone="positive">
              {pluralize("commit", session.commit_count, true)}
            </Badge>
          ) : (
            <Badge size="compact" tone={OUTCOME_TONES[session.outcome] ?? "neutral"}>
              {session.outcome}
            </Badge>
          )}
        </span>
      </button>

      {meta ? (
        <div
          className={cn(
            "flex flex-wrap items-center gap-x-3 gap-y-1.5 px-5 py-2.5 text-base",
            open && "border-b border-(--border-subtle)",
          )}
        >
          {meta}
        </div>
      ) : null}

      {open ? (
        <div className="px-5 py-4">
          {/* Nothing of this session's own figures is on the card until they
              arrive: zeroes would read as measurements. */}
          {typeof detail === "string" ? (
            <p className="flex items-center gap-2 text-base text-(--text-tertiary) italic">
              {detail === "loading" ? <Spinner /> : null}
              {detail === "loading" ? "Loading this session…" : "This session could not be loaded."}
            </p>
          ) : (
            <>
              <SessionTotals session={full} modelLabel={modelLabel} />
              {full.has_inner_trace ? (
                <PromptTimeline session={full} source={source} focusTurn={focusTurn} />
              ) : (
                <Empty title="Inner trace not captured" className="py-8">
                  This session reported its outcome and usage but no tool calls, file reads or
                  diffs. What each prompt did is unknown rather than nothing.
                </Empty>
              )}
            </>
          )}
        </div>
      ) : null}
    </Card>
  );
}

function SessionTotals({
  session,
  modelLabel,
}: {
  session: SessionCardSession;
  modelLabel: (model: string) => string;
}) {
  const totals = session.totals;
  return (
    <dl className="mb-4 flex flex-wrap gap-x-6 gap-y-2 text-base">
      <Stat label="Commits" value={formatCount(session.commit_count)} />
      {session.models.length > 0 ? (
        <Stat
          label="Models"
          value={
            <span className="inline-flex flex-wrap gap-x-3 gap-y-1">
              {session.models.map((model) => (
                <ModelName key={model} model={model} label={modelLabel(model)} />
              ))}
            </span>
          }
        />
      ) : null}
      <Stat
        label="Tokens"
        value={formatCompact(session.tokens)}
        detail={
          join([
            estimatedTokenDetail(session.tokens, session.estimated_tokens),
            session.tokens > 0 && !session.cost_per_prompt && "reported at session level",
          ]) ?? undefined
        }
      />
      {session.cost_usd !== undefined ? (
        <Stat
          label="Cost"
          value={formatUSD(session.cost_usd)}
          detail={
            session.cost_usd == null && session.tokens > 0
              ? "no price for these models"
              : !session.cost_per_prompt
                ? "reported per session"
                : undefined
          }
        />
      ) : null}
      {session.git_branch ? <Stat label="Branch" value={session.git_branch} /> : null}
      {totals ? (
        <>
          <Stat label="Files read" value={formatCount(totals.files_read)} />
          <Stat label="Files edited" value={formatCount(totals.files_edited)} />
          <Stat
            label="Lines"
            value={`+${formatCount(totals.added_lines)} / −${formatCount(totals.removed_lines)}`}
          />
          <Stat label="Tool calls" value={formatCount(totals.tool_calls)} />
          {totals.failed_calls > 0 ? (
            <Stat label="Failed calls" value={formatCount(totals.failed_calls)} />
          ) : null}
        </>
      ) : null}
      {session.unattributed && session.unattributed.tool_calls + session.unattributed.reads > 0 ? (
        <Stat
          label="Not tied to a prompt"
          value={formatCount(session.unattributed.tool_calls + session.unattributed.reads)}
          detail="counted in the session, under no prompt"
        />
      ) : null}
      {session.working_dir ? (
        <Stat label="Working dir" value={session.working_dir} truncate />
      ) : null}
    </dl>
  );
}

function Stat({
  label,
  value,
  detail,
  truncate,
}: {
  label: string;
  value: ReactNode;
  detail?: string;
  truncate?: boolean;
}) {
  return (
    <div className={cn("min-w-0", truncate && "max-w-80")}>
      <dt className="text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">{label}</dt>
      <dd className={cn("mt-0.5 tabular-nums text-(--text-primary)", truncate && "truncate")}>
        {value}
        {detail ? (
          <span className="ml-1.5 text-xs text-(--text-tertiary) normal-case">{detail}</span>
        ) : null}
      </dd>
    </div>
  );
}

function PromptTimeline({
  session,
  source,
  focusTurn,
}: {
  session: SessionCardSession;
  source?: SessionCardSource;
  focusTurn?: number;
}) {
  if (session.prompts.length === 0) {
    return (
      <Empty title="No prompt text recorded" className="py-8">
        This session is linked and traced, but nothing was captured of what was asked.
      </Empty>
    );
  }

  return (
    <>
      <ol className="flex flex-col gap-3.5">
        {session.prompts.map((prompt) => (
          <PromptRow
            key={prompt.turn_id}
            prompt={prompt}
            runId={session.run_id}
            source={source}
            costPerPrompt={session.cost_per_prompt}
            sessionLevelTokens={session.tokens > 0 && !session.cost_per_prompt}
            focused={focusTurn === prompt.ordinal}
          />
        ))}
      </ol>
      {session.prompts_truncated ? (
        <p className="mt-3 text-base text-(--text-tertiary)">
          This session has more prompts than are shown here.
        </p>
      ) : null}
    </>
  );
}

function PromptRow({
  prompt,
  runId,
  source,
  costPerPrompt,
  sessionLevelTokens,
  focused = false,
}: {
  prompt: SessionCardPrompt;
  runId: string;
  source?: SessionCardSource;
  costPerPrompt: boolean;
  sessionLevelTokens: boolean;
  focused?: boolean;
}) {
  // The detail response may carry a preview. Expanding fetches the rest, where
  // the host can.
  const [text, setText] = useState(prompt.text);
  const [truncated, setTruncated] = useState(prompt.truncated);
  const [activityOpen, setActivityOpen] = useState(false);
  const [activity, setActivity] = useState<SessionCardActivity | "loading" | "failed" | null>(null);
  const row = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (focused) row.current?.scrollIntoView({ block: "nearest" });
  }, [focused]);

  const loadPromptText = source?.loadPromptText;
  const loadTurnActivity = source?.loadTurnActivity;
  const loadFullText = () => {
    if (!truncated || !loadPromptText) return;
    loadPromptText(runId, prompt.turn_id)
      .then((full) => {
        setText(full.text);
        setTruncated(false);
      })
      // Leaving the preview in place is a better failure than an error where
      // the prompt was.
      .catch(() => undefined);
  };

  const toggleActivity = () => {
    setActivityOpen((value) => !value);
    if (!activityOpen && activity === null && loadTurnActivity) {
      setActivity("loading");
      loadTurnActivity(runId, prompt.turn_id)
        .then(setActivity)
        .catch(() => setActivity("failed"));
    }
  };

  return (
    <li
      ref={row}
      aria-current={focused ? "step" : undefined}
      className={cn(
        "grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-2",
        focused && "-mx-2 rounded-(--radius-sm) px-2 py-1 ring-2 ring-(--action-accent)",
      )}
    >
      <span className="pt-3 text-base tabular-nums text-(--text-tertiary)">{prompt.ordinal}</span>
      <div className="min-w-0">
        <PromptBlock
          text={text}
          fullLength={prompt.text_length}
          truncated={truncated}
          collapsedLines={4}
          onExpand={loadFullText}
          label={prompt.started_at ? formatDate(prompt.started_at) : undefined}
        />
        <EvidenceLines
          effects={prompt.effects}
          cost={costPerPrompt ? prompt.cost_usd : null}
          tokens={prompt.tokens}
          estimatedTokens={prompt.estimated_tokens}
        />
        {sessionLevelTokens ? (
          <p className="mt-1.5 text-base text-(--text-tertiary) italic">
            Per-prompt token usage missing · reported at session level.
          </p>
        ) : null}
        {loadTurnActivity ? (
          <button
            type="button"
            aria-expanded={activityOpen}
            onClick={toggleActivity}
            className={cn(
              "mt-2 flex cursor-pointer items-center gap-1 rounded-(--radius-xs) text-base text-(--text-secondary) hover:text-(--text-primary)",
              FOCUS_RING,
            )}
          >
            <ChevronDownIcon
              size={14}
              className={cn("transition-transform", !activityOpen && "-rotate-90")}
            />
            Agent activity
          </button>
        ) : null}
        {activityOpen ? (
          <TurnActivityDetails activity={activity} sessionLevelTokens={sessionLevelTokens} />
        ) : null}
      </div>
    </li>
  );
}

function TurnActivityDetails({
  activity,
  sessionLevelTokens,
}: {
  activity: SessionCardActivity | "loading" | "failed" | null;
  sessionLevelTokens: boolean;
}) {
  if (activity === null || activity === "loading") {
    return (
      <p className="mt-2 flex items-center gap-2 text-base text-(--text-tertiary) italic">
        <Spinner />
        Loading agent activity…
      </p>
    );
  }
  if (activity === "failed") {
    return (
      <p className="mt-2 text-base text-(--text-tertiary) italic">
        Agent activity could not be loaded.
      </p>
    );
  }
  if (
    activity.messages.length === 0 &&
    activity.invocations.length === 0 &&
    activity.tool_calls.length === 0 &&
    activity.token_usage.tokens === 0 &&
    !sessionLevelTokens
  ) {
    return (
      <p className="mt-2 text-base text-(--text-tertiary) italic">
        No agent output was captured for this prompt.
      </p>
    );
  }

  const usage = activity.token_usage;
  const models = usage.models ?? [];

  return (
    <div className="mt-2 flex flex-col gap-2">
      {sessionLevelTokens ? (
        <p className="text-base text-(--text-tertiary) italic">
          Per-prompt token usage missing · reported at session level.
        </p>
      ) : usage.api_calls > 0 || usage.tokens > 0 ? (
        <p className="text-base text-(--text-tertiary)">
          {join([
            usage.api_calls > 0 && pluralize("model call", usage.api_calls, true),
            models.length > 0 && models.join(", "),
            usage.tokens > 0 && formatTokenUsage(usage.tokens, usage.estimated_tokens),
            usage.uncached_input_tokens + usage.output_tokens > 0 &&
              `${formatCompact(usage.uncached_input_tokens)} uncached in / ${formatCompact(usage.output_tokens)} out`,
          ])}
        </p>
      ) : null}
      <ToolCallGroups calls={activity.tool_calls} />
      {activity.tool_calls_truncated ? (
        <p className="text-base text-(--text-tertiary)">More tool calls were recorded.</p>
      ) : null}
      {activity.messages.map((message, index) => (
        <PromptBlock
          key={`${message.timestamp ?? "undated"}-${index}`}
          text={message.text}
          fullLength={message.text_length}
          truncated={message.truncated}
          tone="agent"
          collapsedLines={4}
          label={join([message.model, message.timestamp ? formatDate(message.timestamp) : null])}
        />
      ))}
      {activity.messages_truncated ? (
        <p className="text-base text-(--text-tertiary)">
          This prompt has more agent messages than are shown here.
        </p>
      ) : null}
    </div>
  );
}

function ToolCallGroups({ calls }: { calls: SessionCardToolCall[] }) {
  const groups = calls.reduce<Record<string, SessionCardToolCall[]>>((grouped, call) => {
    (grouped[call.verb] ??= []).push(call);
    return grouped;
  }, {});
  return Object.entries(groups).map(([verb, verbCalls]) => (
    <details key={verb} className="rounded-(--radius-sm) border border-(--border-subtle) px-3 py-2">
      <summary className="cursor-pointer text-base text-(--text-secondary)">
        {verb.replaceAll("_", " ")} · {pluralize("call", verbCalls.length, true)}
      </summary>
      <ul className="mt-2 flex flex-col gap-2">
        {verbCalls.map((call) => (
          <li key={call.id} className="text-base text-(--text-tertiary)">
            {join([call.name, call.status, call.timestamp && formatDate(call.timestamp)])}
            <pre className="mt-1 max-h-56 overflow-auto rounded-(--radius-xs) bg-(--surface-sunken) p-2 text-xs text-(--text-secondary)">
              {call.input}
              {call.input_truncated ? "\n…" : ""}
            </pre>
            {call.output != null ? (
              <>
                <p className="mt-1 text-xs text-(--text-tertiary)">Output</p>
                <pre className="mt-1 max-h-56 overflow-auto rounded-(--radius-xs) bg-(--surface-sunken) p-2 text-xs text-(--text-secondary)">
                  {call.output}
                  {call.output_truncated ? "\n…" : ""}
                </pre>
              </>
            ) : null}
          </li>
        ))}
      </ul>
    </details>
  ));
}

/** The "Result:" block, folded into at most two lines.
 *
 *  A fact the trace did not capture is dropped from its line rather than
 *  rendered as a dash — a line of nothing but dashes says less than no line. */
function EvidenceLines({
  effects,
  cost,
  tokens,
  estimatedTokens,
}: {
  effects: SessionCardEffects | null;
  cost: number | null;
  tokens: number;
  estimatedTokens: number;
}) {
  if (!effects && tokens === 0 && cost == null) return null;

  const touched = join([
    effects && effects.files_read > 0 && `read ${pluralize("file", effects.files_read, true)}`,
    // Re-reading the same file is the exploration signal, and it disappears if
    // only the distinct count is shown.
    effects && effects.reads > effects.files_read && `${formatCount(effects.reads)} reads`,
    effects && effects.files_edited > 0 && `edited ${formatCount(effects.files_edited)}`,
  ]);
  const changed = join([
    effects && effects.diff_hunks > 0 && `${pluralize("diff hunk", effects.diff_hunks, true)}`,
    effects &&
      effects.added_lines + effects.removed_lines > 0 &&
      `+${formatCount(effects.added_lines)}/-${formatCount(effects.removed_lines)}`,
    effects && effects.tool_calls > 0 && `${pluralize("tool call", effects.tool_calls, true)}`,
    effects && effects.failed_calls > 0 && `${formatCount(effects.failed_calls)} failed`,
    tokens > 0 && formatTokenUsage(tokens, estimatedTokens),
    cost != null && formatUSD(cost),
  ]);

  const lines = [touched, changed].filter(Boolean) as string[];
  if (lines.length === 0) {
    return (
      <p className="mt-1.5 text-base text-(--text-tertiary) italic">
        This prompt caused no recorded activity.
      </p>
    );
  }

  return (
    <ul className="mt-1.5 flex flex-col gap-0.5">
      {lines.map((line) => (
        <li key={line} className="flex gap-1.5 text-base leading-[1.45] text-(--text-tertiary)">
          <span aria-hidden className="flex-none">
            →
          </span>
          <span className="min-w-0">{line}</span>
        </li>
      ))}
    </ul>
  );
}

function formatTokenUsage(tokens: number, estimatedTokens: number): string {
  const detail = estimatedTokenDetail(tokens, estimatedTokens);
  return `${formatCompact(tokens)} tokens${detail ? ` (${detail})` : ""}`;
}

function estimatedTokenDetail(tokens: number, estimatedTokens: number): string | undefined {
  if (estimatedTokens <= 0) return undefined;
  return estimatedTokens >= tokens ? "estimated" : `${formatCompact(estimatedTokens)} estimated`;
}

function join(parts: (string | false | null | undefined)[]): string | null {
  const kept = parts.filter(Boolean) as string[];
  return kept.length > 0 ? kept.join(" · ") : null;
}
