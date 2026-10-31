import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Entry } from "./api";
import {
  canRerunChecks,
  canUpdateBranch,
  RerunChecksDialog,
  UpdateBranchDialog,
  type RerunOptions,
  type UpdateOptions,
} from "./unblock";

const entry = (
  patch: Partial<Entry["pull_request"]> = {},
  checks: Entry["checks"] = "failing",
): Entry => ({
  pull_request: {
    repository: "acme/demo",
    number: 7,
    title: "Change 7",
    url: "https://github.com/acme/demo/pull/7",
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: true,
    review_requested_from_me: false,
    author_has_write: true,
    team_membership_known: true,
    review: "approved",
    checks,
    merge: "blocked",
    unresolved: 0,
    unresolved_capped: false,
    viewer_can_push: true,
    behind: true,
    ...patch,
  },
  clone_id: "clone",
  status: "current",
  version_count: 1,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "approved",
  checks,
  merge: "blocked",
  unresolved: 0,
  unresolved_capped: false,
  review_minutes: null,
});
const updateOptions: UpdateOptions = {
  state: "OPEN",
  draft: false,
  head_sha: "head",
  head_ref: "feature",
  base_ref: "main",
  behind: true,
  conflict: false,
  fork: false,
};
const rerunOptions: RerunOptions = {
  state: "OPEN",
  head_sha: "head",
  runs: [
    { id: 11, name: "CI" },
    { id: 12, name: "E2E" },
  ],
  not_rerunnable: ["ci/circle"],
};
let root: Root;
let node: HTMLDivElement;
let posts: { path: string; body: unknown }[];
let optionsAnswer: unknown;
let writeAnswer: Response;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  posts = [];
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string, init?: RequestInit) => {
      if (init?.method === "GET") return Response.json(optionsAnswer);
      posts.push({ path: String(path), body: JSON.parse(String(init?.body)) });
      return writeAnswer;
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const button = (text: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent?.includes(text))!;

it("offers Update branch only to someone who can push to an open, clean, behind pull request", () => {
  expect(canUpdateBranch(entry())).toBe(true);
  expect(canUpdateBranch(entry({ behind: false }))).toBe(false);
  expect(canUpdateBranch(entry({ behind: undefined }))).toBe(false);
  expect(canUpdateBranch(entry({ draft: true }))).toBe(false);
  expect(canUpdateBranch(entry({ fork: true }))).toBe(false);
  expect(canUpdateBranch(entry({ viewer_can_push: false }))).toBe(false);
  expect(canUpdateBranch(entry({ viewer_can_push: null }))).toBe(false);
  expect(canUpdateBranch(entry({ state: "CLOSED" }))).toBe(false);
  expect(canUpdateBranch({ ...entry(), historical: true })).toBe(false);
});

it("offers Re-run checks only for failing checks to someone who can push", () => {
  expect(canRerunChecks(entry())).toBe(true);
  expect(canRerunChecks(entry({}, "passing"))).toBe(false);
  expect(canRerunChecks(entry({}, "running"))).toBe(false);
  expect(canRerunChecks(entry({ viewer_can_push: false }))).toBe(false);
  expect(canRerunChecks(entry({ state: "MERGED" }))).toBe(false);
});

it("confirms an update with the branches named, then sends the head the page showed", async () => {
  optionsAnswer = updateOptions;
  writeAnswer = Response.json({ updating: true });
  const updated = vi.fn(async () => {});
  await act(async () =>
    root.render(<UpdateBranchDialog entry={entry()} close={() => {}} updated={updated} />),
  );
  expect(node.textContent).toContain("merge main into feature");
  await act(async () => button("Update branch").click());
  expect(posts).toEqual([
    {
      path: expect.stringContaining("api/prs/update-branch"),
      body: { repository: "acme/demo", pr: 7, head_sha: "head" },
    },
  ]);
  expect(updated).toHaveBeenCalledOnce();
  expect(node.textContent).toContain("GitHub accepted the update");
  expect(button("Close")).toBeTruthy();
  expect(node.textContent).not.toContain("Cancel");
});

it("confirms from the keyboard with Enter, since Tab moves nothing", async () => {
  optionsAnswer = updateOptions;
  writeAnswer = Response.json({ updating: true });
  await act(async () =>
    root.render(<UpdateBranchDialog entry={entry()} close={() => {}} updated={async () => {}} />),
  );
  // The confirm button holds the focus once GitHub's answer allows the write.
  expect(document.activeElement).toBe(button("Update branch"));
  await act(async () => {
    node
      .querySelector("dialog")!
      .dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  expect(posts).toHaveLength(1);
});

it("answers an update click at once and shows GitHub's refusal", async () => {
  optionsAnswer = updateOptions;
  let reply!: (response: Response) => void;
  writeAnswer = undefined as unknown as Response;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_path: string, init?: RequestInit) =>
      init?.method === "GET"
        ? Response.json(optionsAnswer)
        : new Promise<Response>((resolve) => (reply = resolve)),
    ),
  );
  const close = vi.fn();
  await act(async () =>
    root.render(<UpdateBranchDialog entry={entry()} close={close} updated={async () => {}} />),
  );
  await act(async () => void button("Update branch").click());
  expect(button("Updating…").disabled).toBe(true);
  await act(async () =>
    reply(
      Response.json(
        { code: "head_moved", message: "The pull request changed since you opened it." },
        { status: 409 },
      ),
    ),
  );
  expect(node.textContent).toContain("The pull request changed since you opened it.");
  expect(close).not.toHaveBeenCalled();
  expect(button("Update branch").disabled).toBe(false);
});

