import { useUrlState } from "@baum/viewer/navigation";
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

/** Whether a pull request's review page reads its view to open an arranged
 *  version (one whose view has more than the review alone) on its tabs: tile
 *  reviews are on and the address asks for neither page. A version that was
 *  not arranged still opens on the full review. */
export function useArrangedTiles(): boolean {
  const { search } = useUrlState();
  const { preferences } = usePreferences();
  const asked = search.get("view");
  return preferences.arranged_tiles && asked !== "tiles" && asked !== "full";
}
