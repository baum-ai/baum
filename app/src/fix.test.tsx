import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { fixJob, fixLabel, reviewJob, type Entry, type Estimate, type Job } from "./api";
import { FixDialog } from "./fix";
import { Inbox } from "./inbox";

const broken: Entry = {
  pull_request: {
    repository: "acme/demo",
    number: 8,
    head_sha: "head",
    head_ref: "feature",
    title: "Broken",
    url: "https://github.com/acme/demo/pull/8",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: true,
    review_requested_from_me: false,
    author_has_write: true,
    team_membership_known: true,
    viewer_can_push: true,
    review: "",
    checks: "failing",
    merge: "conflict",
    unresolved: null,
    unresolved_capped: false,
  },
  clone_id: "clone",
  status: "no_review",
  version_count: 0,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "",
  checks: "failing",
  merge: "conflict",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
};
const variant = (patch: Partial<Entry>, pr: Partial<Entry["pull_request"]> = {}): Entry => ({
  ...broken,
  ...patch,
  pull_request: { ...broken.pull_request, ...pr },
});
const estimate = (item: Partial<Estimate["items"][number]>): Estimate => ({
  id: "estimate",
  estimate_usd: 1.5,
  price_version: "v1",
  expires_at: "2026-10-26T12:00:00Z",
  items: [
    {
      clone: "clone",
      pr: 8,
      kind: "fix",
      fix: { conflicts: true, head_ref: "feature", base_ref: "main" },
      head_sha: "head",
      backend: "claude",
      model: "claude-sonnet-4-5",
      estimate_usd: 1.5,
      max_tokens: 1000,
      max_calls: 10,
      requires_isolation_confirmation: false,
      ...item,
    },
  ],
});

let root: Root, node: HTMLDivElement, answer: Estimate;
const requests: { path: string; body: unknown }[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  // jsdom has no modal dialogs.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  requests.length = 0;
  answer = estimate({});
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      requests.push({ path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return path.endsWith("api/jobs/estimate")
        ? Response.json(answer)
        : Response.json({ jobs: [{ id: "fix-job", kind: "fix" }] });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

it("offers a fix only for broken pull requests the person can push to", () => {
  expect(fixLabel(broken)).toBe("Fix conflicts and CI");
  expect(fixLabel(variant({ checks: "passing" }))).toBe("Fix conflicts");
  expect(fixLabel(variant({ merge: "ready" }))).toBe("Fix CI");
  expect(fixLabel(variant({ merge: "ready", checks: "passing" }))).toBeNull();
  expect(fixLabel(variant({}, { fork: true }))).toBeNull();
  expect(fixLabel(variant({}, { viewer_can_push: false }))).toBeNull();
  expect(fixLabel(variant({}, { viewer_can_push: null }))).toBeNull();
  expect(fixLabel(variant({ historical: true }))).toBeNull();
  expect(fixLabel(variant({ needs_clone: true }))).toBeNull();
});

it("keeps fix jobs apart from review jobs", () => {
  const review = { id: "r", repository: "acme/demo", pr: 8, state: "running" } as Job;
  const fix = { ...review, id: "f", kind: "fix" } as Job;
  expect(reviewJob(broken, [review, fix])).toBe(review);
  expect(fixJob(broken, [review, fix])).toBe(fix);
  expect(fixJob(broken, [review])).toBeUndefined();
});

it("puts the fix action in the broken status cell, not on a fork", async () => {
  const fix = vi.fn();
  const Link = ({ children, ...props }: ViewerLinkProps) => <a {...props}>{children}</a>;
  await act(async () =>
    root.render(
      <Inbox
        rows={[broken, variant({}, { number: 9, fork: true })]}
        cloneFor={(entry) => entry.clone_id ?? ""}
        selection={new Set()}
        setSelection={() => {}}
        Link={Link}
        fix={fix}
      />,
    ),
  );
  const buttons = node.querySelectorAll<HTMLButtonElement>("button.inbox-fix");
  expect(buttons).toHaveLength(1);
  expect(buttons[0]!.getAttribute("aria-label")).toBe("Fix conflicts and CI");
  await act(async () => buttons[0]!.click());
  expect(fix).toHaveBeenCalledWith(broken);
  await act(async () =>
    root.render(
      <Inbox
        rows={[broken]}
        cloneFor={(entry) => entry.clone_id ?? ""}
        selection={new Set()}
        setSelection={() => {}}
        Link={Link}
        fix={fix}
        jobs={[{ id: "f", kind: "fix", repository: "acme/demo", pr: 8, state: "running" } as Job]}
      />,
    ),
  );
  expect(node.querySelector("button.inbox-fix")).toBeNull();
  expect(node.textContent).toContain("Fixing…");
});

it("confirms the harness, model, branch and write access before starting", async () => {
  const close = vi.fn();
  const started = vi.fn(async () => {});
  await act(async () =>
    root.render(<FixDialog entry={broken} clone="clone" close={close} started={started} />),
  );
  expect(requests[0]).toEqual({
    path: "/s/test-ns/api/jobs/estimate",
    body: { items: [{ clone: "clone", pr: 8, kind: "fix" }], mode: "single" },
  });
  const text = node.textContent ?? "";
  for (const fact of [
    "claude",
    "claude-sonnet-4-5",
    "feature",
    "main",
    "write access",
    "never force-pushes",
  ])
    expect(text).toContain(fact);
  expect(text).toContain("cannot run your tests");
  const confirm = Array.from(node.querySelectorAll("button")).find(
    (button) => button.textContent === "Fix conflicts",
  )!;
  await act(async () => confirm.click());
  expect(requests[1]).toEqual({
    path: "/s/test-ns/api/jobs",
    body: { estimate_id: "estimate", confirm_estimate: true },
  });
  expect(started).toHaveBeenCalled();
  expect(close).toHaveBeenCalled();
});

it("explains a fix that cannot run and offers no confirmation", async () => {
  answer = estimate({ blocked_reason: "no_push_access" });
  await act(async () =>
    root.render(
      <FixDialog entry={broken} clone="clone" close={() => {}} started={async () => {}} />,
    ),
  );
  expect(node.textContent).toContain("can't push to this repository");
  expect(Array.from(node.querySelectorAll("button")).map((b) => b.textContent)).toEqual(["Close"]);
});
