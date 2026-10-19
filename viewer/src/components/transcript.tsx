import { centerJump, flashJump } from "./jump-focus";
import pluralize from "pluralize";
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { harnessName } from "../agent-names";
import { dayBreaks } from "../conversation";
import {
  type ActivityState,
  type CostFormat,
  effectFacts,
  endsWithResponse,
  estimatedTokenDetail,
  followupLabel,
  formatTokenUsage,
  fullReplyText,
  join,
  missingResponseNote,
  redactedSpawnMessage,
  spawnStatusLabel,
  timeline,
  type TranscriptActivity,
  type TranscriptPrompt,
  type TranscriptSession,
  type TranscriptSource,
  type TranscriptSubagent,
  type TranscriptSubagentActivity,
  type TranscriptToolCall,
  type TranscriptTurn,
} from "../transcript";
import { HarnessName, ModelName } from "./brand-logo";
import { AgentAvatar, AgentText, ChatSurface, PromptBubble, SystemNote } from "./chat";
import { cn, FOCUS_RING } from "./classnames";
import { CARD_LABEL } from "./editorial";
import {
  durationSeconds,
  formatCompact,
  formatCount,
  formatDuration,
  formatTime,
  formatUSD,
} from "./format";
import { ChevronDownIcon } from "./icons";
import { Stat } from "./stat";
import { Empty } from "./states";
import { Badge, Button, Card, Spinner } from "./ui";

/** Where the replies of a session stand: carried by the session, still being
 *  fetched with it, or not available. */
export type RepliesState = "ready" | "loading" | "failed";

/** What a host adds to a transcript: its loaders (`source`), how it writes a
 *  cost (`formatCost`, none and no cost is shown), how it names a model
 *  (`modelLabel`, the model id by default), and the picture beside a person's
 *  prompt (`renderAvatar`, none and no picture or its column). */
export type TranscriptHostProps = {
  source?: TranscriptSource;
  formatCost?: CostFormat;
  modelLabel?: (model: string) => string;
  renderAvatar?: (author: string) => ReactNode;
};

/** What a subagent card, however deep, needs of the session it is in: the
 *  root run its conversation is read under, the harness that ran it, and the
 *  host's loader and wording. Provided once by the transcript. */
type SubagentHost = {
  runId: string;
  vendor: string;
  loadSubagentActivity?: TranscriptSource["loadSubagentActivity"];
  formatCost?: CostFormat;
  modelLabel: (model: string) => string;
};

const SubagentHostContext = createContext<SubagentHost>({
  runId: "",
  vendor: "",
  modelLabel: (model) => model,
});

const subscribe = () => () => {};

/** True once mounted in a browser, false in a server render. */
function useMounted(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );
}

/** A session read as the conversation it was: each prompt on the right, the
 *  agent's text replies on the left, and what the agent did in between — tool
 *  calls, model calls, tokens — folded under each turn until somebody asks.
 *
 *  `focusTurn` marks the prompt of that turn and scrolls it into view, for a
 *  reader sent to one moment of the session. */
