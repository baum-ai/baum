import type { ReviewDocument } from "@baum/viewer/review-document";
import fixture from "./testdata/custom-tiles.json" with { type: "json" };
import { tileSourceOf, type Tile, type TileSource } from "./types";

// A review with the custom tiles a view serves beside it (the route_changes
// type, ready and in each unavailable state) and the prose tile they cite.

/** A fresh copy of the fixture's review. */
export function fixtureReview(): ReviewDocument {
  return structuredClone(fixture.review) as unknown as ReviewDocument;
}

/** A fresh copy of the fixture's tile with this ID. */
export function fixtureTile(id: string): Tile {
  const tile = fixture.tiles.find((each) => each.id === id);
  if (!tile) throw Error(`no fixture tile ${id}`);
  return structuredClone(tile) as unknown as Tile;
}

/** The fixture's citation source: its review with every tile. */
export function fixtureSource(): TileSource {
  return tileSourceOf(fixtureReview(), fixture.tiles as unknown as Tile[]);
}
