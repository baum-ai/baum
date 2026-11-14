import pluralize from "pluralize";
import { useState, type ReactNode } from "react";
import { Badge, Button, LiveDot } from "@baum/viewer/components/ui";
import { JumpLink } from "@baum/viewer/components/review-parts";
import {
  RECOMMENDATION_LABEL,
  RECOMMENDATION_TONE,
  riskScoreTone,
  riskScoreView,
  type ReviewDocument,
} from "@baum/viewer/review-document";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { cn } from "@baum/viewer/components/classnames";
import { plainText } from "./parts";
import { itemTarget } from "./place";
import {
  changesInState,
  currentGaps,
  tileById,
  tileOfChange,
  type Tile,
  type TileDocument,
} from "./tile-document";
import { useTileHost } from "./host";
import type { TileContext } from "./types";

/** The band over every landing page, in the same place on every review: the
 *  review's bottom line when it gives one, the pull request's risk score, and
 *  where this version stands when that is news (outdated, a draft, changes
 *  being written or that failed, an earlier version); the version bar above
 *  already says a version is current. One line once wide. */
export function FixedStrip({
  ctx,
  generation,
}: {
  ctx: TileContext;
  generation: ReviewPlaceContext["generation"];
}) {
  return (
    <section
      aria-label="Review at a glance"
      data-review-block=""
      className="flex min-w-0 flex-col gap-1.5 rounded-lg border border-default bg-surface-card px-4 py-2.5 outline-none data-[review-block-active]:border-focus"
    >
      {given(ctx) ? <Recommendation ctx={ctx} /> : null}
      <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1.5">
        <RiskScore ctx={ctx} />
        <EvidenceGaps ctx={ctx} />
        <Generation ctx={ctx} generation={generation} />
        <YourLayout ctx={ctx} />
      </div>
    </section>
  );
}

/** Whether the review gives a recommendation this page knows. */
function given(ctx: TileContext) {
  const recommendation = ctx.doc.recommendation;
  return Boolean(recommendation && RECOMMENDATION_LABEL[recommendation.decision]);
}

/** Where a reference in the recommendation leads: the tile holding it. */
function referenceTargets(doc: TileDocument, review: ReviewDocument, refs: string[]) {
  const sections = new Map<string, string>();
  for (const section of review.sections) {
    sections.set(section.id, section.id);
    for (const risk of section.risks) sections.set(risk.id, section.id);
    for (const item of section.evidence) sections.set(item.id, section.id);
  }
  return [...new Set(refs)].flatMap((ref) => {
    const section = sections.get(ref);
    if (!section) return [];
    const own = tileOfChange(doc, section);
    const target: { tile: Tile; item?: string } | null =
      ref === section ? (own ? { tile: own } : null) : itemTarget(doc, section, ref);
    if (!target) return [];
    const label =
      ref === section
        ? (review.sections.find((item) => item.id === section)?.title ?? target.tile.title)
        : target.tile.title;
    return [{ ref, label, ...target }];
  });
}

/** The review's bottom line: its decision and its sentence, then the tiles
 *  it cites. */
function Recommendation({ ctx }: { ctx: TileContext }) {
  const recommendation = ctx.doc.recommendation!;
  const targets = referenceTargets(ctx.doc, ctx.review, recommendation.refs ?? []);
  return (
    // A line of its own over the risk score: two lines once wide, five on a
    // phone, where there is no one screen to keep it short for.
    <div role="group" aria-label="Recommendation" className="min-w-0">
      <p className="line-clamp-5 min-w-0 text-base text-primary @min-[36rem]/tiles:line-clamp-2">
        <Badge tone={RECOMMENDATION_TONE[recommendation.decision]} className="mr-2 align-middle">
          {RECOMMENDATION_LABEL[recommendation.decision]}
        </Badge>
        {plainText(recommendation.text, ctx.labels)}
        {targets.slice(0, 3).map((target) => (
          <span key={target.ref}>
            {" "}
            <JumpLink className="inline" onClick={() => ctx.open(target.tile.id, target.item)}>
              {target.label} →
            </JumpLink>
          </span>
        ))}
      </p>
    </div>
  );
}

