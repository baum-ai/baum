import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { ComponentType } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { App } from "./app";
import {
  noAgentRequests,
  parseAgentRequests,
  type AgentRequest,
  type AgentRequests,
  type LiveSession,
} from "./api";
import { SessionsPage } from "./sessions";
import { SettingsPage } from "./settings";
import { forgetLastRows } from "./shortcuts";

vi.mock("@baum/viewer/components/review-viewer", () => ({ ReviewViewer: () => null }));

const Link: ComponentType<ViewerLinkProps> = ({ href, children, ...rest }) => (
  <a href={href} {...rest}>
    {children}
  </a>
);

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString();
const session = (over: Partial<LiveSession> = {}): LiveSession => ({
  agent: "claude-code",
  session_id: "s1",
  state: "running",
  started_at: ago(7 * 60),
  last_activity_at: ago(5),
  last_event: "tool",
  repository: "acme/demo",
  branch: "feature",
  title: "Tidy the buttons",
  ...over,
});
const permission = (over: Partial<AgentRequest> = {}): AgentRequest => ({
  id: "r1",
  agent: "claude-code",
  session_id: "s1",
  kind: "permission",
  tool: "Bash",
  created_at: ago(41),
  input: [
    { label: "Command", value: "just platform::test ./internal/githubgit/...", code: true },
    { label: "Description", value: "Run the githubgit tests" },
  ],
  suggestions: [{ id: "0", label: "Always allow Bash(just platform::test:*) in this project" }],
  questions: [],
  ...over,
});
const requests = (over: Partial<AgentRequests> = {}): AgentRequests => ({
  ...noAgentRequests,
  ...over,
});

let root: Root, node: HTMLDivElement;
let calls: { path: string; method: string; body: unknown }[];
let respond: (path: string, method: string) => Response;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=sessions");
  calls = [];
  respond = () => Response.json({ ok: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      calls.push({
        path,
        method: init?.method ?? "GET",
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      });
      return respond(path, init?.method ?? "GET");
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  forgetLastRows();
});

async function render(sessions: LiveSession[], pending = requests()) {
  await act(async () =>
    root.render(
      <SessionsPage sessions={sessions} requests={pending} prHref={() => null} Link={Link} />,
    ),
  );
}
const button = (text: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent?.startsWith(text)) as
    | HTMLButtonElement
    | undefined;
const click = (b: HTMLButtonElement | undefined) => act(async () => b!.click());
const posts = () => calls.filter((c) => c.method !== "GET");

it("shows a permission request with its input rows and posts Allow once, Always allow and Deny", async () => {
  const waiting = session({
    state: "idle",
    waiting_reason: "permission",
    waiting_since: ago(41),
    activity: { steps: [], last_message: "Running the githubgit tests to check nothing broke." },
  });
  await render([waiting], requests({ requests: [permission()] }));
  const card = node.querySelector('[aria-label="Needs you"] li')!;
  expect(card.querySelector("h3")!.textContent).toBe("Claude Code wants to run a command");
  expect(card.textContent).toContain("waiting 41s");
  expect(card.classList.contains("local-feed-ask")).toBe(true);
  expect(card.querySelector(".local-feed-where")!.textContent).toBe("Tidy the buttons · feature");
  expect(card.querySelector(".local-feed-quote")!.textContent).toContain("Running the githubgit");
  expect(card.querySelector("pre.local-feed-code")!.textContent).toContain(
    "just platform::test ./internal/githubgit/...",
  );
  expect(card.querySelector(".local-feed-row")!.textContent).toContain("Run the githubgit tests");
  expect(button("Always allow Bash")).toBeDefined();
  // The session is not listed again under Running now.
  expect(node.querySelector('[aria-label="Running now"] li')).toBeNull();

  respond = () => new Response(null, { status: 500 });
  await click(button("Allow once"));
  expect(posts()[0]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/r1/answer",
    method: "POST",
    body: { behavior: "allow" },
  });
  // Failure re-enables the buttons.
  expect(node.querySelector('[role="alert"]')).not.toBeNull();
  expect(button("Allow once")!.disabled).toBe(false);

  respond = () => Response.json({ ok: true });
  await click(button("Always allow Bash"));
  expect(posts()[1]!.body).toEqual({ behavior: "allow", suggestion: "0" });
  // Sent: disabled until the next refresh drops the request, with the notice.
  expect(button("Deny")!.disabled).toBe(true);
  expect(node.textContent).toContain("Sent");
  expect(node.querySelector('[aria-live="polite"]')!.textContent).toBe(
    "Bash(just platform::test:*) in this project is now always allowed.",
  );

  await render([waiting], requests({ requests: [permission({ id: "r2" })] }));
  await click(button("Deny"));
  expect(posts()[2]!.body).toEqual({ behavior: "deny" });
  expect(node.querySelector('[aria-live="polite"]')!.textContent).toContain("You denied");

  // Nothing waits any more: the notice says so, with what was done.
  await render([session()]);
  expect(node.querySelector('[aria-label="Needs you"]')).toBeNull();
  expect(node.querySelector('[aria-live="polite"]')!.textContent).toMatch(
    /^Nobody is waiting on you\. You denied/,
  );
});

