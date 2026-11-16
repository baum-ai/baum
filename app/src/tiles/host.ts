import { createContext, useContext } from "react";
import type { Tile } from "./types";

/** What the page around the tiles offers them: comments, asking about a
 *  tile, saving a chat-made one to the library, removing a tile the person
 *  added, and the pending cards of tiles being made. */
export type TileHost = {
  /** The current document is a draft still being written. */
  draft: boolean;
  /** Starts a comment on a tile; absent where the pull request takes no
   *  review. */
  comment?: (tile: string) => void;
  /** Asks the session panel about a tile. */
  ask?: (tile: Tile) => void;
  /** Opens the session panel about a custom tile with "Save this tile to my
   *  library" typed, which the agent proposes as a type of the person's
   *  tile library. */
  saveToLibrary?: (tile: Tile) => void;
  /** Removes a tile the person added from every version of the pull
   *  request, with Undo; absent on a group and on a draft. */
  removeAdded?: (tile: Tile) => void;
  /** Removes a pending card of a tile being made in the session panel. */
  removeDraft?: (tile: string) => void;
  /** Reads the shown version's tile route again, which registers its custom
   *  tiles' frame documents with the daemon again; a custom tile whose frame
   *  never answered calls it once before mounting the frame afresh. */
  reread?: () => Promise<unknown>;
};

export const TileHostContext = createContext<TileHost>({ draft: false });

export function useTileHost(): TileHost {
  return useContext(TileHostContext);
}