export function SessionTranscript({
  session,
  replies,
  focusTurn,
  source,
  formatCost,
  modelLabel,
  renderAvatar,
}: {
  session: TranscriptSession;
  replies: RepliesState;
  focusTurn?: number;
} & TranscriptHostProps) {
  // Days split in the reader's own zone. The routes that show a transcript
  // render it on the client only, so this guard is defensive, as charts are.
  const mounted = useMounted();
  const loadSubagentActivity = source?.loadSubagentActivity;
  const host = useMemo<SubagentHost>(
    () => ({
      runId: session.run_id,
      vendor: session.source,
      loadSubagentActivity,
      formatCost,
      modelLabel: modelLabel ?? ((model) => model),
    }),
    [session.run_id, session.source, loadSubagentActivity, formatCost, modelLabel],
  );
  if (session.prompts.length === 0) {
    return (
      <Empty title="No prompt text recorded" className="py-8">
        This session is linked and traced, but nothing was captured of what was asked.
      </Empty>
    );
  }

  const state = session.replies_included ? "ready" : replies;
  const days = mounted ? dayBreaks(session.prompts.map((prompt) => prompt.started_at)) : [];
  return (
    <SubagentHostContext.Provider value={host}>
      <ChatSurface plain>
        {/* Said once for the session rather than once a turn, in a region that
          stays mounted so a screen reader hears it change. */}
        <div aria-live="polite" className="flex flex-col">
          {state === "loading" ? (
            <SystemNote className="mb-3">
              <Spinner />
              Loading the agent's replies…
            </SystemNote>
          ) : state === "failed" ? (
            <SystemNote className="mb-3">
              The agent's replies could not be loaded. Each prompt's agent activity still has them.
            </SystemNote>
          ) : null}
        </div>
        <ol className="flex flex-col gap-5">
          {session.prompts.map((prompt, index) => (
            <Turn
              key={prompt.turn_id}
              prompt={prompt}
              session={session}
              replies={state}
              day={days[index] ?? null}
              focused={focusTurn === prompt.ordinal}
              source={source}
              formatCost={formatCost}
              renderAvatar={renderAvatar}
            />
          ))}
        </ol>
        {session.prompts_truncated ? (
          <SystemNote className="mt-3">
            This session has more prompts than are shown here.
          </SystemNote>
        ) : null}
      </ChatSurface>
    </SubagentHostContext.Provider>
  );
}

/** A turn's activity, fetched the first time anything asks for it: the
 *  activity toggle, or a reply whose preview somebody expands. A failed fetch
 *  is tried again on the next ask. Without a loader nothing is fetched. */
function useTurnActivity(runId: string, turnId: string, source: TranscriptSource | undefined) {
  const [activity, setActivity] = useState<ActivityState>(null);
  const requested = useRef(false);
  const loadTurnActivity = source?.loadTurnActivity;
  const load = useCallback(() => {
    if (requested.current || !loadTurnActivity) return;
    requested.current = true;
    setActivity("loading");
    loadTurnActivity(runId, turnId)
      .then(setActivity)
      .catch(() => {
        requested.current = false;
        setActivity("failed");
      });
  }, [runId, turnId, loadTurnActivity]);
  return { activity, load, available: Boolean(loadTurnActivity) };
}

function Turn({
  prompt,
  session,
  replies,
  day,
  focused,
  source,
  formatCost,
  renderAvatar,
}: {
  prompt: TranscriptPrompt;
  session: TranscriptSession;
  replies: RepliesState;
  /** The day this turn starts, when it is not the previous turn's. */
  day: string | null;
  focused: boolean;
} & TranscriptHostProps) {
  const { activity, load, available } = useTurnActivity(session.run_id, prompt.turn_id, source);
  const [activityOpen, setActivityOpen] = useState(false);
  const [replyExpanded, setReplyExpanded] = useState(false);
  const row = useRef<HTMLLIElement>(null);
  // Wait for replies: their arrival changes the height of preceding turns.
  useEffect(() => {
    const target = row.current;
    if (!focused || replies === "loading" || !target) return;
    const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    const promptElement = target.querySelector<HTMLElement>('[data-message="human"]') ?? target;
    const cancelScroll = centerJump(promptElement, reduced);
    flashJump(promptElement, reduced);
    return cancelScroll;
  }, [focused, replies]);

  const toggleActivity = () => {
    if (!activityOpen) load();
    setActivityOpen((value) => !value);
  };
  const sessionLevelTokens = session.tokens > 0 && !session.cost_per_prompt;

  return (
    <li
      ref={row}
      aria-current={focused ? "step" : undefined}
      className="flex flex-col gap-2 rounded-xl"
    >
      {day ? <SystemNote className="mb-1">{day}</SystemNote> : null}
      <HumanMessage
        prompt={prompt}
        runId={session.run_id}
        author={session.user_email}
        loadPromptText={source?.loadPromptText}
        renderAvatar={renderAvatar}
      />
      <AgentSide>
        <AgentReplies
          vendor={session.source}
          prompt={prompt}
          state={replies}
          activity={replyExpanded ? activity : null}
          loadActivity={() => {
            setReplyExpanded(true);
            load();
          }}
          openActivity={available ? toggleActivity : undefined}
        />
        <TurnFooter
          prompt={prompt}
          cost={session.cost_per_prompt ? prompt.cost_usd : null}
          costIsFloor={session.cost_per_prompt && prompt.cost_is_floor}
          formatCost={formatCost}
          sessionLevelTokens={sessionLevelTokens}
          activityOpen={activityOpen}
          onToggleActivity={available ? toggleActivity : undefined}
        />
        {activityOpen ? (
          <div
            role="region"
            aria-label={`Agent activity for prompt ${prompt.ordinal}`}
            className="flex min-w-0 flex-col gap-2 border-l-2 border-(--border-subtle) pl-3"
          >
            <ActivityTimeline
              vendor={session.source}
              activity={activity}
              sessionLevelTokens={sessionLevelTokens}
              onRetry={load}
            />
          </div>
        ) : null}
      </AgentSide>
    </li>
  );
}

