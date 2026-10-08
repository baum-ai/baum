import { readFile } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import { buildReviewTileValidators } from "../scripts/review-tile-validators.mjs";

const committed = () =>
  readFile(new URL("./precompiled/review-tile-validators.js", import.meta.url), "utf8");

describe("precompiled tile validators", () => {
  it("are current: run pnpm generate and commit the result", async () => {
    expect(await committed()).toBe(await buildReviewTileValidators());
  });

  it("compile nothing at run time", async () => {
    expect(await committed()).not.toMatch(/new Function|\beval\(/);
  });
});