it("names the request by its tool", async () => {
  const s = session({ state: "idle", waiting_reason: "permission", waiting_since: ago(5) });
  await render(
    [s],
    requests({
      requests: [
        permission({ tool: "Edit", input: [{ label: "File", value: "a.go" }] }),
        permission({ id: "r2", tool: "WebFetch", session_id: "s2" }),
      ],
    }),
  );
  const titles = [...node.querySelectorAll("h3")].map((h) => h.textContent);
  expect(titles).toContain("Claude Code wants to edit a file");
  expect(titles).toContain("Claude Code wants to use WebFetch");
});

it("posts a question's answers, joining multi-select labels, once every question has one", async () => {
  const s = session({ state: "idle", waiting_reason: "input", waiting_since: ago(5) });
  const question: AgentRequest = {
    id: "q1",
    agent: "claude-code",
    session_id: "s1",
    kind: "question",
    tool: "AskUserQuestion",
    created_at: ago(5),
    input: [],
    suggestions: [],
    questions: [
      {
        question: "Which approach?",
        header: "Approach",
        options: [{ label: "A", description: "Fast" }, { label: "B" }],
      },
      {
        question: "Which checks?",
        multi_select: true,
        options: [{ label: "Unit" }, { label: "E2E" }],
      },
    ],
  };
  await render([s], requests({ requests: [question] }));
  expect(button("A")!.getAttribute("title")).toBe("Fast");
  expect(button("Submit")!.disabled).toBe(true);
  await click(button("B"));
  await click(button("Unit"));
  await click(button("E2E"));
  expect(button("Unit")!.getAttribute("aria-pressed")).toBe("true");
  await click(button("Submit"));
  expect(posts()[0]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/q1/answer",
    body: { behavior: "allow", answers: { "Which approach?": "B", "Which checks?": "Unit, E2E" } },
  });

  // One single-choice question is answered by its option.
  await render(
    [s],
    requests({ requests: [{ ...question, id: "q2", questions: [question.questions[0]!] }] }),
  );
  await click(button("A"));
  expect(posts()[1]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/q2/answer",
    body: { behavior: "allow", answers: { "Which approach?": "A" } },
  });
});

it("says the request was answered in the terminal on a 409", async () => {
  const s = session({ state: "idle", waiting_reason: "permission", waiting_since: ago(5) });
  respond = () => Response.json({ code: "request_gone", message: "gone" }, { status: 409 });
  await render([s], requests({ requests: [permission()] }));
  await click(button("Allow once"));
  expect(node.textContent).toContain("Already answered in the terminal");
  expect(button("Allow once")!.disabled).toBe(true);
  expect(node.querySelector('[aria-live="polite"]')!.textContent).toBe("");
});

