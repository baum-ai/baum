import { cn } from "@baum/viewer/components/classnames";
import { formatDateTime } from "@baum/viewer/components/format";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { Part, PROSE } from "@baum/viewer/components/review-parts";
import { ReviewImages } from "@baum/viewer/components/review-images";
import { harnessName } from "@baum/viewer/agent-names";
import { useUrlState, ViewerLink } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { withSelection } from "@baum/viewer/review-selection";
import { draftsOn } from "../tiles/drafts";
import type { TileContext } from "../tiles/types";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { DecisionPart } from "./decision-part";
import {
  intentRows,
  isBrief,
  OVERVIEW_OWNER,
  tilesOn,
  viewFiles,
  viewTile,
  type IntentRow,
  type ListedSection,
  type ReviewV2,
} from "./model";
import { SectionFiles } from "./section-views";
import { SectionOverview, SectionTiles, WhereThisFits } from "./sections-tab";
import { ViewTileCard } from "./view-tile";

/** A part of the Overview j and k move to, washed while they are on it, as a
 *  section's blocks are. */
const STOP =
  "min-w-0 scroll-mt-4 rounded-md outline-none data-[review-block-active]:bg-surface-wash data-[review-block-active]:shadow-[0_0_0_0.5rem_var(--surface-wash)]";

/** The Overview, always in the same order: the summary, what to decide (the
 *  verdict, the risk score and what to check before merging: `DecisionPart`),
 *  what it looks like, the main diagram, the tiles the person added here (and
 *  the pending cards of those being made in the session panel) and what was
 *  asked. It reads the baum.review/2 `review`; `document` is what the
 *  viewer's parts read.
 *
 *  A brief (a small, low-risk change with one section; see `isBrief`) is the
 *  whole review on this page: under the risk come what its section says and
 *  then the change's own diff, and "Where this fits" closes the page, folded. */
export function OverviewTab({
  document,
  review,
  list,
  ctx,
  idPrefix,
  historical,
  context,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  list: ListedSection[];
  ctx: TileContext;
  idPrefix: string;
  historical: boolean;
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
      <DecisionPart review={review} listed={!brief} titleOf={titleOf} select={context.select} />
      {section ? (
        <>
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
          <Part label="The change">
            <SectionFiles document={document} files={viewFiles(review)} context={context} />
          </Part>
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
