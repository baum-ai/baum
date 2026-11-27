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
import { draftsOn } from "../tiles/drafts";
import type { TileContext } from "../tiles/types";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import {
  intentRows,
  isBrief,
  OVERVIEW_OWNER,
  riskSummary,
  tilesOn,
  viewFiles,
  viewTile,
  type IntentRow,
  type ListedSection,
  type ReviewV2,
  type ViewPlace,
} from "./model";
import { SectionFiles } from "./section-views";
import { SectionOverview, SectionTiles, WhereThisFits } from "./sections-tab";
import { ViewTileCard } from "./view-tile";

const SCORE_INK: Record<Tone, string> = {
  neutral: "text-primary",
  info: "text-status-info",
  positive: "text-status-positive",
  caution: "text-status-caution",
  critical: "text-status-critical",
};

/** A part of the Overview j and k move to, washed while they are on it, as a
 *  section's blocks are. */
const STOP =
  "min-w-0 scroll-mt-4 rounded-md outline-none data-[review-block-active]:bg-surface-wash data-[review-block-active]:shadow-[0_0_0_0.5rem_var(--surface-wash)]";

/** The Overview, always in the same order: the summary, the risk (with the
 *  verdict under it when something must happen before merging), what it
 *  looks like, the main diagram, the tiles the person added here (and the
 *  pending cards of those being made in the session panel) and what was
 *  asked. It reads the baum.review/2 `review`; `document` is what the
 *  viewer's parts read.
 *
 *  A brief (a small, low-risk change with one section; see `isBrief`) is the
 *  whole review on this page: under the risk come the change's own diff and
 *  what its section says, and "Where this fits" closes the page, folded. */
export function OverviewTab({
  document,
  review,
  list,
  ctx,
  idPrefix,
  historical,
  go,
  context,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  list: ListedSection[];
  ctx: TileContext;
  idPrefix: string;
  historical: boolean;
  go: (place: ViewPlace) => void;
  context: ReviewPlaceContext;
}) {
  const brief = isBrief(review);
  const section = brief ? review.sections[0]! : null;
  const diagram = viewTile(review, review.overview.diagram);
  const added = tilesOn(review, OVERVIEW_OWNER);
  // Pending cards stand on the latest version only, as on the sections.
  const drafts = historical
    ? []
    : draftsOn(
        review.drafts ?? [],
        OVERVIEW_OWNER,
        list.map((entry) => entry.id),
      );
  const attachments = new Set((review.index.attachments ?? []).map((image) => image.id));
  const screenshots = review.overview.screenshots.filter((id) => attachments.has(id));
  const videos = review.overview.videos.filter((id) => attachments.has(id));
  const intent = intentRows(review);
  const titleOf = (id: string) => list.find((section) => section.id === id)?.title;
  return (
    <div className="mx-auto w-full max-w-5xl space-y-8">
      {review.summary.text ? (
        <div data-review-block="" className={STOP}>
          <Markdown className={cn(PROSE, "text-lg leading-[1.6]")}>{review.summary.text}</Markdown>
        </div>
      ) : null}
      <RiskPart review={review} titleOf={titleOf} go={go} brief={brief} />
      {section ? (
        <>
          <Part label="The change">
            <SectionFiles document={document} files={viewFiles(review)} context={context} />
          </Part>
          <div data-brief-section="">
            <SectionOverview
              bare
              document={document}
              review={review}
              section={section}
              ctx={ctx}
              idPrefix={idPrefix}
              // Every pending card that names a section stands here: the
              // brief has the one.
              drafts={
                historical
                  ? []
                  : (review.drafts ?? []).filter(
                      (draft) => draft.section && draft.section !== OVERVIEW_OWNER,
                    )
              }
              onRetryDetails={historical ? undefined : context.onRetryDetails}
            />
          </div>
        </>
      ) : null}
      {videos.length > 0 ? (
        <div data-review-block="" className={STOP}>
          <ReviewImages
            document={document}
            heading={videos.length === 1 ? "Video" : "Videos"}
            block={{
              kind: "images",
              items: videos.map((attachment) => ({ attachment })),
              source: [],
            }}
          />
        </div>
      ) : null}
      {screenshots.length > 0 ? (
        <div data-review-block="" className={STOP}>
          <ReviewImages
            document={document}
            heading="What it looks like"
            block={{
              kind: "images",
              items: screenshots.map((attachment) => ({ attachment })),
              source: [],
            }}
          />
        </div>
      ) : null}
      {diagram ? (
        <ViewTileCard tile={diagram} review={document} ctx={ctx} idPrefix={idPrefix} />
      ) : null}
      <SectionTiles
        tiles={added}
        drafts={drafts}
        document={document}
        ctx={ctx}
        idPrefix={idPrefix}
      />
      {intent.length > 0 ? <AskedPart document={document} rows={intent} /> : null}
      {brief && (review.context || context.contextPending) ? (
        <details data-brief-context="" className="border-t border-subtle pt-4">
          <summary className="cursor-pointer text-base text-link hover:underline">
            Where this fits
          </summary>
          <div className="mt-5">
            <WhereThisFits
              plain
              document={document}
              review={review}
              ctx={ctx}
              idPrefix={idPrefix}
              drafts={[]}
              pending={context.contextPending && !historical}
            />
          </div>
        </details>
      ) : null}
    </div>
  );
}

/** The score, what it means and why it is what it is, in two sentences,
 *  and below them, when the review asks for something before merging (a
 *  verdict with points), what that is: the verdict belongs to the risk it
 *  answers. A brief leads the two sentences with the verdict's own line even
 *  when it has no points, since the page is all there is to decide on. */
function RiskPart({
  review,
  titleOf,
  go,
  brief,
}: {
  review: ReviewV2;
  titleOf: (id: string) => string | undefined;
  go: (place: ViewPlace) => void;
  brief: boolean;
}) {
  const score = riskScoreView({
    summary: { risk_score: review.summary.risk_score },
    sections: review.sections,
  } as unknown as Pick<ReviewDocument, "summary" | "sections">);
  const why = riskSummary(review);
  const verdict = review.summary.verdict?.points.length ? review.summary.verdict : null;
  const lead = brief && !verdict ? (review.summary.verdict?.text.trim() ?? "") : "";
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
        {why || lead ? (
          <div className="max-w-[72ch] space-y-1 self-center">
            {lead ? (
              <p
                data-brief-verdict=""
                className="text-base leading-[1.6] font-semibold text-primary"
              >
                <InlineMarkdown>{lead}</InlineMarkdown>
              </p>
            ) : null}
            {why ? (
              <Markdown
                className={cn("text-base leading-[1.6]", lead ? "text-secondary" : "text-primary")}
              >
                {why}
              </Markdown>
            ) : null}
          </div>
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
