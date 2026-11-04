import type { ReviewDocument } from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import type { PR } from "./api";
import { uiState } from "./ui-state";
import {
  firstSentence,
  placeOf,
  type CommentPlace,
  type ReviewEvent,
  type ReviewSubmission,
} from "./walkthrough-model";

/** What a comment is about: the item being read when it was written. */
export type CommentTarget = {
  kind: "general" | "section" | "risk" | "file";
  /** The section's or risk's id, or the file's path; empty for general. */
  id: string;
  title: string;
};

/** One comment of the reader's own review, kept by the daemon until the
 *  review is sent. `place` is a line of the diff at `head`, the commit the
 *  review that cited it described. */
export type PendingComment = {
  id: string;
  target: CommentTarget;
  body: string;
  place: CommentPlace | null;
  head: string;
  created: number;
};

/** A review the reader sent from baum, as it was sent: what is listed under
 *  Sent, never the pull request's other reviews. */
export type SentReview = {
  url: string;
  event: ReviewEvent;
  /** When GitHub took it, in milliseconds. */
  at: number;
  note: string;
  comments: { title: string; where: string; body: string }[];
};

/** How many sent reviews a pull request's record keeps, the latest. */
export const KEEP_SENT = 20;

/** The reader's review of one pull request: its comments and its summary
 *  until it is sent, and the reviews sent from here. `sent` is absent from a
 *  record saved before it was kept. */
export type PendingReview = { comments: PendingComment[]; note: string; sent?: SentReview[] };

const GENERAL: CommentTarget = { kind: "general", id: "", title: "The pull request" };

export function pendingReviewKey(pr: Pick<PR, "repository" | "number">): string {
  return `${pr.repository.toLowerCase()}#${pr.number}`;
}

export function pendingReviewState(pr: Pick<PR, "repository" | "number">) {
  return uiState<PendingReview>(
    `api/ui-state/review?key=${encodeURIComponent(pendingReviewKey(pr))}`,
    () => ({ comments: [], note: "" }),
  );
}

const basename = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** A place in a few characters, such as `service.go:241`. */
export function placeText(place: CommentPlace | null): string {
  return place ? `${basename(place.path)}:${place.line}` : "";
}

/** What `c` comments on: the section, risk or file being read, placed on the
 *  first line of code it cites; anything else is a general comment. */
export function commentTarget(
  document: ReviewDocument | null,
  selection: Selection,
): { target: CommentTarget; place: CommentPlace | null; head: string } {
  if (!document) return { target: GENERAL, place: null, head: "" };
  const head = document.review.head;
  const hunks = new Map(document.index.hunks.map((hunk) => [hunk.id, hunk]));
  const firstHunk = (ids: string[]) => ids.slice(0, 1).map((id) => ({ type: "hunk" as const, id }));
  if (selection.kind === "section" || selection.kind === "risk") {
    const sectionId = selection.kind === "section" ? selection.id : selection.section;
    const section = document.sections.find((s) => s.id === sectionId);
    if (section) {
      const risk =
        selection.kind === "risk"
          ? (section.risks ?? []).find((r) => r.id === selection.id)
          : undefined;
      if (risk) {
        const title = firstSentence(risk.text.trim()) || section.title;
        const anchors = risk.anchors.filter((a) => a.type === "hunk");
        const place = placeOf(anchors.length ? anchors : firstHunk(section.hunks), hunks);
        return { target: { kind: "risk", id: risk.id, title }, place, head };
      }
      const cited = (section.summary?.anchors ?? []).filter((a) => a.type === "hunk");
      const place = placeOf(cited.length ? cited : firstHunk(section.hunks), hunks);
      return { target: { kind: "section", id: section.id, title: section.title }, place, head };
    }
  }
  if (selection.kind === "file") {
    const ids = document.index.hunks.filter((h) => h.path === selection.id).map((h) => h.id);
    const place = placeOf(firstHunk(ids), hunks);
    return { target: { kind: "file", id: selection.id, title: selection.id }, place, head };
  }
  return { target: GENERAL, place: null, head };
}

