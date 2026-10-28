import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./app";
import type { Entry, Job, LiveSession } from "./api";
import { snoozeOptions, snoozeText, upNext } from "./up-next";

type Overrides = Partial<Omit<Entry, "pull_request">> & {
  pull_request?: Partial<Entry["pull_request"]>;
};
const entry = (number: number, overrides: Overrides = {}): Entry => {
  const { pull_request, ...rest } = overrides;
  return {
    pull_request: {
      repository: "acme/demo",
      number,
      title: `Change ${number}`,
      url: `https://github.com/acme/demo/pull/${number}`,
      head_sha: "head",
      state: "OPEN",
      draft: false,
      fork: false,
      authored_by_me: false,
      review_requested_from_me: false,
      author_has_write: true,
      team_membership_known: true,
      viewer_can_push: true,
      review: "",
      checks: "",
      merge: "",
      unresolved: null,
      unresolved_capped: false,
      ...pull_request,
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
    ...rest,
  };
};
const requested = (number: number, overrides: Overrides = {}) =>
  entry(number, {
    ...overrides,
    pull_request: { review_requested_from_me: true, author: "sam", ...overrides.pull_request },
  });
const mine = (number: number, overrides: Overrides = {}) =>
  entry(number, {
    ...overrides,
    pull_request: { authored_by_me: true, ...overrides.pull_request },
  });
const ready = (number: number) => mine(number, { merge: "ready", checks: "passing" });

const fixture = () => [
  ready(3),
  mine(2, { checks: "failing" }),
  requested(1, {
    status: "current",
    version_count: 1,
    risk_score: { score: 4, status: "assessed" },
    review_minutes: 12,
  }),
  ready(4),
  mine(5, { merge: "draft", pull_request: { draft: true } }),
  entry(6),
];

it("orders what waits on the person and leaves out what waits on others", () => {
  const fixing: Job = {
    id: "job",
    clone: "clone",
    repository: "acme/demo",
    pr: 7,
    state: "running",
    phase: "",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
    kind: "fix",
  };
  const working: LiveSession = {
    agent: "claude-code",
    session_id: "s",
    state: "running",
    started_at: "",
    last_activity_at: "",
    last_event: "tool",
    repository: "acme/demo",
    pr: 1,
  };
  const { items, waiting } = upNext(
    [
      ...fixture(),
      mine(7, { merge: "conflict" }),
      mine(8, { historical: true, merge: "ready" }),
      // The same pull request in a second clone is listed once.
      { ...ready(3), clone_id: "other-clone" },
      requested(9, { needs_clone: true, clone_id: undefined }),
      mine(10, { pull_request: { behind: true }, review: "approved" }),
    ],
    [working],
    [fixing],
  );
  expect(items.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["review", 1],
    ["clone", 9],
    ["merge", 3],
    ["merge", 4],
    ["fix", 2],
    ["update", 10],
  ]);
  // A draft waits on its author's work, and a fix already running on its agent.
  expect(waiting.map((e) => e.pull_request.number)).toEqual([5, 7]);
  const [review, clone, merge, , fix] = items;
  expect(review!.why).toBe(
    "baum's review is ready, risk 4 of 5. 1 agent session is still working on its branch, so reviewing now catches problems before more commits pile up.",
  );
  expect(review!.working).toBe(1);
  expect(review!.minutes).toBe(12);
  // Its group already says a merge is ready.
  expect(merge!.why).toBe("");
  expect(fix!.why).toBe("Checks are failing. An agent can fix it while you do something else.");
  expect(clone!.why).toContain("no local clone of acme/demo");
});

it("offers an hour, this afternoon while it is over an hour off, tomorrow morning and the next push", () => {
  const morning = new Date(2026, 9, 27, 10, 20).getTime();
  expect(snoozeOptions(morning).map((o) => [o.label, o.until])).toEqual([
    ["1 hour", new Date(2026, 9, 27, 11, 20).getTime()],
    ["This afternoon", new Date(2026, 9, 27, 15, 0).getTime()],
    ["Tomorrow morning", new Date(2026, 9, 28, 9, 0).getTime()],
    ["Until it changes", "change"],
  ]);
  const later = new Date(2026, 9, 27, 14, 30).getTime();
  expect(snoozeOptions(later).map((o) => o.label)).toEqual([
    "1 hour",
    "Tomorrow morning",
    "Until it changes",
  ]);
  expect(snoozeText("change", later)).toBe("the next push");
  expect(snoozeText(new Date(2026, 9, 28, 9, 0).getTime(), later)).toMatch(/^tomorrow 9/);
});

