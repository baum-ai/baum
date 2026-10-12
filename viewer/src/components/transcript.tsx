import pluralize from "pluralize";
import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { dayBreaks } from "../conversation";
import {
  type ActivityState,
  type CostFormat,
  effectFacts,
  formatTokenUsage,
  fullReplyText,
  join,
  timeline,
  type TranscriptPrompt,
  type TranscriptSession,
  type TranscriptSource,
  type TranscriptToolCall,
} from "../transcript";
import { AgentText, ChatSurface, PromptBubble, SystemNote } from "./chat";
import { cn, FOCUS_RING } from "./classnames";
import { formatCompact, formatTime } from "./format";
import { ChevronDownIcon } from "./icons";
import { Empty } from "./states";
import { Button, Spinner } from "./ui";

/** Where the replies of a session stand: carried by the session, still being
 *  fetched with it, or not available. */
export type RepliesState = "ready" | "loading" | "failed";

/** What a host adds to a transcript: its loaders (`source`), how it writes a
 *  cost (`formatCost`, none and no cost is shown), and the picture beside a
 *  person's prompt (`renderAvatar`, none and no picture or its column). */
export type TranscriptHostProps = {
  source?: TranscriptSource;
  formatCost?: CostFormat;
  renderAvatar?: (author: string) => ReactNode;
};

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
  renderAvatar,
}: {
  session: TranscriptSession;
  replies: RepliesState;
  focusTurn?: number;
} & TranscriptHostProps) {
  // Days split in the reader's own zone. The routes that show a transcript
  // render it on the client only, so this guard is defensive, as charts are.
  const mounted = useMounted();
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
    <ChatSurface>
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
        <SystemNote className="mt-3">This session has more prompts than are shown here.</SystemNote>
      ) : null}
    </ChatSurface>
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
  const row = useRef<HTMLLIElement>(null);
  // Again once the replies arrive: they grow every turn above this one.
  useEffect(() => {
    if (focused) row.current?.scrollIntoView({ block: "nearest" });
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
      className={cn(
        "flex flex-col gap-2",
        focused && "-mx-2 rounded-xl px-2 py-2 ring-2 ring-(--action-accent)",
      )}
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
        {activityOpen ? (
          <ActivityTimeline
            vendor={session.source}
            activity={activity}
            sessionLevelTokens={sessionLevelTokens}
            onRetry={load}
          />
        ) : (
          <AgentReplies
            vendor={session.source}
            prompt={prompt}
            state={replies}
            activity={activity}
            loadActivity={load}
            openActivity={available ? toggleActivity : undefined}
          />
        )}
        <TurnFooter
          prompt={prompt}
          cost={session.cost_per_prompt ? prompt.cost_usd : null}
          costIsFloor={session.cost_per_prompt && prompt.cost_is_floor}
          formatCost={formatCost}
          sessionLevelTokens={sessionLevelTokens}
          activityOpen={activityOpen}
          onToggleActivity={available ? toggleActivity : undefined}
        />
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
      <SystemNote>
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
  if (
    activity.messages.length === 0 &&
    activity.invocations.length === 0 &&
    activity.tool_calls.length === 0 &&
    activity.token_usage.tokens === 0 &&
    !sessionLevelTokens
  ) {
    return <SystemNote>No agent output was captured for this prompt.</SystemNote>;
  }

  const usage = activity.token_usage;
  const models = usage.models ?? [];
  const entries = timeline(activity);
  const firstMessage = entries.findIndex((entry) => entry.kind === "message");
  return (
    <>
      {!sessionLevelTokens && (usage.api_calls > 0 || usage.tokens > 0) ? (
        <SystemNote>
          {join([
            usage.api_calls > 0 && pluralize("model call", usage.api_calls, true),
            models.length > 0 && models.join(", "),
            usage.tokens > 0 && formatTokenUsage(usage.tokens, usage.estimated_tokens),
            usage.uncached_input_tokens + usage.output_tokens > 0 &&
              `${formatCompact(usage.uncached_input_tokens)} uncached in / ${formatCompact(usage.output_tokens)} out`,
          ])}
        </SystemNote>
      ) : null}
      {entries.map((entry, index) =>
        entry.kind === "message" ? (
          <AgentText
            key={`message-${index}`}
            vendor={index === firstMessage ? vendor : undefined}
            text={entry.message.text}
            length={entry.message.text_length}
            truncated={entry.message.truncated}
            caption={join([
              entry.message.model,
              entry.message.timestamp && formatTime(entry.message.timestamp),
            ])}
          />
        ) : (
          <ToolRun key={`tools-${entry.calls[0]?.id ?? index}`} calls={entry.calls} />
        ),
      )}
      {activity.messages_truncated ? (
        <SystemNote>This prompt has more agent messages than are shown here.</SystemNote>
      ) : null}
      {activity.tool_calls_truncated ? (
        <SystemNote>More tool calls were recorded.</SystemNote>
      ) : null}
    </>
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
    <details className="group flex flex-col">
      <summary
        className={cn(
          "mx-auto flex w-fit max-w-full cursor-pointer list-none items-center gap-1.5 rounded-full border border-(--border-subtle) bg-(--surface-card) px-3 py-1 text-xs text-(--text-secondary) hover:text-(--text-primary) [&::-webkit-details-marker]:hidden",
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
      <ul className="mx-auto mt-1.5 flex w-full max-w-[92%] flex-col gap-2 rounded-xl border border-(--border-subtle) bg-(--surface-card) px-3 py-2">
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
