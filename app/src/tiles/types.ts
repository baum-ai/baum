import type { ComponentType, ReactElement, ReactNode } from "react";
import type { IconProps } from "@baum/viewer/components/icons";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import type {
  Anchor,
  LoadReviewVersion,
  ReviewDocument,
  Section,
} from "@baum/viewer/review-document";
import type { Tile, TileDocument } from "./tile-document";

/** What a tile's faces are given besides the tile: the snapshot they draw
 *  (its tile document and the `baum.review/1` view the viewer's parts read),
 *  and the ways out of the tile. */
export type TileContext = {
  doc: TileDocument;
  review: ReviewDocument;
  /** Opens a tile's detail, at an item of it. */
  open: (tile: string, item?: string) => void;
  /** Opens the tile that holds a change, at one of its items. */
  openChange: (change: string, item?: string) => void;
  /** Opens one of the workspace's own panes. */
  openPane: (pane: "decisions" | "files") => void;
  /** An earlier version is drawn, alone or compared. */
  historical: boolean;
  comparing: boolean;
  /** The current document is a draft still being written. */
  draft: boolean;
  /** A draft whose context is still being written. */
  contextPending: boolean;
  /** Asks the host to write a change's details again; current version only. */
  onRetryDetails?: (change: string) => void | Promise<unknown>;
  loadVersion?: LoadReviewVersion;
  /** Reads the patch of cited code the document omitted; null while two
   *  versions are compared. */
  patches: HunkPatchSource | null;
  /** The versions in which a change had hunks, once known. */
  activeVersions: (change: string) => readonly number[] | undefined;
  /** Prefixes the item IDs of an earlier version's pane, so both compared
   *  columns keep their own targets. */
  idPrefix: string;
  /** The host's labels for a change, such as the stack members it covers. */
  renderLabels?: (section: Section) => ReactNode;
  /** What each ID prose may name stands for, as plain words: a glance shows
   *  "the retry loop" where the text says `rsk_retry_storm`. */
  labels: ReadonlyMap<string, string>;
  /** The tile IDs and change IDs new or changed since the compared version. */
  compare: { added: ReadonlySet<string>; changed: ReadonlySet<string> } | null;
};

export type TileFaceProps = { tile: Tile; ctx: TileContext; item?: string };

/** A tile type the page can draw. */
export type TileType = {
  type: string;
  /** The type's name, as the kicker over a tile and in the rail. */
  label: string;
  icon: (props: IconProps) => ReactElement;
  /** The face on the landing grid: whether the tile needs opening. */
  Glance: ComponentType<TileFaceProps>;
  /** Everything about the tile. */
  Detail: ComponentType<TileFaceProps>;
  /** A light check of the tile's data before either face reads it; a tile
   *  that fails it is drawn unavailable. */
  valid: (data: unknown) => boolean;
  /** The code a comment on the tile (or on one of its items) is placed on, or
   *  null for a comment that belongs in the review's body. */
  commentAnchors?: (
    ctx: Pick<TileContext, "doc" | "review">,
    tile: Tile,
    item?: string,
  ) => Anchor[];
  /** A tile whose glance is drawn taller, such as Risks while it holds must-read risks. */
  tall?: (tile: Tile, doc: TileDocument) => boolean;
};

export const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