it("shows a finished turn without a reply box and a terminal-only wait with a note", async () => {
  await render([
    session({
      state: "idle",
      waiting_reason: "turn_finished",
      waiting_since: ago(720),
      activity: { steps: [], last_message: "Want me to open a pull request?" },
    }),
    session({
      session_id: "s2",
      state: "idle",
      waiting_reason: "permission",
      waiting_since: ago(60),
      waiting_message: "Claude needs your permission to use Bash",
    }),
  ]);
  const [finished, asking] = [...node.querySelectorAll('[aria-label="Needs you"] li')];
  expect(finished!.textContent).toContain("finished its turn");
  expect(finished!.querySelector(".local-feed-soft")!.textContent).toBe(
    "Want me to open a pull request?",
  );
  expect(finished!.textContent).not.toContain("Answer in the terminal");
  expect(finished!.querySelector("input, textarea")).toBeNull();
  expect(finished!.querySelectorAll("button")).toHaveLength(0);
  expect(finished!.querySelector("a.local-session-open")!.getAttribute("aria-label")).toContain(
    "Tidy the buttons",
  );
  expect(asking!.textContent).toContain("Answer in the terminal");
  expect(asking!.textContent).toContain("Claude needs your permission to use Bash");
});

it("shows a running session's current step, trail and files touched, and a quiet one", async () => {
  const steps = [
    { tool: "Read", target: "oss/app/src/sessions.tsx" },
    { tool: "Edit", target: "oss/app/README.md" },
    { tool: "Bash", target: "pnpm test sessions" },
  ];
  await render([
    session({ activity: { steps, files_touched: 3 } }),
    session({
      session_id: "s2",
      state: "idle",
      last_activity_at: ago(12 * 60),
      title: "Quiet one",
    }),
    session({
      session_id: "s3",
      title: "No activity",
      activity: undefined,
      last_activity_at: ago(120),
    }),
  ]);
  const [busy, quiet, bare] = [...node.querySelectorAll('[aria-label="Running now"] li')];
  expect(busy!.querySelector(".local-feed-tool")!.textContent).toBe("Bash");
  expect(busy!.querySelector(".local-feed-target")!.textContent).toContain("pnpm test sessions");
  expect(busy!.querySelector(".local-feed-caret")).not.toBeNull();
  const chips = [...busy!.querySelectorAll(".local-feed-trail .local-feed-chip")];
  expect(chips.map((c) => c.textContent)).toEqual(["Read", "Edit"]);
  expect(chips[1]!.getAttribute("title")).toBe("oss/app/README.md");
  expect(busy!.querySelector(".local-feed-files")!.textContent).toBe("3 files touched");
  expect(busy!.querySelector(".local-feed-time")!.textContent).toBe("7m 00s");
  expect(quiet!.textContent).toContain("Quiet for 12 min");
  expect(quiet!.querySelector(".local-feed-caret")).toBeNull();
  expect(quiet!.querySelector(".local-feed-dot.muted")).not.toBeNull();
  expect(bare!.textContent).toContain("Active 2 minutes ago");
  expect(bare!.querySelector(".local-feed-step")).toBeNull();
});

it("sends a message for when the turn finishes, and cancels a queued one", async () => {
  await render([session()]);
  const input = node.querySelector("input")! as HTMLInputElement;
  expect(node.querySelector(`label[for="${input.id}"]`)!.textContent).toBe("Send when it finishes");
  expect(button("Send")!.disabled).toBe(true);
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      input,
      "open a PR",
    );
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(button("Send"));
  expect(posts()[0]).toMatchObject({
    path: "/s/test-ns/api/agent-messages",
    method: "POST",
    body: { agent: "claude-code", session_id: "s1", text: "open a PR" },
  });
  // Queued: its text and Cancel replace the input.
  expect(node.textContent).toContain("Queued for when it finishes: “open a PR”");
  expect(node.querySelector("input")).toBeNull();
  await click(button("Cancel"));
  expect(posts()[1]).toMatchObject({
    path: "/s/test-ns/api/agent-messages/claude-code/s1",
    method: "DELETE",
  });
  expect(node.querySelector("input")).not.toBeNull();

  // A message the daemon reports shows the same way.
  await render(
    [session()],
    requests({
      messages: [
        { agent: "claude-code", session_id: "s1", text: "from earlier", queued_at: ago(3) },
      ],
    }),
  );
  expect(node.textContent).toContain("Queued for when it finishes: “from earlier”");
});

