import { DEFAULT_INBOX, mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import example from "../../schema/review-example.json" with { type: "json" };
import { App } from "./app";
import type { Entry, Job, LiveSession, ReviewGroup } from "./api";
import { FOR_ME } from "./inboxes";
import {
  metadataStale,
  othersState,
  queueItems,
  snoozeOptions,
  snoozeText,
  upNext,
  waitingOn,
  withGroups,
} from "./up-next";
import { forgetLastRows, MAC } from "./shortcuts";

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

it("keeps a stacked pull request GitHub would merge waiting on the lowest open one under it", () => {
  const stacked = (number: number, head: string, base: string, overrides: Overrides = {}) =>
    mine(number, {
      merge: "ready",
      checks: "passing",
      ...overrides,
      pull_request: { head_ref: head, base_ref: base, ...overrides.pull_request },
    });
  const stack: ReviewGroup = {
    id: "s1",
    repository: "acme/demo",
    name: "Stack",
    members: [1, 2, 3, 4],
    stack: true,
    created_at: "",
    updated_at: "",
    version_count: 0,
    latest: null,
    risk_score: null,
    reviewed: [],
    outdated: false,
  };
  const entries = [
    // The bottom targets main, which wants a review first.
    stacked(1, "one", "main", { merge: "blocked", pull_request: { review_required: true } }),
    stacked(2, "two", "one"),
    stacked(3, "three", "two"),
    // A failing one still needs fixing, whatever lies under it.
    stacked(4, "four", "three", { merge: "blocked", checks: "failing" }),
    // Outside a stack, a pull request on another's branch merges as GitHub says.
    stacked(5, "five", "four"),
  ];
  const { items, waiting, stackedOn } = upNext(entries, [], [], [stack]);
  expect(items.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["merge", 5],
    ["fix", 4],
  ]);
  expect(waiting.map((e) => e.pull_request.number)).toEqual([1, 2, 3]);
  expect([...stackedOn]).toEqual([
    ["acme/demo#2", 1],
    ["acme/demo#3", 1],
    ["acme/demo#4", 1],
  ]);
  expect(waitingOn(entries[0]!, stackedOn.get("acme/demo#1"))).toBe("Waiting on review");
  expect(waitingOn(entries[2]!, stackedOn.get("acme/demo#3"))).toBe("Waiting on #1");
  // Once the bottom merges, the next one up is ready.
  const { items: after } = upNext(entries.slice(1), [], [], [stack]);
  expect(after.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["merge", 2],
    ["merge", 5],
    ["fix", 4],
  ]);
});

it("says no checks reported for a blocked pull request with no checks and no review required", () => {
  const stuck = (review_required: boolean | null | undefined) =>
    entry(1, {
      checks: "none",
      merge: "blocked",
      review: "unreviewed",
      pull_request: { authored_by_me: true, review_required },
    });
  expect(waitingOn(stuck(false))).toBe("No checks reported");
  // A required review may be what blocks it; an older daemon doesn't say.
  expect(waitingOn(stuck(true))).toBe("Waiting on review");
  expect(waitingOn(stuck(undefined))).toBe("Blocked on GitHub");
  expect(waitingOn({ ...stuck(false), checks: "passing" })).toBe("Blocked on GitHub");
});

it("orders what waits on the person and sets apart what waits on others", () => {
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
  const { items, waiting, others } = upNext(
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
  // Someone else's waits on nothing from the person: listed apart, not left out.
  expect(others.map((e) => e.pull_request.number)).toEqual([6]);
  const [review, clone, merge, , fix] = items;
  expect(review!.why).toBe(
    "baum's review is ready, risk 4 of 5. 1 agent session is still working on its branch, so reviewing now catches problems before more commits pile up.",
  );
  expect(review!.working).toEqual([working]);
  expect(review!.minutes).toBe(12);
  // Its group already says a merge is ready.
  expect(merge!.why).toBe("");
  // What needs fixing already lists a fix's problems.
  expect(fix!.why).toBe("");
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
  liveList = [];
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
    mockStateFetch(async (path: string) =>
      Response.json(
        path.includes("api/index")
          ? { entries, problems: [] }
          : path.endsWith("api/repositories")
            ? repositories
            : path.endsWith("api/repos")
              ? [{ id: "clone", repository: "acme/demo", path: "/c", available: true }]
              : path.endsWith("api/live-sessions")
                ? { sessions: liveList }
                : path.endsWith("api/prs?view=health") || path.endsWith("api/background")
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
  forgetLastRows();
});
// The agent sessions the daemon says are running.
let liveList: LiveSession[] = [];
const render = async () => {
  await act(async () => root.unmount());
  root = createRoot(node);
  await act(async () => root.render(<App />));
};
const panel = () => node.querySelector("article.upnext-panel")!;
const shown = () => panel().querySelector("h2")!.textContent;

it("distinguishes a GitHub blocker from a required review without requiring approvals to merge", async () => {
  entries = [
    mine(1130, {
      merge: "blocked",
      checks: "passing",
      review: "commented",
      pull_request: { review_required: false },
    }),
    mine(1131, {
      merge: "blocked",
      checks: "passing",
      review: "commented",
      pull_request: { review_required: true },
    }),
    mine(1132, { merge: "ready", checks: "passing", review: "unreviewed" }),
    // An older snapshot has no review-required signal, so its blocker is unknown.
    mine(1133, { merge: "blocked", checks: "passing", review: "unreviewed" }),
  ];
  await render();
  expect(groups()).toEqual(["Ready to merge · 1", "Waiting · 3"]);
  expect(row("Change 1130").textContent).toContain("Blocked on GitHub");
  expect(row("Change 1131").textContent).toContain("Waiting on review");
  expect(upNext(entries).items.map(({ kind, entry }) => [kind, entry.pull_request.number])).toEqual(
    [["merge", 1132]],
  );
  expect(row("Change 1133").textContent).toContain("Blocked on GitHub");
  await act(async () => row("Change 1130").click());
  expect(panel().querySelector(".upnext-chip-waiting")?.textContent).toBe("Blocked on GitHub");
  expect(panel().querySelector('[aria-label="Why this is waiting"]')).toBeNull();
  const github = Array.from(panel().querySelectorAll(".upnext-block")).find(
    (block) => block.querySelector("h3")?.textContent === "On GitHub",
  )!;
  expect(github.textContent).toContain("Merge blocked by GitHub");
  expect(github.textContent).toContain("GitHub has not supplied the reason.");
  expect(
    panel().querySelector('.upnext-actions a[href="https://github.com/acme/demo/pull/1130"]')
      ?.textContent,
  ).toBe("Open on GitHub");
});

it.each<Overrides>([
  { merge: "blocked", checks: "running" },
  { merge: "blocked", checks: "failing" },
  { merge: "conflict" },
  { merge: "blocked", pull_request: { review_required: true } },
  { merge: "blocked", review: "changes_requested" },
  { merge: "blocked", unresolved: 1 },
  { merge: "blocked", pull_request: { behind: true } },
])("keeps the existing explanation for a known blocker: %j", async (state) => {
  entries = [mine(1, state)];
  await render();
  if (!panel()) await act(async () => row("Change 1").click());
  expect(panel().textContent).not.toContain("Merge blocked by GitHub");
  expect(panel().querySelector('[aria-label="Why this is waiting"]')).toBeNull();
});

const list = () =>
  Array.from(node.querySelectorAll(".upnext-row-title")).map((title) => title.textContent);
// A heading's name and count, as "Ready to merge · 2".
const groups = () =>
  Array.from(node.querySelectorAll(".upnext-list-body .list-heading")).map((h: Element) =>
    Array.from(h.querySelectorAll(".list-heading-label > span:not([aria-hidden])"))
      .map((part) => part.textContent)
      .join(" · "),
  );
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
  expect(groups()).toEqual([
    "Needs your eyes · 1",
    "Ready to merge · 2",
    "Hand to an agent · 1",
    "Waiting · 1",
    "Not waiting on you · 1",
  ]);
  // The person's own pull requests that wait on others come next, then
  // everyone else's.
  expect(list()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2", "Change 5", "Change 6"]);
  expect(shown()).toBe("Change 1");
  expect(panel().textContent).toContain("sam asked for your review");
  // Open is always the default action.
  const open = panel().querySelector<HTMLAnchorElement>("a.upnext-primary")!;
  expect(open.textContent).toBe("Open↵");
  expect(open.getAttribute("href")).toBe(`/s/test-ns/?inbox=${DEFAULT_INBOX.id}&clone=clone&pr=1`);
  expect(row("Change 5").textContent).toContain("#5 · Draft");
  // Inbox is a heading with the count; Default under it is the current page.
  const heading = node.querySelector(".local-rail-heading")!;
  expect(heading.getAttribute("aria-current")).toBeNull();
  expect(heading.querySelector(".local-rail-badge")?.textContent).toBe("4");
  expect(node.querySelector(".local-rail a.local-rail-sub")!.getAttribute("aria-current")).toBe(
    "page",
  );
});

const githubKey = () =>
  new KeyboardEvent("keydown", {
    key: "G",
    code: "KeyG",
    shiftKey: true,
    ...(MAC ? { metaKey: true } : { ctrlKey: true }),
    bubbles: true,
    cancelable: true,
  });

it("opens the selected pull request on GitHub with the command key, Shift and G", async () => {
  const opened: string[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    if (this.target === "_blank") opened.push(this.href);
  });
  await render();
  await press("j");
  expect(shown()).toBe("Change 3");
  await act(async () => document.body.dispatchEvent(githubKey()));
  expect(opened).toEqual(["https://github.com/acme/demo/pull/3"]);
  vi.restoreAllMocks();
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
  // Drawn again, with no dialog open, it comes back on the same pull request.
  await render();
  await press("Enter");
  expect(location.search).toBe(`?inbox=${DEFAULT_INBOX.id}&clone=clone&pr=3`);
});

// A right click on a row opens its menu, without picking the row.
const rightClick = (title: string) =>
  act(async () => {
    row(title).dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 9, clientY: 9 }),
    );
  });