it("will not update a conflicting or moved pull request", async () => {
  optionsAnswer = { ...updateOptions, conflict: true };
  await act(async () =>
    root.render(<UpdateBranchDialog entry={entry()} close={() => {}} updated={async () => {}} />),
  );
  expect(node.textContent).toContain("conflicts with its base branch");
  expect(button("Update branch").disabled).toBe(true);
  optionsAnswer = { ...updateOptions, head_sha: "newer" };
  await act(async () =>
    root.render(
      <UpdateBranchDialog key="2" entry={entry()} close={() => {}} updated={async () => {}} />,
    ),
  );
  expect(node.textContent).toContain("new commits");
  expect(posts).toEqual([]);
});

it("lists the runs to re-run and the checks baum cannot re-run, then closes", async () => {
  optionsAnswer = rerunOptions;
  writeAnswer = Response.json({ rerun: rerunOptions.runs, refused: [] });
  const rerun = vi.fn(async () => {});
  const close = vi.fn();
  await act(async () =>
    root.render(<RerunChecksDialog entry={entry()} close={close} rerun={rerun} />),
  );
  const items = Array.from(node.querySelectorAll("li")).map((li) => li.textContent);
  expect(items).toEqual(["CI", "E2E", "ci/circle"]);
  expect(node.textContent).toContain("Not re-runnable from baum");
  await act(async () => button("Re-run failed checks").click());
  expect(posts).toEqual([
    {
      path: expect.stringContaining("api/prs/rerun-checks"),
      body: { repository: "acme/demo", pr: 7, head_sha: "head" },
    },
  ]);
  expect(rerun).toHaveBeenCalledOnce();
  expect(close).toHaveBeenCalledOnce();
});

it("keeps the dialog open to report runs GitHub refused beside the ones it started", async () => {
  optionsAnswer = rerunOptions;
  writeAnswer = Response.json({
    rerun: [{ id: 12, name: "E2E" }],
    refused: [{ name: "CI", message: "The workflow run is too old for GitHub to re-run." }],
  });
  const close = vi.fn();
  await act(async () =>
    root.render(<RerunChecksDialog entry={entry()} close={close} rerun={async () => {}} />),
  );
  await act(async () => button("Re-run failed checks").click());
  expect(node.textContent).toContain("Re-running E2E");
  expect(node.textContent).toContain(
    "GitHub would not re-run CI (The workflow run is too old for GitHub to re-run.)",
  );
  expect(close).not.toHaveBeenCalled();
});

it("has nothing to re-run when only other apps' checks fail", async () => {
  optionsAnswer = { ...rerunOptions, runs: [] };
  await act(async () =>
    root.render(<RerunChecksDialog entry={entry()} close={() => {}} rerun={async () => {}} />),
  );
  expect(node.textContent).toContain("nothing baum can re-run");
  expect(button("Re-run failed checks").disabled).toBe(true);
});
