import type {
  Anchor,
  Attention,
  Block,
  Decision,
  Evidence,
  Implements,
  Recommendation,
  ReviewContext,
  ReviewDocument,
  Risk,
  RiskScore,
  Section,
} from "@baum/viewer/review-document";

/** A `baum.tiles/1` document as the daemon serves it beside the review it was
 *  converted from (`oss/tiles`, whose goldens keep these types honest). The
 *  substrate keeps `baum.review/1`'s shapes; tiles present it and the layout
 *  orders them. */
export type TileDocument = {
  schema: "baum.tiles/1";
  origin: { kind: "converted" | "generated"; from?: string; schema_revision?: string | null };
  review: ReviewDocument["review"];
  facts: {
    title: string;
    author: ReviewDocument["summary"]["author"];
    authorship: unknown;
    review_exchange?: unknown;
    review_minutes?: number | null;
  };
  stack?: ReviewDocument["stack"];
  stats: ReviewDocument["stats"];
  index: ReviewDocument["index"];
  agent_reviews: ReviewDocument["agent_reviews"];
  unassigned_hunks: string[];
  /** Absent on an older review, null when the review gives none. */
  recommendation?: Recommendation | null;
  risk_score?: RiskScore;
  changes: TileChange[];
  risks: TileRisk[];
  evidence: TileEvidence[];
  tiles: Tile[];
  layout: TileLayout;
};

export type TileChange = Pick<
  Section,
  "id" | "title" | "kind" | "hunks" | "assessment" | "pull_requests" | "details" | "derived"
>;

/** Set on a risk, evidence item or decision renamed because its ID was not
 *  unique across the document: its change and its ID there. */
export type RenameOrigin = { change: string; id: string };

/** Where a block, or an item of one, an aggregate tile holds came from. */
export type BlockOrigin = { change: string; block: number; item?: number; id?: string };

export type TileRisk = Risk & {
  change: string;
  finding?: string | null;
  origin?: RenameOrigin;
};

export type TileEvidence = Evidence & { change: string; origin?: RenameOrigin };

export type TileState = "ready" | "pending" | "failed" | "unavailable";
export type TileSize = "full" | "half" | "third";

export type Tile = {
  id: string;
  type: string;
  title: string;
  headline?: string;
  data: unknown;
  covers?: string[];
  state: TileState;
  problems?: string[];
  definition?: unknown;
};

export type TileLayoutEntry = { tile: string; size: TileSize; group?: string; pin?: boolean };
export type TileGroup = { id: string; title: string; collapse_after?: number };
export type TileLayout = { entries: TileLayoutEntry[]; groups: TileGroup[]; landing: number };

export type OverviewData = {
  text: string;
  change_kind: string;
  release_note?: string | null;
  breaking_change?: string | null;
  attention?: Attention;
};
export type ChangeData = {
  change: string;
  summary?: { text: string; anchors: Anchor[] } | null;
  attention: Attention;
  blocks: Block[];
};
export type RisksData = { ranked: string[] };
export type IntentData = {
  implements: Implements[];
  lines: { change?: string; text: string; anchors: Anchor[]; origin?: BlockOrigin }[];
};
export type TileDecision = Decision & { change?: string; origin?: BlockOrigin };
export type DecisionsData = { items: TileDecision[] };
export type ContextData = ReviewContext;
export type MediaItem = { change?: string; block: Block; origin?: BlockOrigin };
export type MediaData = { items: MediaItem[] };
export type EverythingElseData = {
  change: string | null;
  unassigned: string[];
  summary?: { text: string; anchors: Anchor[] } | null;
  attention?: Attention;
  blocks?: Block[];
};
export type ProseData = { text: string; anchors: Anchor[] };
export type TableData = { columns: string[]; rows: string[][]; source: Anchor[] };
export type DiagramData = { format: string; content: string; source: Anchor[] };
export type CodeData = { language: string; content: string; caption?: string; source: Anchor[] };

/** Where a review document carries the tile document served with it. An
 *  enumerable symbol property, so the copies the page makes of a document
 *  (`{...doc}`) keep it, and JSON never sees it. `null` says the daemon could
 *  not convert the document; absent says nobody asked. */
export const TILES: unique symbol = Symbol.for("baum.tiles");

export type TiledDocument = ReviewDocument & { [TILES]?: TileDocument | null };

export function withTiles(document: ReviewDocument, tiles: TileDocument | null): TiledDocument {
  const tiled: TiledDocument = document;
  tiled[TILES] = tiles;
  return tiled;
}

export function tileDocOf(document: ReviewDocument | null | undefined): TileDocument | null {
  return (document as TiledDocument | null | undefined)?.[TILES] ?? null;
}

/** Whether the page asked for tiles with this document and the daemon could
 *  not give them. */
