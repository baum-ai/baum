import { changeTile } from "./builtins/change";
import { contextTile } from "./builtins/context";
import { decisionsTile } from "./builtins/decisions";
import { everythingElseTile } from "./builtins/everything-else";
import { evidenceTile } from "./builtins/evidence";
import { codeTile, diagramTile, proseTile, tableTile } from "./builtins/generic";
import { intentTile } from "./builtins/intent";
import { mediaTile } from "./builtins/media";
import { overviewTile } from "./builtins/overview";
import { risksTile } from "./builtins/risks";
import { customTile } from "./custom/custom-tile";
import type { Tile } from "./tile-document";
import type { TileType } from "./types";

/** The tile types this page draws. A type missing here (one a newer daemon
 *  knows) is drawn unavailable, never guessed at. */
const TYPES: ReadonlyMap<string, TileType> = new Map(
  [
    overviewTile,
    changeTile,
    risksTile,
    evidenceTile,
    intentTile,
    decisionsTile,
    contextTile,
    mediaTile,
    everythingElseTile,
    proseTile,
    tableTile,
    diagramTile,
    codeTile,
    customTile,
  ].map((type) => [type.type, type]),
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
