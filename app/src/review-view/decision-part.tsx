import pluralize from "pluralize";
import { cn } from "@baum/viewer/components/classnames";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { JumpLink } from "@baum/viewer/components/review-parts";
import { RiskMeter } from "@baum/viewer/components/review-summary";
import { Badge, type BadgeTone } from "@baum/viewer/components/ui";
import {
  riskScoreTone,
  riskScoreView,
  type RecommendationDecision,
  type ReviewDocument,
  type Tone,
} from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import { decisionChecks, riskSummary, type CheckedRisk, type ReviewV2 } from "./model";

const SCORE_INK: Record<Tone, string> = {
  neutral: "text-primary",
  info: "text-status-info",
  positive: "text-status-positive",
  caution: "text-status-caution",
  critical: "text-status-critical",
};

/** The verdict in two words, as the pill that opens the strip. */
const DECISION: Record<RecommendationDecision, { label: string; tone: BadgeTone }> = {
  approve: { label: "Safe to merge", tone: "positive" },
  look_first: { label: "Look first", tone: "caution" },
  request_changes: { label: "Changes needed", tone: "critical" },
};

const TIER: Record<string, { label: string; ink: string }> = {
  critical: { label: "Critical", ink: "text-status-critical" },
  high: { label: "High", ink: "text-status-critical" },
  medium: { label: "Medium", ink: "text-status-caution" },
  low: { label: "Low", ink: "text-tertiary" },
};

/** A row of the list: what it is, what it says, and where to read on. */
const ROW =
  "grid gap-x-4 gap-y-0.5 px-4 py-2.5 @2xl:grid-cols-[7rem_minmax(0,1fr)_auto] @2xl:items-baseline";
const ROW_LABEL = "text-xs font-medium tracking-[0.08em] uppercase";

/** What to decide, first on the Overview: the verdict (its two words and its
 *  sentence) beside the risk score, then what to check before merging (the
 *  verdict's points and the open risks of the sections rated medium or
 *  higher, each leading to its section, the low ones folded under their
 *  count), and why the score is what it is. Every review with a verdict
 *  shows it, points or not.
 *
 *  `listed` is false on a brief, whose one section is on the Overview
 *  itself: its risks are right below, so the list keeps to the points. */
export function DecisionPart({
  review,
  listed,
  titleOf,
  select,
}: {
  review: ReviewV2;
  listed: boolean;
  titleOf: (id: string) => string | undefined;
  select: (selection: Selection) => void;
}) {
  const score = riskScoreView({
    summary: { risk_score: review.summary.risk_score },
    sections: review.sections,
  } as unknown as Pick<ReviewDocument, "summary" | "sections">);
  const why = riskSummary(review);
  const verdict = review.summary.verdict;
  const decision = verdict ? DECISION[verdict.decision] : undefined;
  const sentence = verdict?.text.trim() ?? "";
  const checks = decisionChecks(review);
  const risks = listed ? checks.risks : [];
  const low = listed ? checks.low : [];
  const riskRow = ({ risk, section }: CheckedRisk) => {
    const tier = TIER[risk.tier] ?? TIER.low!;
    const title = titleOf(section);
    return (
      <li key={risk.id} data-check="risk" className={ROW}>
        <span className={cn(ROW_LABEL, tier.ink)}>{tier.label}</span>
        <span className="min-w-0 text-base leading-[1.55] text-primary">
          <InlineMarkdown>{risk.text}</InlineMarkdown>
        </span>
        {title ? (
          <JumpLink
            className="justify-self-start @2xl:justify-self-end"
            onClick={() => select({ kind: "risk", section, id: risk.id })}
          >
            <InlineMarkdown>{title}</InlineMarkdown> →
          </JumpLink>
        ) : null}
      </li>
    );
  };
  return (
    <section
      aria-label="Risk"
      data-review-block=""
      className="overflow-hidden rounded-lg border border-default bg-surface-card outline-none data-[review-block-active]:border-strong"
    >
      <div className="flex flex-wrap items-center gap-x-6 gap-y-3 p-4">
        <p className="flex min-w-0 flex-[1_1_24rem] flex-wrap items-baseline gap-x-3 gap-y-1.5">
          {decision ? <Badge tone={decision.tone}>{decision.label}</Badge> : null}
          {sentence ? (
            <span data-verdict="" className="min-w-0 text-base leading-[1.5] font-semibold">
              <InlineMarkdown>{sentence}</InlineMarkdown>
            </span>
          ) : null}
        </p>
        <p className="flex flex-none flex-wrap items-center gap-x-3 gap-y-1">
          <span className="text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
            Risk
          </span>
          {score.assessed ? (
            <>
              <span
                className={cn(
                  "text-2xl font-medium tabular-nums",
                  SCORE_INK[riskScoreTone(score.score)],
                )}
              >
                {score.lowerBound ? `≥ ${score.score}` : score.score}
                <span className="text-base text-tertiary"> of 5</span>
              </span>
              <RiskMeter
                score={score.score}
                lowerBound={score.lowerBound}
                tone={riskScoreTone(score.score)}
              />
              {score.lowerBound ? null : (
                <span className="text-base text-secondary">{score.meaning}</span>
              )}
            </>
          ) : (
            <span className="text-base font-medium text-primary">Not assessed</span>
          )}
        </p>
      </div>
      {checks.points.length + risks.length + low.length > 0 ? (
        <div className="border-t border-default">
          {checks.points.length + risks.length > 0 ? (
            <ul aria-label="Check these" className="divide-y divide-subtle">
              {checks.points.map((point, at) => {
                // A section several points name is linked once, after the
                // last of them.
                const later = checks.points
                  .slice(at + 1)
                  .some((next) => next.section === point.section);
                const title = listed && point.section && !later ? titleOf(point.section) : "";
                return (
                  <li key={`point-${at}`} data-check="point" className={ROW}>
                    <span className={cn(ROW_LABEL, "text-status-caution")}>Before merging</span>
                    <span className="min-w-0 text-base leading-[1.55] text-primary">
                      <InlineMarkdown>{point.text}</InlineMarkdown>
                    </span>
                    {title && point.section ? (
                      <JumpLink
                        className="justify-self-start @2xl:justify-self-end"
                        onClick={() => select({ kind: "section", id: point.section! })}
                      >
                        <InlineMarkdown>{title}</InlineMarkdown> →
                      </JumpLink>
                    ) : null}
                  </li>
                );
              })}
              {risks.map(riskRow)}
            </ul>
          ) : null}
          {low.length > 0 ? (
            <details
              data-check-low=""
              className={cn(checks.points.length + risks.length > 0 && "border-t border-subtle")}
            >
              <summary className="cursor-pointer px-4 py-2.5 text-base text-secondary hover:text-primary">
                {pluralize("low risk", low.length, true)} in{" "}
                {pluralize("section", new Set(low.map((each) => each.section)).size, true)}
              </summary>
              <ul aria-label="Low risks" className="divide-y divide-subtle border-t border-subtle">
                {low.map(riskRow)}
              </ul>
            </details>
          ) : null}
        </div>
      ) : null}
      {why ? (
        <div className="border-t border-default px-4 py-3">
          <Markdown className="max-w-[80ch] text-base leading-[1.6] text-secondary">{why}</Markdown>
        </div>
      ) : !score.assessed ? (
        <p className="border-t border-default px-4 py-3 text-base text-secondary">
          {score.legacy
            ? "This review was generated before risk scores. Regenerate it to assess."
            : "No current section of this review was rated."}
        </p>
      ) : null}
    </section>
  );
}
