import { openExternal, type PR } from "./api";
import type { Toast } from "./toast";
import type { ReviewEvent } from "./walkthrough-model";

/** What the toast says once GitHub has a review sent from the app. */
export const REVIEW_SENT_TITLES: Record<ReviewEvent, string> = {
  APPROVE: "Approved on GitHub",
  COMMENT: "Review sent to GitHub",
  REQUEST_CHANGES: "Changes requested on GitHub",
};

/** What a toast says once GitHub took a write to a pull request: what
 *  happened, what it was on under it (the pull request's title, or a
 *  thread's or check's), the repository and number as its context, and Open
 *  on GitHub to see it there (`url`, the pull request itself by default).
 *  Every write the app sends to GitHub says so this way, so the person sees
 *  it landed without opening the pull request. */
export function githubToast(
  pr: Pick<PR, "repository" | "number" | "title" | "url">,
  title: string,
  url: string = pr.url,
): Toast {
  return {
    title,
    detail: pr.title,
    context: `${pr.repository} #${pr.number}`,
    action: { label: "Open on GitHub", run: () => openExternal(url) },
  };
}
