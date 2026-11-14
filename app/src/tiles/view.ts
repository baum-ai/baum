import { useUrlState } from "@baum/viewer/navigation";
import type { Selection } from "@baum/viewer/review-selection";
import { usePreferences, type ReviewPageChoice } from "../preferences";

/** The review page an address shows: `?view=tiles` or `?view=full` wins,
 *  then the reader's setting. */
export function reviewPageOf(search: URLSearchParams, saved: ReviewPageChoice): ReviewPageChoice {
  const asked = search.get("view");
  return asked === "tiles" || asked === "full" ? asked : saved;
}

export function useReviewPage(): ReviewPageChoice {
  const { search } = useUrlState();
  const { preferences } = usePreferences();
  return reviewPageOf(search, preferences.review_page);
}

/** The same address on the other review page. */
export function withView(search: URLSearchParams, view: ReviewPageChoice): URLSearchParams {
  const next = new URLSearchParams(search);
  next.set("view", view);
  return next;
}

/** Where the full review shows what a tile does. */
export function fullReviewPlace(
  tile: { id: string; type: string; data: unknown } | null,
): Selection {
  if (!tile) return { kind: "summary" };
  if (tile.type === "change") return { kind: "section", id: tile.id };
  const change = (tile.data as { change?: unknown } | null)?.change;
  if (tile.type === "everything_else" && typeof change === "string")
    return { kind: "section", id: change };
  if (tile.type === "risks") return { kind: "changes" };
  return { kind: "summary" };
}
