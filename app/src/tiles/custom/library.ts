import type { Tile } from "../tile-document";

/** Whether a custom tile is of a built-in library type: the daemon marks
 *  its served definition `builtin: true` only when the stored definition's
 *  content has a built-in type's digest, current or past. The page names
 *  such a tile as baum's own and still mounts it in its sandboxed frame; it
 *  never decides this from anything else the tile carries. */
export function isBuiltinType(tile: Tile): boolean {
  return (
    tile.type === "custom" &&
    (tile.definition as { builtin?: unknown } | null | undefined)?.builtin === true
  );
}

/** A tile's name where the page lists it: its title, marked "Custom ·" for
 *  a custom tile that is not of a built-in type. */
export function tileName(tile: Tile): string {
  return tile.type === "custom" && !isBuiltinType(tile) ? `Custom · ${tile.title}` : tile.title;
}