/** Which verdicts GitHub takes on this pull request, and why not the others:
 *  on the reader's own, only a comment. */
export function reviewEvents(
  pr: Pick<PR, "authored_by_me">,
  review: PendingReview,
): { id: ReviewEvent; label: string; ok: boolean; hint: string }[] {
  const own = pr.authored_by_me;
  const count = review.comments.length;
  const along = count ? ` with ${count === 1 ? "your comment" : `your ${count} comments`}` : "";
  return [
    {
      id: "APPROVE",
      label: "Approve",
      ok: !own,
      hint: own ? "GitHub doesn’t let you approve your own pull request" : `Approve it${along}`,
    },
    {
      id: "COMMENT",
      label: "Comment",
      ok: true,
      hint: "Send your feedback without a verdict",
    },
    {
      id: "REQUEST_CHANGES",
      label: "Request changes",
      ok: !own,
      hint: own
        ? "GitHub doesn’t let you request changes on your own pull request"
        : "Ask the author to address your feedback",
    },
  ];
}

/** GitHub takes a comment or a request for changes only with something said:
 *  a summary, when there are no comments. */
export function needsSummary(event: ReviewEvent, review: PendingReview): boolean {
  return event !== "APPROVE" && review.comments.length === 0 && review.note.trim() === "";
}

/** The review as GitHub takes it, on the pull request's current head: each
 *  comment on its line when it was placed on that head, and the rest (all of
 *  them, `inline` false) in the body under the summary, each with what and
 *  where it is about. */
export function pendingSubmission(
  pr: Pick<PR, "repository" | "number" | "head_sha">,
  review: PendingReview,
  event: ReviewEvent,
  inline = true,
): ReviewSubmission {
  const comments: ReviewSubmission["comments"] = [];
  const unplaced: string[] = [];
  for (const comment of review.comments) {
    const body = comment.body.trim();
    if (inline && comment.place && comment.head === pr.head_sha) {
      comments.push({ ...comment.place, body });
      continue;
    }
    if (comment.target.kind === "general") {
      unplaced.push(body);
      continue;
    }
    const where = placeText(comment.place);
    unplaced.push(`**${comment.target.title}**${where ? ` (\`${where}\`)` : ""}\n${body}`);
  }
  return {
    repository: pr.repository,
    pr: pr.number,
    head_sha: pr.head_sha,
    event,
    body: [review.note.trim(), ...unplaced].filter(Boolean).join("\n\n"),
    comments,
  };
}

/** The draft once `sent` is on GitHub as `record`: what was sent moves to
 *  the sent reviews, and anything written or changed while it was being
 *  sent stays. */
export function afterSent(
  current: PendingReview,
  sent: PendingReview,
  record: SentReview,
): PendingReview {
  const posted = new Map(sent.comments.map((c) => [c.id, c.body]));
  const next: PendingReview = {
    comments: current.comments.filter((c) => posted.get(c.id) !== c.body),
    note: current.note === sent.note ? "" : current.note,
    sent: [...(current.sent ?? []), record].slice(-KEEP_SENT),
  };
  // The daemon takes a record of at most 384 KiB: the oldest sent reviews
  // give way before the draft could no longer be saved.
  while (next.sent!.length > 1 && JSON.stringify(next).length > 300 << 10)
    next.sent = next.sent!.slice(1);
  return next;
}

/** What a review sent now records of `review`: its summary and comments,
 *  each with its item and place. */
export function sentRecord(
  review: PendingReview,
  event: ReviewEvent,
  url: string,
  at: number,
): SentReview {
  return {
    url,
    event,
    at,
    note: review.note.trim(),
    comments: review.comments.map((c) => ({
      title: c.target.title,
      where: placeText(c.place),
      body: c.body.trim(),
    })),
  };
}

/** Whether the pull request takes a review from here: open, and current. */
export function reviewable(entry: { historical: boolean; pull_request: Pick<PR, "state"> }) {
  return !entry.historical && entry.pull_request.state.toUpperCase() === "OPEN";
}
