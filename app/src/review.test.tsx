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
vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, header, placeholder, notices }: ReviewViewerProps) => (
    <div data-generation={header?.generation?.state}>
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
  },
  clone_id: "selected-clone",
  status: "no_review",
  version_count: 0,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
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
        : path.startsWith("/api/document")
          ? documentValue
            ? Response.json(documentValue)
            : new Response("missing", { status: 404 })
          : path.startsWith("/api/versions")
            ? Response.json([])
            : Response.json({ available: true, sessions: [], commits: [] }),
    ),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
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
  expect(node.textContent).toContain("Review generation is queued");
  expect(node.textContent).toContain("Calls 2 · Tokens 600 · Repairs 1");
  expect(node.textContent).not.toContain("No generated review yet");
  expect(
    Array.from(node.querySelectorAll("button")).find(
      (button) => button.textContent === "Review queued",
    )!.disabled,
  ).toBe(true);
  expect(node.querySelector('[data-generation="pending"]')).not.toBeNull();
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
    expect(node.textContent).toContain(`Review generation ${state}`);
    expect(node.querySelector('[data-generation="pending"]')).toBeNull();
    expect(
      Array.from(node.querySelectorAll("button")).some(
        (button) => button.textContent === "Generate review" && !button.disabled,
      ),
    ).toBe(true);
    if (state === "failed") expect(node.textContent).toContain("provider_unavailable");
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
      path === "/api/repos"
        ? Response.json(
            ["selected-clone", "another-clone"].map((id) => ({
              id,
              repository: "acme/demo",
              available: true,
              path: "/fixture",
              settings: { automatic: false },
            })),
          )
        : path.startsWith("/api/index")
          ? Response.json({ entries: [indexed], problems: [] })
          : path === "/api/jobs"
            ? Response.json({ jobs: [currentJob] })
            : path === "/api/prs"
              ? Response.json([])
              : path === "/api/health"
                ? Response.json({})
                : original(path, ...(args as [])),
    ),
  );
  history.replaceState(null, "", "/?clone=selected-clone&pr=7");
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Review generation is queued");
  currentJob = job("running");
  indexed = { ...entry, clone_id: "another-clone", status: "generating" };
  await act(async () => events[0]!.dispatchEvent(new Event("progress")));
  expect(node.textContent).toContain("Generating review");
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