const choices = () =>
  Array.from(node.querySelectorAll(".context-menu [role=menuitem] .context-menu-label")).map(
    (label) => label.textContent,
  );
const choose = (label: string) =>
  act(async () =>
    Array.from(node.querySelectorAll<HTMLButtonElement>(".context-menu [role=menuitem]"))
      .find((item) => item.textContent?.startsWith(label))!
      .click(),
  );

it("offers a pull request's actions on a right click on its row", async () => {
  await render();
  await rightClick("Change 3");
  expect(node.querySelector(".context-menu")!.getAttribute("aria-label")).toBe(
    "Actions for acme/demo #3",
  );
  expect(choices()).toEqual(expect.arrayContaining(["Open", "Merge…", "Snooze…"]));
  expect(choices()).not.toContain("Update the branch…");
  // The panel stays on what it showed.
  expect(shown()).toBe("Change 1");
  await choose("Merge…");
  expect(node.querySelector("dialog")).not.toBeNull();
});

it("snoozes from a row's menu with the panel's choices", async () => {
  await render();
  await rightClick("Change 3");
  await choose("Snooze…");
  expect(shown()).toBe("Change 3");
  await act(async () => snoozeFor("Tomorrow morning").click());
  expect(list()).not.toContain("Change 3");
});

it("opens a pull request from its row's menu", async () => {
  await render();
  await rightClick("Change 3");
  await choose("Open");
  expect(location.search).toBe(`?inbox=${DEFAULT_INBOX.id}&clone=clone&pr=3`);
});

it("snoozes, undoes and brings back, and remembers it", async () => {
  await render();
  await act(async () => row("Change 3").click());
  await press("s");
  await press("1");
  expect(node.querySelector('[role="status"]')!.textContent).toMatch(/^Snoozed #3 until /);
  // The pull request that takes its place is selected.
  expect(shown()).toBe("Change 4");
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2", "Change 5", "Change 6"]);
  await press("u");
  expect(shown()).toBe("Change 3");
  expect(list()).toContain("Change 3");
  await act(async () => button("Snooze").click());
  await act(async () => snoozeFor("Tomorrow morning").click());
  expect(shown()).toBe("Change 4");
  // Snoozed pull requests start folded away.
  expect(groups()).toContain("Snoozed · 1");
  expect(node.querySelector<HTMLElement>(".upnext-snoozed")!.hidden).toBe(true);
  // A reload keeps the snooze.
  await render();
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2", "Change 5", "Change 6"]);
  await act(async () => button("Snoozed").click());
  await act(async () =>
    node.querySelector<HTMLButtonElement>('button[aria-label="Actions for acme/demo #3"]')!.click(),
  );
  const choices = Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]'));
  expect(choices.map((item) => item.textContent)).toEqual(["Bring back", "Archive"]);
  await act(async () => choices[0]!.click());
  expect(list()).toContain("Change 3");
  expect(shown()).toBe("Change 3");
});

