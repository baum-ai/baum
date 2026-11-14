import type { Expansions } from "./place";
import {
  placedTiles,
  type Tile,
  type TileDocument,
  type TileGroup,
  type TileLayoutEntry,
  type TileSize,
} from "./tile-document";

/** One cell or row of the landing grid, in reading order. */
export type LandingItem =
  | { kind: "tile"; tile: Tile; entry: TileLayoutEntry }
  /** A group's heading, over its members. */
  | { kind: "heading"; group: TileGroup; count: number }
  /** The members a collapsed group holds back, as one cell that shows them. */
  | { kind: "more"; group: TileGroup; hidden: Tile[] }
  /** A group opened past its first members, which can fold them again. */
  | { kind: "less"; group: TileGroup; extra: number };

export type Landing = {
  items: LandingItem[];
  /** The tiles past the landing area that are not hidden in a group, held
   *  back until the reader asks. */
  rest: Tile[];
};

/** The landing page's grid, by the rule of the Go layout (`tiles.Hidden`,
 *  `tiles.More`): a group member past its group's `collapse_after` is hidden
 *  wherever it stands, and only its group's own cell reveals it, counting all
 *  of them; every other entry before `landing` shows, and those from
 *  `landing` on wait behind "Show N more tiles" (`more`). */
export function landingOf(document: TileDocument, expansions: Expansions): Landing {
  const placed = placedTiles(document);
  const groups = new Map(document.layout.groups.map((group) => [group.id, group]));
  const landing = document.layout.landing;
  const items: LandingItem[] = [];
  const rest: Tile[] = [];
  let at = 0;
  while (at < placed.length) {
    const groupId = placed[at]!.entry.group;
    const group = groupId ? groups.get(groupId) : undefined;
    if (!group) {
      const { tile, entry } = placed[at]!;
      if (at < landing || expansions.more) items.push({ kind: "tile", tile, entry });
      else rest.push(tile);
      at++;
      continue;
    }
    let end = at;
    while (end < placed.length && placed[end]!.entry.group === group.id) end++;
    const open = expansions.groups.has(group.id);
    const limit = group.collapse_after;
    const members = placed.slice(at, end).map((member, position) => ({
      ...member,
      index: at + position,
      hidden: limit !== undefined && position >= limit && !open,
    }));
    const hidden = members.filter((member) => member.hidden).map((member) => member.tile);
    const shown = members.filter(
      (member) => !member.hidden && (member.index < landing || expansions.more),
    );
    for (const member of members)
      if (!member.hidden && member.index >= landing && !expansions.more) rest.push(member.tile);
    // The group is on the page once it starts in the landing area, or the
    // reader asked for the rest.
    if (shown.length > 0 || at < landing || expansions.more) {
      if (shown.length > 0 || hidden.length > 0)
        items.push({ kind: "heading", group, count: members.length });
      for (const { tile, entry } of shown) items.push({ kind: "tile", tile, entry });
      if (hidden.length > 0) items.push({ kind: "more", group, hidden });
      if (open && limit !== undefined && members.length > limit)
        items.push({ kind: "less", group, extra: members.length - limit });
    }
    at = end;
  }
  return { items, rest };
}

/** How a card grows to the end of its row: `wide`, its span in units on the
 *  six-unit grid; `two`, that it spans both columns of the two-column one. */
export type RowFill = { wide?: number; two?: boolean };

export const UNITS: Record<TileSize, number> = { full: 6, half: 3, third: 2 };

/** The cards that grow so a row the page breaks early has no hole. A group's
 *  heading, a group's show more or fewer cell and the landing area's own
 *  "Show more" row each span the whole grid, so a row ends at them full or
 *  not. The Go layout fills the rows it knows (`tiles.RowFills`), which has
 *  no headings, so a short row before one would leave a hole: its last card
 *  grows to the row's end instead, on the wide grid and on the two-column
 *  one. A row that ends because the next card does not fit is the layout's
 *  to fill, and a last row with nothing after it stays as it is. */
export function breakFills(landing: Landing, more: boolean): Map<string, RowFill> {
  const fills = new Map<string, RowFill>();
  let wide = 0;
  let two = 0;
  let lastWide: { id: string; units: number } | null = null;
  let lastTwo: string | null = null;
  const close = () => {
    if (lastWide && wide < 6)
      fills.set(lastWide.id, { ...fills.get(lastWide.id), wide: lastWide.units + 6 - wide });
    if (lastTwo && two === 1) fills.set(lastTwo, { ...fills.get(lastTwo), two: true });
    wide = 0;
    two = 0;
    lastWide = null;
    lastTwo = null;
  };
  for (const item of landing.items) {
    if (item.kind !== "tile") {
      close();
      continue;
    }
    const units = UNITS[item.entry.size] ?? 6;
    if (wide + units > 6) wide = 0;
    wide += units;
    lastWide = { id: item.tile.id, units };
    const columns = item.entry.size === "full" ? 2 : 1;
    if (two + columns > 2) two = 0;
    two += columns;
    lastTwo = item.tile.id;
  }
  if (more) close();
  return fills;
}

/** Whether a tile is on the landing page as it is shown now. */
export function onLanding(landing: Landing, id: string): boolean {
  return landing.items.some((item) => item.kind === "tile" && item.tile.id === id);
}
