import { writeFile } from "node:fs/promises";
import { buildReviewTileValidators } from "./review-tile-validators.mjs";

// The output is committed, and "pnpm generate" does not touch it, so that a
// unit test can fail when review-1.json or legacy-review-tiles.json change
// without a rerun of this script.
await writeFile(
  new URL("../src/precompiled/review-tile-validators.js", import.meta.url),
  await buildReviewTileValidators(),
);
