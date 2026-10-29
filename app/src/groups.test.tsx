import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import example from "../../schema/review-example.json" with { type: "json" };
import { App } from "./app";
import { groupJob, reviewJob, type Clone, type Entry, type Job, type ReviewGroup } from "./api";
import { inboxGroups } from "./inbox";

vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, header, placeholder, notices }: ReviewViewerProps) => (
    <div>
      <h1>{header?.heading}</h1>
      {header?.beside}
      {notices}
      {document ? <p>{document.summary.text}</p> : placeholder}
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
  settings: { automatic: null, backend: null, model: null },
};

let root: Root, node: HTMLDivElement;
let entries: Entry[], groups: ReviewGroup[], jobs: Job[];
let requests: { path: string; method: string; body: unknown }[];
let jobAnswer: () => Response;
beforeEach(() => {
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
    vi.fn(async (input: string, init?: RequestInit) => {
      const path = String(input).replace("/s/test-ns/", "");
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      requests.push({ path, method, body });
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
      if (path === "api/repositories" || path === "api/prs") return Response.json([]);
      return Response.json({});
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
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
  expect(bucket("Your pull requests").textContent).toContain("Your pull requests1");
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
  expect(node.textContent).toContain("Generate selected (3)");
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

it("shows a group's members and manages them", async () => {
  entries = [entry(7), { ...entry(8), status: "outdated", version_count: 1 }];
  groups = [group([7, 8, 9], { version_count: 1, outdated: true })];
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  expect(node.querySelector("h1")!.textContent).toBe("Auth rework");
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

it("offers to generate the reviews a group review builds on", async () => {
  entries = [entry(7), entry(8)];
  groups = [group([7, 8, 9], { version_count: 1 })];
  jobAnswer = () => Response.json({ members_need_reviews: [8, 9] }, { status: 409 });
  history.replaceState(null, "", "/s/test-ns/?group=g1");
  await act(async () => root.render(<App />));
  await press("Regenerate");
  expect(posted().at(-1)).toEqual({
    path: "api/jobs",
    method: "POST",
    body: { items: [{ clone: "clone", group: "g1" }], mode: "single", confirm_isolation: false },
  });
  expect(node.textContent).toContain(
    "A group review builds on each member's own review. #8 and #9 have no review yet.",
  );
  jobAnswer = () => Response.json({ jobs: [] });
  await press("Generate their reviews");
  expect(posted().at(-1)).toEqual({
    path: "api/jobs",
    method: "POST",
    body: {
      items: [
        { clone: "clone", pr: 8 },
        { clone: "clone", pr: 9 },
      ],
      mode: "batch",
      confirm_isolation: false,
    },
  });
  expect(node.textContent).not.toContain("have no review yet");
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
  expect(node.textContent).toContain("In review group Auth rework");
  expect(node.querySelector('a[href="/s/test-ns/?group=g1"]')).not.toBeNull();
});
