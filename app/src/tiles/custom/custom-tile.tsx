import { GridIcon } from "../icons";
import { Line } from "../parts";
import type { Tile } from "../tile-document";
import { isRecord, type TileFaceProps, type TileType } from "../types";
import { citedHunks } from "./refs";
import { definitionKit, frameDigest, NEWER_KIT, TileFrame } from "./tile-frame";
import { KIT_SUPPORTED } from "./protocol";

/** A custom tile: a tile type an agent wrote, whose faces draw in a sandboxed
 *  frame (see "Custom tile frames" in oss/ARCHITECTURE.md). The page draws
 *  the card, its title and headline, and the detail's heading itself; only
 *  the body is the tile's own. */

/** The definition's title, which names the tile's kind as a built-in's label
 *  does; "Custom" when it has none. */
function kindOf(tile: Tile): string {
  const title = (tile.definition as { title?: unknown } | null)?.title;
  return typeof title === "string" && title.trim() ? title.trim() : "Custom";
}

function CustomGlance({ tile, ctx }: TileFaceProps) {
  return (
    <>
      {tile.headline && tile.headline !== tile.title ? (
        <Line className="-mt-1.5 mb-1 text-base text-secondary">{tile.headline}</Line>
      ) : null}
      <TileFrame tile={tile} ctx={ctx} face="glance" />
    </>
  );
}

function CustomDetail({ tile, ctx }: TileFaceProps) {
  return <TileFrame tile={tile} ctx={ctx} face="detail" />;
}

const SIZES = new Set(["full", "half", "third"]);

export const customTile: TileType = {
  type: "custom",
  label: "Custom",
  labelOf: kindOf,
  icon: GridIcon,
  Glance: CustomGlance,
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
  commentAnchors: (_ctx, tile) => citedHunks(tile.data),
};
