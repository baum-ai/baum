import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { TranscriptSession } from "@baum/viewer/transcript";
import type { LiveSession } from "./api";
import {
  LIVE_DEBOUNCE,
  parseLiveTranscript,
  POLL_RUNNING,
  SessionTranscriptPage,
} from "./session-transcript";

// The card is the viewer's; here it shows the prompts it is given and counts
// how often it is drawn.
const drawn = vi.hoisted(() => ({ count: 0, revisions: [] as (string | undefined)[] }));
vi.mock("@baum/viewer/components/session-card", () => ({
  SessionCard: ({
    session,
    source,
  }: {
    session: TranscriptSession;
    source?: { revision?: string };
  }) => {
    drawn.count++;
    drawn.revisions.push(source?.revision);
    return (
      <ol aria-label="Prompts">
        {session.prompts.map((p) => (
          <li key={p.turn_id}>{p.text}</li>
        ))}
      </ol>
    );
  },
}));

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

const prompt = (n: number, text: string) => ({
  turn_id: `turn-${n}`,
  ordinal: n,
  started_at: "2026-10-27T10:00:00Z",
  text,
  text_length: text.length,
  truncated: false,
  effects: null,
  tokens: 0,
  estimated_tokens: 0,
  replies: [],
  replies_omitted: 0,
});

/** The daemon's transcript of a session with these prompts. */
const transcript = (...prompts: string[]) => ({
  session: {
    run_id: "s1",
    source: "claude_code",
    title: "Wire the retry budget",
    git_branch: "feature",
    working_dir: "~/src/demo",
    outcome: "unknown",
    turn_count: prompts.length,
    commit_count: 0,
    confidence: 0,
    tokens: 0,
    estimated_tokens: 0,
    prompts_truncated: false,
    cost_per_prompt: false,
    has_inner_trace: true,
    started_at: null,
    ended_at: null,
    models: [],
    prompts: prompts.map((text, i) => prompt(i + 1, text)),
    totals: null,
    unattributed: null,
  },
  activity: {},
  truncated: false,
});

let root: Root, node: HTMLDivElement;
// What the daemon answers: a version and its transcript, or a body as is.
let answer: { version: string; prompts: string[] } | { body: unknown; status?: number };
let requests: string[];

beforeEach(() => {
  vi.useFakeTimers();
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=sessions&session=claude-code%3As1");
  drawn.count = 0;
  drawn.revisions = [];
  requests = [];
  answer = { version: "v1", prompts: ["Add a retry budget"] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      requests.push(path);
      if ("body" in answer) return Response.json(answer.body, { status: answer.status ?? 200 });
      const known = new URL(path, "http://x").searchParams.get("version");
      return Response.json(
        known === answer.version
          ? { available: true, version: answer.version, live: true }
          : {
              available: true,
              version: answer.version,
              live: true,
              transcript: transcript(...answer.prompts),
            },
      );
    }),
  );
  vi.stubGlobal("scrollTo", vi.fn());
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function show(s: LiveSession | null, tick = 0) {
  await act(async () =>
    root.render(
      <SessionTranscriptPage
        agent="claude-code"
        id="s1"
        session={s}
        tick={tick}
        prHref={() => "/s/test-ns/?clone=clone&pr=7"}
        Link={(props) => <a {...props} />}
      />,
    ),
  );
}

