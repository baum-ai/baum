import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import example from "../../schema/review-example.json" with { type: "json" };
import { App } from "./app";
import {
  automaticLabel,
  groupJob,
  reviewJob,
  type Clone,
  type Entry,
  type Job,
  type ReviewGroup,
} from "./api";
import { inboxGroups } from "./inbox";
import { forgetLastRows } from "./shortcuts";

// The group page loads on demand; have its module ready so a render inside
// act() finds it resolved.
beforeAll(() => import("./group"));

const rail = vi.hoisted(() => {
  const held = {
    commit: null as string | null,
    file: null as string | null,
    // The live review threads the page last handed the viewer.
    liveThreads: undefined as ReviewViewerProps["liveThreads"],
  };
  return Object.assign(held, {
    hold: (liveThreads: ReviewViewerProps["liveThreads"]) => {
      held.liveThreads = liveThreads;
      return null;
    },
  });
});
vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({
    document,
    header,
    placeholder,
    notices,
    sessions,
    commits,
    decisions,
    files,
    liveThreads,
  }: ReviewViewerProps) => (
    <div>
      {rail.hold(liveThreads)}
      <h1>{header?.heading}</h1>
      {header?.titleAside}
      {header?.subtitle}
      {header?.statistics}
      {header?.beside}
      {notices}
      {document ? <p>{document.summary.text}</p> : placeholder}
      {decisions ? (
        <section aria-label="Decisions">{decisions.renderPane({} as never)}</section>
      ) : null}
      {sessions ? (
        <section aria-label="Sessions">{sessions.renderPane(null, {} as never)}</section>
      ) : null}
      {commits ? (
        <section aria-label="Commits">{commits.renderPane(rail.commit, {} as never)}</section>
      ) : null}
      {files?.files ? (
        <section aria-label="Files">
          <ul>
            {files.files.map((file) => (
              <li key={file.id}>{file.path}</li>
            ))}
          </ul>
          {rail.file
            ? files.renderFiles({ files: files.files, open: { id: rail.file, at: 0 } } as never)
            : null}
        </section>
      ) : null}
    </div>
  ),
}));

