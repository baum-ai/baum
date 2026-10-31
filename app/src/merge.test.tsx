import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Inbox } from "./inbox";
import { MergeDialog, type MergeOptions } from "./merge";
import type { Entry } from "./api";
import { forgetLastRows } from "./shortcuts";

const entry = (number: number, merge: Entry["merge"] = "ready"): Entry => ({
  pull_request: {
    repository: "acme/demo",
    number,
    title: `Change ${number}`,
    url: `https://github.com/acme/demo/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: true,
    review_requested_from_me: false,
    author_has_write: true,
    team_membership_known: true,
    review: "approved",
    checks: "passing",
    merge,
    unresolved: 0,
    unresolved_capped: false,
  },
  clone_id: "clone",
  status: "current",
  version_count: 1,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "approved",
  checks: "passing",
  merge,
  unresolved: 0,
  unresolved_capped: false,
  review_minutes: null,
});
const options: MergeOptions = {
  methods: ["MERGE", "SQUASH"],
  default: "SQUASH",
  state: "OPEN",
  draft: false,
  head_sha: "head",
  mergeable: "MERGEABLE",
  merge_state_status: "CLEAN",
};
let root: Root;
let node: HTMLDivElement;
let posts: unknown[];
let mergeAnswer: Response;
let optionsAnswer: MergeOptions;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  posts = [];
  optionsAnswer = options;
  mergeAnswer = Response.json({ merged: true });
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string, init?: RequestInit) => {
      if (path.includes("api/prs/merge-options")) return Response.json(optionsAnswer);
      posts.push(JSON.parse(String(init?.body)));
      return mergeAnswer;
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
});
const button = (text: string) =>
  Array.from(node.querySelectorAll("button")).find((b) => b.textContent?.includes(text))!;
const render = async (merged = async () => {}, close = () => {}) => {
  await act(async () =>
    root.render(<MergeDialog entry={entry(7)} close={close} merged={merged} />),
  );
};

it("offers only the allowed methods, preselects GitHub's default and merges the shown head", async () => {
  const merged = vi.fn(async () => {});
  const close = vi.fn();
  await render(merged, close);
  const radios = Array.from(node.querySelectorAll<HTMLInputElement>('input[type="radio"]'));
  expect(radios.map((r) => r.value)).toEqual(["MERGE", "SQUASH"]);
  expect(radios.find((r) => r.checked)?.value).toBe("SQUASH");
  await act(async () => radios[0]!.click());
  await act(async () => button("Merge pull request").click());
  expect(posts).toEqual([{ repository: "acme/demo", pr: 7, method: "MERGE", head_sha: "head" }]);
  expect(merged).toHaveBeenCalledOnce();
  expect(close).toHaveBeenCalledOnce();
});

it("shows GitHub's refusal in the dialog and stays open", async () => {
  mergeAnswer = Response.json(
    { code: "blocked", message: "Required checks have not passed yet." },
    { status: 422 },
  );
  const close = vi.fn();
  const merged = vi.fn(async () => {});
  await render(merged, close);
  await act(async () => button("Merge pull request").click());
  expect(node.textContent).toContain("Required checks have not passed yet.");
  expect(merged).not.toHaveBeenCalled();
  expect(close).not.toHaveBeenCalled();
  expect(button("Merge pull request").disabled).toBe(false);
});

it("will not merge when the head moved since the inbox was read", async () => {
  optionsAnswer = { ...options, head_sha: "newer" };
  await render();
  expect(node.textContent).toContain("new commits");
  expect(button("Merge pull request").disabled).toBe(true);
  expect(posts).toEqual([]);
});

it("offers Merge on ready rows only and opens the dialog from the row", async () => {
  const rows = [entry(1), entry(2, "blocked"), entry(3, "conflict")];
  await act(async () =>
    root.render(
      <Inbox
        rows={rows}
        cloneFor={() => "clone"}
        selection={new Set()}
        setSelection={() => {}}
        Link={({ children, ...props }) => <a {...props}>{children}</a>}
      />,
    ),
  );
  const choices = () =>
    Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]')).filter(
      (item) => item.querySelector(".context-menu-label")?.textContent === "Merge…",
    );
  const openActions = (name: string) =>
    act(async () =>
      node.querySelector<HTMLButtonElement>(`button[aria-label="Actions for ${name}"]`)!.click(),
    );
  for (const name of ["acme/demo #2", "acme/demo #3"]) {
    await openActions(name);
    expect(choices()).toHaveLength(0);
  }
  await openActions("acme/demo #1");
  expect(choices()).toHaveLength(1);
  await act(async () => choices()[0]!.click());
  expect(node.querySelector("dialog")?.textContent).toContain("Merge pull request #1?");
});