/** Lets timers and the reads they start finish. */
async function wait(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const prompts = () =>
  Array.from(node.querySelectorAll('[aria-label="Prompts"] li'), (li) => li.textContent);

it("reads the daemon's answers", () => {
  expect(parseLiveTranscript({ available: true, version: "v1", live: true })).toEqual({
    available: true,
    version: "v1",
    live: true,
    transcript: null,
  });
  expect(
    parseLiveTranscript({
      available: false,
      live: false,
      message: "The transcript file is not there any more.",
    }),
  ).toEqual({
    available: false,
    live: false,
    message: "The transcript file is not there any more.",
  });
  expect(
    parseLiveTranscript({ available: true, version: "v1", transcript: { nope: 1 } }),
  ).toBeNull();
  expect(parseLiveTranscript(null)).toBeNull();
});

it("shows the compaction summary the daemon sends, closed above the transcript", async () => {
  answer = {
    body: {
      available: true,
      version: "v1",
      live: true,
      transcript: transcript("Add a retry budget"),
      summary: "1. Primary Request and Intent:\n   Add a retry budget.",
    },
  };
  await show(session());
  const panel = node.querySelector("details.local-session-summary")!;
  expect(panel.hasAttribute("open")).toBe(false);
  expect(panel.querySelector("summary")!.textContent).toContain(
    "Summary of the earlier conversation",
  );
  expect(panel.textContent).toContain("Add a retry budget.");
});

it("shows no summary for a session that was never compacted", async () => {
  await show(session());
  expect(node.querySelector("details.local-session-summary")).toBeNull();
});

it("titles the page with the session's title when its agent gave one", async () => {
  await show(session({ title: "Wire the retry budget" }));
  expect(node.querySelector("h1")?.textContent).toBe("Wire the retry budget");
});

it("opens a running session's transcript under the session's header", async () => {
  await show(session());
  expect(requests).toEqual(["/s/test-ns/api/live-sessions/claude-code/s1/transcript"]);
  expect(node.querySelector("h1")?.textContent).toBe("Claude Code session");
  const head = node.querySelector('[aria-label="Session"]')!;
  expect(head.textContent).toContain("Working");
  expect(head.querySelector(".local-session-branch")?.textContent).toBe("feature");
  expect(head.querySelector('a[href$="&pr=7"]')?.textContent).toBe("PR #7");
  expect(head.textContent).toContain("Started 5 minutes ago");
  expect(node.textContent).toContain("Local transcript; secrets and e-mail addresses masked.");
  expect(prompts()).toEqual(["Add a retry budget"]);
  expect(node.querySelector('a[href="/s/test-ns/?page=sessions"]')?.textContent).toBe(
    "Back to sessions",
  );
});

it("shows a new version after a live event, and draws nothing again for the same one", async () => {
  await show(session(), 0);
  const first = drawn.count;
  // A live event for an unchanged transcript: the read names the version
  // shown, and the card is not drawn again.
  await show(session(), 1);
  const afterTick = drawn.count;
  await wait(LIVE_DEBOUNCE);
  expect(requests.at(-1)).toBe("/s/test-ns/api/live-sessions/claude-code/s1/transcript?version=v1");
  expect(drawn.count).toBe(afterTick);
  expect(afterTick).toBe(first);
  // Events in a burst are read once, after the last.
  answer = { version: "v2", prompts: ["Add a retry budget", "Now test it"] };
  const before = requests.length;
  await show(session(), 2);
  await wait(LIVE_DEBOUNCE / 2);
  await show(session(), 3);
  await wait(LIVE_DEBOUNCE);
  expect(requests.length).toBe(before + 1);
  expect(prompts()).toEqual(["Add a retry budget", "Now test it"]);
  // The card is told the new revision, so an opened turn loads again.
  expect(drawn.revisions.at(-1)).toBe("v2");
});

it("polls a running session, which grows between hook events, and stops once it ended", async () => {
  await show(session());
  answer = { version: "v2", prompts: ["Add a retry budget", "Now test it"] };
  await wait(POLL_RUNNING);
  expect(prompts()).toEqual(["Add a retry budget", "Now test it"]);
  await show(session({ state: "ended", ended_at: new Date().toISOString(), last_event: "end" }));
  expect(node.querySelector('[aria-label="Session"]')!.textContent).toContain("Ended");
  const before = requests.length;
  await wait(10 * POLL_RUNNING);
  expect(requests.length).toBe(before);
});

it("follows the end of the transcript only while the reader is there", async () => {
  const page = document.documentElement;
  let height = 2000;
  Object.defineProperty(page, "scrollHeight", { configurable: true, get: () => height });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 800 });
  const scrolled = (y: number) => {
    Object.defineProperty(window, "scrollY", { configurable: true, value: y });
    window.dispatchEvent(new Event("scroll"));
  };
  const scrollTo = vi.mocked(window.scrollTo);
  const toEnd = () => scrollTo.mock.calls.filter(([o]) => (o as ScrollToOptions).top === height);
  await show(session());
  // A running session opens at its end.
  expect(toEnd()).toHaveLength(1);
  scrolled(1200);
  // At the end, a new version keeps the reader there.
  height = 2600;
  answer = { version: "v2", prompts: ["Add a retry budget", "Now test it"] };
  await wait(POLL_RUNNING);
  expect(toEnd()).toHaveLength(1);
  // Scrolled up, the reader's place is kept.
  scrolled(300);
  height = 3200;
  answer = { version: "v3", prompts: ["Add a retry budget", "Now test it", "Ship it"] };
  await wait(POLL_RUNNING);
  expect(prompts()).toHaveLength(3);
  expect(toEnd()).toHaveLength(0);
});

it("says why a transcript is not served, and when the session is gone", async () => {
  answer = {
    body: {
      available: false,
      live: true,
      code: "transcript_unavailable",
      message: "Claude Code did not say where this session's transcript is.",
    },
  };
  await show(session());
  expect(node.textContent).toContain("No transcript to show");
  expect(node.textContent).toContain("Claude Code did not say where this session's transcript is.");
  answer = {
    body: { code: "live_session_not_found", message: "This session is no longer listed." },
    status: 404,
  };
  await act(async () => root.unmount());
  root = createRoot(node);
  await show(null);
  expect(node.textContent).toContain("This session is no longer listed");
  expect(node.querySelector("button")?.textContent).toBe("Retry");
});
