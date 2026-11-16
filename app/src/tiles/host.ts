import { createContext, useContext, type ReactNode } from "react";
import type { ReviewDocument, Section } from "@baum/viewer/review-document";
import type { Tile } from "./tile-document";
import type { LayoutControls } from "./your-layout";

/** What the page around the tiles offers them: comments, asking about a
 *  tile, the way to the full review, and the labels a group page puts on a
 *  change. */
export type TileHost = {
  /** The current document is a draft still being written. */
  draft: boolean;
  /** The reader's pending comments on each tile, by tile ID. */
  commentCounts: ReadonlyMap<string, number>;
  /** Starts a comment on a tile, or on an item of it; absent where the pull
   *  request takes no review. */
  comment?: (tile: string, item?: string) => void;
  /** Asks the session panel about a tile. */
  ask?: (tile: Tile) => void;
  /** Opens the session panel about a custom tile with "Save this tile to my
   *  library" typed, which the agent proposes as a type of the person's
   *  tile library. */
  saveToLibrary?: (tile: Tile) => void;
  /** Removes a pending card of a tile being made in the session panel. */
  removeDraft?: (tile: string) => void;
  /** The address of the full review at the place a tile shows. */
  fullReview?: (tile: Tile | null) => string;
  renderSectionLabels?: (section: Section, document: ReviewDocument) => ReactNode;
  /** Reads the shown version's tile route again, which registers its custom
   *  tiles' frame documents with the daemon again; a custom tile whose frame
   *  never answered calls it once before mounting the frame afresh. */
  reread?: () => Promise<unknown>;
  /** The person's own layout of the pull request's tiles: "Your layout" with
   *  Undo and Reset, Show on a hidden tile, and the tiles a change named.
   *  Absent on a group, a draft and an earlier version. */
  layout?: LayoutControls;
};

const NO_COUNTS: ReadonlyMap<string, number> = new Map();

export const TileHostContext = createContext<TileHost>({ draft: false, commentCounts: NO_COUNTS });

export function useTileHost(): TileHost {
  return useContext(TileHostContext);
}

/** Where the reader left the landing page for a tile's detail: its scroll
 *  and the card they opened, so coming back puts both where they were. Kept
 *  per page, outside React, as Back remounts the landing page. */
export type LandingMemory = {
  /** The landing page's scroll as the reader left it, and the tile opened
   *  then; null when the reader arrived on a tile from elsewhere. */
  left: { scroll: number; tile: string } | null;
  /** The tile the reader read last, as they stepped between tiles. */
  read: string;
} | null;
