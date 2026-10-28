import { mkdir, writeFile } from "node:fs/promises";
import { compile } from "json-schema-to-typescript";
import { loadReviewTileSchema } from "./review-tile-schema.mjs";

const output = new URL("../generated/review-tile-types.d.ts", import.meta.url);
await mkdir(new URL(".", output), { recursive: true });
await writeFile(
  output,
  await compile(await loadReviewTileSchema(), "ReviewTile", {
    bannerComment:
      "/* Generated from the shared review-1.json and legacy-review-tiles.json. Do not edit. */",
    ignoreMinAndMaxItems: true,
  }),
);