/** The prompt, as a bubble on the right: the side a chat puts its reader on.
 *  The host may send a preview; expanding loads the rest. */
function HumanMessage({
  prompt,
  runId,
  author,
  loadPromptText,
  renderAvatar,
}: {
  prompt: TranscriptPrompt;
  runId: string;
  author: string;
  loadPromptText?: TranscriptSource["loadPromptText"];
  renderAvatar?: (author: string) => ReactNode;
}) {
  const [text, setText] = useState(prompt.text);
  const [truncated, setTruncated] = useState(prompt.truncated);

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

  return (
    <PromptBubble
      text={text}
      author={author}
      avatar={renderAvatar ? (author ? renderAvatar(author) : null) : undefined}
      length={prompt.text_length}
      truncated={truncated}
      onExpand={loadFullText}
      meta={
        <>
          <span className="tabular-nums">#{prompt.ordinal}</span>
          {prompt.started_at ? (
            <time dateTime={prompt.started_at}>{formatTime(prompt.started_at)}</time>
          ) : null}
        </>
      }
    />
  );
}

/** The agent's side of a turn: its bubbles, notes and the turn's footer. */
function AgentSide({ children }: { children: ReactNode }) {
  return (
    <div data-message="agent" className="flex min-w-0 flex-col gap-1.5">
      {children}
    </div>
  );
}

function AgentReplies({
  vendor,
  prompt,
  state,
  activity,
  loadActivity,
  openActivity,
}: {
  vendor: string;
  prompt: TranscriptPrompt;
  state: RepliesState;
  activity: ActivityState;
  loadActivity: () => void;
  /** Absent when the host has no activity to open. */
  openActivity?: () => void;
}) {
  // The transcript says once that the replies are loading or missing.
  if (state !== "ready") return null;
  const omitted = pluralize("earlier reply", prompt.replies_omitted, true);
  return (
    <>
      {prompt.replies_omitted > 0 ? (
        openActivity ? (
          <button
            type="button"
            onClick={openActivity}
            className={cn(
              "cursor-pointer self-center rounded-full border border-(--border-subtle) bg-(--surface-card) px-3 py-1 text-xs text-(--text-link) hover:underline",
              FOCUS_RING,
            )}
          >
            {omitted} in the agent activity
          </button>
        ) : (
          <SystemNote>{omitted} not shown</SystemNote>
        )
      ) : null}
      {prompt.replies.length === 0 && prompt.replies_omitted === 0 ? (
        <SystemNote>No text reply was recorded.</SystemNote>
      ) : null}
      {prompt.replies.map((reply, index) => {
        const text = fullReplyText(reply, activity);
        const stillPreview = reply.truncated && text === reply.text;
        return (
          <AgentText
            key={`${reply.timestamp ?? "undated"}-${index}`}
            vendor={index === 0 ? vendor : undefined}
            text={text}
            length={reply.text_length}
            truncated={stillPreview}
            onExpand={reply.truncated ? loadActivity : undefined}
            caption={join([
              reply.model,
              reply.timestamp && formatTime(reply.timestamp),
              // The activity carries a turn's first messages, and a long
              // turn's last replies can fall outside them.
              stillPreview &&
                typeof activity === "object" &&
                activity !== null &&
                "the rest is beyond what the activity holds",
            ])}
          />
        );
      })}
    </>
  );
}

