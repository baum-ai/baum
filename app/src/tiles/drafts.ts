import type { TileSize } from "./tile-document";

/** A tile being made in the session panel, as the tile route lists it
 *  (tiledrafts.Draft): where it will stand, its title and how far it got. */
export type DraftCard = {
  tile: string;
  title: string;
  size?: TileSize;
  to?: { edge?: "first" | "last"; before?: string; after?: string };
  phase: "writing" | "checking" | "failed" | "unfinished";
  round?: number;
  reason?: string;
};

const PHASES = new Set(["writing", "checking", "failed", "unfinished"]);
const SIZES = new Set(["full", "half", "third"]);

/** The tile route's pending cards; a card that is not well formed is left
 *  out. */
export function parseDrafts(value: unknown): DraftCard[] {
  if (!Array.isArray(value)) return [];
  const out: DraftCard[] = [];
  for (const item of value) {
    if (typeof item !== "object" || item === null) continue;
    const d = item as Record<string, unknown>;
    if (typeof d.tile !== "string" || typeof d.title !== "string" || !PHASES.has(d.phase as string))
      continue;
    const card: DraftCard = { tile: d.tile, title: d.title, phase: d.phase as DraftCard["phase"] };
    if (SIZES.has(d.size as string)) card.size = d.size as TileSize;
    if (typeof d.round === "number" && d.round > 0) card.round = d.round;
    if (typeof d.reason === "string" && d.reason) card.reason = d.reason;
    if (typeof d.to === "object" && d.to !== null) {
      const to = d.to as Record<string, unknown>;
      if (to.edge === "first" || to.edge === "last") card.to = { edge: to.edge };
      else if (typeof to.before === "string") card.to = { before: to.before };
      else if (typeof to.after === "string") card.to = { after: to.after };
    }
    out.push(card);
  }
  return out;
}

/** What a pending card says of its tile: being made, and the step it is
 *  at, or that it was left unfinished. */
export function draftLine(draft: DraftCard): string {
  switch (draft.phase) {
    case "writing":
      return "Being made in the session panel · Writing the component";
    case "checking":
      return `Being made in the session panel · Checking${draft.round && draft.round > 1 ? ` (round ${draft.round})` : ""}`;
    case "failed":
      return `Being made in the session panel · ${draft.reason ?? "The check could not run"}`;
    case "unfinished":
      return "Not finished";
  }
}

/** Places the pending cards among the page's tiles: each card's tiles
 *  before and after which it stands, and those that lead or end the grid
 *  (an edge, or a tile the page does not show). */
export function placeDrafts(
  drafts: DraftCard[],
  shown: ReadonlySet<string>,
): {
  before: Map<string, DraftCard[]>;
  after: Map<string, DraftCard[]>;
  first: DraftCard[];
  last: DraftCard[];
} {
  const before = new Map<string, DraftCard[]>();
  const after = new Map<string, DraftCard[]>();
  const first: DraftCard[] = [];
  const last: DraftCard[] = [];
  const push = (map: Map<string, DraftCard[]>, key: string, draft: DraftCard) =>
    map.set(key, [...(map.get(key) ?? []), draft]);
  for (const draft of drafts) {
    const to = draft.to;
    if (to?.edge === "first") first.push(draft);
    else if (to?.before && shown.has(to.before)) push(before, to.before, draft);
    else if (to?.after && shown.has(to.after)) push(after, to.after, draft);
    else last.push(draft);
  }
  return { before, after, first, last };
}