let root: Root;
let node: HTMLDivElement;
let entries: Entry[] = [];
let repositories: unknown[] = [];
let jobsList: Job[] = [];
const fixJobOf = (overrides: Partial<Job> = {}): Job => ({
  id: "fix-1",
  clone: "clone",
  repository: "acme/demo",
  pr: 2,
  state: "running",
  phase: "fixing",
  calls: 0,
  tokens: 0,
  repairs: 0,
  actual_cost_usd: null,
  kind: "fix",
  has_transcript: true,
  ...overrides,
});
beforeEach(() => {
  jobsList = [];
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value(this: HTMLDialogElement) {
      this.open = true;
    },
  });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  entries = fixture();
  repositories = [{ repository: "acme/demo", settings: {}, clones: [] }];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      Response.json(
        path.includes("api/index")
          ? { entries, problems: [] }
          : path.endsWith("api/repositories")
            ? repositories
            : path.endsWith("api/repos")
              ? [{ id: "clone", repository: "acme/demo", path: "/c", available: true }]
              : path.endsWith("api/prs") || path.endsWith("api/background")
                ? []
                : path.endsWith("api/jobs")
                  ? { jobs: jobsList }
                  : path.includes("api/prs/merge-options")
                    ? {
                        methods: ["SQUASH"],
                        default: "SQUASH",
                        state: "OPEN",
                        draft: false,
                        head_sha: "head",
                        mergeable: "MERGEABLE",
                        merge_state_status: "CLEAN",
                      }
                    : {},
      ),
    ),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const render = async () => {
  await act(async () => root.unmount());
  root = createRoot(node);
  await act(async () => root.render(<App />));
};
const panel = () => node.querySelector("article.upnext-panel")!;
const shown = () => panel().querySelector("h2")!.textContent;
const list = () =>
  Array.from(node.querySelectorAll(".upnext-row-title")).map((title) => title.textContent);
const groups = () =>
  Array.from(node.querySelectorAll(".upnext-group-head h2")).map((h) => h.textContent);
const button = (name: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent?.startsWith(name))!;
const row = (title: string) =>
  Array.from(node.querySelectorAll<HTMLButtonElement>(".upnext-row")).find((r) =>
    r.textContent?.includes(title),
  )!;
const snoozeFor = (label: string) =>
  Array.from(node.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')).find((item) =>
    item.textContent?.includes(label),
  )!;
const press = (key: string) =>
  act(async () => {
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });
const progress = () => {
  const bar = node.querySelector('[role="progressbar"]')!;
  return `${bar.getAttribute("aria-valuenow")} of ${bar.getAttribute("aria-valuemax")}`;
};
/** What the list says was done today; it starts folded. */
const cleared = async () => {
  const toggle = node.querySelector<HTMLButtonElement>('[aria-label="Done today"] button');
  if (!toggle) return null;
  if (toggle.getAttribute("aria-expanded") === "false") await act(async () => toggle.click());
  return Array.from(node.querySelectorAll(".upnext-done-text"))
    .map((text) => text.textContent)
    .join("\n");
};