/** What the turn did, on one quiet line, and the switch to the activity
 *  behind it. A fact the trace did not capture is left out rather than
 *  rendered as a dash. */
function TurnFooter({
  prompt,
  cost,
  costIsFloor,
  formatCost,
  sessionLevelTokens,
  activityOpen,
  onToggleActivity,
}: {
  prompt: TranscriptPrompt;
  cost: number | null;
  costIsFloor: boolean;
  formatCost?: CostFormat;
  sessionLevelTokens: boolean;
  activityOpen: boolean;
  /** Absent when the host has no activity to open. */
  onToggleActivity?: () => void;
}) {
  const facts = effectFacts(
    prompt.effects,
    prompt.tokens,
    prompt.estimated_tokens,
    cost,
    costIsFloor,
    formatCost,
  );
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 pl-9 text-xs text-(--text-tertiary)">
      {onToggleActivity ? (
        <button
          type="button"
          aria-expanded={activityOpen}
          aria-label={`Agent activity for prompt ${prompt.ordinal}`}
          onClick={onToggleActivity}
          className={cn(
            "flex cursor-pointer items-center gap-1 self-center rounded-(--radius-xs) text-xs font-medium text-(--text-secondary) hover:text-(--text-primary)",
            FOCUS_RING,
          )}
        >
          <ChevronDownIcon
            size={12}
            className={cn("transition-transform", !activityOpen && "-rotate-90")}
          />
          Agent activity
        </button>
      ) : null}
      {facts ? <span className="min-w-0">{facts}</span> : null}
      {prompt.effects && !facts ? (
        <span className="italic">This prompt caused no recorded activity.</span>
      ) : null}
      {sessionLevelTokens ? (
        <span className="italic">Per-prompt token usage missing · reported at session level.</span>
      ) : null}
    </div>
  );
}

function ActivityTimeline({
  vendor,
  activity,
  sessionLevelTokens,
  onRetry,
}: {
  vendor: string;
  activity: ActivityState;
  sessionLevelTokens: boolean;
  onRetry: () => void;
}) {
  if (activity === null || activity === "loading") {
    return (
      <SystemNote align="start">
        <Spinner />
        Loading agent activity…
      </SystemNote>
    );
  }
  if (activity === "failed") {
    return (
      <p role="alert" className="flex flex-wrap items-center gap-2 self-center text-base">
        <span className="text-(--status-critical)">Agent activity could not be loaded.</span>
        <Button variant="secondary" size="tiny" onClick={onRetry}>
          Try again
        </Button>
      </p>
    );
  }
  const followups = activity.followups ?? [];
  if (
    activity.messages.length === 0 &&
    activity.tool_calls.length === 0 &&
    (activity.subagents?.length ?? 0) === 0 &&
    followups.length === 0 &&
    (sessionLevelTokens ||
      (activity.token_usage.tokens === 0 && activity.token_usage.api_calls === 0))
  ) {
    return <SystemNote align="start">No agent output was captured for this prompt.</SystemNote>;
  }

  const usage = activity.token_usage;
  const models = usage.models ?? [];
  return (
    <>
      {!sessionLevelTokens && (usage.api_calls > 0 || usage.tokens > 0) ? (
        <SystemNote align="start">
          {join([
            usage.api_calls > 0 && pluralize("model call", usage.api_calls, true),
            models.length > 0 && models.join(", "),
            usage.tokens > 0 && formatTokenUsage(usage.tokens, usage.estimated_tokens),
            usage.uncached_input_tokens + usage.output_tokens > 0 &&
              `${formatCompact(usage.uncached_input_tokens)} uncached in / ${formatCompact(usage.output_tokens)} out`,
          ])}
        </SystemNote>
      ) : null}
      <ActivityEntries vendor={vendor} activity={activity} />
      {followups.map((followup) => (
        <FollowupTurn key={followup.input.turn_id} followup={followup} vendor={vendor} />
      ))}
      {activity.followups_truncated ? (
        <SystemNote align="start">More follow-up turns were recorded.</SystemNote>
      ) : null}
    </>
  );
}

