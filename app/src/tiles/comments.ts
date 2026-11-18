import type { ReviewDocument } from "@baum/viewer/review-document";
import type { CommentTarget } from "../pending-review-model";
import { itemTarget } from "./place";
import { tileType } from "./registry";
import {
  reviewIdOf,
  tileById,
  tileDocOf,
  tileOfChange,
  type DecisionsData,
  type TileDocument,
} from "./tile-document";
import type { Anchor } from "@baum/viewer/review-document";

/** What a comment on a tile other than a Change (which `commentTarget` takes
 *  as its section), or on an item of it, is about, and the code it is placed
 *  on (`anchors`, placed by the caller; none puts it in the review's body). A
 *  risk keeps the risk target the full review and older daemons know. */
export function tileCommentTarget(
  document: ReviewDocument,
  tileId: string,
  item: string | undefined,
  firstSentence: (text: string) => string,
): { target: CommentTarget; anchors: Anchor[] } | null {
  const doc = tileDocOf(document);
  const tile = doc ? tileById(doc, tileId) : undefined;
  if (!doc || !tile) return null;
  const type = tileType(tile.type);
  const anchors =
    type?.commentAnchors && type.valid(tile.data)
      ? type.commentAnchors({ doc, review: document }, tile, item)
      : [];
  const risk = item ? doc.risks.find((each) => each.id === item) : undefined;
  if (tile.type === "risks" && risk)
    return {
      target: {
        kind: "risk",
        id: reviewIdOf(risk),
        title: firstSentence(risk.text.trim()) || tile.title,
      },
      anchors,
    };
  const named = item ? itemTitle(doc, tile.type, item, firstSentence) : "";
  return {
    target: { kind: "tile", id: tile.id, title: named ? `${tile.title}: ${named}` : tile.title },
    anchors,
  };
}

function itemTitle(
  doc: TileDocument,
  type: string,
  item: string,
  firstSentence: (text: string) => string,
): string {
  if (type === "evidence") {
    const evidence = doc.evidence.find((each) => each.id === item);
    return evidence ? firstSentence(evidence.claim.trim()) : "";
  }
  if (type === "decisions") {
    const tile = tileById(doc, "decisions");
    const decision = (tile?.data as DecisionsData | undefined)?.items?.find(
      (each) => each.id === item,
    );
    return decision ? firstSentence((decision.title ?? decision.text).trim()) : "";
  }
  return "";
}

/** The reader's pending comments counted on the tiles they are about: a tile
 *  comment on its tile, a section comment on its change's tile, a risk
 *  comment where the risk is read. */
export function commentCounts(
  document: ReviewDocument | null,
  comments: { target: CommentTarget }[],
): Map<string, number> {
  const counts = new Map<string, number>();
  const doc = tileDocOf(document);
  if (!doc) return counts;
  const add = (id: string | undefined) => {
    if (id) counts.set(id, (counts.get(id) ?? 0) + 1);
  };
  for (const { target } of comments) {
    if (target.kind === "tile") add(tileById(doc, target.id)?.id);
    else if (target.kind === "section") add(tileOfChange(doc, target.id)?.id);
    else if (target.kind === "risk") {
      const risk = doc.risks.find((each) => reviewIdOf(each) === target.id);
      add(risk ? itemTarget(doc, risk.change, target.id)?.tile.id : undefined);
    }
  }
  return counts;
}