it("groups what waits on the person and opens on the first", async () => {
  await render();
  expect(groups()).toEqual(["Needs your eyes · 1", "Ready to merge · 2", "Hand to an agent · 1"]);
  expect(list()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2"]);
  expect(shown()).toBe("Change 1");
  expect(panel().textContent).toContain("sam asked for your review");
  // Open is always the default action.
  const open = panel().querySelector<HTMLAnchorElement>("a.upnext-primary")!;
  expect(open.textContent).toBe("Open↵");
  expect(open.getAttribute("href")).toBe("/s/test-ns/?clone=clone&pr=1");
  expect(node.textContent).toContain("1 pull request of yours waits on reviewers");
  const inbox = Array.from(node.querySelectorAll(".local-rail a")).find((a) =>
    a.textContent?.startsWith("Inbox"),
  )!;
  expect(inbox.getAttribute("aria-current")).toBe("page");
  expect(inbox.querySelector(".local-rail-badge")?.textContent).toBe("4");
});

it("moves with j and k, opens with Enter and merges with m", async () => {
  await render();
  await press("m");
  // Only a pull request ready to merge merges.
  expect(node.querySelector("dialog")).toBeNull();
  await press("j");
  await press("j");
  await press("k");
  expect(shown()).toBe("Change 3");
  expect(row("Change 3").getAttribute("aria-current")).toBe("true");
  await press("m");
  expect(node.querySelector("dialog")).not.toBeNull();
  // Afresh, with no dialog open.
  await render();
  await press("Enter");
  expect(location.search).toBe("?clone=clone&pr=1");
});

it("snoozes, undoes and brings back, and remembers it", async () => {
  await render();
  await act(async () => row("Change 3").click());
  await press("s");
  await press("1");
  expect(node.querySelector('[role="status"]')!.textContent).toMatch(/^Snoozed #3 until /);
  // The pull request that takes its place is selected.
  expect(shown()).toBe("Change 4");
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2"]);
  await press("u");
  expect(shown()).toBe("Change 3");
  expect(list()).toContain("Change 3");
  await act(async () => button("Snooze").click());
  await act(async () => snoozeFor("Tomorrow morning").click());
  expect(shown()).toBe("Change 4");
  // Snoozed pull requests start folded away.
  expect(groups()).toContain("▸ Snoozed · 1");
  expect(node.querySelector<HTMLElement>(".upnext-snoozed")!.hidden).toBe(true);
  // A reload keeps the snooze.
  await render();
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2"]);
  await act(async () => button("▸ Snoozed").click());
  await act(async () => button("Bring back").click());
  expect(list()).toContain("Change 3");
  expect(shown()).toBe("Change 3");
});

it("brings back a pull request snoozed until it changes once it is pushed to", async () => {
  await render();
  await act(async () => row("Change 4").click());
  await act(async () => button("Snooze").click());
  await act(async () => snoozeFor("Until it changes").click());
  expect(list()).not.toContain("Change 4");
  await render();
  expect(list()).not.toContain("Change 4");
  entries = entries.map((e) =>
    e.pull_request.number === 4
      ? mine(4, { merge: "ready", checks: "passing", pull_request: { head_sha: "new" } })
      : e,
  );
  await render();
  expect(list()).toContain("Change 4");
});

it("counts what left the queue as cleared today", async () => {
  await render();
  expect(progress()).toBe("0 of 4");
  expect(await cleared()).toBeNull();
  // Merged elsewhere: it is gone from the next read.
  entries = entries.filter((e) => e.pull_request.number !== 3);
  await render();
  expect(progress()).toBe("1 of 4");
  expect(await cleared()).toBe("Merged #3 Change 3");
  // Reviewed, fixed and merged; someone else's pull request keeps the
  // repository in the read.
  entries = [entry(6)];
  await render();
  expect(node.textContent).toContain("Inbox zero.");
  expect(node.textContent).toContain("All 4 handled. Nothing needs you right now.");
});

it("clears only what the person finished, and nothing of a repository missing from the read", async () => {
  await render();
  entries = entries.map((e) =>
    e.pull_request.number === 3
      ? // A push leaves the ready pull request blocked: still theirs to merge.
        mine(3, { merge: "blocked", checks: "running" })
      : e.pull_request.number === 2
        ? // Fixed: it waits on its checks now.
          mine(2, { checks: "running" })
        : e,
  );
  await render();
  expect(await cleared()).toBe("Fixed #2 Change 2");
  expect(list()).toEqual(["Change 1", "Change 4"]);
  // GitHub could not be read: nothing of acme/demo is listed.
  entries = [];
  await render();
  expect(await cleared()).toBe("Fixed #2 Change 2");
});

it("starts afresh from storage it cannot read", async () => {
  const now = new Date();
  localStorage.setItem(
    "baum.inbox.v1",
    JSON.stringify({
      day: `${now.getFullYear()}-${now.getMonth() + 1}-${now.getDate()}`,
      seen: [],
      snoozed: null,
      held: { "acme/demo#3": 5 },
    }),
  );
  await render();
  expect(shown()).toBe("Change 1");
  expect(list()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2"]);
});

it("merges every ready pull request after one confirmation", async () => {
  await render();
  await act(async () => button("Merge all 2").click());
  const dialog = node.querySelector("dialog")!;
  expect(dialog.textContent).toContain("Squash and merge");
  await act(async () => button("Merge 2 pull requests").click());
  const merges = vi
    .mocked(fetch)
    .mock.calls.filter(([path]) => String(path).endsWith("api/prs/merge"))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
  expect(merges).toEqual([
    { repository: "acme/demo", pr: 3, method: "SQUASH", head_sha: "head" },
    { repository: "acme/demo", pr: 4, method: "SQUASH", head_sha: "head" },
  ]);
  expect(dialog.textContent).toContain("Merged");
});

it("says how to start when no repository is picked", async () => {
  entries = [];
  repositories = [];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).endsWith("api/repos") ? Response.json([]) : original(...args),
  );
  await render();
  expect(node.textContent).toContain("Nothing to do yet");
});

it("does not clear a pull request whose fix is running, and offers to watch it", async () => {
  await render();
  // Pressing Fix and confirming starts a job; the agent's row stays in its group.
  jobsList = [fixJobOf()];
  await render();
  expect(list()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2"]);
  expect(row("Change 2").textContent).toContain("Agent fixing · The agent is fixing it");
  expect(await cleared()).toBeNull();
  await act(async () => row("Change 2").click());
  expect(panel().textContent).toContain("An agent is fixing it · The agent is fixing it.");
  const watch = Array.from(panel().querySelectorAll("a")).find(
    (a) => a.textContent === "Watch the agent",
  )!;
  expect(watch.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=fix-1");
  // The one other waiting pull request keeps the generic sentence.
  expect(node.textContent).toContain("1 pull request of yours waits on reviewers");
});

it("clears a fixed pull request once its fix pushed", async () => {
  await render();
  jobsList = [fixJobOf({ state: "completed", commit: "abc1234" })];
  entries = entries.map((e) => (e.pull_request.number === 2 ? mine(2, { checks: "running" }) : e));
  await render();
  expect(await cleared()).toBe("Fixed #2 Change 2");
});

it("brings back a pull request whose fix failed, with what happened", async () => {
  await render();
  jobsList = [fixJobOf({ state: "failed", reason: "signing" })];
  await render();
  expect(await cleared()).toBeNull();
  expect(row("Change 2").textContent).toContain("Fix · last fix failed");
  await act(async () => row("Change 2").click());
  expect(panel().textContent).toContain("The last fix failed: Git couldn't sign a commit");
  const see = Array.from(panel().querySelectorAll("a")).find(
    (a) => a.textContent === "See what the agent did",
  )!;
  expect(see.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=fix-1");
  expect(panel().querySelector("button.upnext-secondary")!.textContent).toMatch(/^Fix/);
});

it("keeps a failed fix of a pull request in the queue for its panel", () => {
  const { items } = upNext(
    [mine(2, { checks: "failing" })],
    [],
    [fixJobOf({ state: "failed", has_transcript: false })],
  );
  expect(items[0]!.failedFix!.id).toBe("fix-1");
});

it("shows nothing as cleared on starting a fix", async () => {
  await render();
  jobsList = [fixJobOf({ state: "queued", phase: "" })];
  await render();
  expect(await cleared()).toBeNull();
  expect(row("Change 2").textContent).toContain("Agent fixing · Queued");
  expect(progress()).toBe("0 of 3");
});

it("lists the unresolved review threads and resolves one on request", async () => {
  entries = [mine(7, { unresolved: 2 })];
  const threads = [
    {
      id: "RT_1",
      path: "src/a.go",
      line: 12,
      outdated: false,
      author: "alice",
      excerpt: "Rename this.",
      comments: 2,
      url: "https://github.com/acme/demo/pull/7#discussion_r1",
      can_resolve: true,
    },
    {
      id: "RT_2",
      path: "b.go",
      outdated: true,
      author: "bob",
      excerpt: "Why?",
      comments: 1,
      url: "https://github.com/acme/demo/pull/7#discussion_r2",
      can_resolve: false,
    },
  ];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) => {
    const path = String(args[0]);
    if (path.includes("api/prs/review-threads"))
      return Response.json({ threads: [...threads], capped: false });
    if (path.endsWith("api/prs/resolve-thread")) {
      threads.splice(0, 1);
      return Response.json({ resolved: true });
    }
    return original(...args);
  });
  await render();
  expect(shown()).toBe("Change 7");
  const items = () =>
    Array.from(panel().querySelectorAll('[aria-label="Unresolved review threads"] li'));
  expect(items().map((item) => item.textContent)).toEqual([
    "Rename this.alice · src/a.go:12 · 2 commentsResolve",
    "Why?bob · b.go · Outdated",
  ]);
  expect(items()[0]!.querySelector("a")!.getAttribute("href")).toBe(
    "https://github.com/acme/demo/pull/7#discussion_r1",
  );
  await act(async () => button("Resolve").click());
  const resolves = vi
    .mocked(fetch)
    .mock.calls.filter(([path]) => String(path).endsWith("api/prs/resolve-thread"))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
  expect(resolves).toEqual([{ repository: "acme/demo", pr: 7, thread: "RT_1" }]);
  expect(items().map((item) => item.textContent)).toEqual(["Why?bob · b.go · Outdated"]);
});

it("says why resolving a review thread failed and keeps it", async () => {
  entries = [mine(7, { unresolved: 1 })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) => {
    const path = String(args[0]);
    if (path.includes("api/prs/review-threads"))
      return Response.json({
        threads: [
          {
            id: "RT_1",
            path: "a.go",
            line: 1,
            outdated: false,
            author: "alice",
            excerpt: "Rename this.",
            comments: 1,
            url: "u",
            can_resolve: true,
          },
        ],
        capped: false,
      });
    if (path.endsWith("api/prs/resolve-thread"))
      return Response.json(
        { code: "forbidden", message: "Your GitHub account cannot resolve this review thread." },
        { status: 403 },
      );
    return original(...args);
  });
  await render();
  await act(async () => button("Resolve").click());
  const alert = panel().querySelector(".upnext-thread-error")!;
  expect(alert.textContent).toBe("Your GitHub account cannot resolve this review thread.");
  expect(button("Resolve").disabled).toBe(false);
});

it("lists the failing checks of the head it shows, each linking to its details", async () => {
  entries = [mine(2, { checks: "failing" })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).endsWith("api/jobs/estimate")
      ? Response.json({
          id: "estimate",
          items: [
            { clone: "clone", pr: 2, kind: "fix", fix: { checks: true, head_ref: "feature" } },
          ],
        })
      : String(args[0]).includes("api/prs/failing-checks")
        ? Response.json({
            head_sha: "head",
            checks: [
              {
                name: "test",
                conclusion: "failure",
                title: "2 tests failed in pkg",
                url: "https://github.com/acme/demo/runs/21",
                job_id: 21,
              },
              { name: "ci/circle", conclusion: "timed_out" },
            ],
          })
        : original(...args),
  );
  await render();
  expect(shown()).toBe("Change 2");
  const items = Array.from(panel().querySelectorAll('[aria-label="Failing checks"] li'));
  expect(items.map((item) => item.textContent)).toEqual([
    "testFailed · 2 tests failed in pkgRetryDetails",
    "ci/circleTimed out",
  ]);
  expect(items[0]!.querySelector("a")!.getAttribute("href")).toBe(
    "https://github.com/acme/demo/runs/21",
  );
  // Retry re-runs that one job at once; another app's check has none.
  await act(async () => button("Retry").click());
  const reruns = vi
    .mocked(fetch)
    .mock.calls.filter(([path]) => String(path).endsWith("api/prs/rerun-job"))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
  expect(reruns).toEqual([{ repository: "acme/demo", pr: 2, head_sha: "head", job_id: 21 }]);
  expect(panel().querySelector('[aria-label="Failing checks"] li')!.textContent).toBe(
    "testRe-running on GitHubDetails",
  );
  // The row's Fix CI asks for a fix of the checks alone.
  const row = panel().querySelector<HTMLButtonElement>("li.upnext-has-threads > button")!;
  expect(row.textContent).toBe("Fix CI");
  await act(async () => row.click());
  const estimates = vi
    .mocked(fetch)
    .mock.calls.filter(([path]) => String(path).endsWith("api/jobs/estimate"))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
  expect(estimates).toEqual([
    { items: [{ clone: "clone", pr: 2, kind: "fix", only: "checks" }], mode: "single" },
  ]);
  expect(node.querySelector("dialog h2")!.textContent).toBe("Fix CI");
  const asked = vi
    .mocked(fetch)
    .mock.calls.map(([path]) => String(path))
    .filter((path) => path.includes("api/prs/failing-checks"));
  expect(asked).toEqual([
    "/s/test-ns/api/prs/failing-checks?repository=acme%2Fdemo&pr=2&head_sha=head",
  ]);
});