const SCORE_TEXT = {
  neutral: "text-primary",
  info: "text-status-info",
  positive: "text-status-positive",
  caution: "text-status-caution",
  critical: "text-status-critical",
} as const;

/** The risk score on one line: the score and its meaning, then its strongest
 *  reason, cut to the line, which leads to what it names; every reason and
 *  any gap in the input are in its title. */
function RiskScore({ ctx }: { ctx: TileContext }) {
  const view = riskScoreView(ctx.review);
  const reasons = ctx.doc.risk_score?.reasons ?? [];
  if (!view.assessed)
    return (
      <p role="group" aria-label="Risk score" className="text-base text-secondary">
        <span className="font-medium text-primary">Risk not assessed</span>
        {" · "}
        {view.legacy ? "generated before risk scores" : "no current change was rated"}
      </p>
    );
  const reason = reasons[0];
  // The strongest reason, leading to the risk it names where it names one.
  const target = reason
    ? ((reason.risk ? itemTarget(ctx.doc, reason.section, reason.risk) : null) ??
      itemTarget(ctx.doc, reason.section, undefined))
    : null;
  const all = [
    ...reasons.map((each) => plainText(each.text, ctx.labels)),
    ...(view.limitations.length > 0 ? [`Incomplete input: ${view.limitations.join(", ")}`] : []),
  ].join("\n");
  return (
    <div
      role="group"
      aria-label="Risk score"
      className="flex min-w-0 flex-[1_1_24rem] items-baseline gap-x-2 text-base"
      title={all || undefined}
    >
      <span className="flex-none text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
        Risk
      </span>
      <span
        className={cn(
          "flex-none font-semibold tabular-nums",
          SCORE_TEXT[riskScoreTone(view.score)],
        )}
      >
        {view.lowerBound ? "≥" : ""}
        {view.score}
        <span className="font-normal text-tertiary">/5</span>
      </span>
      <span className="line-clamp-2 min-w-0 text-secondary @min-[36rem]/tiles:flex-none">
        {view.lowerBound ? "at least; input was incomplete" : view.meaning}
      </span>
      {reason && target ? (
        <>
          {/* The reason waits for a wider band; on a phone its title says it. */}
          <span aria-hidden className="hidden flex-none text-tertiary @min-[36rem]/tiles:inline">
            ·
          </span>
          <JumpLink
            tone="secondary"
            className="hidden min-w-0 shrink-[2] truncate @min-[36rem]/tiles:block"
            onClick={() => ctx.open(target.tile.id, target.item)}
          >
            {plainText(reason.text, ctx.labels)}
          </JumpLink>
        </>
      ) : null}
    </div>
  );
}

/** Missing or failed evidence of the current changes, always on the first
 *  screen however far down the Evidence tile sits, leading to the first gap. */
function EvidenceGaps({ ctx }: { ctx: TileContext }) {
  const gaps = currentGaps(ctx.doc);
  if (gaps.length === 0 || !tileById(ctx.doc, "evidence")) return null;
  const missing = gaps.filter((item) => item.status === "missing").length;
  const failed = gaps.length - missing;
  return (
    <JumpLink className="flex-none text-base" onClick={() => ctx.open("evidence", gaps[0]!.id)}>
      <span
        data-evidence-gaps=""
        className={failed > 0 ? "text-status-critical" : "text-status-caution"}
      >
        {[missing > 0 ? `${missing} missing` : "", failed > 0 ? `${failed} failed` : ""]
          .filter(Boolean)
          .join(" · ")}{" "}
        evidence
      </span>
    </JumpLink>
  );
}

/** Where this version stands, when that is news: a draft,
 *  changes being written or that failed (with Retry), a new version being
 *  generated, or an outdated review. A current review says nothing here. */
