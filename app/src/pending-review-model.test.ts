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
  type PendingComment,
  type PendingReview,
} from "./pending-review-model";

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
  const record = sentRecord(sent, "APPROVE", "https://github.com/acme/demo/pull/7#r1", 5);
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
