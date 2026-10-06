import { describe, expect, it } from "vitest";

import { entityGroupKey, groupApiContent } from "./review-entity-groups";
import type { Block, Entity, Section } from "./review-document";

const entity = (kind: string, name: string, hunks: string[] = [], path?: string): Entity => ({
  kind,
  name,
  path,
  change: "added",
  hunks,
});

/** The block that marks a section as the one stating what the change does. */
const behaviorBlock: Block = {
  kind: "behavior",
  steps: [
    {
      id: "beh_1",
      text: "The endpoint answers with the filtered list",
      anchors: [{ type: "hunk", id: "h_list" }],
      evidence: [],
    },
  ],
};

const section = (blocks: Block[], tiles?: unknown[]): Section => ({
  id: "sec_reviews",
  title: "Refresh endpoint",
  kind: "logical",
  hunks: [],
  summary: { text: "", anchors: [] },
  attention: null,
  risks: [],
  evidence: [],
  blocks,
  tiles,
  derived: {
    risk_tier: "low",
    additions: 0,
    deletions: 0,
    role_loc: {},
    reviewers: [],
    state: "new",
    evidence_summary: {},
    open_findings: 0,
  },
});

/** A section that describes behaviour, which is the only kind that gets a panel. */
const behaviorSection = (blocks: Block[], tiles?: unknown[]): Section =>
  section([behaviorBlock, ...blocks], tiles);

const entities = (items: Entity[]): Block => ({ kind: "entities", items });

const contractTile = (id: string, hunks: string[] = [], paths: string[] = []) => ({
  id,
  kind: "api-contract",
  anchors: hunks.map((hunk) => ({ type: "hunk", id: hunk })),
  code: paths.map((path) => ({ path })),
});

describe("groupApiContent", () => {
  it("frames the contract tile under the one changed endpoint", () => {
    const tile = contractTile("tile_contract");
    const grouped = groupApiContent(
      behaviorSection([entities([entity("endpoint", "POST /api/reviews")])], [tile]),
    );
    expect(grouped?.groups.map((group) => group.label)).toEqual(["API"]);
    expect(grouped?.groups[0]!.entity.name).toBe("POST /api/reviews");
    expect(grouped?.groups[0]!.tiles).toEqual([tile]);
    expect(grouped?.remainderTiles).toEqual([]);
  });

  it("leaves tiles that are not about an endpoint where they were", () => {
    const schema = { id: "tile_schema", kind: "database-schema" };
    const grouped = groupApiContent(
      behaviorSection(
        [entities([entity("endpoint", "GET /api/reviews")])],
        [schema, contractTile("tile_contract")],
      ),
    );
    expect(grouped?.remainderTiles).toEqual([schema]);
  });

  it("sends each contract tile to the endpoint whose hunks it cites", () => {
    const listing = contractTile("tile_list", ["h_list"]);
    const create = contractTile("tile_create", ["h_create"]);
    const grouped = groupApiContent(
      behaviorSection(
        [
          entities([
            entity("endpoint", "GET /api/reviews", ["h_list"]),
            entity("endpoint", "POST /api/reviews", ["h_create"]),
          ]),
        ],
        [create, listing],
      ),
    );
    expect(grouped?.groups.map((group) => [group.entity.name, group.tiles] as const)).toEqual([
      ["GET /api/reviews", [listing]],
      ["POST /api/reviews", [create]],
    ]);
  });

  it("matches on the changed file when the tile cites no hunk", () => {
    const tile = contractTile("tile_contract", [], ["server/internal/review/http/reviews.go"]);
    const grouped = groupApiContent(
      behaviorSection(
        [
          entities([
            entity("endpoint", "GET /api/reviews", [], "server/internal/review/http/reviews.go"),
            entity("endpoint", "POST /api/runs", [], "server/internal/review/http/runs.go"),
          ]),
        ],
        [tile],
      ),
    );
    expect(grouped?.groups.map((group) => group.entity.name)).toEqual(["GET /api/reviews"]);
  });

  it("keeps an ambiguous tile flat rather than filing it under a guess", () => {
    expect(
      groupApiContent(
        behaviorSection(
          [
            entities([
              entity("endpoint", "GET /api/reviews"),
              entity("endpoint", "POST /api/reviews"),
            ]),
          ],
          [contractTile("tile_contract")],
        ),
      ),
    ).toBeNull();
  });

  it("adds no panel to a section that does not describe behaviour", () => {
    expect(
      groupApiContent(
        section([entities([entity("endpoint", "GET /api/reviews")])], [contractTile("tile_api")]),
      ),
    ).toBeNull();
  });

  it("adds no panel when the section changed no endpoint", () => {
    expect(
      groupApiContent(
        behaviorSection([entities([entity("table", "pull_requests")])], [contractTile("tile_api")]),
      ),
    ).toBeNull();
  });

  it("adds no panel when a changed endpoint has no contract tile", () => {
    expect(
      groupApiContent(behaviorSection([entities([entity("endpoint", "GET /x")])], [])),
    ).toBeNull();
  });

  it("files a contract tile under the operation it declares", () => {
    const tile = {
      ...contractTile("tile_contract", ["h_shared"], ["handler.go"]),
      data: { method: "POST", path: "/api/reviews" },
    };
    const grouped = groupApiContent(
      behaviorSection(
        [
          entities([
            entity("endpoint", "GET /api/reviews", ["h_shared"], "handler.go"),
            entity("endpoint", "POST /api/reviews", ["h_shared"], "handler.go"),
          ]),
        ],
        [tile],
      ),
    );
    expect(grouped?.groups.map((group) => group.entity.name)).toEqual(["POST /api/reviews"]);
    expect(grouped?.groups[0]!.tiles).toEqual([tile]);
  });

  it("leaves a declared operation flat when the section names a different endpoint", () => {
    const tile = {
      ...contractTile("tile_contract"),
      data: { method: "DELETE", path: "/api/other" },
    };
    expect(
      groupApiContent(
        behaviorSection([entities([entity("endpoint", "GET /api/reviews")])], [tile]),
      ),
    ).toBeNull();
  });

  it("ignores a null anchor instead of throwing while grouping", () => {
    const tile = {
      id: "tile_contract",
      kind: "api-contract",
      anchors: [null],
      data: { method: "GET", path: "/api/reviews" },
    };
    expect(() =>
      groupApiContent(
        behaviorSection(
          [
            entities([
              entity("endpoint", "GET /api/reviews"),
              entity("endpoint", "POST /api/reviews"),
            ]),
          ],
          [tile],
        ),
      ),
    ).not.toThrow();
    expect(
      groupApiContent(behaviorSection([entities([entity("endpoint", "GET /api/reviews")])], [tile]))
        ?.groups[0]!.tiles,
    ).toEqual([tile]);
  });

  it("keys a group by the entity it frames", () => {
    expect(entityGroupKey(entity("endpoint", "GET /x"))).toBe("endpoint:GET /x");
  });
});
