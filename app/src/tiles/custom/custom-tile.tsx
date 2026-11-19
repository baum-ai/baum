import { isRecord, type TileFaceProps, type TileType } from "../types";
import { citedHunks } from "./refs";
import { definitionKit, frameDigest, NEWER_KIT, TileFrame } from "./tile-frame";
import { KIT_SUPPORTED } from "./protocol";

/** A custom tile: a tile type an agent wrote, whose faces draw in a sandboxed
 *  frame (see "Custom tile frames" in oss/ARCHITECTURE.md). The page draws
 *  the tile's title itself; only the body is the tile's own. */

function CustomDetail({ tile, ctx }: TileFaceProps) {
  return <TileFrame tile={tile} ctx={ctx} face="detail" />;
}

const SIZES = new Set(["full", "half", "third"]);

export const customTile: TileType = {
  type: "custom",
  Detail: CustomDetail,
  // The daemon checked the data against the definition's schema; the page
  // checks only what it reads of the definition to mount the frame. A tile
  // still being written has no data its component can be given yet: it is
  // drawn as being written until it is ready.
  valid: (_data, tile) => {
    const definition = tile?.definition;
    return (
      tile?.state !== "pending" &&
      isRecord(definition) &&
      frameDigest(definition) !== null &&
      definitionKit(definition) !== null &&
      Array.isArray(definition.sizes) &&
      definition.sizes.length > 0 &&
      definition.sizes.every((size) => SIZES.has(size as string))
    );
  },
  unavailableWhy: (tile) => {
    const kit = definitionKit(tile.definition);
    return kit !== null && kit > KIT_SUPPORTED ? NEWER_KIT : null;
  },
  commentAnchors: (tile) => citedHunks(tile.data),
};
