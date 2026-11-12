import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import { sectionDetails, type ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import { LocalReview } from "./review";
import { detailsJob as detailsJobOf, type Entry, type Job } from "./api";
import { forgetLastRows } from "./shortcuts";
// The viewer is drawn thinly here: what the page passes it is what is tested.
vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({
    document,
    header,
    notices,
    placeholder,
    versions,
    history,
    renderSectionAside,
    onRetryDetails,
    contextPending,
  }: ReviewViewerProps) => (
    <div
      data-versions={versions?.length ?? 0}
      data-history={history ? "yes" : "no"}
      data-generation={header?.generation?.state}
    >
      {header?.beside}
      {notices}
      {document ? (
        <>
          <p>{document.summary.text}</p>
          {contextPending ? <p>Writing context…</p> : null}
          {document.sections.map((section) => {
            const state = sectionDetails(section);
            return (
              <div key={section.id} data-section={section.id}>
                {state === "pending" ? "Writing details…" : null}
                {state === "failed" ? "Details unavailable" : null}
                {state === "failed" && onRetryDetails ? (
                  <button type="button" onClick={() => void onRetryDetails(section.id)}>
                    Retry
                  </button>
                ) : null}
                {renderSectionAside?.(section.id)}
              </div>
            );
          })}
        </>
      ) : (
        placeholder
      )}
    </div>
  ),
}));
const entry: Entry = {
  pull_request: {
    repository: "acme/demo",
    number: 7,
    head_sha: "head",
    title: "Example",
    url: "https://github.com/acme/demo/pull/7",
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
  status: "no_review",
  version_count: 0,
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
const saved = { ...entry, version_count: 1, status: "current" } as Entry;
const job = (state: string, extra: Partial<Job> = {}): Job => ({
  id: "job",
  clone: "clone",
  repository: "acme/demo",
  pr: 7,
  state,
  phase: "generating",
  calls: 1,
  tokens: 10,
  repairs: 0,
  actual_cost_usd: null,
  ...extra,
});
const clone = () => structuredClone(example) as unknown as ReviewDocument;
// A document as the daemon holds it mid-review: the first section's details
// are still being written, and the context is not there yet.
function draftDocument(): ReviewDocument {
  const doc = clone();
  doc.summary.text = "Draft text";
  doc.review.version = 2;
  delete doc.summary.context;
  doc.sections[0]!.details = "pending";
  return doc;
}
// A published version missing the first section's details and the context.
function gappedDocument(): ReviewDocument {
  const doc = clone();
  doc.summary.text = "Saved text";
  delete doc.summary.context;
  doc.sections[0]!.details = "failed";
  return doc;
}
let root: Root, node: HTMLDivElement;
let savedDocument: ReviewDocument | null, draftValue: ReviewDocument | null;
let posts: unknown[], postStatus: number, postBody: unknown;
const changed = vi.fn(async () => {});
const generate = vi.fn();
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  savedDocument = null;
  draftValue = null;
  posts = [];
  postStatus = 200;
  postBody = {};
  changed.mockClear();
  generate.mockClear();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      if (init?.method === "POST" && path === "/s/test-ns/api/jobs") {
        posts.push(JSON.parse(String(init.body)));
        return Response.json(postBody, { status: postStatus });
      }
      if (path.endsWith("/draft"))
        return draftValue
          ? Response.json({ draft: 1, document: draftValue })
          : new Response("none", { status: 404 });
      if (path.startsWith("/s/test-ns/api/document"))
        return savedDocument
          ? Response.json(savedDocument)
          : new Response("missing", { status: 404 });
      if (path.startsWith("/s/test-ns/api/versions")) return Response.json([]);
      if (path.startsWith("/s/test-ns/api/facts")) return new Response("none", { status: 404 });
      return Response.json({ available: true, sessions: [], commits: [] });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
});
async function render(current: Entry, review?: Job, details?: Job, revision = 0) {
  await act(async () =>
    root.render(
      <UrlStateProvider value={{ search: new URLSearchParams("walk=full"), update: () => {} }}>
        <LocalReview
          entry={current}
          clone="clone"
          revision={revision}
          job={review}
          detailsJob={details}
          changed={changed}
          generate={generate}
        />
      </UrlStateProvider>,
    ),
  );
}
const button = (label: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent === label);
it("shows the draft of a pull request without a version once the job has one", async () => {
  await render(entry, job("running"));
  expect(node.textContent).toContain("Generating the review");
  draftValue = draftDocument();
  await render(entry, job("running", { draft: 1 }));
  expect(node.textContent).toContain("Draft text");
  expect(node.textContent).toContain("Writing details…");
  expect(node.textContent).toContain("Writing context…");
  expect(node.textContent).toContain("Draft — the review is still being written.");
  // Nothing of a version: no history and no retry. (Ask is about the pull
  // request, in the session panel, not about a version.)
  expect(node.querySelector("[data-history=no]")).not.toBeNull();
  expect(button("Retry")).toBeUndefined();
  // When the job ends the draft goes with it.
  await render(entry, job("completed"), undefined, 1);
  expect(node.textContent).not.toContain("Draft text");
});
it("keeps the saved version while regenerating, and offers no view of the draft", async () => {
  savedDocument = clone();
  savedDocument.summary.text = "Saved text";
  draftValue = draftDocument();
  await render(saved, job("running", { draft: 1 }));
  expect(node.textContent).toContain("Saved text");
  expect(node.querySelector('[data-generation="pending"]')).not.toBeNull();
  expect(node.textContent).not.toContain("Draft text");
  expect(button("View draft")).toBeUndefined();
  await render(saved, job("completed"), undefined, 1);
  expect(node.textContent).toContain("Saved text");
});
it("says what a published version is missing and retries its details", async () => {
  savedDocument = gappedDocument();
  await render(saved, job("completed", { context_failed: true }));
  expect(node.textContent).toContain("1 section couldn't be written.");
  expect(node.textContent).toContain("The codebase context couldn't be written.");
  expect(node.textContent).toContain("Details unavailable");
  await act(async () => button("Retry")!.click());
  expect(posts).toEqual([
    {
      items: [{ clone: "clone", pr: 7, kind: "details", version: savedDocument.review.version }],
      mode: "single",
    },
  ]);
  expect(changed).toHaveBeenCalled();
  // While the details job runs the section reads as being written.
  const running = job("running", { id: "details", kind: "details", version: 3 });
  await render(saved, job("completed"), running);
  expect(node.textContent).toContain("Writing details…");
  expect(node.textContent).not.toContain("Details unavailable");
  expect(button("Retry")).toBeUndefined();
  // A job that ends cancelled is said, then dismissed.
  await render(saved, job("completed"), {
    ...running,
    state: "cancelled",
    reason: "version_changed",
  });
  expect(node.textContent).toContain("A newer review replaced this version");
  await act(async () => button("Dismiss")!.click());
  expect(node.textContent).not.toContain("A newer review replaced this version");
});
it("offers to regenerate when the head moved under a retry", async () => {
  savedDocument = gappedDocument();
  postStatus = 409;
  postBody = { blocked_reason: "head_changed" };
  await render(saved, job("completed"));
  await act(async () => button("Retry")!.click());
  expect(node.textContent).toContain(
    "This pull request changed since this review (new commits on it or on its base), so its details can't be retried. Regenerate the review instead.",
  );
  await act(async () => button("Regenerate")!.click());
  expect(generate).toHaveBeenCalledOnce();
});
it("does not offer a retry or a gap notice without gaps", async () => {
  savedDocument = clone();
  await render(saved, job("completed"));
  expect(node.textContent).not.toContain("couldn't be written");
  expect(button("Retry")).toBeUndefined();
});
it("keeps a details job apart from the pull request's review job", () => {
  const review = job("completed");
  const details = job("running", { id: "details", kind: "details" });
  expect(detailsJobOf(entry, [review, details])).toBe(details);
});