it("archives with e, undoes with u, and never shows it again, not even as done", async () => {
  await render();
  await act(async () => row("Change 3").click());
  await press("e");
  expect(node.querySelector('[role="status"]')!.textContent).toMatch(/^Archived #3/);
  // The pull request that takes its place is selected.
  expect(shown()).toBe("Change 4");
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2", "Change 5", "Change 6"]);
  await press("u");
  expect(shown()).toBe("Change 3");
  expect(list()).toContain("Change 3");
  // The panel's button archives too.
  await act(async () => button("Archive").click());
  expect(list()).not.toContain("Change 3");
  // There is no Archived group, it is not done today, and a reload keeps it.
  await render();
  expect(list()).toEqual(["Change 1", "Change 4", "Change 2", "Change 5", "Change 6"]);
  expect(groups().join()).not.toMatch(/Archived|Snoozed/);
  expect(await cleared()).toBeNull();
  expect(node.textContent).not.toContain("Change 3");
});

it("archives from a row's menu, a snoozed one too, and a snooze waits for the unarchive", async () => {
  await render();
  await rightClick("Change 4");
  await choose("Archive");
  expect(list()).not.toContain("Change 4");
  await act(async () => row("Change 3").click());
  await press("s");
  await act(async () => snoozeFor("Until it changes").click());
  await act(async () => button("Snoozed").click());
  await act(async () =>
    node.querySelector<HTMLButtonElement>('button[aria-label="Actions for acme/demo #3"]')!.click(),
  );
  await act(async () =>
    Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]'))
      .find((item) => item.textContent === "Archive")!
      .click(),
  );
  expect(groups().join()).not.toContain("Snoozed");
  expect(node.textContent).not.toContain("Change 3");
  // Undone, it is snoozed again.
  await press("u");
  expect(groups()).toContain("Snoozed · 1");
  expect(list()).not.toContain("Change 3");
});

it("forgets an archive once its pull request closes, so a reopened one comes back", async () => {
  await render();
  await act(async () => row("Change 3").click());
  await press("e");
  const all = entries;
  entries = all.filter((e) => e.pull_request.number !== 3);
  await render();
  expect(list()).not.toContain("Change 3");
  entries = all;
  await render();
  expect(list()).toContain("Change 3");
});

it("forgets an archive once the read says it merged, though its repository has nothing else open", async () => {
  entries = [ready(3)];
  await render();
  await press("e");
  expect(list()).toEqual([]);
  entries = [mine(3, { merge: "ready", checks: "passing", pull_request: { state: "MERGED" } })];
  await render();
  entries = [ready(3)];
  await render();
  expect(list()).toEqual(["Change 3"]);
});

it("keeps arrows, j/k and Control+n/p on Snooze's choices", async () => {
  await render();
  button("Snooze").focus();
  await act(async () => button("Snooze").click());
  const choices = [...node.querySelectorAll<HTMLElement>('.upnext-menu [role="menuitem"]')];
  expect(document.activeElement).toBe(choices[0]);
  for (const [key, ctrlKey, index] of [
    ["ArrowDown", false, 1],
    ["j", false, 2],
    ["n", true, 3],
    ["p", true, 2],
    ["k", false, 1],
    ["ArrowUp", false, 0],
    ["ArrowUp", false, choices.length - 1],
  ] as const) {
    await act(async () => {
      document.activeElement!.dispatchEvent(
        new KeyboardEvent("keydown", { key, ctrlKey, bubbles: true, cancelable: true }),
      );
    });
    expect(document.activeElement).toBe(choices[index % choices.length]);
    expect(shown()).toBe("Change 1");
  }
  await act(async () => {
    document.activeElement!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
  });
  expect(node.querySelector(".upnext-menu")).toBeNull();
  expect(document.activeElement).toBe(button("Snooze"));
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

it("keeps a snoozed Fix under Snoozed when its thread resolves and it waits on checks", async () => {
  entries = [mine(7, { unresolved: 1 })];
  await render();
  await press("s");
  await act(async () => snoozeFor("Tomorrow morning").click());
  entries = [mine(7, { unresolved: 0, checks: "running" })];
  await render();
  expect(list()).not.toContain("Change 7");
  expect(node.querySelector('section[aria-label="Snoozed"]')!.textContent).toContain("Change 7");
  expect(await cleared()).toBeNull();
});

it("says under the actions that the review is generating until its summary is written", async () => {
  entries = [mine(7, { status: "generating", unresolved: 1 })];
  await render();
  const status = [...panel().querySelectorAll('[role="status"]')].find((el) =>
    el.textContent?.includes("Generating the review"),
  );
  expect(status?.textContent).toBe("Generating the review.");
  // Straight after the actions, so it reads under Open, Comment and Snooze.
  expect(status?.previousElementSibling?.classList.contains("upnext-actions")).toBe(true);
});

it("says there is no review yet and generates one from the panel", async () => {
  entries = [requested(7)];
  await render();
  const card = panel().querySelector(".upnext-noreview")!;
  expect(card.textContent).toContain("No review yet.");
  expect(card.previousElementSibling?.classList.contains("upnext-actions")).toBe(true);
  // The next read has the queued job.
  entries = [requested(7, { status: "generating" })];
  await act(async () => button("Generate").click());
  const starts = vi.mocked(fetch).mock.calls.filter(isStart);
  expect(starts.map(([, init]) => JSON.parse(String(init!.body)))).toEqual([
    { items: [{ clone: "clone", pr: 7 }], mode: "single", confirm_isolation: false },
  ]);
  // From the click it reads as generating, not as missing.
  expect(panel().querySelector(".upnext-noreview")).toBeNull();
  expect(panel().textContent).toContain("Generating the review.");
});

it("offers to try again when the review failed", async () => {
  entries = [requested(7, { status: "failed", failure_reason: "the agent timed out" })];
  await render();
  const card = panel().querySelector(".upnext-noreview")!;
  expect(card.textContent).toContain("baum's review failed: the agent timed out");
  expect(button("Try again")).toBeTruthy();
});

it("says in the card why the daemon did not start the review, and on the PR page", async () => {
  // The PR page's review watches its layout.
  const observer = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  };
  vi.stubGlobal("ResizeObserver", observer);
  vi.stubGlobal("IntersectionObserver", observer);
  entries = [requested(7)];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    isStart(args)
      ? Response.json({ message: "the queue is full" }, { status: 400 })
      : original(...args),
  );
  await render();
  await act(async () => button("Generate").click());
  await settle();
  expect(panel().querySelector('[role="alert"]')!.textContent).toContain(
    "Couldn't start the review: the queue is full",
  );
  // Once, in the card: not again under the page.
  expect(node.textContent!.split("the queue is full").length).toBe(2);
  // The PR page opened from the inbox has no panel, so the page says it.
  await press("Enter");
  expect(location.search).toBe(`?inbox=${DEFAULT_INBOX.id}&clone=clone&pr=7`);
  await settle();
  expect(node.querySelector("article.upnext-panel")).toBeNull();
  expect(node.textContent).toContain("the queue is full");
});

it("lists what baum will do instead of the card when it needs a clone", async () => {
  entries = [requested(7, { needs_clone: true })];
  await render();
  // A clone item lists what baum will do instead of the card.
  expect(panel().querySelector(".upnext-noreview")).toBeNull();
  expect(panel().textContent).toContain("What baum will do");
});

it("shows the available summary in a Fix preview while its first review is being written", async () => {
  entries = [mine(7, { status: "generating", unresolved: 1 }), mine(8, { checks: "failing" })];
  jobsList = [fixJobOf({ id: "review-7", kind: undefined, pr: 7, draft: 1 })];
  const draft = structuredClone(example);
  draft.summary.text = "The terminal now remembers which agent CLIs it runs.";
  draft.sections = [];
  const original = globalThis.fetch;
  vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) =>
    String(args[0]).includes("api/jobs/review-7/draft")
      ? Response.json({ document: draft })
      : original(...args),
  );
  await render();
  await act(async () => row("Change 7").click());
  expect(panel().textContent).toContain("What needs fixing");
  expect(panel().textContent).toContain("What changed");
  expect(panel().textContent).toContain(draft.summary.text);
  expect(panel().textContent).not.toContain("Generating the review");
  await act(async () => row("Change 8").click());
  expect(panel().textContent).not.toContain(draft.summary.text);
});

