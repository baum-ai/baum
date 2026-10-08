import { mkdir, readFile, writeFile } from "node:fs/promises";
import { compile } from "json-schema-to-typescript";
import { buildReviewTileValidators } from "./review-tile-validators.mjs";

const schema = JSON.parse(
  await readFile(new URL("../../schema/review-1.json", import.meta.url), "utf8"),
);
// Stored documents may still carry tile kinds the shared schema retired.
const legacy = JSON.parse(
  await readFile(new URL("../src/legacy-review-tiles.json", import.meta.url), "utf8"),
);
const output = new URL("../generated/review-tile-types.d.ts", import.meta.url);
await mkdir(new URL(".", output), { recursive: true });
await writeFile(
  output,
  await compile(
    {
      anyOf: [{ $ref: "#/$defs/tile" }, { $ref: "#/$defs/legacyTile" }],
      $defs: { ...schema.$defs, ...legacy.$defs },
    },
    "ReviewTile",
    {
      bannerComment:
        "/* Generated from the shared review-1.json and legacy-review-tiles.json. Do not edit. */",
      ignoreMinAndMaxItems: true,
    },
  ),
);

// Committed, because the tests and the hosts load it at runtime and only
// "pnpm typecheck" runs this script; a test checks that it is current.
await mkdir(new URL("../src/precompiled/", import.meta.url), { recursive: true });
await writeFile(
  new URL("../src/precompiled/review-tile-validators.js", import.meta.url),
  await buildReviewTileValidators(),
);
