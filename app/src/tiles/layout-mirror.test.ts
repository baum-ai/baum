import { describe, expect, it } from "vitest";
import { landingOf } from "./layout";
import { goldens, golden } from "./goldens-test-helper";
import { parseTileDocument, type TileDocument } from "./tile-document";

// The landing page mirrors the Go layout's rule (`oss/tiles/layout.go`:
// Hidden, Visible, More), transcribed here and checked on every Go golden,
// so the two cannot drift apart.
function goHidden(doc: TileDocument) {
  const collapse = new Map(
    doc.layout.groups
      .filter((g) => g.collapse_after !== undefined)
      .map((g) => [g.id, g.collapse_after!]),
  );
  const members = new Map<string, number>();
  return doc.layout.entries.map((e) => {
    if (!e.group) return false;
    members.set(e.group, (members.get(e.group) ?? 0) + 1);
    const limit = collapse.get(e.group);
    return limit !== undefined && members.get(e.group)! > limit;
  });
}
function check(doc: TileDocument, name: string) {
  const hidden = goHidden(doc);
  const visible = doc.layout.entries
    .filter((_, i) => i < doc.layout.landing && !hidden[i])
    .map((e) => e.tile);
  const more = doc.layout.entries
    .filter((_, i) => i >= doc.layout.landing && !hidden[i])
    .map((e) => e.tile);
  const hiddenIds = doc.layout.entries.filter((_, i) => hidden[i]).map((e) => e.tile);
  const l = landingOf(doc, { more: false, groups: new Set() });
  const shown = l.items.flatMap((it) => (it.kind === "tile" ? [it.tile.id] : []));
  const behindGroups = l.items.flatMap((it) =>
    it.kind === "more" ? it.hidden.map((t) => t.id) : [],
  );
  expect(shown, name + " visible").toEqual(visible);
  expect(
    l.rest.map((t) => t.id),
    name + " more",
  ).toEqual(more);
  // hidden in groups shown on landing must be a subset of Go hidden
  for (const id of behindGroups) expect(hiddenIds, name).toContain(id);
  const all = landingOf(doc, { more: true, groups: new Set(doc.layout.groups.map((g) => g.id)) });
  expect(
    all.items.flatMap((it) => (it.kind === "tile" ? [it.tile.id] : [])),
    name + " all",
  ).toEqual(doc.layout.entries.map((e) => e.tile));
}
describe("TS landing mirrors Go", () => {
  it("on every golden", () => {
    for (const [path, m] of Object.entries(goldens)) {
      const doc = parseTileDocument(structuredClone(m.default));
      if (doc) check(doc, path);
    }
  });
  it("when a collapsed group starts past landing", () => {
    const doc = structuredClone(golden("many-changes"));
    // A landing area of one entry puts the whole Changes group past it.
    doc.layout.landing = 1;
    check(doc, "landing=1");
  });
});
