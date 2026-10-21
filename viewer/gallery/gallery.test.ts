import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import example from "../../schema/review-example.json" with { type: "json" };
import type { ReviewDocument } from "../src/review-document";
import { validReviewTile } from "../src/review-tiles";
import { tileInput } from "../test/review-tile-inputs";
import fixture from "./example-database-schema.json" with { type: "json" };
import { scanSources } from "./sources";
import { documentTiles, fixtureParts, standInDocument } from "./tiles";

const document = example as unknown as ReviewDocument;

describe("documentTiles", () => {
  it("finds the tiles of sections and of their risks, with where each sits", () => {
    const found = documentTiles({
      sections: [
        {
          id: "sec_a",
          title: "A",
          tiles: [{ id: "tile_one" }],
          risks: [{ id: "risk_a", tiles: [{ id: "tile_two" }] }, { id: "risk_b" }],
        },
        { id: "sec_b" },
      ],
    });
    expect(found.map((entry) => (entry.tile as { id: string }).id)).toEqual([
      "tile_one",
      "tile_two",
    ]);
    expect(found[0]?.location).toBe('section sec_a "A"');
    expect(found[1]?.location).toBe('risk risk_a in section sec_a "A"');
  });

  it("reports a tiles value that is not a list, and reads a document without sections", () => {
    expect(documentTiles({ sections: [{ id: "s", tiles: "x" }] })).toHaveLength(1);
    expect(documentTiles({})).toEqual([]);
    expect(documentTiles(null)).toEqual([]);
  });

  it("reads the schema's example, which carries tiles", () => {
    expect(documentTiles(example).length).toBeGreaterThan(0);
  });
});

describe("fixtureParts", () => {
  it("takes one tile, an array, or a document with tiles, and nothing else", () => {
    expect(fixtureParts({ kind: "bug-fix" })?.tiles).toHaveLength(1);
    expect(fixtureParts([{ kind: "a" }, { kind: "b" }])?.tiles).toHaveLength(2);
    expect(
      fixtureParts({ document: { schema: "baum.review/1" }, tiles: [{}] })?.document,
    ).toBeDefined();
    expect(fixtureParts({ hello: "world" })).toBeNull();
    expect(fixtureParts("text")).toBeNull();
  });
});

describe("standInDocument", () => {
  it("lets a valid tile pass the viewer's checks against the adjusted example", () => {
    expect(fixture.kind).toBe("database-schema");
    const standIn = standInDocument(document, [fixture]);
    expect(standIn.review.head).toBe("1".repeat(40));
    expect(validReviewTile(fixture, standIn)).toBe(true);
    // The input is left alone.
    expect(document.review.head).not.toBe("1".repeat(40));
  });

  it("still rejects a tile whose source names a revision the document does not have", () => {
    const tile = structuredClone(fixture);
    tile.code[0]!.review_version = 7;
    tile.code[0]!.revision = "2".repeat(40);
    const other = { ...tile, code: [...tile.code, { ...tile.code[0]!, revision: "3".repeat(40) }] };
    const standIn = standInDocument(document, [other]);
    expect(standIn.review.version).toBe(7);
    expect(standIn.review.head).toBe("2".repeat(40));
    expect(standIn.review.merge_base).toBe("3".repeat(40));
    const wrong = { ...tile, code: [{ ...tile.code[0]!, revision: "4".repeat(40) }] };
    expect(validReviewTile(wrong, standIn)).toBe(false);
  });

  it("accepts a generated sample of every tile kind that has no code", () => {
    const tile = tileInput("database-schema");
    expect(validReviewTile(tile, standInDocument(document, [tile]))).toBe(true);
  });
});

describe("scanSources", () => {
  let root: string;
  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "gallery-"));
  });
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  const write = (path: string, body: unknown) => {
    const file = join(root, path);
    mkdirSync(join(file, ".."), { recursive: true });
    writeFileSync(file, JSON.stringify(body));
  };

  it("finds run attempts, stored reviews and fixtures, and says what it could not read", () => {
    const withTiles = {
      schema: "baum.review/1",
      review: { repository: "o/r", pull_request: 5, version: 1 },
      sections: [{ id: "sec", tiles: [fixture] }],
    };
    write("runs/run-01/attempts/case-001-review-v21-001.json", {
      document: withTiles,
      model: { name: "m" },
      prompt: { version: "review-v21" },
      tile_kinds: ["database-schema"],
    });
    write("runs/run-01/attempts/case-002-review-v21-001.json", {
      document: { ...withTiles, sections: [{ id: "sec" }] },
    });
    // A capture holds a whole bundle; it is never read.
    write("runs/run-01/captures/case-001.json", { document: withTiles });
    write("data/stored/o-r-5-v1.json", { source: {}, document: withTiles });
    write("data/fixtures/one.json", fixture);
    write("data/fixtures/two.json", [fixture, fixture]);
    write("data/fixtures/bad.json", { hello: 1 });

    const sources = scanSources({
      runs: join(root, "runs"),
      data: join(root, "data"),
      hostRuns: "/host/runs",
    });
    const byGroup = (group: string) => sources.items.filter((item) => item.group === group);
    expect(byGroup("runs")).toHaveLength(1);
    expect(byGroup("runs")[0]?.command).toBe(
      "tools/prompt-eval/prompt-eval view /host/runs/run-01/attempts/case-001-review-v21-001.json",
    );
    expect(byGroup("runs")[0]?.label).toContain("o/r#5 v1");
    expect(byGroup("stored")).toHaveLength(1);
    expect(
      byGroup("fixtures")
        .map((item) => item.tiles.length)
        .toSorted(),
    ).toEqual([1, 2]);
    expect(sources.runAttemptsWithoutTiles).toBe(1);
    expect(sources.problems.map((problem) => problem.file)).toEqual(["fixtures/bad.json"]);

    const again = scanSources({ runs: join(root, "runs"), data: join(root, "data") });
    expect(again.version).toBe(sources.version);
    write("data/fixtures/three.json", fixture);
    expect(scanSources({ runs: join(root, "runs"), data: join(root, "data") }).version).not.toBe(
      sources.version,
    );
  });

  it("reads missing directories as empty", () => {
    const sources = scanSources({ runs: join(root, "nope"), data: join(root, "nada") });
    expect(sources.items).toEqual([]);
  });
});
