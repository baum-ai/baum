import { mkdir, readFile, writeFile } from "node:fs/promises";
import { compile } from "json-schema-to-typescript";

const schema = JSON.parse(
  await readFile(new URL("../../schema/review-1.json", import.meta.url), "utf8"),
);
// Stored documents may still carry tile kinds the shared schema retired.
const legacy = JSON.parse(
  await readFile(new URL("../../schema/legacy-review-tiles.json", import.meta.url), "utf8"),
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
