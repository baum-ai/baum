import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import example from "../../schema/review-example.json" with { type: "json" };
import { App } from "./app";
import { Inbox } from "./inbox";
import { LocalReview } from "./review";
import { liveFor, parseLive, type Entry, type LiveSession } from "./api";
import { forgetFoldedSections } from "./sessions";
import { forgetLastRows } from "./shortcuts";

vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, placeholder, notices }: ReviewViewerProps) => (
    <div>
      {notices}
      {document ? null : placeholder}
    </div>
  ),
}));

const entry = (number: number, repository = "acme/demo"): Entry => ({
  pull_request: {
    repository,
    number,
    head_sha: "head",
    title: `Change ${number}`,
    url: `https://github.com/${repository}/pull/${number}`,
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: false,
    review_requested_from_me: false,
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
  risk_score: null,
  historical: false,
  stale: false,
  review: "",
  checks: "",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
});
const session = (over: Partial<LiveSession> = {}): LiveSession => ({
  agent: "claude-code",
  session_id: "s1",
  state: "running",
  started_at: new Date(Date.now() - 5 * 60_000).toISOString(),
  last_activity_at: new Date(Date.now() - 20_000).toISOString(),
  last_event: "tool",
  repository: "acme/demo",
  branch: "feature",
  pr: 7,
  ...over,
});

let root: Root, node: HTMLDivElement;
let liveBody: unknown;
let liveFails = false;
const streams: EventTarget[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  // Every open pull request; the default Inbox view has its own test.
  history.replaceState(null, "", "/s/test-ns/?page=pulls&filter=all");
  localStorage.clear();
  liveBody = { sessions: [] };
  liveFails = false;
  streams.length = 0;
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class extends EventTarget {
      constructor() {
        super();
        streams.push(this);
      }
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string) =>
      path === "/s/test-ns/api/live-sessions"
        ? liveFails
          ? new Response("failed", { status: 500 })
          : Response.json(liveBody)
        : path === "/s/test-ns/api/health"
          ? Response.json({})
          : path.startsWith("/s/test-ns/api/document")
            ? Response.json(example)
            : path.startsWith("/s/test-ns/api/versions")
              ? Response.json([])
              : path === "/s/test-ns/api/repos" || path === "/s/test-ns/api/repositories"
                ? Response.json([
                    {
                      id: "clone",
                      repository: "acme/demo",
                      available: true,
                      path: "/fixture",
                      settings: { automatic: false },
                    },
                  ])
                : path.startsWith("/s/test-ns/api/index")
                  ? Response.json({ entries: [entry(7), entry(8)], problems: [] })
                  : path === "/s/test-ns/api/jobs"
                    ? Response.json({ jobs: [] })
                    : path === "/s/test-ns/api/prs"
                      ? Response.json([])
                      : path === "/s/test-ns/api/background"
                        ? Response.json([])
                        : path.startsWith("/s/test-ns/api/sessions")
                          ? Response.json({ available: true, sessions: [], commits: [] })
                          : new Response("missing", { status: 404 }),
    ),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
  forgetFoldedSections();
});

it("keeps sessions in progress of the pull request, across clones, and none that ended", () => {
  const sessions = [
    session(),
    session({ session_id: "s2", state: "idle" }),
    session({ session_id: "s3", state: "ended" }),
    session({ session_id: "s4", pr: 8 }),
    session({ session_id: "s5", pr: undefined }),
    session({ session_id: "s6", repository: "other/demo" }),
  ];
  expect(
    liveFor(sessions, { repository: "ACME/Demo", number: 7 }).map((s) => s.session_id),
  ).toEqual(["s1", "s2"]);
  expect(parseLive({ available: false, code: "live_sessions_unavailable" })).toEqual([]);
  expect(parseLive(null)).toEqual([]);
  expect(parseLive({ sessions: [session(), { nope: 1 }] })).toHaveLength(1);
});

