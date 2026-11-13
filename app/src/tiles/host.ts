import { createContext, useContext, type ReactNode } from "react";
import type { ReviewDocument, Section } from "@baum/viewer/review-document";
import type { Tile } from "./tile-document";

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
  /** The address of the full review at the place a tile shows. */
  fullReview?: (tile: Tile | null) => string;
  renderSectionLabels?: (section: Section, document: ReviewDocument) => ReactNode;
};

const NO_COUNTS: ReadonlyMap<string, number> = new Map();

export const TileHostContext = createContext<TileHost>({ draft: false, commentCounts: NO_COUNTS });

export function useTileHost(): TileHost {
  return useContext(TileHostContext);
}

/** Where the reader left the landing page for a tile's detail: its scroll
 *  and the card they opened, so coming back puts both where they were. Kept
 *  per page, outside React, as Back remounts the landing page. */
export type LandingMemory = { scroll: number; tile: string } | null;
