import { cn } from "@baum/viewer/components/classnames";
import { formatDateTime } from "@baum/viewer/components/format";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { JumpLink, Part, PROSE } from "@baum/viewer/components/review-parts";
import { ReviewImages } from "@baum/viewer/components/review-images";
import { RiskMeter } from "@baum/viewer/components/review-summary";
import { harnessName } from "@baum/viewer/agent-names";
import { useUrlState, ViewerLink } from "@baum/viewer/navigation";
import {
  riskScoreTone,
  riskScoreView,
  type ReviewDocument,
  type Tone,
} from "@baum/viewer/review-document";
import { withSelection } from "@baum/viewer/review-selection";
import type { TileContext } from "../tiles/types";
import {
  intentRows,
  riskSummary,
  viewTile,
  type IntentRow,
  type ListedSection,
  type ReviewV2,
  type ViewPlace,
} from "./model";
import { ViewTileCard } from "./view-tile";

const SCORE_INK: Record<Tone, string> = {
  neutral: "text-primary",
  info: "text-status-info",
  positive: "text-status-positive",
  caution: "text-status-caution",
  critical: "text-status-critical",
};

/** The Overview, always in the same order: the summary, the risk (with the
 *  verdict under it when something must happen before merging), what it
 *  looks like, the main diagram and what was asked. It reads the
 *  baum.review/2 `review`; `document` is what the viewer's parts read. */
export function OverviewTab({
  document,
  review,
  list,
  ctx,
  idPrefix,
  go,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  list: ListedSection[];
  ctx: TileContext;
  idPrefix: string;
  go: (place: ViewPlace) => void;
}) {
  const diagram = viewTile(review, review.overview.diagram);
  const attachments = new Set((review.index.attachments ?? []).map((image) => image.id));
  const screenshots = review.overview.screenshots.filter((id) => attachments.has(id));
  const intent = intentRows(review);
  const titleOf = (id: string) => list.find((section) => section.id === id)?.title;
  return (
    <div className="mx-auto w-full max-w-5xl space-y-8">
      {review.summary.text ? (
        <Markdown className={cn(PROSE, "text-lg leading-[1.6]")}>{review.summary.text}</Markdown>
      ) : null}
      <RiskPart review={review} titleOf={titleOf} go={go} />
      {screenshots.length > 0 ? (
        <ReviewImages
          document={document}
          heading="What it looks like"
          block={{
            kind: "images",
            items: screenshots.map((attachment) => ({ attachment })),
            source: [],
          }}
        />
      ) : null}
      {diagram ? (
        <ViewTileCard tile={diagram} review={document} ctx={ctx} idPrefix={idPrefix} />
      ) : null}
      {intent.length > 0 ? <AskedPart document={document} rows={intent} /> : null}
    </div>
  );
}

/** The score, what it means and why it is what it is, in two sentences,
 *  and below them, when the review asks for something before merging (a
 *  verdict with points), what that is: the verdict belongs to the risk it
 *  answers. */