it("keeps the saved summary in the preview while a replacement review is being written", async () => {
  entries = [mine(7, { status: "generating", version_count: 1, unresolved: 1 })];
  jobsList = [fixJobOf({ id: "review-7", kind: undefined, pr: 7, draft: 1 })];
  const original = globalThis.fetch;
  const fetch = vi.fn((...args: Parameters<typeof globalThis.fetch>) =>
    String(args[0]).includes("api/document") ? Response.json(example) : original(...args),
  );
  vi.stubGlobal("fetch", fetch);
  await render();
  expect(panel().textContent).toContain(example.summary.text);
  expect(panel().textContent).not.toContain("Generating the review");
  expect(fetch.mock.calls.some(([path]) => String(path).includes("/draft"))).toBe(false);
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
  // Nothing waits on the person, so the zero state gives way to the
  // other's pull request, opened.
  expect(node.textContent).not.toContain("Nothing here right now.");
  expect(node.textContent).toContain("Nothing left. 4 handled today.");
  expect(groups()).toContain("Not waiting on you · 1");
  expect(shown()).toBe("Change 6");
});

it("links what was done today to its page, or to GitHub once the app no longer lists it", async () => {
  await render();
  // Merged: a saved review keeps it listed, as history.
  entries = entries.map((e) =>
    e.pull_request.number === 3
      ? mine(3, { historical: true, pull_request: { state: "MERGED" } })
      : e,
  );
  await render();
  expect(await cleared()).toBe("Merged #3 Change 3");
  const done = () => node.querySelector<HTMLAnchorElement>("a.upnext-done-text")!;
  expect(done().getAttribute("href")).toBe(
    `/s/test-ns/?inbox=${DEFAULT_INBOX.id}&clone=clone&pr=3`,
  );
  expect(done().getAttribute("target")).toBeNull();
  // Without a saved review it leaves the list; GitHub still has it.
  entries = entries.filter((e) => e.pull_request.number !== 3);
  await render();
  expect(await cleared()).toBe("Merged #3 Change 3");
  expect(done().getAttribute("href")).toBe("https://github.com/acme/demo/pull/3");
  expect(done().getAttribute("target")).toBe("_blank");
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
  // Both now wait on their checks.
  expect(list()).toEqual(["Change 1", "Change 4", "Change 3", "Change 2", "Change 5", "Change 6"]);
  // GitHub could not be read: nothing of acme/demo is listed.
  entries = [];
  await render();
  expect(await cleared()).toBe("Fixed #2 Change 2");
});

