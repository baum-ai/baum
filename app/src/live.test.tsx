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
  history.replaceState(null, "", "/s/test-ns/?filter=all");
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
    vi.fn(async (path: string) =>
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
  expect(node.textContent).toContain("No sessions ended in the last day");
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
  expect(running.textContent).toContain("Claude Code");
  expect(running.textContent).toContain("Working");
  expect(running.textContent).toContain("acme/demo · feature");
  expect(running.textContent).toContain("Started 5 minutes ago");
  expect(running.textContent).toContain("Active 20 seconds ago");
  expect(running.textContent).toContain("Codex");
  expect(running.textContent).toContain("Waiting for you");
  expect(running.querySelector('a[href$="&pr=7"]')?.textContent).toBe("PR #7");
  expect(running.querySelector("li:nth-child(2) a")).toBeNull();
  const ended = node.querySelector('[aria-label="Ended recently"]')!;
  expect(ended.querySelectorAll("li")).toHaveLength(1);
  expect(ended.textContent).toContain("Ended 2 hours ago");
  expect(ended.textContent).toContain("Lasted 1 h");
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
  await renderReview([session(), session({ session_id: "s2", state: "idle", agent: "codex" })]);
  const section = node.querySelector('[aria-label="Sessions in progress"]')!;
  expect(section.textContent).toContain("Claude Code");
  expect(section.textContent).toContain("Working");
  expect(section.textContent).toContain("feature");
  expect(section.textContent).toContain("Started 5 minutes ago");
  expect(section.textContent).toContain("Last activity 20 seconds ago");
  expect(section.textContent).toContain("Codex");
  expect(section.textContent).toContain("Waiting for you");
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
