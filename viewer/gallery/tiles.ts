import type { ReviewDocument } from "../src/review-document.ts";

/** Pure helpers shared by the gallery's dev server and its page: where tiles
 *  sit in a stored document, and the stand-in document a loose tile is shown
 *  against. Dev-only; nothing the viewer ships imports this directory. */

/** One tile and where the document holds it. */
export type FoundTile = { tile: unknown; location: string };

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function label(owner: Record<string, unknown>, fallback: string): string {
  const title = typeof owner.title === "string" ? owner.title : undefined;
  const id = typeof owner.id === "string" ? owner.id : undefined;
  return `${fallback} ${id ?? ""}${title ? ` "${title}"` : ""}`.replace(/\s+/g, " ").trim();
}

/** Every tile of a document: those of its sections, then of each section's
 *  risks, as `ReviewTiles` receives them. A tiles value that is not a list is
 *  reported as one tile so the page shows the viewer's own fallback for it. */
export function documentTiles(document: unknown): FoundTile[] {
  const found: FoundTile[] = [];
  const add = (owner: Record<string, unknown>, where: string) => {
    if (owner.tiles === undefined) return;
    if (!Array.isArray(owner.tiles)) found.push({ tile: owner.tiles, location: where });
    else for (const tile of owner.tiles) found.push({ tile, location: where });
  };
  const sections = record(document)?.sections;
  if (!Array.isArray(sections)) return found;
  for (const section of sections) {
    const owner = record(section);
    if (!owner) continue;
    const sectionLabel = label(owner, "section");
    add(owner, sectionLabel);
    if (Array.isArray(owner.risks)) {
      for (const risk of owner.risks) {
        const riskOwner = record(risk);
        if (riskOwner) add(riskOwner, `${label(riskOwner, "risk")} in ${sectionLabel}`);
      }
    }
  }
  return found;
}

/** A fixture file holds one tile, an array of tiles, or `{ document, tiles }`
 *  to show tiles against a document of the author's own. */
export function fixtureParts(
  value: unknown,
): { tiles: unknown[]; document?: Record<string, unknown> } | null {
  if (Array.isArray(value)) return { tiles: value };
  const object = record(value);
  if (!object) return null;
  if (Array.isArray(object.tiles) && record(object.document))
    return { tiles: object.tiles, document: record(object.document) as Record<string, unknown> };
  if (typeof object.kind === "string") return { tiles: [value] };
  return null;
}

/** The example document, adjusted so the tiles' code sources pass the viewer's
 *  checks: its review version is the highest a source names, and its head and
 *  merge base are the first revisions the sources at that version use. A tile
 *  that is wrong in some other way still fails, which is the point. */
export function standInDocument(example: ReviewDocument, tiles: unknown[]): ReviewDocument {
  const document = structuredClone(example);
  let version = 1;
  for (const tile of tiles) {
    const code = record(tile)?.code;
    if (!Array.isArray(code)) continue;
    for (const file of code) {
      const reviewVersion = record(file)?.review_version;
      if (typeof reviewVersion === "number" && reviewVersion > version) version = reviewVersion;
    }
  }
  const revisions: string[] = [];
  for (const tile of tiles) {
    const code = record(tile)?.code;
    if (!Array.isArray(code)) continue;
    for (const file of code) {
      const entry = record(file);
      if (
        entry?.review_version === version &&
        typeof entry.revision === "string" &&
        !revisions.includes(entry.revision)
      )
        revisions.push(entry.revision);
    }
  }
  document.review.version = version;
  document.review.previous_version = version > 1 ? version - 1 : null;
  if (revisions[0]) document.review.head = revisions[0];
  if (revisions[1]) document.review.merge_base = revisions[1];
  return document;
}