it("marks inbox rows whose pull request has a session in progress", async () => {
  await act(async () =>
    root.render(
      <Inbox
        rows={[entry(7), entry(8), entry(9)]}
        cloneFor={() => "clone"}
        selection={new Set()}
        setSelection={() => {}}
        Link={(props) => <a {...props} />}
        live={[session(), session({ session_id: "s2", pr: 8, state: "idle" })]}
      />,
    ),
  );
  const rows = Array.from(node.querySelectorAll("li"));
  expect(rows[0]!.textContent).toContain("Agent running");
  expect(rows[1]!.textContent).toContain("Agent idle");
  expect(rows[2]!.textContent).not.toContain("Agent");
});

it("shows the inbox indicator from the live-sessions request and its live event", async () => {
  liveBody = { sessions: [session()] };
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Agent running");
  liveBody = { sessions: [session({ state: "idle" })] };
  await act(async () => streams[0]!.dispatchEvent(new Event("live")));
  expect(node.textContent).toContain("Agent idle");
  expect(node.textContent).not.toContain("Agent running");
  liveBody = { sessions: [session({ state: "ended" })] };
  await act(async () => streams[0]!.dispatchEvent(new Event("live")));
  expect(node.textContent).not.toContain("Agent");
});

it("keeps the sessions shown when rereading them fails", async () => {
  liveBody = { sessions: [session()] };
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Agent running");
  liveFails = true;
  await act(async () => streams[0]!.dispatchEvent(new Event("live")));
  expect(node.textContent).toContain("Agent running");
});

const sidebarBadge = () =>
  node.querySelector('a[href$="?page=sessions"] .local-rail-badge')?.textContent ?? null;

it("shows running and recently ended sessions on the Sessions page, updated by the live event", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=sessions");
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("No agent sessions are running");
  // Ended recently starts folded, its rows and its note with it.
  expect(
    node.querySelector('[aria-label="Ended recently"] button')!.getAttribute("aria-expanded"),
  ).toBe("false");
  expect(node.textContent).not.toContain("No sessions ended in the last day");
  expect(sidebarBadge()).toBeNull();
  liveBody = {
    sessions: [
      session(),
      session({ session_id: "s2", state: "idle", agent: "codex", pr: undefined, branch: "wip" }),
      session({
        session_id: "s3",
        state: "ended",
        last_event: "end",
        started_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
        last_activity_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
        ended_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
      }),
    ],
  };
  await act(async () => streams[0]!.dispatchEvent(new Event("live")));
  const running = node.querySelector('[aria-label="Running now"]')!;
  expect(running.querySelectorAll("li")).toHaveLength(2);
  // The agent by its mark alone.
  expect(running.textContent).not.toContain("Claude Code");
  expect(running.querySelector('img[alt="Claude Code"]')).not.toBeNull();
  expect(running.querySelector(".local-session-repo")!.textContent).toBe("acme/demo");
  expect(running.querySelector(".local-session-branch")!.textContent).toBe("feature");
  expect(running.textContent).toMatch(/4m 59s|5m 0\ds/);
  expect(running.textContent).toContain("Active 20 seconds ago");
  expect(running.querySelector('img[alt="Codex"]')).not.toBeNull();
  expect(running.textContent).toContain("Quiet for");
  expect(running.querySelector('a[href$="&pr=7"]')?.textContent).toBe("PR #7");
  expect(running.querySelector("li:nth-child(2) a.local-session-pr")).toBeNull();
  // Every row opens its transcript.
  expect(running.querySelector("li a.local-feed-open")?.getAttribute("href")).toBe(
    "/s/test-ns/?page=sessions&session=claude-code%3As1",
  );
  const ended = node.querySelector('[aria-label="Ended recently"]')!;
  await act(async () => ended.querySelector<HTMLButtonElement>("button[aria-expanded]")!.click());
  expect(ended.querySelectorAll("li")).toHaveLength(1);
  expect(ended.textContent).toContain("Ended 2 hours ago");
  expect(ended.textContent).toContain("1.0 h");
  // Only sessions that are working count on the sidebar.
  expect(sidebarBadge()).toBe("1");
  liveBody = { sessions: [session({ state: "ended", ended_at: new Date().toISOString() })] };
  await act(async () => streams[0]!.dispatchEvent(new Event("live")));
  expect(sidebarBadge()).toBeNull();
  expect(node.querySelector('[aria-label="Running now"]')!.textContent).toContain(
    "No agent sessions are running",
  );
  expect(node.querySelector('[aria-label="Ended recently"]')!.querySelectorAll("li")).toHaveLength(
    1,
  );
});

