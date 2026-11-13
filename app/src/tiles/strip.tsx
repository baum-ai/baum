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
import { Clamp, plainText } from "./parts";
import { itemTarget } from "./place";
import {
  changesInState,
  tileById,
  tileOfChange,
  type Tile,
  type TileDocument,
} from "./tile-document";
import type { TileContext } from "./types";

/** The strip over every landing page, in the same place on every review: the
 *  review's bottom line, the pull request's risk score, and where this
 *  version stands. */
export function FixedStrip({
  ctx,
  generation,
}: {
  ctx: TileContext;
  generation: ReviewPlaceContext["generation"];
}) {
  return (
    // Cells side by side while they fit, each wrapping to a row of its own
    // below; the rules between them are each cell's own top and left edge,
    // clipped where a cell starts a row.
    <section
      aria-label="Review at a glance"
      className="flex flex-wrap overflow-hidden rounded-lg border border-default bg-surface-card"
    >
      <Cell label="Recommendation" grow={given(ctx) ? "wide" : "narrow"}>
        <Recommendation ctx={ctx} />
      </Cell>
      <Cell label="Risk score" grow="wide">
        <RiskScore ctx={ctx} />
      </Cell>
      <Cell label={ctx.historical ? "Version" : "This version"} grow="narrow">
        <Generation ctx={ctx} generation={generation} />
      </Cell>
    </section>
  );
}

function Cell({
  label,
  grow,
  children,
}: {
  label: string;
  grow: "wide" | "narrow";
  children: ReactNode;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      data-review-block=""
      className={cn(
        "-mt-px -ml-px min-w-0 space-y-1 border-t border-l border-subtle px-4 pt-2.5 pb-3 outline-none data-[review-block-active]:bg-surface-wash",
        grow === "wide" ? "flex-[2_1_17rem]" : "flex-[1_1_9rem]",
      )}
    >
      <p className="text-xs font-medium tracking-[0.08em] text-tertiary uppercase">{label}</p>
      {children}
    </div>
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

function Recommendation({ ctx }: { ctx: TileContext }) {
  const recommendation = ctx.doc.recommendation;
  if (!recommendation || !given(ctx)) return <p className="text-base text-tertiary">None given</p>;
  const targets = referenceTargets(ctx.doc, ctx.review, recommendation.refs ?? []);
  return (
    <>
      <Clamp lines={3} className="text-base leading-normal text-primary">
        <span title={plainText(recommendation.text, ctx.labels)}>
          <Badge tone={RECOMMENDATION_TONE[recommendation.decision]} className="mr-2 align-[1px]">
            {RECOMMENDATION_LABEL[recommendation.decision]}
          </Badge>
          {plainText(recommendation.text, ctx.labels)}
        </span>
      </Clamp>
      {targets.length > 0 ? (
        <p className="flex flex-wrap gap-x-3 gap-y-0.5 text-base">
          {targets.slice(0, 3).map((target) => (
            <JumpLink
              key={target.ref}
              className="max-w-full truncate"
              onClick={() => ctx.open(target.tile.id, target.item)}
            >
              {target.label} →
            </JumpLink>
          ))}
        </p>
      ) : null}
    </>
  );
}

const SCORE_TEXT = {
  neutral: "text-primary",
  info: "text-status-info",
  positive: "text-status-positive",
  caution: "text-status-caution",
  critical: "text-status-critical",
} as const;

function RiskScore({ ctx }: { ctx: TileContext }) {
  const view = riskScoreView(ctx.review);
  if (!view.assessed)
    return (
      <>
        <p className="text-base font-medium text-primary">Not assessed</p>
        <p className="text-xs text-tertiary">
          {view.legacy
            ? "Generated before risk scores."
            : "No current change of this review was rated."}
        </p>
      </>
    );
  // The strongest reason, leading to the risk it names where it names one.
  const reason = ctx.doc.risk_score?.reasons[0];
  const reasonTarget = reason
    ? ((reason.risk ? itemTarget(ctx.doc, reason.section, reason.risk) : null) ??
      itemTarget(ctx.doc, reason.section, undefined))
    : null;
  return (
    <>
      <p className="flex flex-wrap items-baseline gap-x-2">
        <span
          className={cn(
            "text-lg font-semibold tabular-nums",
            SCORE_TEXT[riskScoreTone(view.score)],
          )}
        >
          {view.lowerBound ? "≥" : ""}
          {view.score}
          <span className="text-base font-normal text-tertiary">/5</span>
        </span>
        <span className="text-base text-secondary">
          {view.lowerBound ? "At least; input was incomplete" : view.meaning}
        </span>
      </p>
      {reason && reasonTarget ? (
        <JumpLink
          tone="secondary"
          className="line-clamp-2 text-base"
          onClick={() => ctx.open(reasonTarget.tile.id, reasonTarget.item)}
        >
          {plainText(reason.text, ctx.labels)} →
        </JumpLink>
      ) : null}
      {view.limitations.length > 0 ? (
        <p className="text-xs text-tertiary">Incomplete input: {view.limitations.join(" · ")}</p>
      ) : null}
    </>
  );
}

/** Where this version stands: current or outdated, a draft being written,
 *  changes being written or that failed (with Retry), or the earlier
 *  version being read. */
function Generation({
  ctx,
  generation,
}: {
  ctx: TileContext;
  generation: ReviewPlaceContext["generation"];
}) {
  const version = ctx.review.review.version;
  const pending = changesInState(ctx.doc, "pending").length;
  const failed = changesInState(ctx.doc, "failed").length;
  const current = ctx.doc.changes.filter((change) => change.derived.state !== "stale").length;
  if (ctx.historical)
    return (
      <p className="text-base text-primary">
        Review v{version} <span className="text-secondary">· an earlier version</span>
      </p>
    );
  const state = ctx.draft ? "draft" : (generation?.state ?? ctx.review.review.generation_state);
  return (
    <>
      <p className="flex flex-wrap items-center gap-2 text-base">
        {ctx.draft ? (
          <Badge tone="info" className="gap-1.5">
            <LiveDot />
            Draft
          </Badge>
        ) : (
          <>
            <span className="text-primary">Review v{version}</span>
            {state === "current" ? (
              <Badge tone="positive">Current</Badge>
            ) : state === "outdated" ? (
              <Badge tone="caution">Outdated</Badge>
            ) : state === "failed" ? (
              <Badge tone="critical">Last run failed</Badge>
            ) : null}
          </>
        )}
      </p>
      {pending > 0 ? (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          {ctx.draft ? null : <LiveDot />}
          Writing details for {pending} of {pluralize("change", current, true)}
        </p>
      ) : generation?.rewriting ? (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />A new version is being generated
        </p>
      ) : state === "outdated" ? (
        <p className="text-base text-secondary">The pull request changed since this review.</p>
      ) : null}
      {failed > 0 ? <Failures count={failed} ctx={ctx} /> : null}
      {ctx.contextPending && tileById(ctx.doc, "context")?.state === "pending" ? (
        <p className="text-xs text-tertiary">The codebase context is still being written.</p>
      ) : null}
    </>
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