/** One activity's messages, tool runs and subagent cards in time order, and
 *  what it holds beyond them. */
function ActivityEntries({ vendor, activity }: { vendor: string; activity: TranscriptActivity }) {
  const entries = timeline(activity);
  // The agent is named on its first message, and again on the first after a
  // subagent's card, whose response reads as another speaker's.
  let afterOther = true;
  const named = entries.map((entry) => {
    if (entry.kind === "subagent") afterOther = true;
    if (entry.kind !== "message") return false;
    const name = afterOther;
    afterOther = false;
    return name;
  });
  return (
    <>
      {entries.map((entry, index) =>
        entry.kind === "message" ? (
          <AgentText
            key={`message-${index}`}
            vendor={named[index] ? vendor : undefined}
            text={entry.message.text}
            length={entry.message.text_length}
            truncated={entry.message.truncated}
            caption={join([
              entry.message.model,
              entry.message.timestamp && formatTime(entry.message.timestamp),
            ])}
          />
        ) : entry.kind === "subagent" ? (
          <SubagentCard
            key={`subagent-${entry.subagent.tool_call_id || index}`}
            subagent={entry.subagent}
          />
        ) : (
          <ToolRun key={`tools-${entry.calls[0]?.id ?? index}`} calls={entry.calls} />
        ),
      )}
      {activity.messages_truncated ? (
        <SystemNote align="start">More agent messages were recorded.</SystemNote>
      ) : null}
      {activity.tool_calls_truncated ? (
        <SystemNote align="start">More tool calls were recorded.</SystemNote>
      ) : null}
    </>
  );
}

/** An input that came after the prompt and before the next prompt shown — a
 *  subagent's report, a task notification, a slash command — and what the
 *  agent did about it. The input reads as a divider naming what it was, with
 *  its text folded behind it, since it is the machinery's words and often
 *  long. */
