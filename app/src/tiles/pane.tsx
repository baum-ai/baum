import { useMemo, type RefObject } from "react";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { ReviewSummary } from "@baum/viewer/components/review-summary";
import { reviewRefs, type ReviewDocument } from "@baum/viewer/review-document";
import { Notice } from "../shell";
import { TileDetail } from "./detail";
import { useTileHost, type LandingMemory } from "./host";
import { rememberLanding, rememberRead, TileLanding } from "./landing";
import { leaveTile } from "./keys";
import { useUrlState } from "@baum/viewer/navigation";
import { placeOf, tileSelection } from "./place";
import { tileDocOf, tileOfChange, type TileDocument } from "./tile-document";
import type { TileContext } from "./types";

/** The tiles page's pane for one snapshot of the review: the landing page or
 *  a tile's detail, for the current version, an earlier one, or either side
 *  of a comparison. */
export function TilePane({
  snapshot,
  historical,
  place: context,
  memory,
}: {
  snapshot: ReviewDocument;
  historical: boolean;
  place: ReviewPlaceContext;
  memory: RefObject<LandingMemory>;
}) {
  const doc = tileDocOf(snapshot);
  const url = useUrlState();
  const ctx = useTileContext(snapshot, doc, historical, context, memory);
  if (!doc || !ctx)
    return (
      <>
        <Notice>
          Review v{snapshot.review.version} can't be shown as tiles. This is its full review.
        </Notice>
        <ReviewSummary
          document={snapshot}
          onSelectSection={(id) => context.select({ kind: "section", id })}
        />
      </>
    );
  const place = placeOf(context.selection, doc);
  if (place.kind === "landing")
    return (
      <TileLanding
        ctx={ctx}
        generation={historical ? null : context.generation}
        focus={place.focus}
        memory={memory}
      />
    );
  return (
    <TileDetail
      key={place.tile.id}
      tile={place.tile}
      item={place.item}
      ctx={ctx}
      back={() => leaveTile(url)}
      onShown={historical ? undefined : () => rememberRead(memory, place.tile.id)}
    />
  );
}

function useTileContext(
  snapshot: ReviewDocument,
  doc: TileDocument | null,
  historical: boolean,
  place: ReviewPlaceContext,
  memory: RefObject<LandingMemory>,
): TileContext | null {
  const host = useTileHost();
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
  const compared = place.comparing && !historical ? tileDocOf(place.historicalDocument) : null;
  const compare = useMemo(
    () => (doc && compared ? differences(doc, compared) : null),
    [doc, compared],
  );
  return useMemo(() => {
    if (!doc) return null;
    const onLandingNow = placeOf(place.selection, doc).kind === "landing";
    const open = (tile: string, item?: string) => {
      if (onLandingNow && !historical)
        rememberLanding(memory, document.getElementById("review-section"), tile);
      place.select(tileSelection(tile, item));
    };
    return {
      doc,
      review: snapshot,
      open,
      openChange: (change, item) => {
        const tile = tileOfChange(doc, change);
        if (tile) open(tile.id, item);
      },
      openPane: (pane) => place.select({ kind: pane }),
      historical,
      comparing: place.comparing,
      draft: host.draft && !historical,
      contextPending: place.contextPending && !historical,
      onRetryDetails: historical ? undefined : place.onRetryDetails,
      loadVersion: place.loadVersion,
      patches: place.hunkPatches,
      activeVersions: (change) => place.sectionHistory?.active.get(change),
      idPrefix: historical ? `history-v${snapshot.review.version}-` : "",
      renderLabels: host.renderSectionLabels
        ? (section) => host.renderSectionLabels!(section, snapshot)
        : undefined,
      labels,
      compare,
    } satisfies TileContext;
  }, [doc, snapshot, historical, place, host, labels, compare, memory]);
}

/** What the current version adds or changes against the one compared: tiles
 *  it has that the other lacks, and changes whose code or title moved. */
function differences(current: TileDocument, earlier: TileDocument) {
  const before = new Set(earlier.tiles.map((tile) => tile.id));
  const added = new Set(
    current.tiles.filter((tile) => !before.has(tile.id)).map((tile) => tile.id),
  );
  const changed = new Set<string>();
  for (const change of current.changes) {
    const old = earlier.changes.find((item) => item.id === change.id);
    if (!old) continue;
    const hunks = (ids: string[]) => [...ids].toSorted().join(",");
    if (old.title !== change.title || hunks(old.hunks) !== hunks(change.hunks))
      changed.add(change.id);
  }
  return { added, changed };
}