it("puts the sessions waiting on the person in Needs you, longest wait first, and counts them on the sidebar", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
  try {
    history.replaceState(null, "", "/s/test-ns/?page=sessions");
    await act(async () => root.render(<App />));
    expect(node.querySelector('[aria-label="Needs you"]')).toBeNull();
    const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
    liveBody = {
      sessions: [
        session(),
        session({
          session_id: "finished",
          state: "idle",
          last_event: "stop",
          waiting_reason: "turn_finished",
          waiting_since: ago(2),
        }),
        session({
          session_id: "asking",
          agent: "codex",
          state: "idle",
          last_event: "wait",
          waiting_reason: "permission",
          waiting_since: ago(4),
          branch: "wip",
          pr: undefined,
        }),
        session({
          session_id: "question",
          state: "idle",
          last_event: "wait",
          waiting_reason: "input",
          waiting_since: new Date(Date.now() - 30_000).toISOString(),
          waiting_message: "Which database should the migration use?",
          title: "Plan the orders migration",
          summary: "Move orders to the new schema without downtime.",
        }),
        // A reason the app does not know, from a newer daemon, is not listed.
        session({
          session_id: "later",
          state: "idle",
          waiting_reason: "something_new" as never,
          waiting_since: ago(9),
        }),
        session({
          session_id: "over",
          state: "ended",
          last_event: "end",
          ended_at: ago(1),
          waiting_reason: "permission",
          waiting_since: ago(3),
        }),
      ],
    };
    await act(async () => streams[0]!.dispatchEvent(new Event("live")));
    // What asks something is Needs you; a finished turn, Waiting for you.
    const needs = node.querySelector('[aria-label="Needs you"]')!;
    const waitingForYou = node.querySelector('[aria-label="Waiting for you"]')!;
    const finished = waitingForYou.querySelector("li")!;
    const asked = [...needs.querySelectorAll("li")];
    const rows = [asked[0]!, finished, asked[1]!];
    expect(asked.map((r) => r.getAttribute("data-row-id"))).toEqual([
      "codex:asking",
      "claude-code:question",
    ]);
    expect(finished.getAttribute("data-row-id")).toBe("claude-code:finished");
    expect(needs.querySelector("h2")!.textContent).toBe("Needs you 2");
    expect(waitingForYou.querySelector("h2")!.textContent).toBe("Waiting for you 1");
    expect(rows[0]!.querySelector("img")!.getAttribute("alt")).toBe("Codex");
    expect(rows[0]!.textContent).toContain("waiting 4m 00s");
    expect(rows[0]!.textContent).toContain("Asks permission in its terminal");
    expect(rows[0]!.querySelector(".local-feed-mono")!.textContent).toBe("wip");
    expect(rows[0]!.querySelector(".local-needs-message")).toBeNull();
    expect(rows[1]!.textContent).not.toContain("in its terminal");
    expect(rows[2]!.textContent).toContain("Asks a question in its terminal");
    expect(rows[2]!.textContent).toContain("waiting 30s");
    expect(rows[2]!.querySelector(".local-needs-message")!.textContent).toBe(
      "Which database should the migration use?",
    );
    expect(rows[2]!.querySelector("h3")!.textContent).toBe("Plan the orders migration");
    expect(rows[2]!.querySelector(".local-feed-branch")!.textContent).toBe("feature");
    expect(rows[0]!.querySelector("a.local-feed-open")).not.toBeNull();
    expect(rows[2]!.querySelector("a.local-feed-open")?.getAttribute("href")).toBe(
      "/s/test-ns/?page=sessions&session=claude-code%3Aquestion",
    );
    // They are not listed again under Running now; the working session and
    // the one of an unknown reason are.
    const running = node.querySelector('[aria-label="Running now"]')!;
    expect([...running.querySelectorAll("li")].map((r) => r.getAttribute("data-row-id"))).toEqual([
      "claude-code:s1",
      "claude-code:later",
    ]);
    // The sidebar counts what asks the person something, in the caution tone.
    expect(sidebarBadge()).toBe("2");
    expect(
      node
        .querySelector('a[href$="?page=sessions"] .local-rail-badge')!
        .classList.contains("caution"),
    ).toBe(true);
    // The wait's length goes on without an event.
    await act(async () => vi.advanceTimersByTime(60_000));
    expect(rows[2]!.textContent).toContain("waiting 1m 30s");
    expect(needs.querySelector("li")!.textContent).toContain("waiting 5m 00s");
    // Once nothing waits, the section goes and the badge counts the running ones.
    liveBody = { sessions: [session()] };
    await act(async () => streams[0]!.dispatchEvent(new Event("live")));
    expect(node.querySelector('[aria-label="Needs you"]')).toBeNull();
    expect(sidebarBadge()).toBe("1");
    expect(
      node
        .querySelector('a[href$="?page=sessions"] .local-rail-badge')!
        .classList.contains("caution"),
    ).toBe(false);
  } finally {
    vi.useRealTimers();
  }
});

