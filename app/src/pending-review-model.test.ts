import { expect, it } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import {
  afterSent,
  commentTarget,
  KEEP_SENT,
  needsSummary,
  pendingReviewKey,
  pendingSubmission,
  reviewEvents,
  sentRecord,
  reviewState,
  standingVerdict,
  type PendingComment,
  type PendingReview,
} from "./pending-review-model";
import type { ReviewEvent } from "./walkthrough-model";

const doc = example as unknown as ReviewDocument;
const pr = {
  repository: doc.review.repository,
  number: doc.review.pull_request,
  head_sha: doc.review.head,
};
const comment = (over: Partial<PendingComment>): PendingComment => ({
  id: "c1",
  target: { kind: "section", id: "sec_retry", title: "Retry policy and backoff" },
  body: "Why a fixed jitter?",
  place: { path: "worker/retry.go", line: 42, side: "RIGHT", start_line: 31 },
  head: doc.review.head,
  created: 1,
  ...over,
});

it("keys a pending review by pull request, not by review version", () => {
  expect(pendingReviewKey({ repository: "Acme/Demo", number: 7 })).toBe("acme/demo#7");
});

it("comments on the section, risk or file being read, on the code it cites", () => {
  const section = commentTarget(doc, { kind: "section", id: "sec_retry" });
  expect(section.target).toEqual({
    kind: "section",
    id: "sec_retry",
    title: "Retry policy and backoff",
  });
  expect(section.place?.path).toBeTruthy();
  expect(section.head).toBe(doc.review.head);

  const risk = commentTarget(doc, { kind: "risk", section: "sec_retry", id: "rsk_retry_herd" });
  expect(risk.target.kind).toBe("risk");
  expect(risk.place).toMatchObject({ line: 42, start_line: 31, side: "RIGHT" });

  const file = commentTarget(doc, { kind: "file", id: "api/auth.go" });
  expect(file.target).toEqual({ kind: "file", id: "api/auth.go", title: "api/auth.go" });
  expect(file.place?.path).toBe("api/auth.go");

  expect(commentTarget(doc, { kind: "summary" }).target.kind).toBe("general");
  expect(commentTarget(null, { kind: "section", id: "sec_retry" })).toEqual({
    target: { kind: "general", id: "", title: "The pull request" },
    place: null,
    head: "",
  });
});

it("puts comments on their line only on the current head, the rest in the body", () => {
  const review: PendingReview = {
    note: "Mostly good.",
    comments: [
      comment({}),
      comment({ id: "c2", head: "0000000", body: "On an older head." }),
      comment({
        id: "c3",
        target: { kind: "general", id: "", title: "The pull request" },
        place: null,
        head: "",
        body: "Overall question.",
      }),
    ],
  };
  const submission = pendingSubmission(pr, review, "COMMENT");
  expect(submission.head_sha).toBe(doc.review.head);
  expect(submission.comments).toEqual([
    {
      path: "worker/retry.go",
      line: 42,
      side: "RIGHT",
      start_line: 31,
      body: "Why a fixed jitter?",
    },
  ]);
  expect(submission.body).toBe(
    "Mostly good.\n\n**Retry policy and backoff** (`retry.go:42`)\nOn an older head.\n\nOverall question.",
  );
  // Without inline comments, every comment goes in the body.
  expect(pendingSubmission(pr, review, "COMMENT", false).comments).toEqual([]);
});

it("offers every verdict but approval and changes on the reader's own pull request", () => {
  const empty: PendingReview = { comments: [], note: "" };
  expect(reviewEvents({ authored_by_me: false }, empty).map((e) => e.ok)).toEqual([
    true,
    true,
    true,
  ]);
  expect(reviewEvents({ authored_by_me: true }, empty).map((e) => e.ok)).toEqual([
    false,
    true,
    false,
  ]);
});

it("asks for a summary for a comment or request for changes with nothing said", () => {
  const empty: PendingReview = { comments: [], note: "" };
  expect(needsSummary("APPROVE", empty)).toBe(false);
  expect(needsSummary("COMMENT", empty)).toBe(true);
  expect(needsSummary("REQUEST_CHANGES", { ...empty, note: "Please split this." })).toBe(false);
  expect(needsSummary("COMMENT", { ...empty, comments: [comment({})] })).toBe(false);
});

