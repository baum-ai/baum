import { cn } from "@baum/viewer/components/classnames";
import { formatDateTime } from "@baum/viewer/components/format";
import { Markdown } from "@baum/viewer/components/markdown";
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
  type ReviewView,
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

/** The Overview, always in the same order: the summary, the verdict when
 *  something must happen before merging, the risk, what it looks like, the
 *  main diagram and what was asked. */
export function OverviewTab({
  document,
  view,
  list,
  ctx,
  idPrefix,
  go,
}: {
  document: ReviewDocument;
  view: ReviewView;
  list: ListedSection[];
  ctx: TileContext;
  idPrefix: string;
  go: (place: ViewPlace) => void;
}) {
  const verdict = view.overview.verdict;
  const diagram = viewTile(view, view.overview.diagram);
  const attachments = new Set((document.index.attachments ?? []).map((image) => image.id));
  const screenshots = view.overview.screenshots.filter((id) => attachments.has(id));
  const intent = intentRows(document, view);
  const titleOf = (id: string) => list.find((section) => section.id === id)?.title;
  return (
    <div className="mx-auto w-full max-w-5xl space-y-8">
      {document.summary.text ? (
        <Markdown className={cn(PROSE, "text-lg leading-[1.6]")}>{document.summary.text}</Markdown>
      ) : null}
      {verdict ? (
        <section
          aria-label="Before merging"
          data-review-block=""
          className="rounded-md border-l-2 border-status-caution bg-status-caution-bg px-4 py-3 outline-none"
        >
          <p className="text-base font-semibold text-primary">{verdict.text}</p>
          {verdict.points.length > 0 ? (
            <ul className="mt-2 space-y-1.5">
              {verdict.points.map((point, at) => {
                const title = point.section ? titleOf(point.section) : undefined;
                return (
                  <li key={at} className="flex gap-2.5 text-base leading-[1.55] text-primary">
                    <span
                      aria-hidden
                      className="mt-[0.6rem] h-1 w-1 flex-none rounded-full bg-(--text-tertiary)"
                    />
                    <span className="min-w-0">
                      {point.text}
                      {title && point.section ? (
                        <>
                          {" "}
                          <JumpLink
                            onClick={() => go({ tab: "sections", section: point.section! })}
                          >
                            {title} →
                          </JumpLink>
                        </>
                      ) : null}
                    </span>
                  </li>
                );
              })}
            </ul>
          ) : null}
        </section>
      ) : null}
      <RiskPart document={document} view={view} />
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

/** The score, what it means and why it is what it is, in two sentences. */
function RiskPart({ document, view }: { document: ReviewDocument; view: ReviewView }) {
  const score = riskScoreView(document);
  const why = riskSummary(document, view);
  return (
    <section
      aria-label="Risk"
      data-review-block=""
      className="grid gap-x-8 gap-y-3 rounded-md outline-none @2xl:grid-cols-[minmax(12rem,auto)_minmax(0,1fr)]"
    >
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
            {score.lowerBound ? null : <p className="text-base text-secondary">{score.meaning}</p>}
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
                <p className="text-base text-primary [overflow-wrap:anywhere]">{row.asked}</p>
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
                  {row.got}
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
    </Part>
  );
}
