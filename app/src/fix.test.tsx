import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  fixEligible,
  fixJob,
  fixLabel,
  fixName,
  reviewJob,
  threadOutcomesSummary,
  type Entry,
  type Estimate,
  type Job,
} from "./api";
import { FixDialog, FixNotice } from "./fix";
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

it("offers review comments only on the person's own pull request with unresolved threads", () => {
  const clean = { merge: "ready" as const, checks: "passing" as const };
  expect(fixLabel(variant({ ...clean, unresolved: 2 }))).toBe("Fix review comments");
  expect(fixLabel(variant({ ...clean, unresolved: 2 }, { authored_by_me: false }))).toBeNull();
  expect(fixLabel(variant({ ...clean, unresolved: 0 }))).toBeNull();
  expect(fixLabel(variant({ unresolved: 3 }))).toBe("Fix conflicts, CI and review comments");
  expect(fixLabel(variant({ merge: "ready", unresolved: 3 }))).toBe("Fix CI and review comments");
  expect(fixName({ conflicts: true, threads: "digest" })).toBe("Fix conflicts and review comments");
  expect(fixName({ checks: true })).toBe("Fix CI");
});

it("lists the review threads and says baum replies and resolves", async () => {
  answer = estimate({
    fix: { threads: "digest", head_ref: "feature", base_ref: "main" },
    fix_threads: [
      { ref: "T1", path: "src/a.go", line: 12, author: "alice", excerpt: "Rename this." },
      { ref: "T2", path: "src/b.go", outdated: true, author: "bob", excerpt: "Why a map?" },
    ],
    fix_threads_capped: true,
    fix_threads_untrusted: 2,
  });
  await act(async () =>
    root.render(
      <FixDialog entry={broken} clone="clone" close={() => {}} started={async () => {}} />,
    ),
  );
  const text = node.textContent ?? "";
  expect(node.querySelector("h2")?.textContent).toBe("Fix review comments");
  expect(text).toContain("An agent addresses the unresolved review comments on feature.");
  expect(text).toContain("baum replies on each thread the agent addressed and resolves it");
  const threads = node.querySelectorAll('[aria-label="Review threads"] li');
  expect(Array.from(threads).map((li) => li.textContent)).toEqual([
    "src/a.go:12alice: Rename this.",
    "src/b.go outdatedbob: Why a map?",
  ]);
  expect(text).toContain("Only the first 2 unresolved threads are included.");
  expect(text).toContain(
    "2 threads started by accounts without access to the repository are left out.",
  );
  expect(
    Array.from(node.querySelectorAll("button")).some(
      (button) => button.textContent === "Fix review comments",
    ),
  ).toBe(true);
});

it("shows what became of each review thread after the push", async () => {
  expect(threadOutcomesSummary([])).toBeNull();
  expect(
    threadOutcomesSummary([
      { ref: "T1", outcome: "resolved" },
      { ref: "T2", outcome: "replied" },
      { ref: "T3", outcome: "failed" },
      { ref: "T4", outcome: "skipped" },
    ]),
  ).toBe(
    "Replied and resolved 1 of 4 threads; replied on 1 more without resolving; 1 reply failed",
  );
  const job = {
    id: "f",
    kind: "fix",
    repository: "acme/demo",
    pr: 8,
    state: "completed",
    phase: "finished",
    commit: "0123456789",
    fix: { threads: "digest", head_ref: "feature" },
    fix_threads: [
      { ref: "T1", path: "src/a.go", line: 12 },
      { ref: "T2", path: "src/b.go" },
    ],
    thread_outcomes: [
      { ref: "T1", outcome: "resolved", summary: "Renamed it." },
      { ref: "T2", outcome: "skipped", summary: "A question for the author." },
    ],
  } as Job;
  await act(async () => root.render(<FixNotice job={job} changed={async () => {}} />));
  const text = node.textContent ?? "";
  expect(text).toContain("Fix review comments: pushed 0123456 to feature");
  expect(text).toContain("Replied and resolved 1 of 2 threads");
  expect(text).toContain("src/a.go:12 Replied and resolved: Renamed it.");
  expect(text).toContain("src/b.go Left open: A question for the author.");
});

it("confirms a fix made from a conversation, sending only its ids", async () => {
  answer = estimate({
    fix: { chat: "c1", chat_turn: 2, head_ref: "feature", base_ref: "main" },
    fix_chat: { conversation_id: "c1", turn: 2 },
  });
  const started = vi.fn(async () => {});
  await act(async () =>
    root.render(
      <FixDialog
        entry={broken}
        clone="clone"
        chat={{ conversation_id: "c1", turn: 2, suggestion: "Lower the cap to 30s" }}
        close={() => {}}
        started={started}
      />,
    ),
  );
  // The daemon reads the conversation: no question, answer or suggestion goes up.
  expect(requests[0]).toEqual({
    path: "/s/test-ns/api/jobs/estimate",
    body: {
      items: [{ clone: "clone", pr: 8, kind: "fix", fix_chat: { conversation_id: "c1", turn: 2 } }],
      mode: "single",
    },
  });
  const text = node.textContent ?? "";
  expect(text).toContain("Uses this conversation as context");
  expect(text).toContain("Lower the cap to 30s");
  expect(text).toContain("makes the change the conversation asks for");
  const confirm = Array.from(node.querySelectorAll("button")).find(
    (button) => button.textContent === "Fix from this answer",
  )!;
  await act(async () => confirm.click());
  expect(requests[1]).toEqual({
    path: "/s/test-ns/api/jobs",
    body: { estimate_id: "estimate", confirm_estimate: true },
  });
  expect(started).toHaveBeenCalled();
});

it("shows why a conversation cannot be fixed from", async () => {
  answer = estimate({ blocked_reason: "chat_unavailable" });
  await act(async () =>
    root.render(
      <FixDialog
        entry={broken}
        clone="clone"
        chat={{ conversation_id: "c1", turn: 1 }}
        close={() => {}}
        started={async () => {}}
      />,
    ),
  );
  expect(node.textContent).toContain("Uses this conversation as context");
  expect(node.textContent).toContain("no answer to fix from");
});

it("names a fix from a conversation and offers it whenever a fix may run", () => {
  expect(fixName({ chat: "c1" })).toBe("Fix from this answer");
  // It is never combined with the other fixes.
  expect(fixName({ checks: true, chat: "c1" })).toBe("Fix from this answer");
  const clean = variant({ merge: "ready", checks: "passing" });
  expect(fixLabel(clean)).toBeNull();
  expect(fixEligible(clean)).toBe(true);
  expect(fixEligible(variant({}, { fork: true }))).toBe(false);
  expect(fixEligible(variant({}, { viewer_can_push: false }))).toBe(false);
  expect(fixEligible(variant({}, { state: "MERGED" }))).toBe(false);
  expect(fixEligible(variant({ needs_clone: true }))).toBe(false);
});
