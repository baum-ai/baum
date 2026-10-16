import pluralize from "pluralize";
import { type ReactNode, useEffect, useState } from "react";

import { estimatedTokenDetail, join, type TranscriptSession } from "../transcript";
import { HarnessName } from "./brand-logo";
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
import { BranchIcon, ChevronDownIcon } from "./icons";
import { Empty } from "./states";
import { SessionTranscript, type TranscriptHostProps } from "./transcript";
import { Badge, type BadgeTone, Spinner } from "./ui";

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

/** A model named by its id, when the host names none. */
const modelId = (model: string) => model;

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
      <section className="flex min-w-0 flex-col" data-session-id={session.run_id}>
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
      </section>
    );
  }

  const toggle = () => setOpen((value) => !value);

  return (
    <section className="flex min-w-0 flex-col" data-session-id={session.run_id}>
      <button
        type="button"
        aria-label={`Session: ${session.title || "Coding session"}`}
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
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-base font-medium text-(--text-primary)">
            {session.title || "Coding session"}
          </span>
          <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-(--text-tertiary)">
            <HarnessName vendor={session.source} size={12} />
            {full.models.length > 0 ? (
              <span
                className="text-(--text-secondary)"
                title={full.models.map(modelLabel).join(", ")}
              >
                {full.models.map(modelLabel).join(" · ")}
              </span>
            ) : null}
            {session.git_branch ? (
              <Badge
                size="compact"
                title={`Branch: ${session.git_branch}`}
                className="min-w-0 max-w-60 gap-1"
              >
                <BranchIcon size={12} className="flex-none" />
                <span className="truncate">{session.git_branch}</span>
              </Badge>
            ) : null}
            <span>
              {session.started_at ? `${formatDate(session.started_at)} · ` : ""}
              {formatDuration(duration)} · {pluralize("prompt", session.turn_count, true)}
            </span>
            {session.user_email ? <span>{session.user_email}</span> : null}
          </span>
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
              <SessionTotals session={full} formatCost={formatCost} />
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
    </section>
  );
}

function SessionTotals({
  session,
  formatCost,
}: {
  session: TranscriptSession;
  formatCost: TranscriptHostProps["formatCost"];
}) {
  const totals = session.totals;
  return (
    <div className="@container mb-5 space-y-3 border-b border-(--border-subtle) pb-4">
      <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-base @lg:grid-cols-4">
        <Stat
          label={formatCost ? "Tokens · Cost" : "Tokens"}
          value={
            <span className="flex flex-wrap items-baseline gap-x-2">
              <span>{formatCompact(session.tokens)}</span>
              {formatCost ? (
                <span className="text-(--text-secondary)">
                  {formatCost(session.cost_usd, session.cost_is_floor) ?? formatUSD(null)}
                </span>
              ) : null}
            </span>
          }
          detail={
            join([
              estimatedTokenDetail(session.tokens, session.estimated_tokens),
              session.tokens > 0 && !session.cost_per_prompt && "reported at session level",
              formatCost &&
                (session.cost_usd == null && session.tokens > 0
                  ? "no price for some of this usage"
                  : session.cost_is_floor
                    ? "lower bound · some usage has no price"
                    : null),
            ]) ?? undefined
          }
        />
        {totals ? (
          <>
            <Stat
              label="Files"
              value={
                <span className="flex flex-wrap gap-x-3">
                  <span>
                    {formatCount(totals.files_read)}{" "}
                    <span className="text-xs text-(--text-tertiary)">read</span>
                  </span>
                  <span>
                    {formatCount(totals.files_edited)}{" "}
                    <span className="text-xs text-(--text-tertiary)">edited</span>
                  </span>
                </span>
              }
            />
            <Stat
              label="Lines"
              value={
                <span className="flex flex-wrap gap-x-2">
                  <span className="text-(--status-positive)">
                    +{formatCount(totals.added_lines)}
                  </span>
                  <span className="text-(--status-critical)">
                    −{formatCount(totals.removed_lines)}
                  </span>
                </span>
              }
            />
            <Stat
              label="Tool calls"
              value={
                <span className="flex flex-wrap items-baseline gap-x-3">
                  <span>{formatCount(totals.tool_calls)}</span>
                  {totals.failed_calls > 0 ? (
                    <span className="text-xs text-(--status-critical)">
                      {formatCount(totals.failed_calls)} failed
                    </span>
                  ) : null}
                </span>
              }
            />
          </>
        ) : null}
      </dl>
    </div>
  );
}
