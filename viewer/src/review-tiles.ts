import { type Anchor, githubBlobUrl, type ReviewDocument } from "./review-document";
import { validateAnchor, validateTile } from "./precompiled/review-tile-validators.js";
import type {
  Anchor as TileAnchor,
  ReviewTile,
  TileCodeFile,
} from "../generated/review-tile-types.js";

/** Stored documents may still hold kinds that generation no longer produces;
 * they keep validating and rendering here rather than falling back. The
 * validators are compiled ahead of time from the shared schema and the legacy
 * tile definitions (scripts/review-tile-validators.mjs), so nothing compiles a
 * schema in the browser, which a strict Content-Security-Policy would block. */
const shape = validateTile as (value: unknown) => value is ReviewTile;
const anchorShape = validateAnchor as (value: unknown) => value is TileAnchor;
export type { ReviewTile } from "../generated/review-tile-types.js";

export function tileRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

/** Valid anchors survive even when the tile's kind or payload is unsupported. */
export function tileAnchors(value: unknown): Anchor[] {
  const anchors = tileRecord(value)?.anchors;
  if (!Array.isArray(anchors)) return [];
  return anchors
    .filter((anchor): anchor is TileAnchor => anchorShape(anchor))
    .map((anchor) => {
      if (
        anchor.type === "pr_comment" &&
        typeof anchor.id === "string" &&
        /^\d+$/.test(anchor.id)
      ) {
        return { ...anchor, id: Number(anchor.id) };
      }
      return anchor;
    }) as Anchor[];
}

function validDiff(file: TileCodeFile): boolean {
  if (!file.diff) return true;
  let previousOldEnd = 0;
  let previousNewEnd = 0;
  for (const hunk of file.diff.hunks) {
    const match = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$/.exec(hunk.header);
    if (!match) return false;
    const oldStart = Number(match[1]);
    const oldCount = Number(match[2] ?? 1);
    const newStart = Number(match[3]);
    const newCount = Number(match[4] ?? 1);
    if (oldStart < previousOldEnd || newStart < previousNewEnd) return false;
    let oldLine = oldStart;
    let newLine = newStart;
    for (const line of hunk.lines) {
      if (line.kind !== "add" && line.old_line !== oldLine++) return false;
      if (line.kind !== "remove" && line.new_line !== newLine++) return false;
    }
    if (oldLine !== oldStart + oldCount || newLine !== newStart + newCount) return false;
    previousOldEnd = oldLine;
    previousNewEnd = newLine;
  }
  return true;
}

/** Architecture fields the schema retired are ignored rather than failing the
 * whole tile, so stored tiles that carry them still render. */
export function withoutRetiredTileFields(value: unknown): unknown {
  const tile = tileRecord(value);
  const data = tileRecord(tile?.data);
  if (tile?.kind !== "architecture" || !data || !("context" in data || "deployment" in data))
    return value;
  const { context: _context, deployment: _deployment, ...rest } = data;
  return { ...tile, data: rest };
}

/** Validate the shared shape and safe source display here. The generator owns
 * permission, relationship and workflow semantics before publishing a review. */
export function validReviewTile(value: unknown, document: ReviewDocument): value is ReviewTile {
  if (!shape(value)) return false;
  const tile = value;
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(document.review.repository)) return false;
  for (const file of tile.code) {
    if (
      file.path.startsWith("/") ||
      file.path.includes("\\") ||
      [...file.path].some((char) => char.charCodeAt(0) < 32) ||
      file.path.split("/").some((part) => !part || part === "." || part === "..") ||
      file.review_version > document.review.version ||
      !validDiff(file)
    )
      return false;
    if (
      file.review_version === document.review.version &&
      file.revision !== document.review.head &&
      file.revision !== document.review.merge_base
    )
      return false;
  }
  return true;
}

export function tileCodeUrl(repository: string, file: TileCodeFile): string {
  return githubBlobUrl(repository, file.revision, file.path);
}