it("keeps the queue usable and reports an unreadable saved record", async () => {
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (path, init) =>
    String(path).includes("api/ui-state/")
      ? Response.json({ message: "unreadable" }, { status: 500 })
      : original(path, init),
  );
  await render();
  expect(shown()).toBe("Change 1");
  expect(node.textContent).toContain("Could not read your saved state.");
  expect(node.textContent).toContain("Retry saving inbox state");
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
  // Pressing Fix starts a job; the agent's row stays in its group.
  jobsList = [fixJobOf()];
  await render();
  expect(list()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2", "Change 5", "Change 6"]);
  // While the agent itself works, the line says so once.
  expect(row("Change 2").textContent).toContain("Agent fixing");
  expect(row("Change 2").textContent).not.toContain("The agent is fixing it");
  expect(await cleared()).toBeNull();
  await act(async () => row("Change 2").click());
  expect(panel().textContent).toContain("An agent is fixing it.");
  expect(panel().textContent).not.toContain("The agent is fixing it");
  const watch = Array.from(panel().querySelectorAll("a")).find((a) =>
    a.textContent?.startsWith("Watch the agent"),
  )!;
  expect(watch.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=fix-1");
  expect(watch.querySelector(".key-hint")!.textContent).toBe("t");
  // The one other waiting pull request is listed as waiting.
  expect(row("Change 5").textContent).toContain("#5 · Draft");
  // t opens the transcript of the selected pull request's fix.
  await press("t");
  expect(location.search).toBe("?page=jobs&job=fix-1");
});

it("opens a finished fix's transcript with t once the agent stopped", async () => {
  await render();
  jobsList = [fixJobOf({ state: "completed", commit: "abc1234" })];
  entries = entries.map((e) => (e.pull_request.number === 2 ? mine(2, { checks: "running" }) : e));
  await render();
  await act(async () => row("Change 2").click());
  await press("t");
  expect(location.search).toBe("?page=jobs&job=fix-1");
});

it("waits on checks that run before calling a pull request ready to merge", async () => {
  // GitHub called the head clean before its checks were queued.
  entries = entries.map((e) =>
    e.pull_request.number === 3 ? mine(3, { merge: "ready", checks: "running" }) : e,
  );
  await render();
  expect(groups()).toEqual([
    "Needs your eyes · 1",
    "Ready to merge · 1",
    "Hand to an agent · 1",
    "Waiting · 2",
    "Not waiting on you · 1",
  ]);
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
  expect(row("Change 2").textContent).toContain("Last fix failed");
  await act(async () => row("Change 2").click());
  expect(panel().textContent).toContain("The last fix failed: Git couldn't sign a commit");
  const see = Array.from(panel().querySelectorAll("a")).find((a) =>
    a.textContent?.startsWith("See what the agent did"),
  )!;
  expect(see.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=fix-1");
  expect(panel().querySelector("button.upnext-secondary")!.textContent).toMatch(/^Fix/);
});

it("says why the last fix changed nothing, without calling it a failure", async () => {
  await render();
  jobsList = [
    fixJobOf({
      state: "completed",
      reason: "no_changes",
      summary: "I couldn't find a cause this pull request is responsible for.\n\n- Details.",
    }),
  ];
  await render();
  // Nothing was pushed, so the pull request stays to fix.
  expect(await cleared()).toBeNull();
  expect(row("Change 2").textContent).toContain("Last fix changed nothing");
  expect(row("Change 2").querySelector(".upnext-row-failed")).toBeNull();
  await act(async () => row("Change 2").click());
  expect(panel().querySelector(".upnext-failed")).toBeNull();
  const notice = panel().querySelector(".upnext-unchanged")!;
  expect(notice.textContent).toContain("The last fix changed nothing, so nothing was pushed.");
  expect(notice.querySelector(".upnext-unchanged-why")!.textContent).toBe(
    "I couldn't find a cause this pull request is responsible for.",
  );
  const read = Array.from(notice.querySelectorAll("a")).find((a) =>
    a.textContent?.startsWith("Read what the agent said"),
  )!;
  expect(read.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=fix-1");
});

it("keeps a failed fix of a pull request in the queue for its panel", () => {
  const { items } = upNext(
    [mine(2, { checks: "failing" })],
    [],
    [fixJobOf({ state: "failed", has_transcript: false })],
  );
  expect(items[0]!.failedFix!.id).toBe("fix-1");
});

/** Lets the daemon's answers and the reload after them land. */
const settle = () => act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
/** Whether a request asks the daemon to start a job. */
const isStart = ([path, init]: Parameters<typeof fetch>) =>
  init?.method === "POST" && String(path).endsWith("api/jobs");
/** The fixes the app asked the daemon to start. */
const fixesStarted = () => {
  const starts = vi.mocked(fetch).mock.calls.filter(isStart);
  return starts.map(([, init]) => JSON.parse(String(init!.body)));
};

it("starts a fix at once with its button or f, with no dialog", async () => {
  await render();
  // Where no fix is offered, f does nothing.
  await press("f");
  expect(fixesStarted()).toEqual([]);
  await act(async () => row("Change 2").click());
  expect(button("Fix CI").textContent).toBe("Fix CIf");
  await press("f");
  expect(node.querySelector("dialog")).toBeNull();
  expect(fixesStarted()).toEqual([
    { items: [{ clone: "clone", pr: 2, kind: "fix" }], mode: "single" },
  ]);
  await settle();
  await act(async () => button("Fix CI").click());
  expect(fixesStarted()).toHaveLength(2);
  expect(node.querySelector("dialog")).toBeNull();
  // The key is listed at the foot of the list.
  expect(node.querySelector(".upnext-keys")!.textContent).toContain("f fix");
});

it("says in the panel why the daemon did not start a fix", async () => {
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    isStart(args)
      ? Response.json({ blocked_reason: "nothing_to_fix" }, { status: 409 })
      : original(...args),
  );
  await render();
  await act(async () => row("Change 2").click());
  await press("f");
  await settle();
  expect(panel().querySelector('[role="alert"]')!.textContent).toBe(
    "Couldn't start the fix: GitHub reports no merge conflict, failing check or unresolved review comment to fix on this pull request now.",
  );
  // The fix is offered again.
  expect(button("Fix CI").disabled).toBe(false);
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

it("lists every check of the head it shows, failing first, each linking to its details", async () => {
  entries = [mine(2, { checks: "failing" })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/prs/checks")
      ? Response.json({
          head_sha: "head",
          checks: [
            { name: "lint", status: "completed", conclusion: "success" },
            { name: "build", status: "in_progress", conclusion: "" },
            { name: "ci/circle", status: "completed", conclusion: "timed_out" },
            {
              name: "test",
              status: "completed",
              conclusion: "failure",
              title: "2 tests failed in pkg",
              url: "https://github.com/acme/demo/runs/21",
              job_id: 21,
            },
          ],
        })
      : original(...args),
  );
  await render();
  expect(shown()).toBe("Change 2");
  // A fix's failing checks start open.
  const items = () => Array.from(panel().querySelectorAll('[aria-label="Checks"] li'));
  // Only the failing checks show at first; the others fold behind a toggle.
  expect(items().map((item) => item.textContent)).toEqual([
    "ci/circleTimed out",
    "testFailed · 2 tests failed in pkgRetryDetails",
  ]);
  await act(async () => button("Show 2 other checks").click());
  expect(items().map((item) => item.textContent)).toEqual([
    "ci/circleTimed out",
    "testFailed · 2 tests failed in pkgRetryDetails",
    "buildRunning",
    "lintPassed",
  ]);
  // The row counts the failing checks of them all beside its title.
  const count = panel().querySelector(".upnext-block-title [title]")!;
  expect(count.textContent).toBe("2/4 failing");
  expect(count.getAttribute("title")).toBe("2 failing, 1 running and 1 passed");
  expect(items()[1]!.querySelector("a")!.getAttribute("href")).toBe(
    "https://github.com/acme/demo/runs/21",
  );
  // Retry re-runs that one job at once; another app's check has none.
  await act(async () => button("Retry").click());
  const reruns = vi
    .mocked(fetch)
    .mock.calls.filter(([path]) => String(path).endsWith("api/prs/rerun-job"))
    .map(([, init]) => JSON.parse(String((init as RequestInit).body)));
  expect(reruns).toEqual([{ repository: "acme/demo", pr: 2, head_sha: "head", job_id: 21 }]);
  expect(items()[1]!.textContent).toBe("testRe-running on GitHubDetails");
  // The panel's own fix covers the checks; their row has no fix of its own.
  expect(
    panel().querySelector("li.upnext-has-threads > button:not(.upnext-detail-toggle)"),
  ).toBeNull();
  const asked = vi
    .mocked(fetch)
    .mock.calls.map(([path]) => String(path))
    .filter((path) => path.includes("api/prs/checks"));
  expect(asked).toEqual(["/s/test-ns/api/prs/checks?repository=acme%2Fdemo&pr=2&head_sha=head"]);
  // The row folds its checks away.
  await act(async () => button("Checks failing").click());
  expect(items()).toEqual([]);
});

it("keeps reading running checks after a read fails", async () => {
  entries = [mine(2, { checks: "failing" })];
  let reads = 0;
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) => {
    if (!String(args[0]).includes("api/prs/checks")) return original(...args);
    reads++;
    if (reads === 2) return Response.json({ code: "failed", message: "offline" }, { status: 502 });
    const done = reads > 2;
    return Response.json({
      head_sha: "head",
      checks: [
        { name: "test", status: "completed", conclusion: "failure" },
        {
          name: "build",
          status: done ? "completed" : "in_progress",
          conclusion: done ? "success" : "",
        },
      ],
    });
  });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    await render();
    expect(reads).toBe(1);
    await act(async () => vi.advanceTimersByTime(20_000));
    expect(reads).toBe(2);
    await act(async () => vi.advanceTimersByTime(20_000));
    expect(reads).toBe(3);
    // The build, done now, folds behind the failing test.
    const listed = () =>
      Array.from(panel().querySelectorAll('[aria-label="Checks"] li')).map((li) => li.textContent);
    expect(listed()).toEqual(["testFailed"]);
    await act(async () => button("Show 1 other check").click());
    expect(listed()).toEqual(["testFailed", "buildPassed"]);
  } finally {
    vi.useRealTimers();
  }
});

it("reads only the failing checks from an older daemon", async () => {
  entries = [mine(2, { checks: "failing" })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/prs/checks")
      ? new Response("404 page not found", { status: 404 })
      : String(args[0]).includes("api/prs/failing-checks")
        ? Response.json({ head_sha: "head", checks: [{ name: "test", conclusion: "failure" }] })
        : original(...args),
  );
  await render();
  expect(
    Array.from(panel().querySelectorAll('[aria-label="Checks"] li')).map((li) => li.textContent),
  ).toEqual(["testFailed"]);
});

it("shows where a pull request merges, its size and time to review, and opens its checks on asking", async () => {
  entries = [
    requested(1, {
      review_minutes: 12,
      checks: "passing",
      pull_request: {
        head_ref: "feature/x",
        base_ref: "main",
        additions: 1200,
        deletions: 34,
        changed_files: 9,
      },
    }),
  ];
  await render();
  const facts = Array.from(panel().querySelectorAll(".upnext-facts > div")).map((fact) =>
    Array.from(fact.children)
      .map((part) => part.textContent)
      .join(": "),
  );
  expect(facts).toEqual([
    "Branch: feature/x→main",
    "Size: +1,200 −34 · 9 files",
    "Time to review: ~12 min review",
  ]);
  expect(panel().querySelector(".upnext-branches a")!.getAttribute("href")).toBe(
    "https://github.com/acme/demo/pull/1",
  );
  expect(panel().querySelector(".upnext-branches a")!.getAttribute("aria-label")).toBe(
    "Open acme/demo#1 on GitHub",
  );
  // A review's checks are folded but read at once, to count them beside the
  // row's title; opening the row shows that read without asking again.
  const checksAsked = () =>
    vi.mocked(fetch).mock.calls.filter(([path]) => String(path).includes("api/prs/checks"));
  expect(button("Checks passing").getAttribute("aria-expanded")).toBe("false");
  expect(checksAsked()).toHaveLength(1);
  await act(async () => button("Checks passing").click());
  expect(checksAsked()).toHaveLength(1);
});

it("formats Markdown in the review summary, risk explanation and section titles", async () => {
  const doc = structuredClone(example);
  doc.summary.text =
    "Uses `topRepositories` with **required** setup.\n\n- Pick a repository\n- [Read the guide](https://example.com/guide)";
  const reason = doc.summary.risk_score.reasons[0]!;
  reason.text = "Only `20` repositories appear; *search* is limited.";
  doc.sections.find((section) => section.id === reason.section)!.title = "Require `repository`";
  entries = [fixture()[2]!];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/document") ? Response.json(doc) : original(...args),
  );
  await render();
  const summary = panel().querySelector(".upnext-lead")!;
  expect(summary.querySelector("code")!.textContent).toBe("topRepositories");
  expect(summary.querySelector("strong")!.textContent).toBe("required");
  expect(summary.querySelectorAll("ul > li")).toHaveLength(2);
  expect(summary.querySelector("a")!.getAttribute("href")).toBe("https://example.com/guide");
  expect(panel().querySelector(".upnext-risk .upnext-prose code")!.textContent).toBe("20");
  expect(panel().querySelector(".upnext-risk em")!.textContent).toBe("search");
  expect(panel().querySelector(".upnext-block-text code")!.textContent).toBe("repository");
});