async function renderReview(live: LiveSession[]) {
  await act(async () =>
    root.render(
      <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
        <LocalReview
          entry={entry(7)}
          clone="clone"
          revision={0}
          changed={async () => {}}
          generate={() => {}}
          live={live}
        />
      </UrlStateProvider>,
    ),
  );
}

it("lists sessions in progress on the pull request page and has no section without them", async () => {
  await renderReview([
    session({ title: "Add a retry budget" }),
    session({ session_id: "s2", state: "idle", agent: "codex" }),
  ]);
  const section = node.querySelector('[aria-label="Sessions in progress"]')!;
  expect(section.querySelector(".local-pr-session-title")?.textContent).toBe("Add a retry budget");
  expect(section.textContent).toContain("Claude Code");
  expect(section.textContent).toContain("Working");
  expect(section.textContent).toContain("feature");
  expect(section.textContent).toContain("Last activity 20 seconds ago");
  expect(section.textContent).toContain("Codex");
  expect(section.textContent).toContain("Waiting for you");
  // Each session opens its transcript, as on the Sessions page.
  expect(section.querySelectorAll('a[aria-label^="Open transcript"]')).toHaveLength(2);
  await renderReview([session({ pr: 8 })]);
  expect(node.querySelector('[aria-label="Sessions in progress"]')).toBeNull();
});

it("reads the sessions from disk again once a session in progress ends", async () => {
  const sessionReads = () =>
    vi.mocked(fetch).mock.calls.filter(([url]) => String(url).startsWith("/s/test-ns/api/sessions"))
      .length;
  await renderReview([session()]);
  const first = sessionReads();
  expect(first).toBeGreaterThan(0);
  // Another session starting is not an end.
  await renderReview([session(), session({ session_id: "s2" })]);
  expect(sessionReads()).toBe(first);
  await renderReview([session({ session_id: "s2" })]);
  expect(sessionReads()).toBe(first + 1);
  expect(node.textContent).toContain("Sessions in progress");
  await renderReview([]);
  expect(sessionReads()).toBe(first + 2);
  expect(node.querySelector('[aria-label="Sessions in progress"]')).toBeNull();
});

it("rereads a session's decisions once for a burst of events", async () => {
  history.replaceState(null, "", "/s/test-ns/?pr=7&clone=clone");
  liveBody = { sessions: [session()] };
  const reads = () =>
    vi
      .mocked(fetch)
      .mock.calls.filter(([url]) => String(url).startsWith("/s/test-ns/api/decisions?agent="))
      .length;
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Sessions in progress");
  const before = reads();
  expect(before).toBeGreaterThan(0);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    await act(async () => {
      for (const kind of ["decisions", "decisions", "open", "decisions"])
        streams[0]!.dispatchEvent(new Event(kind));
    });
    expect(reads()).toBe(before);
    await act(async () => {
      vi.advanceTimersByTime(250);
    });
  } finally {
    vi.useRealTimers();
  }
  await act(async () => {});
  expect(reads()).toBe(before + 1);
});
