import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";
import type { CommentTarget } from "../pending-review-model";
import { reviewOf, viewTile } from "../review-view/model";
import { tileType } from "./registry";

/** What a comment on a tile of the review page is about, and the code it is
 *  placed on (`anchors`, placed by the caller; none puts it in the review's
 *  body): the hunks its type cites, else those its data names as `source`,
 *  else the tile's own citations. */
export function tileCommentTarget(
  document: ReviewDocument,
  tileId: string,
): { target: CommentTarget; anchors: Anchor[] } | null {
  const tile = viewTile(reviewOf(document), tileId);
  if (!tile) return null;
  const type = tileType(tile.type);
  const source = (tile.data as { source?: unknown } | null)?.source;
  const anchors =
    type?.commentAnchors && type.valid(tile.data, tile)
      ? type.commentAnchors(tile)
      : Array.isArray(source)
        ? (source as Anchor[])
        : tile.cites;
  return { target: { kind: "tile", id: tile.id, title: tile.title }, anchors };
}