function FollowupTurn({
  followup: { input, activity },
  vendor,
}: {
  followup: TranscriptTurn;
  vendor: string;
}) {
  const kind = followupLabel(input);
  const empty =
    activity.messages.length === 0 &&
    activity.tool_calls.length === 0 &&
    (activity.subagents?.length ?? 0) === 0;
  return (
    <div data-followup={input.turn_id} className="mt-2 flex flex-col gap-1.5">
      <details className="group flex flex-col">
        <summary
          className={cn(
            "mx-auto flex w-fit max-w-full cursor-pointer list-none items-center gap-1.5 rounded-full border border-dashed border-(--border-strong) bg-(--surface-card) px-3 py-1 text-xs text-(--text-secondary) hover:text-(--text-primary) [&::-webkit-details-marker]:hidden",
            FOCUS_RING,
          )}
        >
          <ChevronDownIcon
            size={12}
            className="flex-none -rotate-90 transition-transform group-open:rotate-0"
          />
          <span className="min-w-0 truncate">
            {join([
              kind.label,
              input.started_at && formatTime(input.started_at),
              `${formatCompact(input.text_length)} characters`,
            ])}
          </span>
        </summary>
        <div className="mx-auto mt-1.5 w-full max-w-[92%] rounded-xl border border-(--border-subtle) bg-(--surface-card) px-3 py-2">
          <p className="max-h-80 overflow-auto text-xs whitespace-pre-wrap text-(--text-secondary) [overflow-wrap:anywhere]">
            {input.text}
          </p>
          {input.truncated ? (
            <p className="mt-1.5 text-xs text-(--text-tertiary)">
              Showing the first {formatCompact([...input.text].length)} of{" "}
              {formatCompact(input.text_length)} characters.
            </p>
          ) : null}
        </div>
      </details>
      {empty ? (
        // A command, its output or an interruption asks nothing of the agent,
        // so silence after it is not a gap.
        kind.wrapper ? null : (
          <SystemNote align="start">No agent output was captured for this input.</SystemNote>
        )
      ) : (
        <ActivityEntries vendor={vendor} activity={activity} />
      )}
    </div>
  );
}

const CALL_TEXT =
  "mt-1 max-h-56 overflow-auto rounded-md bg-(--surface-sunken) p-2 text-xs whitespace-pre-wrap text-(--text-secondary) [overflow-wrap:anywhere]";

/** A run of tool calls between two messages, folded to one line naming what
 *  kinds of call they were. A call's output shows when the host has it. */
