import { useMemo, useRef, type ReactNode } from "react";
import { CARD_LABEL } from "@baum/viewer/components/editorial";
import { RiskIcon, SummaryIcon } from "@baum/viewer/components/icons";
import { ReviewScoreBadge } from "@baum/viewer/components/review-parts";
import type {
  ReviewPlaceContext,
  ReviewPlaces,
  ReviewPlaceSteps,
  ReviewRailRow,
} from "@baum/viewer/components/review-viewer";
import { SideNavButton, SideNavNest } from "@baum/viewer/components/side-nav";
import { Badge, LiveDot } from "@baum/viewer/components/ui";
import { cn } from "@baum/viewer/components/classnames";
import type { Selection } from "@baum/viewer/review-selection";
import type { LandingMemory } from "./host";
import { GridIcon } from "./icons";
import { itemTarget, placeOf, readingOrder, tileSelection, type TilePlace } from "./place";
import { TilePane } from "./pane";
import { tileType } from "./registry";
import {
  changeById,
  mustShowRisks,
  placedTiles,
  tileDocOf,
  tileOfChange,
  type Tile,
  type TileDocument,
} from "./tile-document";

/** The landing page's name, in the rail, the navigator bar and the way back. */
export const LANDING_LABEL = "Summary";

const LANDING: Selection = { kind: "summary" };

/** A tile's name in the rail: its title, but the Overview by its kind, as its
 *  title is the pull request's own, and a custom tile marked as one. */
function railLabel(tile: Tile) {
  if (tile.type === "overview") return "Overview";
  return tile.type === "custom" ? `Custom · ${tile.title}` : tile.title;
}

function railKey(place: TilePlace) {
  return place.kind === "landing" ? "landing" : `tile:${place.tile.id}`;
}

/** The tiles page as the review viewer's places: a rail of the tiles, the
 *  landing page and each tile's detail as panes, stepping in the layout's
 *  order, and the rail folded while the landing grid is the way in. */
export function useTilePlaces(): ReviewPlaces {
  // The latest document the viewer named, for the chips of its prose, which
  // are resolved without a context.
  const latest = useRef<TileDocument | null>(null);
  const memory = useRef<LandingMemory>(null);
  return useMemo<ReviewPlaces>(
    () => ({
      renderRail: (context) => <TileRail context={context} />,
      railRows: (context) => {
        const doc = tileDocOf(context.document);
        if (!doc)
          return [{ key: "landing", label: LANDING_LABEL, go: () => context.select(LANDING) }];
        return [
          { key: "landing", label: LANDING_LABEL, go: () => context.select(LANDING) },
          ...readingOrder(doc).map(
            (tile): ReviewRailRow => ({
              key: `tile:${tile.id}`,
              label: railLabel(tile),
              go: () => context.select(tileSelection(tile.id)),
            }),
          ),
        ];
      },
      steps: (selection, context) => {
        const doc = tileDocOf(context.document);
        latest.current = doc;
        return doc ? stepsOf(selection, doc, context) : null;
      },
      renderPane: (snapshot, historical, context) => (
        <TilePane snapshot={snapshot} historical={historical} place={context} memory={memory} />
      ),
      jump: (section, item) => {
        const doc = latest.current;
        if (!doc) return null;
        const target = item ? itemTarget(doc, section, item) : null;
        if (target) return tileSelection(target.tile.id, target.item);
        const tile = tileOfChange(doc, section);
        return tile ? tileSelection(tile.id) : null;
      },
      // The landing grid takes the width, so the rail starts folded there and
      // opens in a tile's detail for stepping. The side pane starts folded on
      // every place of the tiles page, so the reader's own unfold holds as
      // they move between them.
      railFolded: onLanding,
      sidePaneFolded: onTilesPlace,
      // The version bar says which version is read; the page keeps its room.
      quietHistory: true,
    }),
    [],
  );
}

const onTilesPlace = (selection: Selection) =>
  ["summary", "changes", "risks", "section", "risk", "tile"].includes(selection.kind);

const onLanding = (selection: Selection) =>
  selection.kind === "summary" || selection.kind === "changes" || selection.kind === "risks";