it("lists ended sessions compactly with their length and files", async () => {
  await render([
    session({
      state: "ended",
      started_at: ago(3 * 3600),
      ended_at: ago(600),
      activity: { steps: [], files_touched: 14 },
    }),
  ]);
  const row = node.querySelector('[aria-label="Ended recently"] li')!;
  expect(row.textContent).toContain("2.8 h · 14 files touched");
  expect(row.textContent).toContain("Ended 10 minutes ago");
});

it("keeps the page working, with no requests, when api/agent-requests fails", async () => {
  vi.stubGlobal(
    "EventSource",
    class extends EventTarget {
      close() {}
    },
  );
  respond = (path) =>
    path.endsWith("api/live-sessions")
      ? Response.json({ sessions: [session()] })
      : path.endsWith("api/health")
        ? Response.json({})
        : path.endsWith("api/index") || path.includes("api/index?")
          ? Response.json({ entries: [], problems: [] })
          : path.endsWith("api/jobs")
            ? Response.json({ jobs: [] })
            : path.endsWith("api/repos") || path.endsWith("api/repositories")
              ? Response.json([])
              : path.endsWith("api/prs") || path.endsWith("api/background")
                ? Response.json([])
                : new Response("missing", { status: 404 });
  await act(async () => root.render(<App />));
  expect(calls.some((c) => c.path.endsWith("api/agent-requests"))).toBe(true);
  expect(node.querySelector('[aria-label="Running now"] li')).not.toBeNull();
  expect(node.querySelector('[aria-label="Needs you"]')).toBeNull();
  expect(parseAgentRequests(null)).toEqual(noAgentRequests);
});

it("toggles Codex approvals in Settings, and hides the toggle without the route", async () => {
  const render = async () =>
    act(async () =>
      root.render(
        <SettingsPage
          automatic={null}
          defaults={null}
          github={null}
          section="computer"
          setSection={() => {}}
          changed={async () => {}}
          problems={0}
          diagnosticsOpen={false}
          setDiagnosticsOpen={() => {}}
          onDiagnostics={() => {}}
          authRevision={0}
          authChanged={() => {}}
        />,
      ),
    );
  respond = (path, method) =>
    path.endsWith("api/agent-requests")
      ? Response.json({ requests: [], messages: [], settings: { codex_approvals: false } })
      : path.endsWith("api/agent-requests/settings") && method === "PUT"
        ? Response.json({ codex_approvals: true })
        : new Response("missing", { status: 404 });
  await render();
  const toggle = [...node.querySelectorAll('input[role="switch"]')].find((i) =>
    i.closest("label")?.textContent?.includes("Answer Codex approvals from the app"),
  ) as HTMLInputElement;
  expect(toggle.checked).toBe(false);
  expect(node.textContent).toContain("hides its own approval prompt for up to 25 seconds");
  await act(async () => toggle.click());
  expect(calls.find((c) => c.method === "PUT")).toMatchObject({
    path: "/s/test-ns/api/agent-requests/settings",
    body: { codex_approvals: true },
  });
  expect(toggle.checked).toBe(true);

  await act(async () => root.unmount());
  root = createRoot(node);
  respond = () => new Response("missing", { status: 404 });
  await render();
  expect(node.textContent).not.toContain("Answer Codex approvals");
});
