import type { Expansions } from "./place";
import {
  placedTiles,
  type Tile,
  type TileDocument,
  type TileGroup,
  type TileLayoutEntry,
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

/** Whether a tile is on the landing page as it is shown now. */
export function onLanding(landing: Landing, id: string): boolean {
  return landing.items.some((item) => item.kind === "tile" && item.tile.id === id);
}
