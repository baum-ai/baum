import { describe, expect, it } from "vitest";
import citationKeys from "../../../../tiles/testdata/citation-keys.json" with { type: "json" };
import citationShapes from "../../../../tiles/testdata/citations.json" with { type: "json" };
import { golden } from "../goldens-test-helper";
import { citedHunks, findCitations, isCitation, keyOf, MAX_CITATIONS, resolveRefs } from "./refs";

describe("citation keys", () => {
  it("are the kit's and Go's, for every listed citation", () => {
    for (const { citation, key } of citationKeys as { citation: unknown; key: string | null }[])
      expect(keyOf(citation), JSON.stringify(citation)).toBe(key);
  });
});

describe("citation shapes", () => {
  it("are Go's", () => {
    for (const shape of citationShapes.valid)
      expect(isCitation(shape), JSON.stringify(shape)).toBe(true);
    for (const shape of citationShapes.invalid)
      expect(isCitation(shape), JSON.stringify(shape)).toBe(false);
  });

  it("are found in the daemon's order", () => {
    const found = findCitations({
      z: [{ type: "risk", id: "rsk_b" }],
      a: { nested: { type: "tile", id: "overview" }, not: { type: "risk", id: "ev_wrong" } },
      m: { type: "hunk", id: "h_000000000001", lines: [1, 2] },
    });
    expect(found.map((citation) => keyOf(citation))).toEqual([
      '["tile","overview"]',
      '["hunk","h_000000000001",[1,2]]',
      '["risk","rsk_b"]',
    ]);
  });
});

describe("resolveRefs", () => {
  const doc = golden("custom");
  const tile = doc.tiles.find((each) => each.id === "route_changes")!;
  const labels = new Map([["rsk_retry_storm", "Retries can pile up"]]);

  it("resolves what the tile cites, and only that", () => {
    const { refs, targets } = resolveRefs(tile.data, doc, "detail", labels);
    const keys = findCitations(tile.data).map((citation) => keyOf(citation)!);
    expect(Object.keys(refs).toSorted()).toEqual([...new Set(keys)].toSorted());
    expect(refs['["risk","rsk_retry_storm"]']).toMatchObject({
      status: "ok",
      label: "Retries can pile up",
      navigable: true,
      tier: expect.any(String),
    });
    expect(refs['["tile","overview"]']).toMatchObject({ status: "ok", navigable: true });
    expect(refs['["file","config/config.go"]']).toMatchObject({
      status: "ok",
      path: "config/config.go",
    });
    // Places the page has no jump for are labels only.
    expect(refs['["external_url","https://example.com/spec"]']).toMatchObject({
      status: "ok",
      navigable: false,
    });
    expect(targets.get('["hunk","h_000000000003",[21,25]]')).toEqual({
      kind: "hunk",
      id: "h_000000000003",
      path: expect.any(String),
      line: 21,
    });
    expect(targets.has('["external_url","https://example.com/spec"]')).toBe(false);
    expect(targets.get('["risk","rsk_retry_storm"]')).toMatchObject({
      kind: "item",
      id: "rsk_retry_storm",
    });
  });

  it("gives a glance no code and a detail the cited lines", () => {
    const withDiff = structuredClone(doc);
    const hunk = withDiff.index.hunks.find((each) => each.id === "h_000000000003")!;
    hunk.diff = {
      header: "@@ -1,40 +1,40 @@",
      lines: Array.from({ length: 40 }, (_, at) => ({
        kind: "context" as const,
        old_line: at + 1,
        new_line: at + 1,
        text: `line ${at + 1}`,
      })),
    };
    const glance = resolveRefs(tile.data, withDiff, "glance", labels).refs;
    expect(glance['["hunk","h_000000000003",[21,25]]']).not.toHaveProperty("excerpt");
    const detail = resolveRefs(tile.data, withDiff, "detail", labels).refs;
    const excerpt = detail['["hunk","h_000000000003",[21,25]]']!.excerpt as {
      lines: { new: number }[];
      omitted_before: number;
      omitted_after: number;
    };
    expect(excerpt.lines.map((line) => line.new)).toEqual(
      Array.from({ length: 11 }, (_, at) => 18 + at),
    );
    expect(excerpt.omitted_before).toBe(17);
    expect(excerpt.omitted_after).toBe(12);
    // The whole hunk where no lines are cited.
    expect(
      (detail['["hunk","h_000000000003",null]']!.excerpt as { lines: unknown[] }).lines,
    ).toHaveLength(40);
  });

  it("marks what is not in the review unavailable, never navigable", () => {
    const { refs, targets } = resolveRefs(
      {
        cites: [
          { type: "risk", id: "rsk_gone" },
          { type: "hunk", id: "h_0000000000ff" },
        ],
      },
      doc,
      "detail",
      labels,
    );
    expect(refs['["risk","rsk_gone"]']).toMatchObject({
      status: "unavailable",
      why: "not_in_review",
    });
    expect(refs['["hunk","h_0000000000ff",null]']).toMatchObject({ status: "unavailable" });
    expect(targets.size).toBe(0);
  });

  it("stops resolving past the most citations a tile may have", () => {
    const many = Array.from({ length: MAX_CITATIONS + 5 }, (_, at) => ({
      type: "pr",
      number: at + 1,
    }));
    const { refs } = resolveRefs({ many }, doc, "glance", labels);
    expect(refs[`["pr",${MAX_CITATIONS + 1}]`]).toMatchObject({
      status: "unavailable",
      why: "too_many",
    });
    expect(refs[`["pr",${MAX_CITATIONS + 2}]`]).toBeUndefined();
  });
});

describe("citedHunks", () => {
  it("places a comment on the hunks the data cites, in data order", () => {
    const tile = golden("custom").tiles.find((each) => each.id === "route_changes")!;
    expect(citedHunks(tile.data)).toEqual([
      { type: "hunk", id: "h_000000000003", lines: [21, 25] },
      { type: "hunk", id: "h_000000000001" },
      { type: "hunk", id: "h_000000000003" },
    ]);
  });
});
