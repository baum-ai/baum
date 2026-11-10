import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { App } from "./app";
import { forgetLastRows } from "./shortcuts";
import { usePullRequestJobs } from "./job-history";
import { GeneratedBy } from "./review";
import type { Entry, Job } from "./api";

// The review viewer needs a browser layout; the notices around it are enough.
vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ notices, placeholder }: ReviewViewerProps) => (
    <div>
      {notices}
      {placeholder}
    </div>
  ),
}));

const job = (id: string, state = "completed", patch: Partial<Job> = {}): Job => ({
  id,
  clone: "clone",
  repository: "acme/demo",
  pr: 1,
  state,
  phase: state === "completed" ? "finished" : state,
  calls: 0,
  tokens: 0,
  repairs: 0,
  actual_cost_usd: null,
  ...patch,
});
// j1 is the oldest; a page lists newest first.
const ids = (from: number, to: number) =>
  Array.from({ length: to - from + 1 }, (_, i) => `j${to - i}`);
const rowIds = (node: HTMLElement) => [...node.querySelectorAll(".local-job-row")].map((r) => r.id);

let root: Root;
let node: HTMLDivElement;
let requests: string[];
let answers: Record<string, unknown>;
let listeners: Map<string, () => void>;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=jobs");
  localStorage.clear();
  requests = [];
  answers = {};
  listeners = new Map();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener(name: string, listener: () => void) {
        listeners.set(name, listener);
      }
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string) => {
      requests.push(path);
      const key = path.replace("/s/test-ns/", "");
      if (key in answers) {
        const answer = answers[key];
        const given = typeof answer === "function" ? await answer() : answer;
        return given instanceof Response ? given : Response.json(given);
      }
      if (key.startsWith("api/index")) return Response.json({ entries: [], problems: [] });
      if (key === "api/repos" || key === "api/repositories" || key === "api/prs")
        return Response.json([]);
      return Response.json({});
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
});

it("lists running jobs from the current jobs and finished ones a page at a time", async () => {
  answers["api/jobs"] = { available: true, jobs: [job("live", "running")] };
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(51, 100).map((id) => job(id)),
    next: "j51",
  };
  answers["api/jobs?limit=50&before=j51"] = {
    available: true,
    jobs: ids(1, 50).map((id) => job(id)),
  };
  await act(async () => root.render(<App />));
  expect(rowIds(node)).toEqual(["job-live", ...ids(51, 100).map((id) => `job-${id}`)]);
  expect(node.textContent).toContain("Running now");
  const more = () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Load older jobs");
  await act(async () => more()!.click());
  expect(requests).toContain("/s/test-ns/api/jobs?limit=50&before=j51");
  expect(rowIds(node)).toEqual(["job-live", ...ids(1, 100).map((id) => `job-${id}`)]);
  expect(more()).toBeUndefined();

  // The jobs changing reads the newest page again and keeps the older pages.
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: [job("j101"), ...ids(52, 100).map((id) => job(id))],
    next: "j52",
  };
  await act(async () => listeners.get("index")!());
  await act(async () => {});
  expect(rowIds(node)).toEqual(["job-live", ...["j101", ...ids(1, 100)].map((id) => `job-${id}`)]);
  expect(more()).toBeUndefined();
});

it("starts the list again when more than a page of jobs came in", async () => {
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(51, 100).map((id) => job(id)),
    next: "j51",
  };
  answers["api/jobs?limit=50&before=j51"] = {
    available: true,
    jobs: ids(1, 50).map((id) => job(id)),
  };
  await act(async () => root.render(<App />));
  await act(async () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Load older jobs")!.click(),
  );
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(151, 200).map((id) => job(id)),
    next: "j151",
  };
  await act(async () => listeners.get("index")!());
  await act(async () => {});
  expect(rowIds(node)).toEqual(ids(151, 200).map((id) => `job-${id}`));
  expect(
    [...node.querySelectorAll("button")].some((b) => b.textContent === "Load older jobs"),
  ).toBe(true);
});

it("opens a job's transcript page from the daemon when the current jobs lack it", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=jobs&job=old");
  answers["api/jobs"] = { available: true, jobs: [] };
  answers["api/jobs/old"] = job("old", "completed", { kind: "fix", pr: 3 });
  await act(async () => root.render(<App />));
  expect(requests).toContain("/s/test-ns/api/jobs/old");
  expect(node.textContent).toContain("acme/demo #3");
  expect(node.textContent).not.toContain("Job not found");
});