it("moves what was sent to the sent reviews and keeps what changed meanwhile", () => {
  const sent: PendingReview = { note: "LGTM", comments: [comment({}), comment({ id: "c2" })] };
  const current: PendingReview = {
    note: "LGTM",
    comments: [
      comment({}),
      comment({ id: "c2", body: "Edited while sending." }),
      comment({ id: "c3" }),
    ],
  };
  const record = sentRecord(
    sent,
    "APPROVE",
    "https://github.com/acme/demo/pull/7#r1",
    5,
    pr.head_sha,
  );
  expect(record.head).toBe(pr.head_sha);
  expect(record.comments[0]).toEqual({
    title: "Retry policy and backoff",
    where: "retry.go:42",
    body: "Why a fixed jitter?",
  });
  const next = afterSent(current, sent, record);
  expect(next.comments.map((c) => c.id)).toEqual(["c2", "c3"]);
  expect(next.note).toBe("");
  expect(next.sent).toEqual([record]);

  const many = Array.from({ length: KEEP_SENT }, (_, at) => ({ ...record, at }));
  expect(afterSent({ ...current, sent: many }, sent, record).sent).toHaveLength(KEEP_SENT);
});

it("finds the verdict that stands: the latest approval or request for changes", () => {
  const record = (event: ReviewEvent, at: number, head?: string) => ({
    url: `https://github.com/acme/demo/pull/7#r${at}`,
    event,
    at,
    head,
    note: "",
    comments: [],
  });
  const review = (...sent: ReturnType<typeof record>[]): PendingReview => ({
    comments: [],
    note: "",
    sent,
  });
  expect(standingVerdict(pr, { comments: [], note: "" })).toBeNull();
  expect(standingVerdict(pr, review(record("COMMENT", 1, pr.head_sha)))).toBeNull();
  // A comment after an approval leaves the approval standing, as on GitHub.
  expect(
    standingVerdict(
      pr,
      review(record("APPROVE", 1, pr.head_sha), record("COMMENT", 2, pr.head_sha)),
    ),
  ).toMatchObject({ event: "APPROVE", at: 1, current: true });
  expect(
    standingVerdict(
      pr,
      review(record("APPROVE", 1, pr.head_sha), record("REQUEST_CHANGES", 2, pr.head_sha)),
    ),
  ).toMatchObject({ event: "REQUEST_CHANGES", current: true });
  // Commits since: the approval was on an earlier head.
  expect(standingVerdict(pr, review(record("APPROVE", 1, "0ld")))).toMatchObject({
    current: false,
  });
  // A record from before heads were kept counts as current.
  expect(standingVerdict(pr, review(record("APPROVE", 1)))).toMatchObject({ current: true });
});

it("says what the button shows of the reader's own review", () => {
  const record = (event: ReviewEvent, at: number, head = pr.head_sha) => ({
    url: `https://github.com/acme/demo/pull/7#r${at}`,
    event,
    at,
    head,
    note: "",
    comments: [],
  });
  const kind = (...sent: ReturnType<typeof record>[]) =>
    reviewState(pr, { comments: [], note: "", sent })?.kind ?? null;
  expect(kind()).toBeNull();
  expect(kind(record("COMMENT", 1))).toBe("commented");
  // A comment on an earlier head asks for a review again.
  expect(kind(record("COMMENT", 1, "0ld"))).toBeNull();
  expect(kind(record("APPROVE", 1))).toBe("approved");
  expect(kind(record("APPROVE", 1), record("COMMENT", 2))).toBe("approved");
  // A dismissible approval on an earlier head gives way to a later comment.
  expect(kind(record("APPROVE", 1, "0ld"))).toBeNull();
  expect(kind(record("APPROVE", 1, "0ld"), record("COMMENT", 2))).toBe("commented");
  // Changes requested stands through new commits and later comments.
  expect(kind(record("REQUEST_CHANGES", 1, "0ld"), record("COMMENT", 2))).toBe("changes_requested");
  expect(kind(record("REQUEST_CHANGES", 1), record("APPROVE", 2))).toBe("approved");
});
