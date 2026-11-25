// Saving a tile type file the Tile library exported. The daemon's pages may
// call `save_tile_type`: it opens the native save dialog and writes only where
// the person chose, only a file of the size and name a tile type file has.
import { writeFile } from "node:fs/promises";
import { registerCommand } from "./commands.ts";

/** The largest file the Tile library exports or imports. */
export const MAX_FILE_BYTES = 3 << 20;

export interface SaveDialog {
  title: string;
  defaultName: string;
  filters: { name: string; extensions: string[] }[];
}

export interface TileFileDeps {
  /** The native save dialog; null when the person cancelled. */
  chooseSavePath: (dialog: SaveDialog) => Promise<string | null>;
  writeFile?: (path: string, contents: string) => Promise<void>;
}

/** Whether `name` is a tile type's name: a lowercase letter, then up to 63
 *  lowercase letters, digits and underscores. */
export function validName(name: string): boolean {
  return /^[a-z][a-z0-9_]{0,63}$/.test(name);
}

/** Asks where to save `<name>.baumtile.json` and writes `contents` there.
 *  Returns the path written, or null when the person cancelled. */
export async function saveTileType(
  deps: TileFileDeps,
  name: unknown,
  contents: unknown,
): Promise<string | null> {
  if (typeof name !== "string" || !validName(name)) {
    throw new Error("That is not a tile type's name.");
  }
  if (typeof contents !== "string") throw new Error("The file's contents are missing.");
  if (Buffer.byteLength(contents) > MAX_FILE_BYTES)
    throw new Error("The file is larger than 3 MiB.");
  const path = await deps.chooseSavePath({
    title: "Export tile type",
    defaultName: `${name}.baumtile.json`,
    filters: [{ name: "baum tile type", extensions: ["json"] }],
  });
  if (path === null) return null;
  await (deps.writeFile ?? ((p, c) => writeFile(p, c, "utf8")))(path, contents);
  return path;
}

export function registerTileFileCommands(deps: TileFileDeps): void {
  registerCommand("save_tile_type", (args) => saveTileType(deps, args.name, args.contents));
}