function Generation({
  ctx,
  generation,
}: {
  ctx: TileContext;
  generation: ReviewPlaceContext["generation"];
}) {
  const pending = changesInState(ctx.doc, "pending").length;
  const failed = changesInState(ctx.doc, "failed").length;
  const current = ctx.doc.changes.filter((change) => change.derived.state !== "stale").length;
  const state = ctx.draft ? "draft" : (generation?.state ?? ctx.review.review.generation_state);
  const parts: ReactNode[] = [];
  // An earlier version says so in the version bar above, and in its column
  // while compared: the band adds nothing for it.
  if (!ctx.historical) {
    if (ctx.draft)
      parts.push(
        <Badge key="draft" tone="info" className="gap-1.5">
          <LiveDot />
          Draft
        </Badge>,
      );
    if (pending > 0)
      parts.push(
        <span key="pending" role="status" className="flex items-center gap-2 text-secondary">
          {ctx.draft ? null : <LiveDot />}
          Writing details for {pending} of {pluralize("change", current, true)}
        </span>,
      );
    else if (generation?.rewriting)
      parts.push(
        <span key="rewriting" role="status" className="flex items-center gap-2 text-secondary">
          <LiveDot />A new version is being generated
        </span>,
      );
    else if (state === "outdated")
      parts.push(
        <Badge key="outdated" tone="caution" title="The pull request changed since this review.">
          Outdated
        </Badge>,
      );
    else if (state === "failed")
      parts.push(
        <Badge key="failed" tone="critical">
          Last run failed
        </Badge>,
      );
    if (failed > 0) parts.push(<Failures key="failures" count={failed} ctx={ctx} />);
    if (ctx.contextPending && tileById(ctx.doc, "context")?.state === "pending")
      parts.push(
        <span key="context" className="text-xs text-tertiary">
          Context still being written
        </span>,
      );
  }
  if (parts.length === 0) return null;
  return (
    <div
      role="group"
      aria-label="This version"
      className="flex flex-none flex-wrap items-center gap-x-3 gap-y-1 text-base"
    >
      {parts}
    </div>
  );
}

function Failures({ count, ctx }: { count: number; ctx: TileContext }) {
  const [busy, setBusy] = useState(false);
  const retry = ctx.onRetryDetails;
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-base text-status-critical">
      {count === 1 ? "1 change couldn't be written" : `${count} changes couldn't be written`}
      {retry ? (
        <Button
          variant="secondary"
          size="compact"
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              const first = changesInState(ctx.doc, "failed")[0];
              if (first) await retry(first.id);
            } catch {
              // The page's notices say why.
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Retrying…" : "Retry"}
        </Button>
      ) : null}
    </p>
  );
}

/** Says, quietly, that the tiles are laid out the person's way, while they
 *  are, with Undo (the change it takes back is its title) and Reset to the
 *  generated layout. */
function YourLayout({ ctx }: { ctx: TileContext }) {
  const layout = useTileHost().layout;
  if (!layout?.edited || ctx.historical || ctx.draft) return null;
  const last = layout.lastSummary.join(" · ");
  return (
    <div
      role="group"
      aria-label="Your layout"
      data-your-layout=""
      className="flex flex-none items-center gap-1 text-base @min-[36rem]/tiles:ml-auto"
    >
      <span className="mr-1 flex items-center gap-1.5 text-secondary">
        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-action-primary" />
        Your layout
      </span>
      <Button
        variant="ghost"
        size="tiny"
        disabled={!layout.canUndo}
        title={last ? `Undo: ${last}` : undefined}
        aria-label={last ? `Undo: ${last}` : "Undo"}
        onClick={layout.undo}
      >
        Undo
      </Button>
      <Button
        variant="ghost"
        size="tiny"
        title="Lay the tiles out as the review was generated; Undo brings your layout back"
        onClick={layout.reset}
      >
        Reset
      </Button>
    </div>
  );
}
