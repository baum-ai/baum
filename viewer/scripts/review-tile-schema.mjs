import { readFile } from "node:fs/promises";

async function readSchema(name) {
  return JSON.parse(await readFile(new URL(`../../schema/${name}`, import.meta.url), "utf8"));
}

/** The current and retired tile definitions shared by both build-time generators. */
export async function loadReviewTileSchema() {
  const schema = await readSchema("review-1.json");
  const legacy = await readSchema("legacy-review-tiles.json");
  return {
    anyOf: [{ $ref: "#/$defs/tile" }, { $ref: "#/$defs/legacyTile" }],
    $defs: { ...schema.$defs, ...legacy.$defs },
  };
}
