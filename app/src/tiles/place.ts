import type { Selection } from "@baum/viewer/review-selection";
import {
  changeById,
  evidenceInChange,
  isCurrent,
  placedTiles,
  reviewIdOf,
  riskInChange,
  tileById,
  tileOfChange,
  type DecisionsData,
  type Tile,
  type TileDocument,
} from "./tile-document";

/** Where the reader is on the tiles page: the landing page (scrolled to a
 *  tile or a group when a link asked for one), or one tile's detail, at an
 *  item of it. */
export type TilePlace =
  | { kind: "landing"; focus?: string }
  | { kind: "tile"; tile: Tile; item?: string };

/** The place a selection names on the tiles page. The viewer's own
 *  selections still arrive (a link to a section or a risk, the Changes
 *  overview), and each lands on the tile that now holds it. */
export function placeOf(selection: Selection, document: TileDocument | null): TilePlace {
  if (!document) return { kind: "landing" };
  switch (selection.kind) {
    case "tile": {
      const tile = tileById(document, selection.id);
      return tile ? { kind: "tile", tile, item: selection.item } : { kind: "landing" };
    }
    case "section": {
      const tile = tileOfChange(document, selection.id);
      if (!tile) return { kind: "landing" };
      return { kind: "tile", tile, item: selection.decision };
    }
    case "risk": {
      const target = itemTarget(document, selection.section, selection.id);
      return target ? { kind: "tile", tile: target.tile, item: target.item } : { kind: "landing" };
    }
    case "changes":
      return { kind: "landing", focus: "group:changes" };
    case "risks":
      return { kind: "landing", focus: "risks" };
    default:
      return { kind: "landing" };
  }
}

/** The selection that opens a tile, at an item. */
export function tileSelection(id: string, item?: string): Selection {
  return { kind: "tile", id, ...(item ? { item } : {}) };
}

/** Where an item a change names (a risk, evidence or a decision, by its ID in
 *  that change) is read: an active risk of a current change in Risks,
 *  evidence in Evidence, a decision in Decisions; anything else in the tile
 *  of the change itself. */
export function itemTarget(
  document: TileDocument,
  change: string,
  item: string | undefined,
): { tile: Tile; item?: string } | null {
  const own = tileOfChange(document, change);
  if (!item) return own ? { tile: own } : null;
  const current = isCurrent(changeById(document, change));
  const risk = riskInChange(document, change, item);
  const risks = tileById(document, "risks");
  if (risk && current && risk.state !== "mitigated" && risks) return { tile: risks, item: risk.id };
  const evidence = evidenceInChange(document, change, item);
  const evidenceTile = tileById(document, "evidence");
  if (evidence && current && evidenceTile) return { tile: evidenceTile, item: evidence.id };
  const decisions = tileById(document, "decisions");
  const decision =
    current && decisions && Array.isArray((decisions.data as DecisionsData | null)?.items)
      ? (decisions.data as DecisionsData).items.find(
          (entry) => entry.change === change && reviewIdOf(entry) === item,
        )
      : undefined;
  if (decision && decisions) return { tile: decisions, item: decision.id };
  return own ? { tile: own, item: risk ? reviewIdOf(risk) : item } : null;
}

/** The tiles in reading order, for stepping: the layout's order. */
export function readingOrder(document: TileDocument): Tile[] {
  return placedTiles(document).map(({ tile }) => tile);
}

/** The landing page's expansions, which the URL keeps so Back restores them:
 *  `more=1` shows the tiles past the landing area, `expand` lists the groups
 *  opened past their first members. */
export type Expansions = { more: boolean; groups: ReadonlySet<string> };

export function readExpansions(search: URLSearchParams): Expansions {
  return {
    more: search.get("more") === "1",
    groups: new Set((search.get("expand") ?? "").split(",").filter(Boolean)),
  };
}

export function withExpansions(search: URLSearchParams, expansions: Expansions): URLSearchParams {
  const next = new URLSearchParams(search);
  if (expansions.more) next.set("more", "1");
  else next.delete("more");
  const groups = [...expansions.groups].toSorted();
  if (groups.length > 0) next.set("expand", groups.join(","));
  else next.delete("expand");
  return next;
}