it("shows a hunk ID in the review's prose as a chip for its file and line", async () => {
  const doc = structuredClone(example);
  const hunk = doc.index.hunks[0]!;
  const reason = doc.summary.risk_score.reasons[0]!;
  reason.text = `Skips events already handled (${hunk.id}).`;
  doc.summary.text = `See \`${hunk.id}\` and h_000000000000.`;
  entries = [fixture()[2]!];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/document") ? Response.json(doc) : original(...args),
  );
  await render();
  const name = hunk.path.slice(hunk.path.lastIndexOf("/") + 1);
  // Up next has no file view, so the chip opens the hunk on GitHub.
  for (const prose of [
    panel().querySelector(".upnext-risk .upnext-prose")!,
    panel().querySelector(".upnext-lead")!,
  ]) {
    expect(prose.textContent).not.toContain(hunk.id);
    const chip = prose.querySelector("a")!;
    expect(chip.textContent).toMatch(new RegExp(`^${name}`));
    expect(chip.getAttribute("href")).toContain("https://github.com/");
  }
  // An ID the review does not have stays as written.
  expect(panel().querySelector(".upnext-lead")!.textContent).toContain("h_000000000000");
});

it("lists the files that conflict with the base", async () => {
  entries = [mine(2, { merge: "conflict" })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/prs/conflicts")
      ? Response.json({
          head_sha: "head",
          base_ref: "main",
          base_sha: "base",
          files: ["go.sum", "internal/app/app.go"],
        })
      : original(...args),
  );
  await render();
  expect(
    Array.from(panel().querySelectorAll('[aria-label="Conflicting files"] li')).map(
      (li) => li.textContent,
    ),
  ).toEqual(["go.sum", "internal/app/app.go"]);
});

it("opens an unresolved thread to its whole conversation", async () => {
  entries = [mine(2, { unresolved: 1 })];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).includes("api/prs/review-threads")
      ? Response.json({
          capped: false,
          threads: [
            {
              id: "t1",
              path: "a.go",
              line: 3,
              outdated: false,
              author: "sam",
              excerpt: "Why not a map?",
              comments: 3,
              url: "https://github.com/acme/demo/pull/2#r1",
              can_resolve: true,
              messages: [
                { author: "sam", body: "Why not a map?\n\nIt would be O(1)." },
                { author: "me", body: "Order matters here." },
              ],
            },
          ],
        })
      : original(...args),
  );
  await render();
  const toggle = button("Why not a map?");
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  await act(async () => toggle.click());
  expect(
    Array.from(panel().querySelectorAll('[aria-label="Conversation"] li')).map(
      (li) => li.textContent,
    ),
  ).toEqual([
    "samWhy not a map?\n\nIt would be O(1).",
    "meOrder matters here.",
    "1 more comment on GitHub",
  ]);
});

it("places a stack by its first shown open member and opens to its members", async () => {
  entries = [ready(3), mine(2, { checks: "failing" }), requested(1), ready(4)];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (...args) =>
    String(args[0]).endsWith("api/groups")
      ? Response.json({
          groups: [
            {
              id: "g1",
              repository: "acme/demo",
              name: "Payments stack",
              members: [2, 3],
              stack: true,
              created_at: "",
              updated_at: "",
              version_count: 0,
              latest: null,
              risk_score: null,
              reviewed: [],
              outdated: false,
            },
          ],
        })
      : original(...args),
  );
  await render();
  // The stack follows #2's failing checks, even though #3 is ready to merge.
  expect(groups()).toEqual(["Needs your eyes · 1", "Ready to merge · 1", "Hand to an agent · 2"]);
  expect(list()).toEqual(["Change 1", "Change 4", "Payments stack"]);
  await press("j");
  await press("j");
  expect(shown()).toBe("Payments stack");
  expect(panel().textContent).toContain("There is no group review yet");
  expect(
    Array.from(panel().querySelectorAll('[aria-label="Pull requests"] li')).map(
      (li) => li.textContent,
    ),
  ).toEqual(["Change 2#2 · Checks failing", "Change 3#3 · Ready to merge"]);
  // A stack's members stay out of Merge all.
  expect(node.textContent).not.toContain("Merge all");
  // Space opens the group, and from a member closes it again, back on the group.
  await press(" ");
  expect(list()).toEqual(["Change 1", "Change 4", "Payments stack", "Change 2", "Change 3"]);
  await press("j");
  expect(shown()).toBe("Change 2");
  await press(" ");
  expect(shown()).toBe("Payments stack");
  expect(list()).toEqual(["Change 1", "Change 4", "Payments stack"]);
  // On the group itself it opens and closes it.
  await press(" ");
  expect(list()).toEqual(["Change 1", "Change 4", "Payments stack", "Change 2", "Change 3"]);
  await press(" ");
  expect(list()).toEqual(["Change 1", "Change 4", "Payments stack"]);
});