function stepsOf(
  selection: Selection,
  doc: TileDocument,
  context: ReviewPlaceContext,
): ReviewPlaceSteps {
  const place = placeOf(selection, doc);
  const order = readingOrder(doc);
  const stops = [
    { key: "landing", label: LANDING_LABEL, go: () => context.select(LANDING) },
    ...order.map((tile) => ({
      key: `tile:${tile.id}`,
      label: railLabel(tile),
      go: () => context.select(tileSelection(tile.id)),
    })),
  ];
  const key = railKey(place);
  const at = stops.findIndex((stop) => stop.key === key);
  const tile = place.kind === "tile" ? place.tile : null;
  const type = tile ? tileType(tile.type) : undefined;
  const change = tile ? (changeById(doc, tile.id)?.id ?? changeOfTile(tile)) : undefined;
  return {
    label: tile ? railLabel(tile) : LANDING_LABEL,
    icon: tile ? (type?.icon ?? GridIcon) : SummaryIcon,
    position: tile && at > 0 ? `${at} of ${order.length}` : undefined,
    previous: at > 0 ? stops[at - 1]! : null,
    next: at >= 0 ? (stops[at + 1] ?? null) : null,
    railKey: key,
    sectionId: change,
  };
}

function changeOfTile(tile: Tile): string | undefined {
  const data = tile.data as { change?: unknown } | null;
  return typeof data?.change === "string" ? data.change : undefined;
}

/** The rail: the landing page, then every tile in the layout's order, a
 *  group's members nested under its name. */
function TileRail({ context }: { context: ReviewPlaceContext }) {
  const doc = tileDocOf(context.document);
  const place = placeOf(context.selection, doc);
  const selected = railKey(place);
  const compared = context.comparing ? tileDocOf(context.historicalDocument) : null;
  const urgent = new Set(doc ? mustShowRisks(doc).map((risk) => risk.change) : []);
  const row = (tile: Tile, depth: 0 | 1) => {
    const type = tileType(tile.type);
    const change = changeById(doc!, tile.id);
    const added = compared && !compared.tiles.some((each) => each.id === tile.id);
    const aside: ReactNode[] = [];
    if (urgent.has(tile.id))
      aside.push(<RiskIcon key="risk" size={14} className="mr-1 text-status-critical" />);
    if (tile.state === "pending") aside.push(<LiveDot key="pending" className="mr-1" />);
    if (added)
      aside.push(
        <Badge key="new" size="compact" tone="info">
          New
        </Badge>,
      );
    else if (change?.derived.review_score !== undefined && change.derived.state !== "stale")
      aside.push(
        <ReviewScoreBadge key="score" score={change.derived.review_score} size="compact" />,
      );
    return (
      <div
        key={tile.id}
        className={cn("min-w-0", change?.derived.state === "stale" && "opacity-60 grayscale")}
      >
        <SideNavButton
          label={railLabel(tile)}
          icon={depth === 0 ? (type?.icon ?? GridIcon) : undefined}
          tint={depth === 0 ? "accent" : undefined}
          depth={depth}
          wrap={depth === 1}
          active={selected === `tile:${tile.id}`}
          controls="review-section"
          aside={aside.length > 0 ? <>{aside}</> : undefined}
          description={[
            urgent.has(tile.id) ? "Holds a high or critical risk" : "",
            tile.state === "pending" ? "Being written" : "",
            added ? "New since the compared version" : "",
          ]
            .filter(Boolean)
            .join(". ")}
          onSelect={() => context.select(tileSelection(tile.id))}
        />
      </div>
    );
  };
  const rows: ReactNode[] = [];
  if (doc) {
    const placed = placedTiles(doc);
    for (let at = 0; at < placed.length; ) {
      const { entry } = placed[at]!;
      const group = entry.group
        ? doc.layout.groups.find((item) => item.id === entry.group)
        : undefined;
      if (!group) {
        rows.push(row(placed[at]!.tile, 0));
        at++;
        continue;
      }
      let end = at;
      while (end < placed.length && placed[end]!.entry.group === group.id) end++;
      rows.push(
        <div key={`group-${group.id}`} className="mt-2">
          <p className={cn(CARD_LABEL, "px-2.5 pb-1")}>{group.title}</p>
          <SideNavNest>{placed.slice(at, end).map(({ tile }) => row(tile, 1))}</SideNavNest>
        </div>,
      );
      at = end;
    }
  }
  return (
    <>
      <SideNavButton
        label={LANDING_LABEL}
        icon={SummaryIcon}
        tint="accent"
        active={selected === "landing"}
        controls="review-section"
        onSelect={() => context.select(LANDING)}
      />
      {rows}
    </>
  );
}
