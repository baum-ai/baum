import { mockStateFetch, seedUIState } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Entry } from "./api";
import { ReviewButton, ReviewDialog } from "./pending-review";
import type { PendingReview } from "./pending-review-model";
import { ToastProvider } from "./toast";

const entry: Entry = {
  pull_request: {
    repository: "acme/demo",
    number: 7,
    title: "Retry failed deliveries",
    url: "https://github.com/acme/demo/pull/7",
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: false,
    review_requested_from_me: true,
    author_has_write: true,
    team_membership_known: true,
    review: "",
    checks: "",
    merge: "",
    unresolved: null,
    unresolved_capped: false,
  },
  clone_id: "clone",
  status: "current",
  version_count: 1,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "",
  checks: "",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
};
const STATE = "api/ui-state/review?key=acme%2Fdemo%237";
const REVIEW_URL = "https://github.com/acme/demo/pull/7#pullrequestreview-1";
const approval = (head: string) => ({
  url: REVIEW_URL,
  event: "APPROVE" as const,
  at: Date.now() - 5 * 60_000,
  head,
  note: "",
  comments: [],
});

let root: Root;
let node: HTMLDivElement;
let posts: unknown[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  posts = [];
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (_path: string, init?: RequestInit) => {
      posts.push(JSON.parse(String(init?.body)));
      return Response.json({ url: REVIEW_URL });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
const button = (text: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent?.includes(text));
const seed = (review: PendingReview) => seedUIState(STATE, review);

it("reads Approve before any review was sent from here", async () => {
  await act(async () => root.render(<ReviewButton entry={entry} count={0} review={() => {}} />));
  expect(node.querySelector("[data-review-button]")!.textContent).toContain("Approve");
  expect(node.querySelector("[data-review-state]")).toBeNull();
});

it("reads Approved once the reader approved the current head from here", async () => {
  seed({ comments: [], note: "", sent: [approval("head")] });
  await act(async () => root.render(<ReviewButton entry={entry} count={0} review={() => {}} />));
  const approved = node.querySelector<HTMLButtonElement>("[data-review-button]")!;
  expect(approved.dataset.reviewState).toBe("approved");
  expect(approved.textContent).toContain("Approved");
  expect(approved.title).toMatch(/^You approved this/);
});

it("reads Approve again once commits came after the approval", async () => {
  seed({ comments: [], note: "", sent: [approval("older")] });
  await act(async () => root.render(<ReviewButton entry={entry} count={0} review={() => {}} />));
  const again = node.querySelector<HTMLButtonElement>("[data-review-button]")!;
  expect(again.dataset.reviewState).toBeUndefined();
  expect(again.textContent).not.toContain("Approved");
});

it("says in the dialog that the reader already approved, with the link", async () => {
  seed({ comments: [], note: "", sent: [approval("older")] });
  await act(async () =>
    root.render(<ReviewDialog entry={entry} event="APPROVE" close={() => {}} sent={() => {}} />),
  );
  const standing = node.querySelector(".review-standing")!;
  expect(standing.textContent).toContain("You approved this");
  expect(standing.textContent).toContain("before its latest commits");
  expect(standing.querySelector("a")!.getAttribute("href")).toBe(REVIEW_URL);
});

it("approves, records the head and says so in a toast that opens the review", async () => {
  const sent = vi.fn();
  const close = vi.fn();
  await act(async () =>
    root.render(
      <ToastProvider>
        <ReviewDialog entry={entry} event="APPROVE" close={close} sent={sent} />
      </ToastProvider>,
    ),
  );
  expect(node.querySelector(".review-standing")).toBeNull();
  await act(async () => button("Approve")!.click());
  expect(posts).toContainEqual(expect.objectContaining({ event: "APPROVE", head_sha: "head" }));
  expect(sent).toHaveBeenCalledWith(REVIEW_URL);
  expect(close).toHaveBeenCalledOnce();
  const toast = node.querySelector(".local-toast")!;
  expect(toast.textContent).toContain("Approved on GitHub");
  expect(toast.textContent).toContain("Retry failed deliveries");
  expect(toast.textContent).toContain("acme/demo #7");
  const opened: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    opened.push(this.href);
  });
  await act(async () => button("Open on GitHub")!.click());
  expect(opened).toEqual([REVIEW_URL]);
  // The dialog stays mounted here (close is a mock) and now sees the approval.
  expect(node.querySelector(".review-standing")!.textContent).toContain("You approved this");
});

it("reads Changes requested, which new commits leave standing, and says so in the dialog", async () => {
  seed({
    comments: [],
    note: "",
    sent: [{ ...approval("older"), event: "REQUEST_CHANGES" }],
  });
  await act(async () => root.render(<ReviewButton entry={entry} count={0} review={() => {}} />));
  const requested = node.querySelector<HTMLButtonElement>("[data-review-button]")!;
  expect(requested.dataset.reviewState).toBe("changes_requested");
  expect(requested.textContent).toContain("Changes requested");
  await act(async () =>
    root.render(<ReviewDialog entry={entry} event="APPROVE" close={() => {}} sent={() => {}} />),
  );
  expect(node.querySelector(".review-standing-changes_requested")!.textContent).toContain(
    "You requested changes",
  );
});

it("reads Commented after a comment on the current head, and says so in the dialog", async () => {
  seed({ comments: [], note: "", sent: [{ ...approval("head"), event: "COMMENT" }] });
  await act(async () => root.render(<ReviewButton entry={entry} count={0} review={() => {}} />));
  const commented = node.querySelector<HTMLButtonElement>("[data-review-button]")!;
  expect(commented.dataset.reviewState).toBe("commented");
  expect(commented.textContent).toContain("Commented");
  await act(async () =>
    root.render(<ReviewDialog entry={entry} event="COMMENT" close={() => {}} sent={() => {}} />),
  );
  expect(node.querySelector(".review-standing-commented")!.textContent).toContain("You commented");
});