it.each([
  [true, "review", "merge", true, "review"],
  [true, "waiting", "agent", true, "waiting"],
  [true, "merge", "agent", true, "merge"],
  [true, "agent", "merge", true, "agent"],
  [true, "agent", "waiting", true, "agent"],
  [false, "waiting", "merge", true, "merge"],
  [true, "waiting", "agent", false, "agent"],
] as const)(
  "places stack=%s, first=%s, later=%s, first visible=%s in %s",
  (stack, first, later, visible, expected) => {
    const firstRow = { key: "first", entry: mine(1), waiting: first === "waiting" };
    const laterRow = { key: "later", entry: mine(2), waiting: later === "waiting" };
    const sections = ["review", "merge", "agent", "waiting"].map((id) => ({
      id,
      rows: [...(visible && id === first ? [firstRow] : []), ...(id === later ? [laterRow] : [])],
    }));
    const group: ReviewGroup = {
      id: "stack",
      repository: "acme/demo",
      name: "Stack",
      members: [1, 2],
      stack,
      created_at: "",
      updated_at: "",
      version_count: 0,
      latest: null,
      risk_score: null,
      reviewed: [],
      outdated: false,
    };
    const placed = withGroups(sections, [group]);
    expect(placed.flatMap(({ id, lines }) => lines.map((line) => [id, line.key]))).toEqual([
      [expected, "group:stack"],
    ]);
    const line = placed.flatMap(({ lines }) => lines)[0]!;
    if (!("members" in line)) throw Error("expected a grouped row");
    expect(line.members.map(({ key }) => key)).toEqual(visible ? ["first", "later"] : ["later"]);
  },
);

it.each(["1 hour", "Until it changes"])(
  "hides a stack when every member is snoozed for %s, including one waiting on its base",
  async (duration) => {
    entries = [
      mine(1, {
        merge: "ready",
        checks: "passing",
        pull_request: { head_ref: "one", base_ref: "main" },
      }),
      mine(2, {
        merge: "ready",
        checks: "passing",
        pull_request: { head_ref: "two", base_ref: "one" },
      }),
    ];
    const original = globalThis.fetch;
    vi.stubGlobal("fetch", (...args: Parameters<typeof fetch>) =>
      String(args[0]).endsWith("api/groups")
        ? Response.json({
            groups: [
              {
                id: "s1",
                repository: "acme/demo",
                name: "Stack",
                members: [1, 2],
                stack: true,
                created_at: "",
                updated_at: "",
                version_count: 0,
                latest: null,
                risk_score: null,
                reviewed: [],
                outdated: false,
              },
            ],
          })
        : original(...args),
    );
    await render();
    await press(" ");
    await act(async () => row("Change 1").click());
    await press("s");
    await act(async () => snoozeFor(duration).click());
    expect(list()).toContain("Stack");
    await act(async () => row("Change 2").click());
    expect(panel().textContent).toContain("Waiting on #1");
    expect(button("Snooze")).toBeDefined();
    await press("s");
    await act(async () => snoozeFor(duration).click());
    expect(list()).not.toContain("Stack");
    const snoozed = node.querySelector('section[aria-label="Snoozed"]')!;
    expect(snoozed.textContent).toContain("Change 1");
    expect(snoozed.textContent).toContain("Change 2");
    await render();
    expect(list()).not.toContain("Stack");
    await act(async () => button("Snoozed").click());
    await act(async () =>
      node
        .querySelector<HTMLButtonElement>('section[aria-label="Snoozed"] .row-menu-button')!
        .click(),
    );
    await act(async () => snoozeFor("Bring back").click());
    expect(list()).toContain("Stack");
  },
);

it("links the agent sessions working on the pull request to their cards on Sessions, opened", async () => {
  liveList = [
    {
      agent: "claude-code",
      session_id: "s1",
      state: "running",
      started_at: "",
      last_activity_at: "",
      last_event: "tool",
      repository: "acme/demo",
      pr: 1,
    },
  ];
  await render();
  expect(shown()).toBe("Change 1");
  const link = panel().querySelector<HTMLAnchorElement>(".upnext-agents a")!;
  expect(link.textContent).toBe("1 agent session working on its branch right now");
  expect(link.getAttribute("href")).toBe("/s/test-ns/?page=sessions&peek=claude-code%3As1");
  await act(async () => link.click());
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  // Sessions shows the list with that session's card open, not its transcript.
  expect(new URLSearchParams(location.search).get("session")).toBeNull();
  expect(node.querySelector('[data-row-id="claude-code:s1"]')).not.toBeNull();
  expect(node.querySelector(".local-feed-peek-row")).not.toBeNull();
});

// --- Everyone else's pull requests -------------------------------------------

it("lists everyone else's open pull requests that wait on nothing from the person, by number", () => {
  const { items, waiting, others } = upNext([
    entry(9),
    entry(6, { pull_request: { author: "sam" } }),
    // Merged or closed, in the app only as history or by its state: not open.
    entry(7, { historical: true, pull_request: { state: "MERGED" } }),
    entry(8, { pull_request: { state: "CLOSED" } }),
    // A second clone of the same pull request lists it once.
    { ...entry(6), clone_id: "other-clone" },
    entry(5, { pull_request: { draft: true } }),
    // The person's own waits under their own, and what asks for them is queued.
    mine(10, { checks: "running" }),
    requested(11),
  ]);
  expect(others.map((e) => e.pull_request.number)).toEqual([5, 6, 9]);
  expect(waiting.map((e) => e.pull_request.number)).toEqual([10]);
  expect(items.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["review", 11],
  ]);
});

it("treats a pull request assigned to the person, not theirs, as a review, and a clone to make", () => {
  const assigned = (number: number, overrides: Overrides = {}) =>
    entry(number, {
      ...overrides,
      pull_request: { assigned_to_me: true, author: "sam", ...overrides.pull_request },
    });
  const { items, others } = upNext([
    assigned(1),
    assigned(2, { needs_clone: true, clone_id: undefined }),
    // Assigned to the person and closed: nothing to do.
    assigned(3, { pull_request: { state: "CLOSED" } }),
  ]);
  expect(items.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["review", 1],
    ["clone", 2],
  ]);
  expect(others).toEqual([]);
  // One the person wrote, assigned to themselves, follows the path of their own.
  const own = upNext([
    mine(4, {
      merge: "ready",
      checks: "passing",
      pull_request: { assigned_to_me: true },
    }),
    mine(5, { checks: "running", pull_request: { assigned_to_me: true } }),
    mine(6, { checks: "failing", pull_request: { assigned_to_me: true } }),
  ]);
  expect(own.items.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["merge", 4],
    ["fix", 6],
  ]);
  expect(own.waiting.map((e) => e.pull_request.number)).toEqual([5]);
  expect(own.others).toEqual([]);
});

