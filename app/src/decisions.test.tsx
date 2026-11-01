import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  DecisionsRevision,
  LiveSessionDecisions,
  PullRequestDecisionsList,
  captureState,
} from "./decisions";
import type { CapturedDecision, LiveSession, SessionDecisions } from "./api";

const live: LiveSession = {
  agent: "claude-code",
  session_id: "s1",
  state: "running",
  started_at: new Date(Date.now() - 600_000).toISOString(),
  last_activity_at: new Date().toISOString(),
  last_event: "tool",
  repository: "acme/demo",
  pr: 7,
  title: "Add a retry budget",
};
const decision = (over: Partial<CapturedDecision> = {}): CapturedDecision => ({
  decision_id: "d1",
  title: "Retry at most three times",
  summary: "Bound retries so a failing provider does not loop.",
  category: "behavior",
  who: "human",
  rationale: "An unbounded loop spent the budget.",
  superseded: false,
  files: ["retry.go"],
  decided_at: new Date(Date.now() - 3600_000).toISOString(),
  ...over,
});
const captured = (over: Partial<SessionDecisions> = {}): SessionDecisions => ({
  agent: "claude-code",
  session_id: "s1",
  coverage: "extracted",
  updated_at: new Date(Date.now() - 3 * 60_000).toISOString(),
  capturing: false,
  decisions: [decision(), decision({ decision_id: "d2", title: "Keep the queue in memory" })],
  ...over,
});

let root: Root, node: HTMLDivElement;
let answers: Record<string, unknown>;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?pr=7&clone=clone");
  answers = {};
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string) => {
      const key = path.replace("/s/test-ns/", "");
      return key in answers
        ? Response.json(answers[key])
        : new Response("missing", { status: 404 });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

const sessionPath = "api/decisions?agent=claude-code&session=s1";
const prPath = "api/decisions?repository=acme%2Fdemo&pr=7";

it("shows a live session's decisions and rereads them on a decisions event", async () => {
  answers[sessionPath] = { session: captured() };
  const render = (revision: number) =>
    root.render(
      <DecisionsRevision.Provider value={revision}>
        <LiveSessionDecisions session={live} />
      </DecisionsRevision.Provider>,
    );
  await act(async () => render(0));
  const details = node.querySelector("details.local-live-decisions")!;
  expect(details.querySelector("summary")!.textContent).toBe(
    "Decisions (2) · Updated 3 minutes ago",
  );
  expect(Array.from(details.querySelectorAll("li"), (li) => li.textContent)).toEqual([
    "Retry at most three times",
    "Keep the queue in memory",
  ]);
  answers[sessionPath] = { session: captured({ capturing: true }) };
  await act(async () => render(1));
  expect(node.textContent).toContain("Decisions (2) · Capturing…");
  answers[sessionPath] = {
    session: captured({ coverage: "skipped", skip_reason: "backend_unsupported", decisions: [] }),
  };
  await act(async () => render(2));
  expect(node.querySelector("details")).toBeNull();
  expect(node.textContent).toContain("Decisions (0) · Cursor Agent can't capture decisions");
});

it("draws nothing for a daemon without decision capture", async () => {
  answers[sessionPath] = { available: false, code: "decisions_unavailable" };
  answers[prPath] = { available: false, code: "decisions_unavailable" };
  await act(async () =>
    root.render(
      <>
        <LiveSessionDecisions session={live} />
        <PullRequestDecisionsList repository="acme/demo" number={7} live={[]} revision={0} />
      </>,
    ),
  );
  expect(node.innerHTML).toBe("");
});

it("lists a pull request's decisions by session and marks the provisional ones", async () => {
  const generated = new Date(Date.now() - 2 * 3600_000).toISOString();
  answers[prPath] = {
    review_generated_at: generated,
    sessions: [
      {
        ...captured({
          decisions: [
            decision(),
            decision({ decision_id: "d2", title: "Keep the queue in memory", provisional: true }),
          ],
        }),
        live: true,
      },
      {
        ...captured({
          agent: "codex",
          session_id: "old",
          decisions: [
            decision({
              decision_id: "d3",
              title: "Use sqlite",
              category: "dependency",
              who: "agent_proposed_human_approved",
            }),
          ],
        }),
        title: "Port the store",
        live: false,
      },
      {
        ...captured({ session_id: "never", coverage: "", updated_at: null, decisions: [] }),
        live: false,
      },
    ],
  };
  await act(async () =>
    root.render(
      <PullRequestDecisionsList repository="acme/demo" number={7} live={[live]} revision={0} />,
    ),
  );
  const section = node.querySelector('[aria-labelledby="pr-decisions-heading"]')!;
  expect(section.querySelector("h3")!.textContent).toBe("Decisions from sessions (3)");
  // The list can be long, so it starts closed behind its heading.
  expect(section.querySelector("details")!.open).toBe(false);
  expect(section.querySelector("summary h3")).not.toBeNull();
  expect(section.textContent).toContain(
    "One decision was made after the saved review was generated",
  );
  const sessions = section.querySelectorAll(".local-decisions-session");
  // A session never captured and without decisions is left out.
  expect(sessions).toHaveLength(2);
  expect(sessions[0]!.querySelector("h4")!.textContent).toBe(
    "Claude CodeAdd a retry budgetIn progress",
  );
  const rows = sessions[0]!.querySelectorAll("li");
  expect(rows[0]!.textContent).not.toContain("Provisional");
  expect(rows[1]!.textContent).toContain("Provisional");
  expect(rows[0]!.textContent).toContain("Behavior · You decided");
  expect(rows[0]!.textContent).toContain("retry.go");
  expect(sessions[1]!.querySelector("h4")!.textContent).toBe("CodexPort the store");
  expect(sessions[1]!.textContent).toContain("Dependency · Agent proposed, you approved");
});

it("shows a session whose first capture is in flight on the pull request", async () => {
  answers[prPath] = {
    review_generated_at: null,
    sessions: [
      {
        ...captured({ coverage: "", updated_at: null, decisions: [], capturing: true }),
        live: true,
      },
    ],
  };
  await act(async () =>
    root.render(
      <PullRequestDecisionsList repository="acme/demo" number={7} live={[live]} revision={0} />,
    ),
  );
  const section = node.querySelector('[aria-labelledby="pr-decisions-heading"]')!;
  expect(section.querySelector("h3")!.textContent).toBe("Decisions from sessions (0)");
  expect(section.textContent).toContain("Add a retry budget");
  expect(section.textContent).toContain("Capturing…");
});

it("puts where a capture stands in words", () => {
  const now = Date.now();
  expect(captureState(captured({ capturing: true }), now)).toBe("Capturing…");
  expect(
    captureState(captured({ coverage: "skipped", skip_reason: "backend_unavailable" }), now),
  ).toBe("No harness that can capture decisions is available on this computer.");
  expect(captureState(captured({ coverage: "skipped", skip_reason: "new_reason" }), now)).toBe(
    "Not captured.",
  );
  expect(captureState(captured({ coverage: "", updated_at: null }), now)).toContain(
    "Not captured yet",
  );
  // When capture will not run, the state says why instead of promising one.
  expect(
    captureState(captured({ coverage: "", updated_at: null, capture_off: "repository_off" }), now),
  ).toBe("Capture is off for this repository.");
  expect(captureState(captured({ capture_off: "paused" }), now)).toBe(
    "Updated 3 minutes ago · Capture is paused with automatic reviews.",
  );
});
