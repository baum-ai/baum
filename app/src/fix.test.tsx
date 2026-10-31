import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  fixEligible,
  fixJob,
  fixLabel,
  fixName,
  LocalRequestError,
  reviewJob,
  startFix,
  threadOutcomesSummary,
  type Entry,
  type Job,
} from "./api";
import { FixNotice } from "./fix";
import { Inbox } from "./inbox";
import { forgetLastRows } from "./shortcuts";

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
const openActions = async (node: HTMLElement, name: string) =>
  act(async () =>
    node.querySelector<HTMLButtonElement>(`button[aria-label="Actions for ${name}"]`)!.click(),
  );
const choices = (node: HTMLElement) =>
  Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]')).map(
    (item) => item.querySelector(".context-menu-label")!.textContent,
  );
const variant = (patch: Partial<Entry>, pr: Partial<Entry["pull_request"]> = {}): Entry => ({
  ...broken,
  ...patch,
  pull_request: { ...broken.pull_request, ...pr },
});
let root: Root, node: HTMLDivElement;
// What POST api/jobs answers: the queued fix, or a refusal.
let answer: () => Response;
const requests: { path: string; body: unknown }[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  requests.length = 0;
  answer = () => Response.json({ jobs: [{ id: "fix-job", kind: "fix" }] });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      requests.push({ path, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return answer();
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
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
  await openActions(node, "acme/demo #8");
  expect(choices(node)).toContain("Fix conflicts and CI");
  await act(async () =>
    Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]'))
      .find((item) => item.textContent?.startsWith("Fix conflicts and CI"))!
      .click(),
  );
  expect(fix).toHaveBeenCalledWith(broken);
  await openActions(node, "acme/demo #9");
  expect(choices(node)).not.toContain("Fix conflicts and CI");
  await act(async () =>
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })),
  );
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
  await openActions(node, "acme/demo #8");
  expect(choices(node).some((label) => label?.startsWith("Fix"))).toBe(false);
  expect(node.querySelector(".inbox-activities")!.textContent).toContain("Fixing…");
  // From the click, before the daemon answers.
  await act(async () =>
    root.render(
      <Inbox
        rows={[broken]}
        cloneFor={(entry) => entry.clone_id ?? ""}
        selection={new Set()}
        setSelection={() => {}}
        Link={Link}
        fix={fix}
        fixStarting={() => true}
      />,
    ),
  );
  await openActions(node, "acme/demo #8");
  expect(choices(node).some((label) => label?.startsWith("Fix"))).toBe(false);
  expect(node.querySelector(".inbox-activities")!.textContent).toContain("Fixing…");
});

it("starts a fix at once, with no estimate or confirmation", async () => {
  const jobs = await startFix("clone", 8);
  expect(requests).toEqual([
    {
      path: "/s/test-ns/api/jobs",
      body: { items: [{ clone: "clone", pr: 8, kind: "fix" }], mode: "single" },
    },
  ]);
  expect(jobs).toEqual([{ id: "fix-job", kind: "fix" }]);
});

it("says in words why the daemon will not start a fix", async () => {
  answer = () => Response.json({ blocked_reason: "no_push_access" }, { status: 409 });
  const refusal = await startFix("clone", 8).catch((e: unknown) => e);
  expect(refusal).toBeInstanceOf(LocalRequestError);
  expect((refusal as LocalRequestError).message).toBe(
    "Your GitHub account can't push to this repository.",
  );
  expect((refusal as LocalRequestError).code).toBe("no_push_access");
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

it("starts a fix made from a conversation, sending only its ids", async () => {
  await startFix("clone", 8, undefined, { conversation_id: "c1", turn: 2 });
  expect(requests[0]).toEqual({
    path: "/s/test-ns/api/jobs",
    body: {
      items: [{ clone: "clone", pr: 8, kind: "fix", fix_chat: { conversation_id: "c1", turn: 2 } }],
      mode: "single",
    },
  });
});

it("reports why a conversation cannot be fixed from", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ blocked_reason: "chat_unavailable" }), { status: 409 }),
    ),
  );
  await expect(
    startFix("clone", 8, undefined, { conversation_id: "c1", turn: 1 }),
  ).rejects.toMatchObject({ code: "chat_unavailable" });
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