function RiskPart({
  review,
  titleOf,
  go,
}: {
  review: ReviewV2;
  titleOf: (id: string) => string | undefined;
  go: (place: ViewPlace) => void;
}) {
  const score = riskScoreView({
    summary: { risk_score: review.summary.risk_score },
    sections: review.sections,
  } as unknown as Pick<ReviewDocument, "summary" | "sections">);
  const why = riskSummary(review);
  const verdict = review.summary.verdict?.points.length ? review.summary.verdict : null;
  return (
    <section
      aria-label="Risk"
      data-review-block=""
      className="overflow-hidden rounded-lg border border-default bg-surface-card outline-none data-[review-block-active]:border-strong"
    >
      <div className="grid gap-x-8 gap-y-3 p-4 @2xl:grid-cols-[minmax(12rem,auto)_minmax(0,1fr)]">
        <div className="space-y-1.5">
          <h3 className="text-xs font-medium tracking-[0.08em] text-tertiary uppercase">Risk</h3>
          {score.assessed ? (
            <>
              <p className="flex items-center gap-3">
                <span
                  className={cn(
                    "text-3xl font-medium tabular-nums",
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
              </p>
              {score.lowerBound ? null : (
                <p className="text-base text-secondary">{score.meaning}</p>
              )}
            </>
          ) : (
            <p className="text-base font-medium text-primary">Not assessed</p>
          )}
        </div>
        {why ? (
          <Markdown className="max-w-[72ch] self-center text-base leading-[1.6] text-primary">
            {why}
          </Markdown>
        ) : !score.assessed ? (
          <p className="self-center text-base text-secondary">
            {score.legacy
              ? "This review was generated before risk scores. Regenerate it to assess."
              : "No current section of this review was rated."}
          </p>
        ) : null}
      </div>
      {verdict ? (
        <div aria-label="Before merging" className="border-t border-default px-4 py-3">
          <p className="flex flex-wrap items-baseline gap-x-2 text-base text-primary">
            <span className="text-xs font-medium tracking-[0.08em] text-status-caution uppercase">
              Before merging
            </span>
            {verdict.text.trim() ? (
              <InlineMarkdown className="font-semibold">{verdict.text}</InlineMarkdown>
            ) : null}
          </p>
          {verdict.points.length > 0 ? (
            <ul className={cn("space-y-1.5", verdict.text.trim() && "mt-2")}>
              {verdict.points.map((point, at) => {
                // A section several points name is linked once, after the
                // last of them.
                const later = verdict.points
                  .slice(at + 1)
                  .some((next) => next.section === point.section);
                const title = point.section && !later ? titleOf(point.section) : undefined;
                return (
                  <li key={at} className="flex gap-2.5 text-base leading-[1.55] text-primary">
                    <span
                      aria-hidden
                      className="mt-[0.6rem] h-1 w-1 flex-none rounded-full bg-(--text-tertiary)"
                    />
                    <span className="min-w-0">
                      <InlineMarkdown>{point.text}</InlineMarkdown>
                      {title && point.section ? (
                        <>
                          {" "}
                          <JumpLink
                            onClick={() => go({ tab: "sections", section: point.section! })}
                          >
                            <InlineMarkdown>{title}</InlineMarkdown> →
                          </JumpLink>
                        </>
                      ) : null}
                    </span>
                  </li>
                );
              })}
            </ul>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

/** What the person asked in the linked sessions and what the pull request
 *  does about each ask, each linking to the prompt in its session. */
function AskedPart({ document, rows }: { document: ReviewDocument; rows: IntentRow[] }) {
  const { search } = useUrlState();
  const answered = rows.some((row) => row.got);
  const vendorOf = (session: string) =>
    document.index.sessions.find((item) => item.id === session)?.vendor;
  return (
    <Part label="What was asked">
      <ul className="divide-y divide-subtle">
        {rows.map((row, at) => {
          const next = withSelection(search, { kind: "session", id: row.session });
          next.set("turn", String(row.prompt));
          const vendor = vendorOf(row.session);
          return (
            <li
              key={`${row.session}:${row.prompt}:${at}`}
              data-review-block=""
              className={cn(
                "grid gap-x-6 gap-y-1 py-2.5 outline-none first:pt-0",
                answered && "@2xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]",
              )}
            >
              <div className="min-w-0 space-y-0.5">
                <p className="text-base text-primary [overflow-wrap:anywhere]">
                  <InlineMarkdown>{row.asked}</InlineMarkdown>
                </p>
                <ViewerLink href={`?${next}`} className="text-xs text-link hover:underline">
                  {[
                    vendor ? harnessName(vendor) : "Session",
                    `prompt ${row.prompt}`,
                    row.at ? formatDateTime(row.at) : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </ViewerLink>
              </div>
              {row.got ? (
                <p className="min-w-0 text-base text-secondary [overflow-wrap:anywhere]">
                  <InlineMarkdown>{row.got}</InlineMarkdown>
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
    </Part>
  );
}
