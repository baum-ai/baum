import { useMemo } from "react";
import { ReviewFilesProvider } from "@baum/viewer/components/code-file-link";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { reviewRefs, type ReviewDocument } from "@baum/viewer/review-document";
import { tileSourceOf, type TileContext } from "../tiles/types";
import { CodeTab } from "./code-tab";
import {
  listedSections,
  placeOf,
  reviewOf,
  sectionOfTile,
  selectionOf,
  shownFile,
  viewFiles,
  type ReviewV2,
  type ViewPlace,
} from "./model";
import { OverviewTab } from "./overview-tab";
import { SectionsTab } from "./sections-tab";

/** The review page for one snapshot of the review (the current version, an
 *  earlier one, or either side of a comparison): the place the address names,
 *  which the viewer's rail leads to. */
export function ViewPane({
  snapshot,
  historical,
  place: context,
}: {
  snapshot: ReviewDocument;
  historical: boolean;
  place: ReviewPlaceContext;
}) {
  // The places read the baum.review/2 document the review was served with; the
  // viewer's parts they draw (chips, images, diffs) read the review itself.
  const review = reviewOf(snapshot);
  const place = placeOf(context.selection, review);
  const prefix = historical ? `history-v${snapshot.review.version}-` : "";
  const list = useMemo(() => listedSections(review), [review]);
  const files = useMemo(() => viewFiles(review), [review]);
  const file = place.tab === "code" ? shownFile(files, place.file) : undefined;
  const ctx = useViewTileContext(snapshot, review, historical, context);
  const go = (next: ViewPlace) => context.select(selectionOf(next));
  // A code link on the page (a hunk chip, a file in "Where this fits", a
  // diff's file name) opens the file in Files changed rather than the viewer's
  // Files pane.
  const reviewFiles = useMemo(
    () => ({
      has: (path: string) => files.some((each) => each.path === path),
      open: (path: string) => {
        if (!files.some((each) => each.path === path)) return false;
        context.select(selectionOf({ tab: "code", file: path }));
        return true;
      },
    }),
    [files, context],
  );
  return (
    <ReviewFilesProvider files={reviewFiles}>
      <div className="@container min-w-0 flex-1">
        {place.tab === "overview" ? (
          <OverviewTab
            document={snapshot}
            review={review}
            list={list}
            ctx={ctx}
            idPrefix={prefix}
            go={go}
          />
        ) : place.tab === "sections" ? (
          <SectionsTab
            document={snapshot}
            review={review}
            list={list}
            section={place.section}
            item={place.item}
            sectionView={place.view}
            ctx={ctx}
            idPrefix={prefix}
            context={context}
            historical={historical}
          />
        ) : (
          <CodeTab
            document={snapshot}
            review={review}
            files={files}
            file={file}
            list={list}
            idPrefix={prefix}
            context={context}
          />
        )}
      </div>
    </ReviewFilesProvider>
  );
}

/** What the exhibits and the library and chat-made tiles read, as the tiles
 *  page gave them: a tile they cite opens where the page places it. */
function useViewTileContext(
  snapshot: ReviewDocument,
  review: ReviewV2,
  historical: boolean,
  place: ReviewPlaceContext,
): TileContext {
  const labels = useMemo(() => {
    const words = new Map<string, string>();
    for (const [key, ref] of reviewRefs(snapshot)) {
      if (ref.kind === "item") words.set(key, ref.label);
      else if (ref.kind === "check") words.set(key, ref.name);
      else if (ref.kind === "session" && ref.title) words.set(key, ref.title);
    }
    for (const hunk of snapshot.index.hunks)
      words.set(hunk.id, hunk.path.slice(hunk.path.lastIndexOf("/") + 1));
    return words;
  }, [snapshot]);
  return useMemo(
    () =>
      ({
        source: tileSourceOf(snapshot, review.tiles),
        review: snapshot,
        open: (tile) =>
          place.select(
            sectionOfTile(review, tile) ? { kind: "tile", id: tile } : { kind: "summary" },
          ),
        openChange: (change, item) =>
          place.select(
            item && review.sections.some((s) => s.risks.some((risk) => risk.id === item))
              ? { kind: "risk", section: change, id: item }
              : { kind: "section", id: change },
          ),
        openPane: (pane) => place.select({ kind: pane }),
        patches: place.hunkPatches,
        idPrefix: historical ? `history-v${snapshot.review.version}-` : "",
        labels,
      }) satisfies TileContext,
    [snapshot, review, historical, place, labels],
  );
}
