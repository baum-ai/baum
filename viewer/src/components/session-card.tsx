import pluralize from "pluralize";
import { type ReactNode, useEffect, useState } from "react";

import { estimatedTokenDetail, join, type TranscriptSession } from "../transcript";
import { HarnessName, ModelName } from "./brand-logo";
import { Stat } from "./stat";
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
import { Empty } from "./states";
import { SessionTranscript, type TranscriptHostProps } from "./transcript";
import { Badge, type BadgeTone, Card, Spinner } from "./ui";

/** A linked session as a card: its header, its totals, and its prompts as a
 *  conversation (`SessionTranscript`). The view model is plain data in
 *  `../transcript`; the host passes its loaders and wording as props, and a
 *  feature whose loader is left out is off. */

const OUTCOME_TONES: Record<string, BadgeTone> = {
  committed: "positive",
  abandoned: "neutral",
  interrupted: "caution",
  unknown: "neutral",
};

/** The rule down a session card's left edge: the outcome badge's colour,
 *  drawn where a reader scanning a stack of cards will see it first. A session
 *  matched to commits is committed whatever its recorded outcome says. */
function outcomeRule(session: TranscriptSession): string {
  if (session.commit_count > 0 || session.outcome === "committed")
    return "border-l-(--status-positive)";
  if (session.outcome === "interrupted") return "border-l-(--status-caution)";
  return "border-l-(--border-strong)";
}

/** A card that answers the pointer: it lifts a pixel and takes a shadow, the
 *  one place in a flat page where depth says "this opens". */
/** A model named by its id, when the host names none. */
const modelId = (model: string) => model;

const LIFT =
  "border-l-2 transition-[transform,box-shadow,border-color] duration-200 ease-out hover:-translate-y-px hover:shadow-(--shadow-raised) motion-reduce:transition-none motion-reduce:hover:translate-y-0";

/** `meta` is a strip below the header, always visible. The header is a button,
 *  so anything the reader can click — a commit or pull request link — has to
 *  live outside it.
 *
 *  A session can arrive with prompts but without the agent's replies, or with
 *  neither; opening the card loads the whole session once (`loadSession`) and
 *  keeps it.
 *
 *  `expand` opens the card from outside — the review rail naming this session
 *  — without taking the toggle away from the reader: it opens on the rising
 *  edge and leaves the card alone otherwise.
 *
 *  `focusTurn` marks the prompt of that turn and scrolls it into view once the
 *  card shows it, for a reader sent to one moment of the session.
 *
 *  `modelLabel` names a model (the model id by default), `formatCost` writes
 *  a cost (without it no cost is shown), and `restrictedNote` says why a
 *  restricted session's details are withheld, in the host's own terms. */
export function SessionCard({
  session,
  defaultOpen,
  expand = false,
  meta,
  focusTurn,
  source,
  formatCost,
  renderAvatar,
  modelLabel = modelId,
  restrictedNote = "This session's prompts, metadata, usage, and activity are not shown here.",
}: {
  session: TranscriptSession;
  defaultOpen: boolean;
  expand?: boolean;
  meta?: ReactNode;
  focusTurn?: number;
  modelLabel?: (model: string) => string;
  restrictedNote?: ReactNode;
} & TranscriptHostProps) {
  const [open, setOpen] = useState(defaultOpen || expand);
  const [detail, setDetail] = useState<TranscriptSession | "loading" | "failed" | null>(null);
  const duration = durationSeconds(session.started_at, session.ended_at);
  const full = detail && typeof detail !== "string" ? detail : session;
  const loadSession = source?.loadSession;
  // The whole session is loaded the first time the card opens, whichever
  // way it opened: a report can carry prompts but not the agent's replies,
  // and a list carries neither.
  const needsDetail =
    open &&
    detail === null &&
    Boolean(loadSession) &&
    !session.replies_included &&
    !session.restricted;
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
        size="flush"
        className="border-l-2 border-l-(--border-strong)"
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
    <Card size="flush" className={cn(LIFT, outcomeRule(session))} data-session-id={session.run_id}>
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
          {typeof detail === "string" && full.prompts.length === 0 ? (
            <p className="flex items-center gap-2 text-base text-(--text-tertiary) italic">
              {detail === "loading" ? <Spinner /> : null}
              {detail === "loading" ? "Loading this session…" : "This session could not be loaded."}
            </p>
          ) : (
            <>
              <SessionTotals session={full} modelLabel={modelLabel} formatCost={formatCost} />
              {full.has_inner_trace ? (
                // A report's prompts show at once; their replies follow
                // with the session.
                <SessionTranscript
                  session={full}
                  // Loading from the first render, before the effect asks:
                  // an empty reply area would say the agent never answered.
                  replies={
                    detail === "failed"
                      ? "failed"
                      : needsDetail || detail === "loading"
                        ? "loading"
                        : "ready"
                  }
                  focusTurn={focusTurn}
                  source={source}
                  formatCost={formatCost}
                  modelLabel={modelLabel}
                  renderAvatar={renderAvatar}
                />
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
  formatCost,
}: {
  session: TranscriptSession;
  modelLabel: (model: string) => string;
  formatCost: TranscriptHostProps["formatCost"];
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
      {/* A host without prices shows no Cost at all, rather than an empty one. */}
      {formatCost ? (
        <Stat
          label="Cost"
          value={formatCost(session.cost_usd, session.cost_is_floor) ?? formatUSD(null)}
          detail={
            session.cost_usd == null && session.tokens > 0
              ? "no price for some of this usage"
              : session.cost_is_floor
                ? "lower bound · some usage has no price"
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
