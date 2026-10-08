import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import { buildReviewTileValidators } from "../scripts/review-tile-validators.mjs";

const committed = () =>
  readFile(new URL("./precompiled/review-tile-validators.js", import.meta.url), "utf8");

describe("precompiled tile validators", () => {
  it("are current with the schema files", async () => {
    // Compared as a boolean: the files are hundreds of kilobytes, so a diff of
    // them would bury the message.
    expect(
      (await committed()) === (await buildReviewTileValidators()),
      "src/precompiled/review-tile-validators.js is out of date: run `pnpm generate:validators`, rebuild the app with `pnpm build:app` and commit the results",
    ).toBe(true);
  });

  it("compile nothing at run time", async () => {
    expect(await committed()).not.toMatch(/new Function|\beval\(/);
  });
});
