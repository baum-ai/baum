import { readFile } from "node:fs/promises";

/** The current and retired tile definitions shared by both build-time generators. */
export async function loadReviewTileSchema() {
  const read = async (name) =>
    JSON.parse(await readFile(new URL(`../../schema/${name}`, import.meta.url), "utf8"));
  const schema = await read("review-1.json");
  const legacy = await read("legacy-review-tiles.json");
  return {
    anyOf: [{ $ref: "#/$defs/tile" }, { $ref: "#/$defs/legacyTile" }],
    $defs: { ...schema.$defs, ...legacy.$defs },
  };
}
