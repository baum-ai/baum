import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import { App } from "./app";
import { LocalReview } from "./review";
import { reviewJob, type Entry, type Job } from "./api";
import type { FixState } from "./fix";
import { forgetLastRows } from "./shortcuts";
vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, header, placeholder, notices }: ReviewViewerProps) => (
    <div data-generation={header?.generation?.state} data-back={header?.back?.href}>
      {header?.beside}
      {notices}
      {document ? <p>{document.summary.text}</p> : placeholder}
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
  clone_id: "selected-clone",
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
const job = (state: string): Job => ({
  id: "job",
  clone: "another-clone",
  repository: "ACME/DEMO",
  pr: 7,
  state,
  phase: state === "queued" ? "queued" : "generating",
  calls: 2,
  tokens: 600,
  repairs: 1,
  actual_cost_usd: null,
});
let root: Root, node: HTMLDivElement, documentValue: ReviewDocument | null, cancelFails: boolean;
const changed = vi.fn(async () => {});
const generate = vi.fn();
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  documentValue = null;
  cancelFails = false;
  changed.mockClear();
  generate.mockClear();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      path.includes("/cancel")
        ? new Response(cancelFails ? "failed" : "{}", {
            status: cancelFails ? 500 : 200,
            headers: { "Content-Type": "application/json" },
          })
        : path.startsWith("/s/test-ns/api/document")
          ? documentValue
            ? Response.json(documentValue)
            : new Response("missing", { status: 404 })
          : path.startsWith("/s/test-ns/api/versions")
            ? Response.json([])
            : Response.json({ available: true, sessions: [], commits: [] }),
    ),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
});
// These test the full review's page; the walkthrough has its own tests.
async function render(current: Job, revision = 0, currentEntry = entry) {
  await act(async () =>
    root.render(
      <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
        <LocalReview
          entry={currentEntry}
          clone="selected-clone"
          revision={revision}
          job={current}
          changed={changed}
          generate={generate}
        />
      </UrlStateProvider>,
    ),
  );
}
it("matches active PR jobs across clones while isolating GitHub hosts and PRs", () => {
  const earlier = job("running");
  const terminal = job("completed");
  expect(reviewJob(entry, [earlier, terminal])).toBe(earlier);
  expect(
    reviewJob(entry, [
      { ...earlier, repository: "enterprise.test/acme/demo" },
      { ...earlier, pr: 8 },
    ]),
  ).toBeUndefined();
  expect(reviewJob(entry, [job("failed"), terminal])).toBe(terminal);
});
it("shows queued initial generation and progress instead of an empty review", async () => {
  await render(job("queued"));
  expect(node.textContent).toContain("Review queued");
  expect(node.textContent).not.toContain("No generated review yet");
  expect(node.querySelector('[data-generation="pending"]')).not.toBeNull();
  expect(node.querySelector("[data-back]")!.getAttribute("data-back")).toBe("/s/test-ns/");
  expect(node.textContent).not.toContain("Started automatically");
});
it("shows a review as starting from the click, before the daemon answers", async () => {
  await act(async () =>
    root.render(
      <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
        <LocalReview
          entry={entry}
          clone="selected-clone"
          revision={0}
          job={job("failed")}
          starting
          changed={changed}
          generate={generate}
        />
      </UrlStateProvider>,
    ),
  );
  expect(node.textContent).toContain("Starting the review");
  expect(node.querySelector('[data-generation="pending"]')).not.toBeNull();
  // The failed job before it is not the one starting: nothing to cancel.
  expect(node.textContent).not.toContain("Cancel job");
});
it("says when the PR's job was started automatically", async () => {
  await render({ ...job("running"), automatic: true, trigger: "review_requested" });
  expect(node.textContent).toContain("Started automatically because your review was requested");
});
it("keeps the saved document readable while regenerating and updates after completion", async () => {
  documentValue = structuredClone(example) as unknown as ReviewDocument;
  documentValue.summary.text = "Saved version";
  await render(job("running"), 0, { ...entry, version_count: 1 });
  expect(node.textContent).toContain("Saved version");
  expect(node.textContent).toContain("Generating review");
  documentValue.summary.text = "Completed version";
  await render(job("completed"), 1, { ...entry, version_count: 2, status: "current" });
  expect(node.textContent).toContain("Completed version");
  expect(node.textContent).not.toContain("Generating review");
  for (const [url] of vi.mocked(fetch).mock.calls) expect(url).toMatch(/^\/s\/test-ns\/api\//);
  expect(
    Array.from(node.querySelectorAll("button")).find(
      (button) => button.textContent === "Regenerate review",
    )!.disabled,
  ).toBe(false);
});
it("keeps saved content and offers retry when the completed review cannot be read", async () => {
  documentValue = structuredClone(example) as unknown as ReviewDocument;
  documentValue.summary.text = "Saved version";
  await render(job("running"), 0, { ...entry, version_count: 1 });
  documentValue = null;
  await render(job("completed"), 1, { ...entry, version_count: 2, status: "current" });
  expect(node.textContent).toContain("Saved version");
  expect(node.textContent).toContain("The latest review could not be read");
  documentValue = structuredClone(example) as unknown as ReviewDocument;
  documentValue.summary.text = "Retried version";
  await act(async () => {
    Array.from(node.querySelectorAll("button"))
      .find((button) => button.textContent === "Retry review")!
      .click();
  });
  expect(node.textContent).toContain("Retried version");
  expect(node.textContent).not.toContain("The latest review could not be read");
});
it.each(["cancelled", "failed", "interrupted"])(
  "terminal %s ends pending UI even while the index still says generating",
  async (state) => {
    await render(job("running"));
    await render(
      { ...job(state), reason: state === "failed" ? "provider_unavailable" : undefined },
      1,
      { ...entry, status: "generating" },
    );
    expect(node.textContent).toContain(
      state === "failed" ? "The review couldn't be generated" : `Review generation was ${state}`,
    );
    expect(node.querySelector('[data-generation="pending"]')).toBeNull();
    expect(
      Array.from(node.querySelectorAll("button")).some(
        (button) =>
          button.textContent === (state === "failed" ? "Try again" : "Generate review") &&
          !button.disabled,
      ),
    ).toBe(true);
    if (state === "failed") expect(node.textContent).toContain("provider unavailable");
  },
);
it("shows cancellation errors and keeps the active job available for retry", async () => {
  cancelFails = true;
  await render(job("running"));
  const cancel = Array.from(node.querySelectorAll("button")).find(
    (button) => button.textContent === "Cancel job",
  )!;
  await act(async () => cancel.click());
  expect(node.textContent).toContain("The job could not be cancelled. Try again.");
  expect(cancel.disabled).toBe(false);
  expect(changed).not.toHaveBeenCalled();
  cancelFails = false;
  await act(async () => cancel.click());
  expect(changed).toHaveBeenCalledOnce();
});

it("keeps the PR page through queued, running and completed SSE states across clone stores", async () => {
  let currentJob = job("queued");
  let indexed = { ...entry };
  const events: EventTarget[] = [];
  vi.stubGlobal(
    "EventSource",
    class extends EventTarget {
      constructor() {
        super();
        events.push(this);
      }
      close() {}
    },
  );
  const original = globalThis.fetch;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, ...args: unknown[]) =>
      path === "/s/test-ns/api/repos"
        ? Response.json(
            ["selected-clone", "another-clone"].map((id) => ({
              id,
              repository: "acme/demo",
              available: true,
              path: "/fixture",
              settings: { automatic: false },
            })),
          )
        : path.startsWith("/s/test-ns/api/index")
          ? Response.json({ entries: [indexed], problems: [] })
          : path === "/s/test-ns/api/jobs"
            ? Response.json({ jobs: [currentJob] })
            : path === "/s/test-ns/api/prs"
              ? Response.json([])
              : path === "/s/test-ns/api/health"
                ? Response.json({})
                : original(path, ...(args as [])),
    ),
  );
  history.replaceState(null, "", "/s/test-ns/?clone=selected-clone&pr=7");
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Review queued");
  currentJob = job("running");
  indexed = { ...entry, clone_id: "another-clone", status: "generating" };
  await act(async () => events[0]!.dispatchEvent(new Event("progress")));
  expect(node.textContent).toContain("Generating the review");
  expect(node.textContent).not.toContain("No generated review yet.");
  documentValue = structuredClone(example) as unknown as ReviewDocument;
  documentValue.summary.text = "Published review from the job clone";
  currentJob = job("completed");
  indexed = { ...indexed, status: "current", version_count: 1 };
  await act(async () => events[0]!.dispatchEvent(new Event("index")));
  expect(node.textContent).toContain("Published review from the job clone");
  expect(node.textContent).not.toContain("Generating review");
  expect(
    Array.from(node.querySelectorAll("button")).find(
      (button) => button.textContent === "Regenerate review",
    )!.disabled,
  ).toBe(false);
});
it("offers Merge in the PR page header only for an open, ready pull request", async () => {
  await render(job("completed"));
  expect(node.textContent).not.toContain("Merge…");
  await render(job("completed"), 0, { ...entry, merge: "ready" });
  expect(node.textContent).toContain("Merge…");
  await render(job("completed"), 0, {
    ...entry,
    merge: "ready",
    pull_request: { ...entry.pull_request, draft: true },
  });
  expect(node.textContent).not.toContain("Merge…");
});
it("offers Re-run checks and Update branch in the PR page header when they apply", async () => {
  const withPush = { ...entry.pull_request, viewer_can_push: true };
  await render(job("completed"));
  expect(node.textContent).not.toContain("Re-run checks");
  expect(node.textContent).not.toContain("Update branch");
  await render(job("completed"), 0, { ...entry, checks: "failing", pull_request: withPush });
  expect(node.textContent).toContain("Re-run checks");
  expect(node.textContent).not.toContain("Update branch");
  await render(job("completed"), 0, {
    ...entry,
    merge: "blocked",
    pull_request: { ...withPush, behind: true },
  });
  expect(node.textContent).toContain("Update branch");
  expect(node.textContent).not.toContain("Re-run checks");
  // Without push access neither is offered.
  await render(job("completed"), 0, {
    ...entry,
    checks: "failing",
    pull_request: { ...withPush, behind: true, viewer_can_push: false },
  });
  expect(node.textContent).not.toContain("Re-run checks");
  expect(node.textContent).not.toContain("Update branch");
});
it("leads with the fix of a broken pull request and follows its job", async () => {
  const fix = vi.fn();
  const broken: Entry = {
    ...entry,
    merge: "conflict",
    pull_request: { ...entry.pull_request, viewer_can_push: true },
  };
  const fixJob = (state: string, patch: Partial<Job> = {}): Job => ({
    ...job(state),
    kind: "fix",
    fix: { conflicts: true, head_ref: "feature" },
    head_sha: "head",
    phase: "merging",
    ...patch,
  });
  const show = (current?: Job, fixState?: FixState) =>
    act(async () =>
      root.render(
        <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
          <LocalReview
            entry={broken}
            clone="selected-clone"
            revision={0}
            changed={changed}
            generate={generate}
            fix={fix}
            fixState={fixState}
            fixJob={current}
          />
        </UrlStateProvider>,
      ),
    );
  await show();
  const button = Array.from(node.querySelectorAll("button")).find(
    (b) => b.textContent === "Fix conflicts",
  )!;
  await act(async () => button.click());
  expect(fix).toHaveBeenCalled();
  // The fix starts at once: no dialog, and the button answers the click.
  expect(node.querySelector("dialog")).toBeNull();
  await show(undefined, { starting: true, error: "" });
  expect(
    Array.from(node.querySelectorAll("button")).find((b) => b.textContent === "Starting…")!
      .disabled,
  ).toBe(true);
  // A fix the daemon refused says why on the page.
  await show(undefined, {
    starting: false,
    error: "Your GitHub account can't push to this repository.",
  });
  expect(node.textContent).toContain(
    "Couldn't start the fix: Your GitHub account can't push to this repository.",
  );
  await show(fixJob("running"));
  expect(node.textContent).toContain("Fix conflicts: Merging the base branch");
  expect(
    Array.from(node.querySelectorAll("button")).find((b) => b.textContent === "Fixing…")!.disabled,
  ).toBe(true);
  await show(
    fixJob("completed", {
      commit: "abcdef1234567890",
      commit_url: "https://github.com/acme/demo/commit/abcdef1234567890",
    }),
  );
  expect(node.textContent).toContain("Fix conflicts: pushed abcdef1 to feature");
  await show(fixJob("failed", { reason: "push_rejected" }));
  expect(node.textContent).toContain("GitHub refused the push");
});
it("keeps a pull request's fix runs one fold away and opens the latest transcript with t", async () => {
  const fixRun = (id: string, state: string, patch: Partial<Job> = {}): Job => ({
    ...job(state),
    id,
    kind: "fix",
    fix: { checks: true },
    has_transcript: true,
    created_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
    ...patch,
  });
  const pushed = fixRun("fix-2", "completed", { head_sha: "old", commit: "abcdef1234567890" });
  const failed = fixRun("fix-1", "failed", { head_sha: "older", reason: "push_rejected" });
  const show = (fixJob: Job | undefined, fixRuns: Job[]) =>
    act(async () =>
      root.render(
        <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
          <LocalReview
            entry={entry}
            clone="selected-clone"
            revision={0}
            changed={changed}
            generate={generate}
            fixJob={fixJob}
            fixRuns={fixRuns}
          />
        </UrlStateProvider>,
      ),
    );
  const press = (key: string) =>
    act(async () => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  // No fix yet: nothing to show and t does nothing.
  await show(undefined, []);
  expect(node.querySelector(".local-fix-runs")).toBeNull();
  await press("t");
  expect(location.search).toBe("");
  // The head moved on since: the notice is gone but the runs stay, folded.
  await show(pushed, [pushed, failed]);
  expect(node.querySelector('[aria-label="Fix"]')).toBeNull();
  const runs = node.querySelector<HTMLDetailsElement>(".local-fix-runs")!;
  expect(runs.open).toBe(false);
  expect(runs.querySelector("summary")!.textContent).toBe("2 fix runs, the last 2 hours agot");
  const lines = Array.from(runs.querySelectorAll("li")).map((li) => li.textContent);
  expect(lines).toEqual([
    "2 hours agoFix CI: pushed abcdef1Transcript",
    "2 hours agoFix CI: failed: GitHub refused the push, usually because the branch moved on. Nothing was force-pushed.Transcript",
  ]);
  await press("t");
  expect(location.search).toBe("?page=jobs&job=fix-2");
  // A running fix shows in its notice, Watch with its key; the rest are earlier runs.
  history.replaceState(null, "", "/s/test-ns/");
  const running = fixRun("fix-3", "running", { head_sha: "head", phase: "fixing" });
  await show(running, [running, pushed, failed]);
  const watch = Array.from(node.querySelectorAll('[aria-label="Fix"] a')).find((a) =>
    a.textContent?.startsWith("Watch"),
  )!;
  expect(watch.querySelector(".key-hint")!.textContent).toBe("t");
  expect(node.querySelector(".local-fix-runs summary")!.textContent).toBe("2 earlier fix runs");
  await press("t");
  expect(location.search).toBe("?page=jobs&job=fix-3");
});
