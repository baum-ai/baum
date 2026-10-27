import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./app";
import type { Entry, Job, LiveSession } from "./api";
import { snoozeUntil, upNext } from "./up-next";

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
    ["fix", 2],
    ["merge", 3],
    ["merge", 4],
    ["update", 10],
    ["clone", 9],
  ]);
  // A draft waits on its author's work, and a fix already running on its agent.
  expect(waiting.map((e) => e.pull_request.number)).toEqual([5, 7]);
  const [review, fix] = items;
  expect(review!.why).toBe(
    "sam asked for your review. baum's review is ready, risk 4 of 5. 1 agent session is still working on its branch, so reviewing now catches problems before more commits pile up.",
  );
  expect(review!.facts).toEqual([
    { label: "Risk", value: "4 of 5" },
    { label: "baum review", value: "Ready" },
    { label: "Agents", value: "1 working" },
  ]);
  expect(review!.minutes).toBe(12);
  expect(fix!.why).toBe(
    "Yours. Checks are failing. An agent can fix it while you do something else.",
  );
  expect(items[5]!.why).toContain("no local clone of acme/demo");
});

it("snoozes to a whole hour three hours on, or to tomorrow morning in the evening", () => {
  expect(new Date(snoozeUntil(new Date(2026, 9, 27, 10, 20).getTime()))).toEqual(
    new Date(2026, 9, 27, 14, 0),
  );
  expect(new Date(snoozeUntil(new Date(2026, 9, 27, 12, 0).getTime()))).toEqual(
    new Date(2026, 9, 27, 15, 0),
  );
  expect(new Date(snoozeUntil(new Date(2026, 9, 27, 17, 0).getTime()))).toEqual(
    new Date(2026, 9, 28, 9, 0),
  );
});

let root: Root;
let node: HTMLDivElement;
let entries: Entry[] = [];
let repositories: unknown[] = [];
beforeEach(() => {
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
                  ? { jobs: [] }
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
const card = () => node.querySelector("article.upnext-card");
const queue = () =>
  Array.from(node.querySelectorAll(".upnext-row-title")).map((title) => title.textContent);
const button = (name: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent?.startsWith(name))!;

it("opens on the one thing to do next, with the rest after it", async () => {
  await render();
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 1");
  expect(card()!.textContent).toContain("Why now:");
  const start = Array.from(card()!.querySelectorAll("a")).find(
    (a) => a.textContent === "Start reviewing",
  )!;
  expect(start.getAttribute("href")).toBe("/s/test-ns/?clone=clone&pr=1");
  expect(queue()).toEqual(["Change 2", "Change 3", "Change 4"]);
  expect(node.textContent).toContain("1 pull request of yours waits on reviewers");
  const inbox = Array.from(node.querySelectorAll(".local-rail a")).find((a) =>
    a.textContent?.startsWith("Inbox"),
  )!;
  expect(inbox.getAttribute("aria-current")).toBe("page");
  expect(inbox.textContent).toBe("Inbox4");
});

it("skips, promotes and snoozes, and remembers it", async () => {
  await render();
  await act(async () => button("Skip").click());
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 2");
  expect(queue()).toEqual(["Change 3", "Change 4", "Change 1"]);
  await act(async () =>
    Array.from(node.querySelectorAll<HTMLButtonElement>(".upnext-row"))
      .find((row) => row.textContent?.includes("Change 4"))!
      .click(),
  );
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 4");
  await act(async () => button("Snooze until").click());
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 2");
  expect(node.textContent).toContain("1 pull request snoozed until");
  // A reload keeps the order and the snooze.
  await render();
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 2");
  expect(queue()).toEqual(["Change 3", "Change 1"]);
  await act(async () => button("Bring them back").click());
  expect(queue()).toContain("Change 4");
});

it("counts what left the queue as cleared today", async () => {
  await render();
  expect(node.querySelector('[aria-label="0 of 4 cleared today"]')).not.toBeNull();
  // Merged elsewhere: it is gone from the next read.
  entries = entries.filter((e) => e.pull_request.number !== 3);
  await render();
  expect(node.querySelector('[aria-label="1 of 4 cleared today"]')).not.toBeNull();
  expect(node.querySelector(".upnext-cleared")!.textContent).toBe("Merged #3 Change 3");
  // Reviewed, fixed and merged; someone else's pull request keeps the
  // repository in the read.
  entries = [entry(6)];
  await render();
  expect(node.textContent).toContain("Inbox zero.");
  expect(node.textContent).toContain("All 4 handled today.");
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
  expect(node.querySelector(".upnext-cleared")!.textContent).toBe("Fixed #2 Change 2");
  expect(queue()).toEqual(["Change 4"]);
  // GitHub could not be read: nothing of acme/demo is listed.
  entries = [];
  await render();
  expect(node.querySelector(".upnext-cleared")!.textContent).toBe("Fixed #2 Change 2");
});

it("starts afresh from storage it cannot read", async () => {
  const now = new Date();
  localStorage.setItem(
    "baum.inbox.v1",
    JSON.stringify({
      day: `${now.getFullYear()}-${now.getMonth() + 1}-${now.getDate()}`,
      seen: [],
      skipped: "Change 1",
      promoted: 4,
      snoozed: null,
    }),
  );
  await render();
  expect(card()!.querySelector("h2")!.textContent).toBe("Change 1");
  expect(queue()).toEqual(["Change 2", "Change 3", "Change 4"]);
});

it("merges every ready pull request after one confirmation", async () => {
  await render();
  await act(async () => button("Merge 2 ready").click());
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
