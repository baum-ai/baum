import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewFacts } from "@baum/viewer/review-document";
import { LocalReview } from "./review";
import type { Entry } from "./api";
import { forgetLastRows } from "./shortcuts";

const entry: Entry = {
  pull_request: {
    repository: "acme/demo",
    number: 7,
    head_sha: "1111111",
    title: "Parse the config",
    url: "https://github.com/acme/demo/pull/7",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: false,
    review_requested_from_me: true,
    author_has_write: true,
    team_membership_known: true,
    review: "",
    checks: "failing",
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
  checks: "failing",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
};

const facts = (head: string): ReviewFacts => ({
  schema: "baum.facts/1",
  review: {
    repository: "acme/demo",
    pull_request: 7,
    pr_state: "open",
    head,
    base: "2222222",
    merge_base: "2222222",
    merged_commit: null,
    evidence_refreshed_at: new Date().toISOString(),
    sources: { diff: true, sessions: 1, checks: 1, review_threads: true },
    truncated: [],
  },
  summary: {
    title: "Parse the config",
    author: { login: "octocat", association: "MEMBER", bot: false },
    authorship: { observed_sessions: 1, observed_vendors: ["claude"] },
    review_exchange: {
      reviews: 0,
      back_and_forths: 0,
      critical_issues_found: 0,
      changes_after_open: null,
    },
  },
  stats: {
    files: 2,
    hunks: 2,
    additions: 21,
    deletions: 1,
    role_loc: { code: 8, test: 14 },
    checks: { required_failed: 1, optional_failed: 0, pending: 0, skipped: 0 },
    agent_findings: { open: 0, resolved: 0, dismissed: 0 },
  },
  agent_reviews: [],
  unassigned_hunks: ["h_0123456789ab", "h_0123456789ac"],
  index: {
    hunks: [
      {
        id: "h_0123456789ab",
        path: "src/config.ts",
        new_range: [1, 3],
        additions: 7,
        deletions: 1,
        change: "modified",
        role: "code",
      },
      {
        id: "h_0123456789ac",
        path: "src/config.test.ts",
        new_range: [1, 14],
        additions: 14,
        deletions: 0,
        change: "added",
        role: "test",
      },
    ],
    checks: [
      {
        id: "ci",
        kind: "check_run",
        name: "CI / test",
        conclusion: "failure",
        required: true,
        head,
      },
    ],
    reports: [],
    comments: [],
    threads: [],
    sessions: [
      { id: "session-1", vendor: "claude", title: "Parse the config file", restricted: false },
    ],
    prs: [],
  },
});

let root: Root, node: HTMLDivElement, fetched: string[];

beforeAll(() => {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= (query: string) =>
    ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }) as unknown as MediaQueryList;
  Element.prototype.scrollIntoView ??= () => {};
});

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  fetched = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      fetched.push(path);
      const url = new URL(path, "http://local");
      if (url.pathname === "/s/test-ns/api/facts")
        return Response.json(facts(url.searchParams.get("head") ?? ""));
      if (url.pathname === "/s/test-ns/api/facts/sessions")
        return Response.json({
          available: true,
          sessions: [{ id: "session-1", vendor: "claude", title: "Parse the config file" }],
          commits: [
            {
              oid: "3333333333333333333333333333333333333333",
              subject: "Parse the config",
              links: [{ session: "session-1", method: "trailer", confidence: 1 }],
            },
          ],
        });
      if (url.pathname === "/s/test-ns/api/facts/patch")
        return Response.json({
          patch: "@@ -1,2 +1,3 @@\n const a = 1;\n+const parsed = parse(a);\n end();\n",
        });
      if (url.pathname === "/s/test-ns/api/document")
        return new Response("missing", { status: 404 });
      if (url.pathname === "/s/test-ns/api/versions") return Response.json([]);
      return Response.json({});
    }),
  );
});

afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  localStorage.clear();
  vi.unstubAllGlobals();
  forgetLastRows();
});

async function settle() {
  for (let i = 0; i < 5; i++) await act(async () => new Promise((done) => setTimeout(done, 0)));
}

async function render(current: Entry, revision = 0) {
  await act(async () =>
    root.render(
      <UrlStateProvider value={{ search: new URLSearchParams("walk=full"), update: () => {} }}>
        <LocalReview
          entry={current}
          clone="selected-clone"
          revision={revision}
          changed={async () => {}}
          generate={() => {}}
        />
      </UrlStateProvider>,
    ),
  );
  await settle();
}

