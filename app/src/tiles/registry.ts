import { codeTile, diagramTile, proseTile, tableTile } from "./builtins/generic";
import { customTile } from "./custom/custom-tile";
import type { Tile, TileType } from "./types";

/** The tile types drawn as exhibits or in a sandboxed frame; the view's
 *  native diagrams are the review page's own. A type missing here (one a
 *  newer daemon knows) is drawn unavailable, never guessed at. */
const TYPES: ReadonlyMap<string, TileType> = new Map(
  [proseTile, tableTile, diagramTile, codeTile, customTile].map((type) => [type.type, type]),
);

export function tileType(type: string): TileType | undefined {
  return TYPES.get(type);
}

/** How a tile can be drawn: by its type, as pending before its data is
 *  written, or not at all. */
export type TileFace =
  | { kind: "ready"; type: TileType }
  | { kind: "pending"; type: TileType | undefined }
  | { kind: "unavailable"; type: TileType | undefined; why: string };

export function tileFace(tile: Tile): TileFace {
  const type = tileType(tile.type);
  if (tile.state === "unavailable" || !type)
    return {
      kind: "unavailable",
      type,
      why:
        type?.unavailableWhy?.(tile) ??
        tile.problems?.[0] ??
        (type ? "This tile couldn't be read." : "This version can't show this kind of tile."),
    };
  if (!type.valid(tile.data, tile))
    return tile.state === "pending"
      ? { kind: "pending", type }
      : { kind: "unavailable", type, why: "This tile's content couldn't be read." };
  return { kind: "ready", type };
}
