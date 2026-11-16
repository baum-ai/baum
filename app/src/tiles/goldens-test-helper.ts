import { parseTileDocument, type TileDocument } from "./tile-document";

// The Go converter's goldens (`oss/tiles/testdata`): every edge shape of the
// format, which the TypeScript types must read without drifting from Go.
export const goldens = import.meta.glob<{ default: unknown }>(
  "../../../tiles/testdata/*.tiles.json",
  { eager: true },
);

/** A golden by its name, parsed by the page's own structural guard. */
export function golden(name: string): TileDocument {
  const found = Object.entries(goldens).find(([path]) => path.endsWith(`/${name}.tiles.json`));
  if (!found) throw Error(`no golden ${name}`);
  const parsed = parseTileDocument(structuredClone(found[1].default));
  if (!parsed) throw Error(`golden ${name} does not parse`);
  return parsed;
}
