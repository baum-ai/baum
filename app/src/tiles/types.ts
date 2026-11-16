import type { ComponentType } from "react";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import type { Anchor, Evidence, ReviewDocument, Risk, Section } from "@baum/viewer/review-document";

export type TileState = "ready" | "pending" | "failed" | "unavailable";
/** The sizes a tile type declares (`definition.sizes`), which its frame is
 *  told; on the review page a tile is drawn full width. */
export type TileSize = "full" | "half" | "third";

/** A tile as the review page's view serves it (`baum.view/1`): an exhibit
 *  (code, prose, table, diagram) or a library or chat-made tile drawn in its
 *  sandboxed frame (`custom`, with its `definition`). */
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

/** What a tile's citations resolve against: the shown version's index, its
 *  sections (`changes`), their risks and evidence each with the section it
 *  is in, and the tiles on the page. Built from the review and its view by
 *  `tileSourceOf`. */
export type TileSource = {
  version: number;
  index: ReviewDocument["index"];
  changes: Pick<Section, "id" | "title" | "kind">[];
  risks: (Risk & { change: string })[];
  evidence: (Evidence & { change: string })[];
  tiles: { id: string; title: string }[];
};

/** The citation source of a review with the tiles its page shows. */
export function tileSourceOf(
  review: ReviewDocument,
  tiles: readonly { id: string; title: string }[] = [],
): TileSource {
  return {
    version: review.review.version,
    index: review.index,
    changes: review.sections.map(({ id, title, kind }) => ({ id, title, kind })),
    risks: review.sections.flatMap((section) =>
      (section.risks ?? []).map((risk) => ({ ...risk, change: section.id })),
    ),
    evidence: review.sections.flatMap((section) =>
      (section.evidence ?? []).map((item) => ({ ...item, change: section.id })),
    ),
    tiles: tiles.map(({ id, title }) => ({ id, title })),
  };
}

/** What a tile's face is given besides the tile: what its citations resolve
 *  against, the review the viewer's parts read, and the ways out of the
 *  tile. */
export type TileContext = {
  source: TileSource;
  review: ReviewDocument;
  /** Shows a tile where the page places it. */
  open: (tile: string) => void;
  /** Shows a section, at one of its items. */
  openChange: (change: string, item?: string) => void;
  /** Opens one of the workspace's own panes. */
  openPane: (pane: "decisions" | "files") => void;
  /** Reads the patch of cited code the document omitted; null while two
   *  versions are compared. */
  patches: HunkPatchSource | null;
  /** Prefixes the IDs of an earlier version's tiles, so both compared
   *  columns keep their own. */
  idPrefix: string;
  /** What each ID prose may name stands for, as plain words. */
  labels: ReadonlyMap<string, string>;
};

export type TileFaceProps = { tile: Tile; ctx: TileContext };

/** A tile type the page can draw. */
export type TileType = {
  type: string;
  /** The tile, drawn under its title. */
  Detail: ComponentType<TileFaceProps>;
  /** A light check of the tile's data (and, for a custom tile, of its
   *  definition) before the face reads it; a tile that fails it is drawn
   *  unavailable. */
  valid: (data: unknown, tile?: Tile) => boolean;
  /** Why a tile the daemon marked unavailable is, in the type's own words,
   *  where they differ from the daemon's first problem; null to keep it. */
  unavailableWhy?: (tile: Tile) => string | null;
  /** The code a comment on the tile is placed on. */
  commentAnchors?: (tile: Tile) => Anchor[];
};

export const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