const entry = (number: number, repository = "acme/demo", historical = false): Entry => ({
  pull_request: {
    repository,
    number,
    title: `Change ${number}`,
    url: `https://github.com/${repository}/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: number === 2,
    review_requested_from_me: number === 1,
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
  risk_score: { score: 2, status: "assessed" },
  historical,
  stale: false,
  review: "",
  checks: "",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
});
const group = (members: number[], extra: Partial<ReviewGroup> = {}): ReviewGroup => ({
  id: "g1",
  repository: "acme/demo",
  name: "Auth rework",
  members,
  created_at: "2026-10-26T10:00:00Z",
  updated_at: "2026-10-26T10:00:00Z",
  version_count: 0,
  latest: null,
  risk_score: null,
  reviewed: [],
  outdated: false,
  ...extra,
});
const clone: Clone = {
  id: "clone",
  path: "/work/demo",
  repository: "acme/demo",
  available: true,
  settings: { automatic: null },
};

let root: Root, node: HTMLDivElement;
let entries: Entry[], groups: ReviewGroup[], jobs: Job[];
let requests: { path: string; method: string; body: unknown }[];
let jobAnswer: () => Response;
// Answers of the daemon's per-pull-request routes, by path and query.
let answers: Record<string, unknown>;
beforeEach(() => {
  answers = {};
  rail.commit = null;
  rail.file = null;
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  // Every open pull request; the default Inbox view hides other people's.
  history.replaceState(null, "", "/s/test-ns/?page=pulls&filter=all");
  localStorage.clear();
  entries = [];
  groups = [];
  jobs = [];
  requests = [];
  jobAnswer = () => Response.json({ jobs: [] });
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
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
    mockStateFetch(async (input: string, init?: RequestInit) => {
      const path = String(input).replace("/s/test-ns/", "");
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      requests.push({ path, method, body });
      if (path in answers) return Response.json(answers[path]);
      if (method === "POST" && path === "api/groups")
        return Response.json(group([1, 2], { name: body.name }), { status: 201 });
      if (method === "PATCH" && path === "api/groups/g1") return Response.json(groups[0]);
      if (method === "POST" && path === "api/jobs") return jobAnswer();
      if (path.startsWith("api/groups/g1/document")) return Response.json(example);
      if (path.startsWith("api/groups/g1/versions")) return Response.json([]);
      if (path === "api/groups") return Response.json({ available: true, groups });
      if (path.startsWith("api/index")) return Response.json({ entries, problems: [] });
      if (path === "api/repos") return Response.json([clone]);
      if (path === "api/jobs") return Response.json({ jobs });
      if (path === "api/github-auth")
        return Response.json({ state: "disconnected", host: "github.com", saved: false });
      if (path === "api/repositories" || path === "api/prs?view=health") return Response.json([]);
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
const buttons = (scope: ParentNode = node) =>
  Array.from(scope.querySelectorAll<HTMLButtonElement>("button"));
const press = (label: string, scope: ParentNode = node) =>
  act(async () =>
    buttons(scope)
      .find((b) => b.textContent?.includes(label))!
      .click(),
  );
const selectAll = () =>
  act(async () =>
    node
      .querySelector<HTMLInputElement>('input[aria-label="Select all visible pull requests"]')!
      .click(),
  );
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const posted = () => requests.filter((r) => r.method !== "GET");

it("creates a group from the selected rows and opens its page", async () => {
  entries = [entry(1), entry(2)];
  await act(async () => root.render(<App />));
  await selectAll();
  await press("Review together (2)");
  const input = node.querySelector<HTMLInputElement>("dialog input")!;
  expect(input.value).toBe("Change 1");
  await press("Create group", node.querySelector("dialog")!);
  expect(posted()).toEqual([
    {
      path: "api/groups",
      method: "POST",
      body: {
        name: "Change 1",
        members: [
          { repository: "acme/demo", number: 1 },
          { repository: "acme/demo", number: 2 },
        ],
      },
    },
  ]);
  expect(location.search).toBe("?page=pulls&group=g1");
  expect(node.textContent).not.toContain("Review together");
});

it("does not group pull requests of several repositories or more than eight", async () => {
  entries = [entry(1), entry(2, "acme/other")];
  await act(async () => root.render(<App />));
  await selectAll();
  await press("Review together (2)");
  expect(node.textContent).toContain(
    "Review together works with pull requests of one repository. Groups across repositories are not supported yet.",
  );
  expect(node.querySelector("dialog")).toBeNull();
  expect(posted()).toEqual([]);
  await act(async () => root.unmount());
  root = createRoot(node);
  entries = Array.from({ length: 9 }, (_, i) => entry(i + 1));
  await act(async () => root.render(<App />));
  await selectAll();
  await press("Review together (9)");
  expect(node.textContent).toContain("A review group holds at most 8 pull requests.");
  expect(posted()).toEqual([]);
});

it("places a group in its highest-priority bucket and nests its shown members", () => {
  const rows = [entry(1), entry(2), entry(3), entry(4, "acme/demo", true)];
  const buckets = inboxGroups(rows, [
    group([2, 3, 9], { id: "mixed" }),
    group([4, 5], { id: "old" }),
    group([8, 9], { id: "absent" }),
  ]);
  expect(
    buckets.map((b) => [
      b.id,
      b.rows.map((r) => r.pull_request.number),
      b.groups.map((g) => [g.group.id, g.members.map((m) => m.pull_request.number)]),
    ]),
  ).toEqual([
    ["requested", [1], []],
    ["mine", [], [["mixed", [2, 3]]]],
    ["history", [], [["old", [4]]]],
  ]);
});

it("renders a group as one row with its members nested when expanded", async () => {
  entries = [entry(1), entry(2), entry(3)];
  groups = [group([2, 3], { outdated: true, version_count: 1 })];
  await act(async () => root.render(<App />));
  const bucket = (name: string) =>
    node.querySelector<HTMLElement>(`section[aria-label="${name}"]`)!;
  expect(bucket("Your pull requests").textContent).toContain("Auth rework");
  expect(bucket("Your pull requests").textContent).toContain("Group of 2");
  expect(bucket("Your pull requests").textContent).toContain("acme/demo · #2, #3");
  expect(bucket("Your pull requests").textContent).toContain("Group review outdated");
  // One group row, counted as one row; nothing is repeated loose.
  expect(bucket("Your pull requests").textContent).toContain("Your pull requests 1");
  expect(bucket("Other pull requests")).toBeNull();
  expect(node.querySelectorAll('input[aria-label="Select acme/demo #3"]')).toHaveLength(1);
  const toggle = node.querySelector<HTMLButtonElement>('button[aria-label="Expand Auth rework"]')!;
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  expect(
    toggle.getAttribute("aria-controls") &&
      document.getElementById(toggle.getAttribute("aria-controls")!)!.hidden,
  ).toBe(true);
  await act(async () => toggle.click());
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  expect(document.getElementById(toggle.getAttribute("aria-controls")!)!.hidden).toBe(false);
  expect(
    node.querySelector<HTMLAnchorElement>('a[href="/s/test-ns/?page=pulls&group=g1"]')!.textContent,
  ).toBe("Auth rework");
  // Select all covers the loose row and the expanded members.
  await selectAll();
  expect(node.textContent).toContain("Generate reviews (3)");
});

it("shows and hides a focused group's pull requests with Space", async () => {
  entries = [entry(1), entry(2), entry(3)];
  groups = [group([2, 3])];
  await act(async () => root.render(<App />));
  const key = (key: string) =>
    act(async () => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });
  const toggle = node.querySelector<HTMLButtonElement>('button[aria-label="Expand Auth rework"]')!;
  // j moves down to the group's row, wherever its bucket puts it.
  const groupRow = () => node.querySelector('[data-row-id="group:g1"]')!;
  for (let i = 0; i < 3 && !groupRow().hasAttribute("data-focused"); i++) await key("j");
  expect(groupRow().hasAttribute("data-focused")).toBe(true);
  await key(" ");
  expect(toggle.getAttribute("aria-expanded")).toBe("true");
  await key(" ");
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  // A group's row has no peek.
  expect(node.querySelector(".inbox-peek-panel")).toBeNull();
});

it("marks a stack baum grouped as the hosted list does, and a person's group as a group", async () => {
  entries = [entry(1), entry(2), entry(3), entry(4)];
  groups = [
    group([1, 2], { id: "g1", name: "Mine" }),
    group([3, 4], { id: "g2", name: "Stacked", stack: true }),
  ];
  await act(async () => root.render(<App />));
  const row = (name: string) =>
    node.querySelector<HTMLAnchorElement>(`a[title="${name}"]`)!.closest("li")!;
  expect(row("Stacked").querySelector('[title="Stack of 2 pull requests"]')).not.toBeNull();
  expect(row("Stacked").textContent).not.toContain("Group of 2");
  expect(row("Mine").textContent).toContain("Group of 2");
  expect(row("Mine").querySelector('[title^="Stack of"]')).toBeNull();
});

it("labels a stack group's page as a stack", async () => {
  entries = [entry(3), entry(4)];
  groups = [group([3, 4], { id: "g2", name: "Stacked", stack: true })];
  history.replaceState(null, "", "/s/test-ns/?group=g2");
  await act(async () => root.render(<App />));
  expect(
    node.querySelector('[title="baum grouped these stacked pull requests"]')!.textContent,
  ).toBe("Stack");
  expect(node.textContent).not.toContain("Review group");
});

it("counts a stack's merged and closed members in the stack on its row", async () => {
  entries = [entry(3), entry(4)];
  groups = [
    group([3, 4], {
      id: "g2",
      name: "Stacked",
      stack: true,
      landed: [
        { number: 1, state: "merged" },
        { number: 2, state: "closed" },
      ],
    }),
  ];
  await act(async () => root.render(<App />));
  expect(node.querySelector('[title="Stack of 4 pull requests"]')).not.toBeNull();
});

it("shows a stack's merged and closed members first on its page, each marked as it is", async () => {
  entries = [entry(3), entry(4)];
  groups = [
    group([3, 4], {
      id: "g2",
      name: "Stacked",
      stack: true,
      landed: [
        { number: 1, state: "merged" },
        { number: 2, state: "closed" },
        { number: 5, state: "unknown" },
      ],
    }),
  ];
  history.replaceState(null, "", "/s/test-ns/?group=g2");
  await act(async () => root.render(<App />));
  // The pull requests start folded under the title, counted with those landed.
  const toggle = node.querySelector<HTMLButtonElement>(".local-group-toggle")!;
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  expect(node.querySelector("li.local-group-member")).toBeNull();
  await act(async () => toggle.click());
  const rows = [...node.querySelectorAll("li.local-group-member")].map((li) => li.textContent);
  expect(rows).toHaveLength(5);
  expect(rows[0]).toContain("#1");
  expect(rows[0]).toContain("Merged");
  expect(rows[1]).toContain("#2");
  expect(rows[1]).toContain("Closed");
  expect(rows[1]).not.toContain("Merged");
  expect(rows[2]).toContain("#5");
  expect(rows[2]).toContain("No longer open");
  expect(rows[3]).toContain("#3");
  expect(rows[3]).not.toContain("Merged");
  expect(node.querySelectorAll("[data-landed] button")).toHaveLength(0);
});

it("keeps group jobs apart from pull request jobs", () => {
  const base = {
    clone: "clone",
    repository: "acme/demo",
    state: "running",
    phase: "",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
  };
  const forGroup: Job = { ...base, id: "j1", pr: 0, group: "g1" };
  const forPR: Job = { ...base, id: "j2", pr: 7 };
  expect(reviewJob(entry(7), [forGroup, forPR])).toBe(forPR);
  expect(reviewJob(entry(0), [forGroup])).toBeUndefined();
  expect(groupJob(group([1, 2]), [forPR, forGroup])).toBe(forGroup);
});

it("labels a stack's automatic group review like one generated by hand", () => {
  const base = {
    clone: "clone",
    repository: "acme/demo",
    state: "running",
    phase: "",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
  };
  expect(
    automaticLabel({
      ...base,
      id: "j1",
      pr: 0,
      group: "g1",
      automatic: true,
      trigger: "stack_reviewed",
    }),
  ).toBeNull();
  expect(automaticLabel({ ...base, id: "j2", pr: 7, automatic: true, trigger: "new_head" })).toBe(
    "Started automatically because new commits were pushed",
  );
  expect(automaticLabel({ ...base, id: "j3", pr: 7, automatic: true, trigger: "setup" })).toBe(
    "Started automatically when baum was set up",
  );
});

it("shows a group's members and manages them", async () => {
  entries = [entry(7), { ...entry(8), status: "outdated", version_count: 1 }];
  groups = [group([7, 8, 9], { version_count: 1, outdated: true })];
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  expect(node.querySelector("h1")!.textContent).toBe("Auth rework");
  expect(node.querySelector(".local-group-toggle")!.textContent).toBe("3 pull requests");
  await act(async () => node.querySelector<HTMLButtonElement>(".local-group-toggle")!.click());
  expect(node.textContent).toContain("Change 7");
  expect(node.textContent).toContain("Review outdated");
  expect(node.textContent).toContain("#9");
  expect(node.textContent).toContain("Not in the inbox");
  expect(node.textContent).toContain("Members changed since this group review was generated.");
  expect(node.querySelector('a[href="/s/test-ns/?clone=clone&pr=7"]')).not.toBeNull();

  await press("Remove");
  expect(posted().at(-1)).toMatchObject({
    path: "api/groups/g1",
    method: "PATCH",
    body: { members: [8, 9] },
  });

  await press("Rename");
  await type(node.querySelector<HTMLInputElement>("dialog input")!, "Login work");
  await press("Rename", node.querySelector("dialog")!);
  expect(posted().at(-1)).toMatchObject({ method: "PATCH", body: { name: "Login work" } });
});

it("says so when the group does not exist, and links groups on a pull request page", async () => {
  history.replaceState(null, "", "/s/test-ns/?group=gone");
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Review group not found");
  expect(node.querySelector('a[href="/s/test-ns/"]')).not.toBeNull();
});

it("names the review groups a pull request belongs to", async () => {
  entries = [entry(7), entry(8)];
  groups = [group([7, 8])];
  history.replaceState(null, "", "/s/test-ns/?clone=clone&pr=7");
  await act(async () => root.render(<App />));
  // The group is a link on the line under the title, named by its kind.
  const link = node.querySelector<HTMLAnchorElement>(
    'a.local-group-link[href="/s/test-ns/?group=g1"]',
  )!;
  expect(link.textContent).toBe("GroupAuth rework");
});

const local = (sessions: object[], commits: object[]) => ({ available: true, sessions, commits });
const commitOf = (oid: string, session: string, committedAt: string) => ({
  oid,
  subject: `Commit ${oid}`,
  committed_at: committedAt,
  links: [{ session, method: "commit_ref", confidence: 1 }],
});

it("shows Decisions, Sessions and Commits in a stack's rail, read through each member", async () => {
  entries = [
    { ...entry(7), status: "current", version_count: 1 },
    { ...entry(8), status: "current", version_count: 1 },
  ];
  groups = [group([7, 8], { version_count: 1, stack: true })];
  const shared = { id: "s-shared", vendor: "claude", title: "Shared session" };
  answers = {
    "api/sessions?clone=clone&pr=7": local(
      [shared, { id: "s-seven", vendor: "claude", title: "Seven session" }],
      [commitOf("b".repeat(40), "s-shared", "2026-10-26T12:00:00Z")],
    ),
    "api/sessions?clone=clone&pr=8": local(
      [shared],
      [
        commitOf("a".repeat(40), "s-shared", "2026-10-26T11:00:00Z"),
        commitOf("b".repeat(40), "s-shared", "2026-10-26T12:00:00Z"),
      ],
    ),
    "api/decisions?repository=acme%2Fdemo&pr=7": {
      review_generated_at: null,
      sessions: [
        {
          agent: "claude",
          session_id: "s-shared",
          title: "Shared session",
          live: false,
          coverage: "extracted",
          updated_at: null,
          capturing: false,
          decisions: [
            {
              decision_id: "d1",
              title: "Use a queue",
              summary: "Queue it",
              category: "design",
              who: "human",
              decided_at: "2026-10-26T10:00:00Z",
              files: [],
            },
          ],
        },
      ],
    },
    [`api/commit?clone=clone&pr=8&oid=${"a".repeat(40)}`]: {
      body: "Body of a",
      files: [{ path: "src/a.ts", status: "M", additions: 1, deletions: 0 }],
    },
  };
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  const sessions = node.querySelector('section[aria-label="Sessions"]')!;
  expect(sessions.textContent).toContain("Seven session");
  expect(sessions.textContent?.match(/Shared session/g)).toHaveLength(1);
  const commits = node.querySelector('section[aria-label="Commits"]')!;
  expect(commits.textContent).toContain("2 commits");
  expect(commits.textContent!.indexOf(`Commit ${"a".repeat(40)}`)).toBeLessThan(
    commits.textContent!.indexOf(`Commit ${"b".repeat(40)}`),
  );
  expect(node.querySelector('section[aria-label="Decisions"]')!.textContent).toContain(
    "Use a queue",
  );

  // A commit opened alone reads its detail through the member that linked it.
  rail.commit = "a".repeat(40);
  await act(async () => root.render(<App />));
  expect(requests.map((r) => r.path)).toContain(
    `api/commit?clone=clone&pr=8&oid=${"a".repeat(40)}`,
  );
});

it("hands the viewer every member's review threads as GitHub has them now", async () => {
  entries = [
    { ...entry(7), status: "current", version_count: 1 },
    { ...entry(8), status: "current", version_count: 1 },
  ];
  entries[0]!.pull_request.unresolved = 1;
  entries[1]!.pull_request.unresolved = 0;
  // A landed member discovery no longer lists: its count is unknown.
  groups = [
    group([7, 8], { version_count: 1, stack: true, landed: [{ number: 9, state: "merged" }] }),
  ];
  answers = {
    "api/prs/review-threads?repository=acme%2Fdemo&pr=7&unresolved=1": {
      threads: [{ id: "T7" }],
      capped: false,
    },
    "api/prs/review-threads?repository=acme%2Fdemo&pr=9&unresolved=-1": {
      threads: [{ id: "T9" }],
      capped: true,
    },
  };
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  const ours = { ...example, review: { ...example.review, repository: "acme/demo" } };
  // One list for the whole group, cut short as one member's is.
  expect(rail.liveThreads?.(ours as never)).toEqual({
    unresolved: new Set(["T7", "T9"]),
    complete: false,
  });
  // With none unresolved, a member's threads are not asked for.
  expect(requests.map((r) => r.path).filter((path) => path.includes("review-threads"))).toEqual([
    "api/prs/review-threads?repository=acme%2Fdemo&pr=9&unresolved=-1",
    "api/prs/review-threads?repository=acme%2Fdemo&pr=7&unresolved=1",
  ]);
  // Another repository's review keeps its own threads.
  expect(rail.liveThreads?.(example as never)).toBeNull();
});

it("keeps reading a removed member's threads while its saved review is displayed", async () => {
  entries = [entry(7)];
  entries[0]!.pull_request.unresolved = 0;
  groups = [group([7], { version_count: 1, outdated: true })];
  const saved = {
    ...example,
    review: { ...example.review, repository: "acme/demo" },
    stack: {
      title: "Saved group",
      members: [7, 8].map((number) => ({ number, head_sha: "head", review_version: 1 })),
    },
  };
  const path = "api/prs/review-threads?repository=acme%2Fdemo&pr=8&unresolved=-1";
  answers = {
    "api/groups/g1/document": saved,
    [path]: { threads: [{ id: "T8" }], capped: false },
  };
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  expect(requests.map((r) => r.path)).toContain(path);
  expect(rail.liveThreads?.(saved as never)).toEqual({
    unresolved: new Set(["T8"]),
    complete: true,
  });
});

it("shows a stack member's file patch once however many of its hunks the review cites", async () => {
  entries = [{ ...entry(7), status: "current", version_count: 1 }];
  groups = [group([7], { version_count: 1, stack: true })];
  const patch = "@@ -1,1 +1,1 @@\n-old line\n+only once\n";
  // The daemon answers the member's whole file patch for each cited hunk.
  for (const hunk of example.index.hunks.filter((h) => h.path === "queue/store.go"))
    answers[`api/groups/g1/patch?version=3&path=queue%2Fstore.go&hunk=${hunk.id}`] = { patch };
  rail.file = "queue/store.go";
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  const files = node.querySelector('section[aria-label="Files"]')!;
  expect(files.textContent?.match(/only once/g)).toHaveLength(1);
});

it("lists and reads a stack's files for the earlier version on screen", async () => {
  entries = [{ ...entry(7), status: "current", version_count: 2 }];
  groups = [group([7], { version_count: 2, stack: true })];
  const earlier = {
    ...example,
    review: { ...example.review, version: 2 },
    index: {
      ...example.index,
      hunks: example.index.hunks
        .filter((h) => h.path === "queue/store.go")
        .map((h) => ({ ...h, path: "queue/earlier.go" })),
    },
  };
  answers["api/groups/g1/document?version=2"] = earlier;
  answers["api/groups/g1/versions"] = [{ version: 2 }, { version: 3 }];
  rail.file = "queue/earlier.go";
  history.replaceState(null, "", "/s/test-ns/?group=g1&review=2");
  await act(async () => root.render(<App />));
  const files = node.querySelector('section[aria-label="Files"]')!;
  expect(Array.from(files.querySelectorAll("li"), (li) => li.textContent)).toEqual([
    "queue/earlier.go",
  ]);
  expect(
    requests.filter((r) => r.path.startsWith("api/groups/g1/patch")).map((r) => r.path),
  ).toSatisfy(
    (paths: string[]) =>
      paths.length > 0 && paths.every((path) => path.includes("version=2&path=queue%2Fearlier.go")),
  );
});
