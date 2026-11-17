import { type RefObject, useEffect, useMemo } from "react";
import { ReviewFilesProvider } from "@baum/viewer/components/code-file-link";
import { ReviewSidePaneButton } from "@baum/viewer/components/review-navigator";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { tabPanelProps } from "@baum/viewer/components/tabs";
import { SegmentedControl } from "@baum/viewer/components/ui";
import { reviewRefs, type ReviewDocument } from "@baum/viewer/review-document";
import { tileSourceOf, type TileContext } from "../tiles/types";
import { CodeTab } from "./code-tab";
import {
  defaultSection,
  listedSections,
  placeOf,
  reviewOf,
  sectionOfTile,
  selectionOf,
  shownFile,
  viewFiles,
  type ReviewV2,
  type ViewPlace,
  type ViewTab,
} from "./model";
import { OverviewTab } from "./overview-tab";
import { SectionsTab } from "./sections-tab";

/** What the page keeps between its places, outside React, as the viewer
 *  mounts the pane afresh for each: the section and file read last, which
 *  their tabs open on again. */
export type ViewMemory = { section: string | null; file: string | null };

const TABS: { value: ViewTab; label: string }[] = [
  { value: "overview", label: "Overview" },
  { value: "sections", label: "How it works" },
  { value: "code", label: "Code" },
];

/** The review page for one snapshot of the review (the current version, an
 *  earlier one, or either side of a comparison): its tabs and the tab the
 *  address names. */
export function ViewPane({
  snapshot,
  historical,
  place: context,
  memory,
}: {
  snapshot: ReviewDocument;
  historical: boolean;
  place: ReviewPlaceContext;
  memory: RefObject<ViewMemory>;
}) {
  // The tabs read the baum.review/2 document the review was served with; the
  // viewer's parts they draw (chips, images, diffs) read the review itself.
  const review = reviewOf(snapshot);
  const place = placeOf(context.selection, review);
  const prefix = historical ? `history-v${snapshot.review.version}-` : "";
  const list = useMemo(() => listedSections(review), [review]);
  const files = useMemo(() => viewFiles(review), [review]);
  const file = place.tab === "code" ? shownFile(files, place.file) : undefined;
  const ctx = useViewTileContext(snapshot, review, historical, context);
  const go = (next: ViewPlace) => context.select(selectionOf(next));
  useEffect(() => {
    if (historical) return;
    if (place.tab === "sections") memory.current.section = place.section;
    if (file) memory.current.file = file.path;
  }, [historical, place.tab, place.tab === "sections" ? place.section : "", file, memory]);
  // A code link on the page (a hunk chip, a file in "Where this fits", a
  // diff's file name) opens the file in the Code tab rather than the Files
  // pane.
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
  const tablist = `${prefix}review-view`;
  return (
    <ReviewFilesProvider files={reviewFiles}>
      <div className="flex min-w-0 flex-1 flex-col gap-5">
        <ViewTabs
          document={snapshot}
          context={context}
          memory={memory}
          tab={place.tab}
          tablist={tablist}
          // One way to the side pane, beside the current version's tabs.
          sidePane={!(historical && context.comparing)}
        />
        {TABS.map((tab) => (
          <div
            key={tab.value}
            {...tabPanelProps(tablist, tab.value, tab.value === place.tab)}
            className="@container min-w-0"
          >
            {tab.value !== place.tab ? null : place.tab === "overview" ? (
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
        ))}
      </div>
    </ReviewFilesProvider>
  );
}

/** The page's tabs, which open where each was read last. On a place the
 *  viewer draws (a session, a commit or the decisions, opened from the side
 *  pane) no tab is current, and they are the way back to the review. Where
 *  the panes stack, the button at their end opens the side pane. */
export function ViewTabs({
  document,
  context,
  memory,
  tab,
  tablist,
  sidePane = true,
}: {
  document: ReviewDocument | null;
  context: ReviewPlaceContext;
  memory: RefObject<ViewMemory>;
  /** The tab on show; absent away from the tabs. */
  tab?: ViewTab;
  /** The tab panels' ids, with the tab on show. */
  tablist?: string;
  sidePane?: boolean;
}) {
  const list = useMemo(() => (document ? listedSections(reviewOf(document)) : []), [document]);
  const go = (next: ViewPlace) => context.select(selectionOf(next));
  const openTab = (next: ViewTab) => {
    if (next === "overview") return go({ tab: next });
    if (next === "sections") {
      const last = memory.current.section;
      const section = last && list.some((each) => each.id === last) ? last : defaultSection(list);
      return section ? go({ tab: next, section }) : undefined;
    }
    go({ tab: next, file: memory.current.file });
  };
  return (
    <div className="flex min-w-0 items-end">
      <SegmentedControl<ViewTab | "">
        label="Review"
        variant="tabs"
        tablist={tab ? tablist : undefined}
        value={tab ?? ""}
        items={TABS}
        onChange={(next) => next && openTab(next)}
        className="min-w-0 flex-1"
      />
      {sidePane ? (
        <span className="box-content flex h-9 flex-none items-center border-b border-subtle wide:hidden">
          <ReviewSidePaneButton className="h-9" />
        </span>
      ) : null}
    </div>
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