export function tilesMissing(document: ReviewDocument | null | undefined): boolean {
  return Boolean(document) && (document as TiledDocument)[TILES] === null;
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** A light structural check of a served tile document: the parts every
 *  component reads without looking twice. Deeper shapes are the Go
 *  validator's; a tile whose data is wrong renders unavailable on its own. */
export function parseTileDocument(value: unknown): TileDocument | null {
  if (!isObject(value) || value.schema !== "baum.tiles/1") return null;
  const layout = value.layout;
  if (
    !isObject(value.review) ||
    !isObject(value.index) ||
    !Array.isArray(value.changes) ||
    !Array.isArray(value.risks) ||
    !Array.isArray(value.evidence) ||
    !Array.isArray(value.tiles) ||
    !isObject(layout) ||
    !Array.isArray(layout.entries) ||
    !Array.isArray(layout.groups) ||
    typeof layout.landing !== "number"
  )
    return null;
  const tiles = value.tiles as unknown[];
  if (
    !tiles.every(
      (tile) =>
        isObject(tile) &&
        typeof tile.id === "string" &&
        typeof tile.type === "string" &&
        typeof tile.title === "string" &&
        typeof tile.state === "string",
    )
  )
    return null;
  const ids = new Set(tiles.map((tile) => (tile as Tile).id));
  if (
    !(layout.entries as unknown[]).every(
      (entry) => isObject(entry) && typeof entry.tile === "string" && ids.has(entry.tile),
    )
  )
    return null;
  return value as TileDocument;
}

/** The tile with this ID. */
export function tileById(document: TileDocument, id: string): Tile | undefined {
  return document.tiles.find((tile) => tile.id === id);
}

/** The tiles in layout order, each with its entry. */
export function placedTiles(document: TileDocument): { tile: Tile; entry: TileLayoutEntry }[] {
  return document.layout.entries.flatMap((entry) => {
    const tile = tileById(document, entry.tile);
    return tile ? [{ tile, entry }] : [];
  });
}

/** A renamed item's ID in its change, where the `baum.review/1` view and the
 *  substrate's own references (risk score reasons, a change's review reason)
 *  still use it. */
export function reviewIdOf(item: { id: string; origin?: { id?: string } }): string {
  return item.origin?.id || item.id;
}

/** The substrate risk a reference made with a change's own ID names: the
 *  risk score's reasons and a change's review reason keep the ID a risk had
 *  in its change, which a renamed duplicate no longer has in `risks`. */
export function riskInChange(
  document: TileDocument,
  change: string,
  id: string,
): TileRisk | undefined {
  return document.risks.find((risk) => risk.change === change && reviewIdOf(risk) === id);
}

/** The same for evidence. */
export function evidenceInChange(
  document: TileDocument,
  change: string,
  id: string,
): TileEvidence | undefined {
  return document.evidence.find((item) => item.change === change && reviewIdOf(item) === id);
}

export function changeById(document: TileDocument, id: string): TileChange | undefined {
  return document.changes.find((change) => change.id === id);
}

/** Whether a change still has code in this version. */
export function isCurrent(change: TileChange | undefined): boolean {
  return Boolean(change) && change!.derived.state !== "stale";
}

/** The active critical and high risks of current changes, most severe first:
 *  the ones the landing page always shows. */
export function mustShowRisks(document: TileDocument): TileRisk[] {
  const current = new Set(document.changes.filter(isCurrent).map((change) => change.id));
  const urgent = document.risks.filter(
    (risk) =>
      current.has(risk.change) &&
      risk.state !== "mitigated" &&
      (risk.tier === "critical" || risk.tier === "high"),
  );
  return urgent.toSorted(
    (left, right) => Number(left.tier !== "critical") - Number(right.tier !== "critical"),
  );
}

/** The changes whose details are still being written or failed, of the
 *  current version. */
export function changesInState(document: TileDocument, details: "pending" | "failed") {
  return document.changes.filter(
    (change) =>
      isCurrent(change) &&
      (details === "pending" ? change.details === "pending" : change.details === "failed"),
  );
}

/** The document with the Change tiles of `ids` marked pending, as a retry of
 *  their details runs. */
export function withPendingChanges(document: TileDocument, ids: readonly string[]): TileDocument {
  if (ids.length === 0) return document;
  const pending = new Set(ids);
  return {
    ...document,
    changes: document.changes.map((change) =>
      pending.has(change.id) ? { ...change, details: "pending" } : change,
    ),
    tiles: document.tiles.map((tile) =>
      pending.has(tile.id) && tile.type === "change" ? { ...tile, state: "pending" } : tile,
    ),
  };
}

/** The tile that presents a change: its own Change tile, or Everything else
 *  for the mechanical change. */
export function tileOfChange(document: TileDocument, change: string): Tile | undefined {
  const own = tileById(document, change);
  if (own) return own;
  return document.tiles.find(
    (tile) =>
      tile.type === "everything_else" &&
      isObject(tile.data) &&
      (tile.data as EverythingElseData).change === change,
  );
}

/** Whether the Risks tile shows every must-read risk in full on the landing
 *  page as the layout places it: ready, before `landing` and in no group. */
export function risksShownInFull(document: TileDocument): boolean {
  const at = document.layout.entries.findIndex((entry) => entry.tile === "risks");
  const entry = document.layout.entries[at];
  return (
    at >= 0 &&
    at < document.layout.landing &&
    !entry?.group &&
    tileById(document, "risks")?.state === "ready"
  );
}

/** The missing and failed evidence of current changes, failed first. */
export function currentGaps(document: TileDocument): TileEvidence[] {
  const gaps = document.evidence.filter(
    (item) =>
      (item.status === "missing" || item.status === "failed") &&
      isCurrent(changeById(document, item.change)),
  );
  return gaps.toSorted((a, b) => Number(a.status !== "failed") - Number(b.status !== "failed"));
}