function ToolRun({ calls }: { calls: TranscriptToolCall[] }) {
  const verbs = new Map<string, number>();
  for (const call of calls) verbs.set(call.verb, (verbs.get(call.verb) ?? 0) + 1);
  const kinds = [...verbs]
    .map(([verb, count]) => `${verb.replaceAll("_", " ")}${count > 1 ? ` ×${count}` : ""}`)
    .join(", ");
  return (
    <details open className="group flex flex-col">
      <summary
        className={cn(
          "flex w-fit max-w-full cursor-pointer list-none items-center gap-1.5 rounded-full border border-(--border-subtle) bg-(--surface-card) px-3 py-1 text-xs text-(--text-secondary) hover:text-(--text-primary) [&::-webkit-details-marker]:hidden",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          size={12}
          className="flex-none -rotate-90 transition-transform group-open:rotate-0"
        />
        <span className="min-w-0 truncate">
          {pluralize("tool call", calls.length, true)} · {kinds}
        </span>
      </summary>
      <ul className="mt-1.5 flex w-full min-w-0 flex-col gap-2 rounded-xl border border-(--border-subtle) bg-(--surface-card) px-3 py-2">
        {calls.map((call) => (
          <li key={call.id} className="text-xs text-(--text-tertiary)">
            {join([call.name, call.status, call.timestamp && formatTime(call.timestamp)])}
            <pre className={CALL_TEXT}>
              {call.input}
              {call.input_truncated ? "\n…" : ""}
            </pre>
            {call.output != null ? (
              <>
                <p className="mt-1.5">Output</p>
                <pre className={CALL_TEXT}>
                  {call.output}
                  {call.output_truncated ? "\n…" : ""}
                </pre>
              </>
            ) : null}
          </li>
        ))}
      </ul>
    </details>
  );
}

type SubagentState = TranscriptSubagentActivity | "loading" | "failed" | "missing" | null;

/** A subagent the agent spawned, as a card where it was spawned, the way a
 *  session is a card: what it was asked to do in the header, its response and
 *  totals always showing, and its own conversation one click away. Opening it
 *  loads that conversation once and keeps it.
 *
 *  A subagent whose own transcript was not captured, or whose host cannot
 *  load one, still opens, onto the spawn's input; its response is what the
 *  host recovered of its report, if anything. */
function SubagentCard({ subagent }: { subagent: TranscriptSubagent }) {
  const { runId, vendor, loadSubagentActivity } = useContext(SubagentHostContext);
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<SubagentState>(null);
  const linked = subagent.session_id !== "" && loadSubagentActivity !== undefined;
  const load = useCallback(() => {
    if (!loadSubagentActivity) return;
    setDetail("loading");
    loadSubagentActivity(runId, subagent.session_id)
      .then(setDetail)
      .catch((error: unknown) =>
        setDetail((error as { status?: number } | null)?.status === 404 ? "missing" : "failed"),
      );
  }, [loadSubagentActivity, runId, subagent.session_id]);
  const toggle = () => {
    if (!open && linked && (detail === null || detail === "failed")) load();
    setOpen((value) => !value);
  };
  const duration = durationSeconds(subagent.started_at, subagent.ended_at);
  const failed = subagent.status === "error";
  const status = spawnStatusLabel(subagent.status);
  // Open, the conversation ends in the response itself; it is said once there
  // when the conversation holds all of it.
  const responseShown =
    !open || typeof detail !== "object" || detail === null || !endsWithResponse(subagent, detail);

  return (
    <Card
      size="flush"
      className={cn(
        "ml-9 border-l-2",
        failed ? "border-l-(--status-critical)" : "border-l-(--border-strong)",
      )}
      data-subagent={subagent.session_id || subagent.tool_call_id}
    >
      <button
        type="button"
        aria-expanded={open}
        onClick={toggle}
        className={cn(
          "flex w-full cursor-pointer items-center gap-2 rounded-t-(--radius-lg) border-b border-(--border-subtle) px-3 py-2 text-left transition-colors duration-150 hover:bg-(--surface-hover)",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          size={12}
          className={cn(
            "flex-none text-(--text-tertiary) transition-transform",
            !open && "-rotate-90",
          )}
        />
        <span className={cn(CARD_LABEL, "flex-none")}>Subagent</span>
        <span className="min-w-0 flex-1 truncate text-base text-(--text-secondary)">
          {subagent.task || subagent.name} ·{" "}
          <HarnessName vendor={vendor} className="align-middle" />
          {duration != null ? ` · ${formatDuration(duration)}` : ""}
        </span>
        {status ? (
          <Badge size="compact" tone={failed ? "critical" : "neutral"}>
            {status}
          </Badge>
        ) : null}
      </button>

      <div className="flex flex-col gap-2.5 px-3 py-2.5">
        {!subagent.response ? (
          <p className="text-xs text-(--text-tertiary) italic">{missingResponseNote(vendor)}</p>
        ) : responseShown ? (
          <AgentText
            vendor={vendor}
            author="Response"
            text={subagent.response}
            length={subagent.response_length}
            truncated={subagent.response_truncated}
            // The whole response is the conversation's last message.
            onExpand={linked && !open ? toggle : undefined}
          />
        ) : null}
        <SubagentTotals subagent={subagent} />
      </div>

      {open ? (
        <div className="flex flex-col gap-1.5 rounded-b-(--radius-lg) border-t border-(--border-subtle) bg-(--surface-sunken) px-2 py-3 sm:px-3">
          {linked ? (
            <SubagentConversation detail={detail} onRetry={load} />
          ) : (
            <>
              {/* A host that cannot load a captured transcript shows the
                  input alone rather than call it missing. */}
              {subagent.session_id === "" ? (
                <SystemNote align="start">
                  This subagent's own transcript was not captured.
                </SystemNote>
              ) : null}
              <p className="text-xs text-(--text-tertiary)">Input to {subagent.name}</p>
              {redactedSpawnMessage(subagent.input) ? (
                <p className="text-xs text-(--text-tertiary)">
                  {harnessName(vendor)} recorded this instruction redacted, so its text is not
                  available.
                </p>
              ) : null}
              <pre className="max-h-56 overflow-auto rounded-md bg-(--surface-card) p-2 text-xs whitespace-pre-wrap text-(--text-secondary) [overflow-wrap:anywhere]">
                {subagent.input}
                {subagent.input_truncated ? "\n…" : ""}
              </pre>
            </>
          )}
        </div>
      ) : null}
    </Card>
  );
}

/** A subagent's figures, covering every subagent it spawned in turn. One the
 *  data lacks is left out rather than shown as zero; a subagent with no
 *  transcript has none, and without the host's `formatCost` no cost shows. */
function SubagentTotals({ subagent }: { subagent: TranscriptSubagent }) {
  const { formatCost, modelLabel } = useContext(SubagentHostContext);
  if (!subagent.session_id) return null;
  const cost = formatCost?.(subagent.cost_usd, subagent.cost_is_floor) ?? null;
  return (
    <dl className="flex flex-wrap gap-x-5 gap-y-2 text-base">
      {subagent.models.length > 0 ? (
        <Stat
          label="Models"
          value={
            <span className="inline-flex flex-wrap gap-x-3 gap-y-1">
              {subagent.models.map((model) => (
                <ModelName key={model} model={model} label={modelLabel(model)} />
              ))}
            </span>
          }
        />
      ) : null}
      {subagent.tokens > 0 ? (
        <Stat
          label="Tokens"
          value={formatCompact(subagent.tokens)}
          detail={estimatedTokenDetail(subagent.tokens, subagent.estimated_tokens)}
        />
      ) : null}
      {!formatCost ? null : cost ? (
        <Stat
          label="Cost"
          value={cost}
          detail={subagent.cost_is_floor ? "lower bound · some usage has no price" : undefined}
        />
      ) : subagent.tokens > 0 ? (
        <Stat label="Cost" value={formatUSD(null)} detail="no price for this usage" />
      ) : null}
      <Stat label="Tool calls" value={formatCount(subagent.tool_calls)} />
      {subagent.failed_calls > 0 ? (
        <Stat label="Failed calls" value={formatCount(subagent.failed_calls)} />
      ) : null}
    </dl>
  );
}

/** A subagent's own turns: each instruction its parent gave it, on the right
 *  where a prompt sits, then what it did about it. */
function SubagentConversation({ detail, onRetry }: { detail: SubagentState; onRetry: () => void }) {
  const { vendor } = useContext(SubagentHostContext);
  if (detail === null || detail === "loading") {
    return (
      <SystemNote align="start">
        <Spinner />
        Loading the subagent's activity…
      </SystemNote>
    );
  }
  if (detail === "failed") {
    return (
      <p role="alert" className="flex flex-wrap items-center gap-2 self-center text-base">
        <span className="text-(--status-critical)">
          The subagent's activity could not be loaded.
        </span>
        <Button variant="secondary" size="tiny" onClick={onRetry}>
          Try again
        </Button>
      </p>
    );
  }
  if (detail === "missing" || detail.turns.length === 0) {
    return <SystemNote align="start">No activity was captured for this subagent.</SystemNote>;
  }
  return (
    <ol className="flex flex-col gap-4">
      {detail.turns.map(({ input, activity }) => (
        <li key={input.turn_id} className="flex flex-col gap-1.5">
          <PromptBubble
            kind="instruction"
            author={input.origin === "agent" ? `Instruction from ${harnessName(vendor)}` : "Input"}
            // The parent agent wrote it, so its harness's mark stands where a
            // person's avatar would.
            avatar={<AgentAvatar vendor={vendor} />}
            text={input.text}
            length={input.text_length}
            truncated={input.truncated}
            meta={
              <>
                <span className="tabular-nums">#{input.ordinal}</span>
                {input.started_at ? (
                  <time dateTime={input.started_at}>{formatTime(input.started_at)}</time>
                ) : null}
              </>
            }
          />
          <ActivityTimeline
            vendor={vendor}
            activity={activity}
            sessionLevelTokens={false}
            onRetry={onRetry}
          />
        </li>
      ))}
    </ol>
  );
}