it("reads the jobs of a pull request on its page, again when the app reads its jobs", async () => {
  const entry: Entry = {
    pull_request: {
      repository: "acme/demo",
      number: 1,
      title: "Change 1",
      url: "https://github.com/acme/demo/pull/1",
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
    status: "no_review",
    version_count: 0,
    latest: null,
    risk_score: { score: 5, status: "assessed" },
    historical: false,
    stale: false,
    review: "",
    checks: "",
    merge: "",
    unresolved: null,
    unresolved_capped: false,
    review_minutes: null,
  };
  history.replaceState(null, "", "/s/test-ns/?filter=all&repository=acme%2Fdemo&pr=1");
  answers["api/index?history=1"] = { entries: [entry], problems: [] };
  answers["api/jobs?repository=acme%2Fdemo&pr=1"] = {
    available: true,
    jobs: [
      job("f1", "completed", { kind: "fix", created_at: "2026-11-01T10:00:00Z" }),
      job("f2", "failed", { kind: "fix", created_at: "2026-11-02T10:00:00Z" }),
    ],
  };
  await act(async () => root.render(<App />));
  await act(async () => {});
  // The current jobs hold no fix, so the runs are the per-PR read's.
  expect(node.querySelector(".local-fix-runs")!.textContent).toContain("2 fix runs");
  const own = "/s/test-ns/api/jobs?repository=acme%2Fdemo&pr=1";
  expect(requests.filter((path) => path === own)).toHaveLength(1);
  await act(async () => listeners.get("index")!());
  await act(async () => {});
  expect(requests.filter((path) => path === own).length).toBeGreaterThan(1);
  // The global list is the one without parameters.
  expect(requests).toContain("/s/test-ns/api/jobs");
});

it("reads a pull request's jobs and drops them when another opens", async () => {
  history.replaceState(null, "", "/s/test-ns/");
  answers["api/jobs?repository=acme%2Fdemo&pr=1"] = { jobs: [job("a", "completed")] };
  answers["api/jobs?repository=acme%2Fdemo&pr=2"] = { jobs: [job("b", "completed")] };
  const entryOf = (number: number) =>
    ({ pull_request: { repository: "acme/demo", number } }) as unknown as Entry;
  function Probe({ entry, revision }: { entry: Entry; revision: number }) {
    return <p>{usePullRequestJobs(entry, revision).map((j) => j.id)}</p>;
  }
  await act(async () => root.render(<Probe entry={entryOf(1)} revision={1} />));
  expect(node.textContent).toBe("a");
  let answer: (response: Response) => void = () => {};
  vi.mocked(fetch).mockImplementationOnce(
    () => new Promise<Response>((resolve) => (answer = resolve)),
  );
  await act(async () => root.render(<Probe entry={entryOf(2)} revision={1} />));
  expect(node.textContent).toBe("");
  await act(async () => answer(Response.json({ jobs: [job("b")] })));
  expect(node.textContent).toBe("b");
});

it("keeps the transcript page mounted when its job leaves the current jobs", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=jobs&job=a");
  answers["api/jobs"] = { available: true, jobs: [job("a", "completed", { kind: "fix" })] };
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("acme/demo #1");
  // A newer fix of the pull request takes the current view's place of "a",
  // which the daemon is then asked for.
  let answer: (response: Response) => void = () => {};
  answers["api/jobs/a"] = () => new Promise<Response>((resolve) => (answer = resolve));
  answers["api/jobs"] = { available: true, jobs: [job("b", "completed", { kind: "fix" })] };
  await act(async () => listeners.get("index")!());
  await act(async () => {});
  expect(node.textContent).toContain("acme/demo #1");
  expect(node.textContent).not.toContain("Loading job");
  expect(node.textContent).not.toContain("Job not found");
  await act(async () => answer(Response.json(job("a", "failed", { kind: "fix" }))));
  expect(node.textContent).toContain("acme/demo #1");
  expect(node.textContent).not.toContain("Loading job");
});

it("links a review's job to its transcript page, whatever its age", async () => {
  const entry = { latest: { version: 1 } } as unknown as Entry;
  history.replaceState(null, "", "/s/test-ns/");
  await act(async () =>
    root.render(
      <GeneratedBy
        saved={{ version: 1, model: "m", recorded_cost_usd: null }}
        entry={entry}
        version={1}
        job={job("old job")}
      />,
    ),
  );
  expect(node.querySelector("a")!.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=old%20job");
});

it("starts the list again from the newest page when the cursor's job was archived", async () => {
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(51, 100).map((id) => job(id)),
    next: "j51",
  };
  answers["api/jobs?limit=50&before=j51"] = () => new Response("unknown job", { status: 400 });
  await act(async () => root.render(<App />));
  const more = () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Load older jobs");
  const newest = () => requests.filter((p) => p === "/s/test-ns/api/jobs?limit=50").length;
  const before = newest();
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(1, 50).map((id) => job(id)),
    next: "j1",
  };
  await act(async () => more()!.click());
  await act(async () => {});
  expect(newest()).toBeGreaterThan(before);
  expect(rowIds(node)).toEqual(ids(1, 50).map((id) => `job-${id}`));
  expect(node.querySelector("[role=alert]")).toBeNull();
});

it("does not leave older jobs loading when the newest page fails meanwhile", async () => {
  answers["api/jobs?limit=50"] = {
    available: true,
    jobs: ids(51, 100).map((id) => job(id)),
    next: "j51",
  };
  answers["api/jobs?limit=50&before=j51"] = () => new Promise<Response>(() => {});
  await act(async () => root.render(<App />));
  await act(async () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Load older jobs")!.click(),
  );
  expect(node.textContent).toContain("Loading older jobs");
  answers["api/jobs?limit=50"] = () => new Response("boom", { status: 500 });
  await act(async () => listeners.get("index")!());
  await act(async () => {});
  expect(node.textContent).not.toContain("Loading older jobs");
  expect(node.textContent).toContain("boom");
});

it("reads the newest page again when a job it showed running left the current jobs", async () => {
  let reads = 0;
  answers["api/jobs"] = { available: true, jobs: [] };
  answers["api/jobs?limit=50"] = () => ({
    available: true,
    jobs: [job("r", ++reads === 1 ? "running" : "completed")],
  });
  await act(async () => root.render(<App />));
  await act(async () => {});
  expect(reads).toBeGreaterThan(1);
  const row = node.querySelector("#job-r")!;
  expect(row.textContent).toContain("Completed");
  expect(node.textContent).toContain("Finished");
});