const factsRequests = () =>
  fetched.filter((path) => new URL(path, "http://local").pathname === "/s/test-ns/api/facts");

const railGroups = () =>
  [
    ...node.querySelectorAll<HTMLButtonElement>(
      'nav[aria-label="Review sections"] button[aria-expanded][aria-controls]',
    ),
  ].map((row) => row.textContent);

it("shows a pull request's facts beside the Generate review placeholder", async () => {
  await render(entry);
  expect(factsRequests()).toEqual(["/s/test-ns/api/facts?clone=selected-clone&pr=7&head=1111111"]);
  expect(node.textContent).toContain("No review yet");
  expect(node.textContent).toContain("Generate review");
  expect(railGroups()).toEqual(["Changes0", "Files changed2", "Sessions1", "Commits1"]);
  // Facts are no version: no version menu.
  expect(node.textContent).not.toMatch(/Review v\d/);

  const activity = [...node.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find(
    (tab) => tab.textContent === "Activity",
  )!;
  await act(async () => activity.click());
  expect(node.querySelector("#conversation-panel-activity")!.textContent).toContain("CI failing");
  const details = [...node.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find(
    (tab) => tab.textContent === "Details",
  )!;
  await act(async () => details.click());
  expect(node.querySelector("#conversation-panel-details")!.textContent).toContain("Files2");

  // The sessions and commits are read against the facts.
  expect(fetched).toContain("/s/test-ns/api/facts/sessions?clone=selected-clone&pr=7&head=1111111");
  const sessions = [...node.querySelectorAll<HTMLButtonElement>("button")].find(
    (button) => button.textContent === "Sessions1",
  )!;
  await act(async () => sessions.click());
  await settle();
  expect(node.textContent).toContain("Parse the config file");

  // The files list opens a file's patch from the clone.
  const files = [...node.querySelectorAll<HTMLButtonElement>("button")].find(
    (button) => button.textContent === "Files changed2",
  )!;
  await act(async () => files.click());
  await settle();
  const file = node.querySelector<HTMLButtonElement>('button[aria-label="src/config.ts code"]')!;
  expect(file).not.toBeNull();
  await act(async () => file.click());
  await settle();
  expect(fetched).toContain(
    "/s/test-ns/api/facts/patch?clone=selected-clone&pr=7&head=1111111&path=src%2Fconfig.ts",
  );
  expect(node.textContent).toContain("const parsed = parse(a);");
  // No version's routes are read for them.
  expect(
    fetched.some((path) => path.includes("/api/patch") || path.includes("/api/sessions")),
  ).toBe(false);
});

it("follows a new head and asks again as the index changes", async () => {
  await render(entry);
  await render(entry, 1);
  const moved = { ...entry, pull_request: { ...entry.pull_request, head_sha: "4444444" } };
  await render(moved, 2);
  expect(factsRequests()).toEqual([
    "/s/test-ns/api/facts?clone=selected-clone&pr=7&head=1111111",
    "/s/test-ns/api/facts?clone=selected-clone&pr=7&head=1111111",
    "/s/test-ns/api/facts?clone=selected-clone&pr=7&head=4444444",
  ]);
});

it("asks for no facts once a review is saved or without a clone", async () => {
  await render({ ...entry, version_count: 1, status: "current" });
  await render({ ...entry, needs_clone: true }, 1);
  expect(factsRequests()).toEqual([]);
  expect(railGroups()).not.toContain("Files changed2");
});

it("says when the facts could not be read, and not when the daemon has none", async () => {
  const answer = (status: number, code: string) =>
    vi.mocked(fetch).mockImplementation(async (path) => {
      fetched.push(String(path));
      return String(path).startsWith("/s/test-ns/api/facts")
        ? Response.json({ code, message: code }, { status })
        : Response.json([]);
    });
  answer(503, "facts_unavailable");
  await render(entry);
  expect(node.textContent).toContain("No review yet");
  expect(node.textContent).not.toContain("couldn't be read");
  expect(railGroups()).toEqual(["Changes0"]);

  answer(503, "rate_limited");
  await render({ ...entry, pull_request: { ...entry.pull_request, head_sha: "5555555" } }, 1);
  expect(node.textContent).toContain("GitHub's rate limit is reached");
  expect(railGroups()).toContain("Files changed");
});
