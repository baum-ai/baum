import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Inbox } from "./inbox";
import type { Entry } from "./api";

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
  risk_score: { status: "assessed", score: 5 },
  historical: false,
  stale: false,
  review: "approved",
  checks: "passing",
  merge,
  unresolved: 0,
  unresolved_capped: false,
  review_minutes: 9,
});
const section = (id: string, title: string, additions: number, deletions: number) => ({
  id,
  title,
  kind: "logical",
  hunks: [],
  summary: { text: "", anchors: [] },
  attention: null,
  risks: [],
  evidence: [],
  blocks: [],
  derived: {
    risk_tier: "standard",
    additions,
    deletions,
    role_loc: {},
    reviewers: [],
    state: "current",
    evidence_summary: {},
    open_findings: 0,
  },
});
const doc = {
  schema: "baum.review/1",
  review: {},
  summary: {
    text: "Retries failed jobs with backoff.",
    anchors: [],
    risk_score: {
      rubric: "v1",
      status: "assessed",
      score: 5,
      reasons: [{ section: "auth", risk: null, text: "Any caller can claim the operator role." }],
      limitations: [],
    },
  },
  sections: [
    section("retry", "Retry policy and backoff", 87, 4),
    section("auth", "Role from request", 34, 5),
  ],
};
let root: Root;
let node: HTMLDivElement;
let requests: string[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  requests = [];
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      requests.push(path);
      if (path.includes("api/document")) return Response.json(doc);
      return Response.json({ methods: ["MERGE"], default: "MERGE", head_sha: "head" });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const Link = ({ children, ...props }: { children?: ReactNode; href: string }) => (
  <a {...props}>{children}</a>
);
function List({ rows, initial = [] }: { rows: Entry[]; initial?: string[] }) {
  const [selection, setSelection] = useState(new Set(initial));
  return (
    <Inbox
      rows={rows}
      cloneFor={() => "clone"}
      selection={selection}
      setSelection={setSelection}
      Link={Link}
    />
  );
}
const button = (label: string) =>
  Array.from(node.querySelectorAll("button")).find(
    (b) => b.getAttribute("aria-label") === label || b.textContent === label,
  );
const press = (key: string) =>
  act(async () => {
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
  });

it("peeks at the saved review under its row and closes it again", async () => {
  await act(async () => root.render(<List rows={[entry(1)]} />));
  expect(node.querySelector(".inbox-peek-panel")).toBeNull();
  await act(async () => button("Peek at acme/demo #1")!.click());
  const panel = node.querySelector(".inbox-peek-panel")!;
  expect(requests.some((path) => path.includes("api/document"))).toBe(true);
  expect(panel.textContent).toContain("Retries failed jobs with backoff.");
  // The first reason for its risk names its section, in the high-risk tone.
  expect(panel.querySelector(".inbox-peek-risk")?.textContent).toBe(
    "Role from request: Any caller can claim the operator role.",
  );
  expect(panel.querySelector(".inbox-peek-risk")?.className).toContain("high");
  expect(Array.from(panel.querySelectorAll("li")).map((li) => li.textContent)).toEqual([
    "Retry policy and backoff+87 −4",
    "Role from request+34 −5",
  ]);
  expect(panel.querySelector("a")?.textContent).toBe("Open the review");
  expect(button("Peek at acme/demo #1")!.getAttribute("aria-expanded")).toBe("true");
  await act(async () => button("Peek at acme/demo #1")!.click());
  expect(node.querySelector(".inbox-peek-panel")).toBeNull();
});

it("opens the focused row's peek with Space", async () => {
  await act(async () => root.render(<List rows={[entry(1), entry(2)]} />));
  await press("j");
  await press("j");
  await press(" ");
  const panels = Array.from(node.querySelectorAll(".inbox-peek-panel"));
  expect(panels).toHaveLength(1);
  expect(panels[0]!.getAttribute("aria-label")).toBe("Peek at #2");
  await press(" ");
  expect(node.querySelector(".inbox-peek-panel")).toBeNull();
});

it("says why there is no review to peek at", async () => {
  const unsaved = { ...entry(3), version_count: 0, needs_clone: true, clone_id: undefined };
  await act(async () => root.render(<List rows={[unsaved]} />));
  await act(async () => button("Peek at acme/demo #3")!.click());
  expect(node.querySelector(".inbox-peek-panel")?.textContent).toContain(
    "baum needs a local clone of acme/demo",
  );
  expect(requests.some((path) => path.includes("api/document"))).toBe(false);
});

it("draws the selection's actions in a bar and merges the ready ones together", async () => {
  const rows = [entry(1), entry(2), entry(3, "conflict")];
  const keys = rows.map((row) => `clone:acme/demo:${row.pull_request.number}`);
  await act(async () => root.render(<List rows={rows} initial={keys} />));
  const bar = node.querySelector('[role="toolbar"]')!;
  expect(bar.getAttribute("aria-label")).toBe("Selected pull requests");
  expect(bar.textContent).toContain("3 selected");
  // Only the two ready pull requests can merge.
  await act(async () => button("Merge 2")!.click());
  expect(node.querySelector("dialog")?.textContent).toContain("Merge 2 ready pull requests?");
  await act(async () => button("Clear")!.click());
  expect(node.querySelector('[role="toolbar"]')).toBeNull();
});

it("offers no bulk Merge when nothing selected can merge", async () => {
  const rows = [entry(3, "conflict")];
  await act(async () => root.render(<List rows={rows} initial={["clone:acme/demo:3"]} />));
  expect(node.querySelector('[role="toolbar"]')?.textContent).toBe("1 selectedClear");
});

it("flashes a row whose state changed in the latest read", async () => {
  await act(async () => root.render(<List rows={[entry(1, "blocked")]} />));
  const row = () => node.querySelector<HTMLElement>(".inbox-row")!;
  expect(row().dataset.changed).toBeUndefined();
  await act(async () => root.render(<List rows={[entry(1, "ready")]} />));
  expect(row().dataset.changed).toBe("true");
});