it("says where someone else's pull request stands", () => {
  const state = (overrides: Overrides = {}) => othersState(entry(1, overrides));
  expect(state()).toBe("Unclaimed");
  expect(state({ pull_request: { draft: true } })).toBe("Draft");
  expect(state({ merge: "draft" })).toBe("Draft");
  // A draft is a draft, whoever is asked.
  expect(state({ pull_request: { draft: true, assigned: true } })).toBe("Draft");
  expect(state({ pull_request: { assigned: true } })).toBe("Assigned");
  expect(state({ pull_request: { review_requested: true } })).toBe("Review requested");
  expect(state({ pull_request: { assigned: true, review_requested: true } })).toBe(
    "Review requested",
  );
  expect(state({ review: "approved", pull_request: { assigned: true } })).toBe("Approved");
  expect(state({ review: "changes_requested", pull_request: { review_requested: true } })).toBe(
    "Changes requested",
  );
  // Nobody holds it: unclaimed, whatever reviews it has.
  expect(state({ review: "approved" })).toBe("Unclaimed");
});

it("joins the queue's items and the others as what the inboxes match", () => {
  const { items, others } = upNext(fixture());
  const all = queueItems({ queue: items, others });
  expect(all.map((item) => [item.kind, item.entry.pull_request.number])).toEqual([
    ["review", 1],
    ["merge", 3],
    ["merge", 4],
    ["fix", 2],
    ["other", 6],
  ]);
  expect(all.at(-1)!.key).toBe("acme/demo#6");
});

it("lists someone else's pull request under Not waiting on you, with where it stands", async () => {
  entries = [
    ...fixture(),
    entry(7, { pull_request: { author: "kim", draft: true } }),
    entry(8, { review: "approved", pull_request: { author: "lee", assigned: true } }),
  ].map((e) => (e.pull_request.number === 6 ? entry(6, { pull_request: { author: "ana" } }) : e));
  await render();
  expect(groups().at(-1)).toBe("Not waiting on you · 3");
  expect(list().slice(-3)).toEqual(["Change 6", "Change 7", "Change 8"]);
  expect(row("Change 6").textContent).toContain("#6 · Unclaimed");
  expect(row("Change 7").textContent).toContain("#7 · Draft");
  expect(row("Change 8").textContent).toContain("#8 · Approved");
  // They wait on nothing from the person: not counted as the queue.
  expect(node.querySelector(".local-rail-heading .local-rail-badge")?.textContent).toBe("4");
  expect(progress()).toBe("0 of 4");
  await act(async () => row("Change 6").click());
  expect(shown()).toBe("Change 6");
  expect(panel().textContent).toContain("By ana");
  expect(panel().querySelector(".upnext-chip-waiting")!.textContent).toBe("Unclaimed");
  await act(async () => row("Change 8").click());
  expect(panel().textContent).toContain("By lee");
  expect(panel().querySelector(".upnext-chip-waiting")!.textContent).toBe("Approved");
});

it("says a pull request is assigned to the person in the panel", async () => {
  entries = [
    ...fixture(),
    // Assigned to the person: asked of them, so in the queue as a review.
    entry(7, { pull_request: { author: "kim", assigned_to_me: true } }),
  ];
  await render();
  expect(groups()[0]).toBe("Needs your eyes · 2");
  await act(async () => row("Change 7").click());
  expect(panel().textContent).toContain("kim's, assigned to you");
});

// Gives the app an inbox with this query.
const withQuery = (query: string) => {
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation(async (path, init) =>
    String(path).endsWith("api/inboxes") && init?.method !== "PUT"
      ? Response.json({
          available: true,
          revision: 1,
          inboxes: [{ ...DEFAULT_INBOX, query }],
        })
      : original(path, init),
  );
};

it("lists those others match with waiting=other alone, and leaves them out of the person's own scope", async () => {
  withQuery("waiting=other");
  await render();
  expect(groups()).toEqual(["Not waiting on you · 1"]);
  expect(list()).toEqual(["Change 6"]);
  // With nothing actionable, the first of them is open, not a zero state.
  expect(shown()).toBe("Change 6");
  expect(panel().textContent).toContain("By someone else");
  expect(node.textContent).not.toContain("Nothing here right now.");
  withQuery("waiting=review");
  await render();
  expect(groups()).toEqual(["Needs your eyes · 1"]);
  withQuery(FOR_ME);
  await render();
  expect(groups()).not.toContain("Not waiting on you · 1");
  expect(list()).not.toContain("Change 6");
  expect(list()).toContain("Change 1");
});

it("snoozes someone else's pull request under Snoozed", async () => {
  await render();
  await act(async () => row("Change 6").click());
  await press("s");
  await press("1");
  expect(node.querySelector('[role="status"]')!.textContent).toMatch(/^Snoozed #6 until /);
  expect(groups()).toContain("Snoozed · 1");
  expect(groups()).not.toContain("Not waiting on you · 1");
  await act(async () => button("Snoozed").click());
  expect(node.querySelector<HTMLElement>(".upnext-snoozed")!.textContent).toContain("Change 6");
  // Undo brings it back under Not waiting on you.
  await press("u");
  expect(groups()).toContain("Not waiting on you · 1");
  expect(groups()).not.toContain("Snoozed · 1");
});

it("counts a review as done once its request goes, while the pull request is listed as not waiting on the person", async () => {
  await render();
  expect(await cleared()).toBeNull();
  // The review request is withdrawn: nothing is asked of the person now.
  entries = entries.map((e) => (e.pull_request.number === 1 ? entry(1, { status: "current" }) : e));
  await render();
  expect(await cleared()).toBe("Reviewed #1 Change 1");
  expect(groups()).toContain("Not waiting on you · 2");
  expect(list()).toEqual(expect.arrayContaining(["Change 1", "Change 6"]));
  expect(progress()).toBe("1 of 4");
});

it("metadataStale: stale by the repository flag or by fetch age over two minutes", () => {
  const now = Date.parse("2026-01-01T12:00:00Z");
  const at = (seconds: number) => new Date(now - seconds * 1000).toISOString();
  expect(metadataStale({ stale: false, metadata_fetched_at: at(30) }, now)).toBe(false);
  expect(metadataStale({ stale: false, metadata_fetched_at: at(120) }, now)).toBe(false);
  expect(metadataStale({ stale: false, metadata_fetched_at: at(121) }, now)).toBe(true);
  expect(metadataStale({ stale: false }, now)).toBe(false);
  expect(metadataStale({ stale: true }, now)).toBe(true);
  expect(metadataStale({ stale: true, metadata_fetched_at: at(1) }, now)).toBe(true);
});
