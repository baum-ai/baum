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
import { forgetFoldedSections, SessionsPage } from "./sessions";
import { ToastProvider } from "./toast";
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
  forgetFoldedSections();
});

async function render(sessions: LiveSession[], pending = requests()) {
  await act(async () =>
    root.render(
      <ToastProvider>
        <SessionsPage sessions={sessions} requests={pending} prHref={() => null} Link={Link} />
      </ToastProvider>,
    ),
  );
}
// A choice's text after the number of its key.
const button = (text: string) =>
  [...node.querySelectorAll("button")].find((b) =>
    (b.textContent ?? "").replace(/^[1-9]/, "").startsWith(text),
  ) as HTMLButtonElement | undefined;
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
  // Headed as the session's other rows, then what it asks.
  expect(card.querySelector("h3")!.textContent).toBe("Tidy the buttons");
  expect(card.querySelector(".local-feed-ask-text")!.textContent).toBe("Wants to run a command");
  expect(card.textContent).toContain("waiting 41s");
  expect(card.classList.contains("local-feed-ask")).toBe(true);
  expect(card.querySelector(".local-feed-branch")!.textContent).toBe("feature");
  // The agent's earlier words are not the request's.
  expect(card.querySelector(".local-feed-quote")).toBeNull();
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
  // The toast: what happened, to what, on which session.
  const toast = () => node.querySelector('[aria-live="polite"] .local-toast');
  expect(toast()!.querySelector(".local-toast-title")!.textContent).toBe("Always allowed");
  expect(toast()!.querySelector(".local-toast-detail")!.textContent).toBe(
    "Bash(just platform::test:*) in this project",
  );
  expect(toast()!.querySelector(".local-toast-context")!.textContent).toBe("Tidy the buttons");
  expect(toast()!.querySelector(".local-toast-context img")!.getAttribute("alt")).toBe(
    "Claude Code",
  );

  await render([waiting], requests({ requests: [permission({ id: "r2" })] }));
  await click(button("Deny"));
  expect(posts()[2]!.body).toEqual({ behavior: "deny" });
  expect(toast()!.querySelector(".local-toast-title")!.textContent).toBe("Denied");

  // Nothing waits any more: the notice says so, with what was done.
  await render([session()]);
  expect(node.querySelector('[aria-label="Needs you"]')).toBeNull();
  // Dismissed, it goes.
  await click(toast()!.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!);
  expect(toast()).toBeNull();
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
  const asks = [...node.querySelectorAll(".local-feed-ask-text")].map((h) => h.textContent);
  expect(asks).toContain("Wants to edit a file");
  expect(asks).toContain("Wants to use WebFetch");
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
  // What a choice means shows beside it.
  expect(button("A")!.querySelector(".local-feed-choice-note")!.textContent).toBe("Fast");
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
      activity: {
        steps: [],
        last_message: "**Done.** Want me to open a pull request?\n\n## Checks\n\n- All pass.",
      },
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
  // The agent by its mark and the session by its title: no "finished its
  // turn" heading.
  expect(finished!.querySelector("img")!.getAttribute("alt")).toBe("Claude Code");
  expect(finished!.querySelector("h3")!.textContent).toBe("Tidy the buttons");
  expect(finished!.querySelector(".local-feed-branch")!.textContent).toBe("feature");
  expect(finished!.textContent).not.toContain("finished its turn");
  // Only the message's opening paragraph, its Markdown rendered.
  const said = finished!.querySelector(".local-feed-soft")!;
  expect(said.textContent!.trim()).toBe("Done. Want me to open a pull request?");
  expect(said.querySelector("strong")!.textContent).toBe("Done.");
  expect(finished!.textContent).not.toContain("in its terminal");
  expect(finished!.querySelector("input, textarea")).toBeNull();
  expect(finished!.querySelectorAll("button")).toHaveLength(0);
  expect(finished!.querySelector("a.local-feed-open")!.getAttribute("aria-label")).toContain(
    "Tidy the buttons",
  );
  expect(asking!.textContent).toContain("Asks permission in its terminal");
  expect(asking!.textContent).toContain("Claude needs your permission to use Bash");
});

it("shows a running session's current step and files touched, and a quiet one", async () => {
  const steps = [
    { tool: "Read", target: "oss/app/src/sessions.tsx" },
    { tool: "Edit", target: "oss/app/README.md" },
    { tool: "Bash", target: "pnpm test sessions" },
  ];
  await render([
    session({ activity: { steps, files_touched: 3, last_prompt: "Run the session tests" } }),
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
  expect(busy!.querySelector(".local-feed-prompt")!.textContent).toBe("You Run the session tests");
  expect(busy!.querySelector(".local-feed-tool")!.textContent).toBe("Bash");
  expect(busy!.querySelector(".local-feed-target")!.textContent).toContain("pnpm test sessions");
  expect(busy!.querySelector(".local-feed-caret")).not.toBeNull();
  // Only the current step: no trail of the earlier ones.
  expect(busy!.textContent).not.toContain("Trail");
  expect(busy!.textContent).not.toContain("Read");
  expect(busy!.querySelector(".local-feed-time")!.textContent).toBe("3 files touched · 7m 00s");
  expect(quiet!.textContent).toContain("Quiet for 12 min");
  expect(quiet!.querySelector(".local-feed-caret")).toBeNull();
  expect(bare!.textContent).toContain("Active 2 minutes ago");

  // Once the agent has said something, its words show in place of the step.
  await render([
    session({
      activity: {
        steps,
        last_message: "Running the **session** tests.\n\nThen the browser ones.",
      },
    }),
  ]);
  const said = node.querySelector('[aria-label="Running now"] .local-feed-said')!;
  expect(said.textContent!.trim()).toBe("Running the session tests.");
  expect(said.querySelector("strong")!.textContent).toBe("session");
  expect(said.querySelector(".local-feed-caret")).not.toBeNull();
  expect(node.querySelector('[aria-label="Running now"] .local-feed-step')).toBeNull();
  expect(bare!.querySelector(".local-feed-step")).toBeNull();
});

it("sends a message for when the turn finishes, and cancels a queued one", async () => {
  await render([session()]);
  // No box until r opens it on the focused card.
  expect(node.querySelector("input")).toBeNull();
  const key = (k: string) =>
    act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }),
      );
    });
  await key("j");
  await key("r");
  const input = node.querySelector("input")! as HTMLInputElement;
  expect(document.activeElement).toBe(input);
  expect(node.querySelector(`label[for="${input.id}"]`)!.textContent).toBe("Send when it finishes");
  const send = () => node.querySelector<HTMLButtonElement>('button[aria-label^="Send message"]')!;
  expect(send().disabled).toBe(true);
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      input,
      "open a PR",
    );
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(send());
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
  // Cancelled, the box stays closed until asked again.
  expect(node.querySelector("input")).toBeNull();

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
  // Folded at first: its heading unfolds it.
  expect(node.querySelector('[aria-label="Ended recently"] li')).toBeNull();
  await click(
    node.querySelector<HTMLButtonElement>(
      '[aria-label="Ended recently"] button[aria-expanded="false"]',
    )!,
  );
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

it("opens a session's transcript on a click anywhere on its card but its controls", async () => {
  await render([session({ state: "running" })]);
  const card = node.querySelector('[aria-label="Running now"] li')!;
  const link = card.querySelector<HTMLAnchorElement>("a.local-feed-open")!;
  expect(card.textContent).not.toContain("Open transcript");
  let opened = 0;
  link.addEventListener("click", (e) => {
    e.preventDefault();
    opened++;
  });
  await act(async () => card.querySelector<HTMLElement>(".local-feed-time")!.click());
  expect(opened).toBe(1);
  // Its box, once r opens it, keeps its clicks.
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
  });
  await act(async () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "r", bubbles: true, cancelable: true }),
    );
  });
  await act(async () => card.querySelector<HTMLElement>("input")!.click());
  await act(async () => card.querySelector<HTMLElement>("button")!.click());
  expect(opened).toBe(1);
});

it("lists a session that went quiet under Needs you, muted, with its last words", async () => {
  await render([
    session({
      state: "idle",
      waiting_reason: "quiet",
      waiting_since: ago(1680),
      activity: {
        steps: [],
        last_message: "Re-running the browser tests.",
        last_prompt: "The cards are too long",
      },
    }),
  ]);
  const card = node.querySelector('[aria-label="Needs you"] li')!;
  expect(card.textContent).toContain("quiet 28m 00s");
  expect(card.querySelector(".local-feed-soft")!.textContent!.trim()).toBe(
    "Re-running the browser tests.",
  );
  // The muted dot and the time say it went quiet: no note.
  expect(card.querySelector(".local-feed-note")).toBeNull();
  // What the user last typed, which it went quiet on.
  expect(card.querySelector(".local-feed-prompt")!.textContent).toBe("You The cards are too long");
  expect(node.querySelector('[aria-label="Running now"] li')).toBeNull();
});

it("never shows End session on a card: the menu offers it where the daemon can stop the agent", async () => {
  await render([
    session({
      state: "idle",
      waiting_reason: "turn_finished",
      waiting_since: ago(60),
      endable: true,
    }),
    session({ session_id: "s2", state: "idle", waiting_reason: "quiet", waiting_since: ago(900) }),
  ]);
  expect(button("End session")).toBeUndefined();
  const menuOf = async (id: string) => {
    await act(async () => {
      node.querySelector(`[data-row-id="${id}"] h3`)!.dispatchEvent(
        new MouseEvent("contextmenu", {
          bubbles: true,
          cancelable: true,
          clientX: 9,
          clientY: 9,
        }),
      );
    });
    const items = [...node.querySelectorAll('[role="menu"] [role="menuitem"]')].map(
      (b) => b.textContent,
    );
    await act(async () => {
      node
        .querySelector('[role="menu"] button')!
        .dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    return items;
  };
  expect(await menuOf("claude-code:s1")).toContain("End session");
  expect(await menuOf("claude-code:s2")).not.toContain("End session");
});

it("offers a session's actions on a right click, in place of the browser's menu", async () => {
  const went: string[] = [];
  await act(async () =>
    root.render(
      <ToastProvider>
        <SessionsPage
          sessions={[
            session({
              state: "idle",
              waiting_reason: "turn_finished",
              waiting_since: ago(60),
              endable: true,
              pr: 7,
            }),
          ]}
          requests={requests()}
          prHref={() => "/s/test-ns/?page=pull-requests&pr=7"}
          Link={Link}
          navigate={(href) => went.push(href)}
        />
      </ToastProvider>,
    ),
  );
  const card = node.querySelector(
    '[data-row-id="claude-code:s1"] .local-feed-prompt, [data-row-id="claude-code:s1"] h3',
  )!;
  const rightClick = () => {
    const event = new MouseEvent("contextmenu", {
      bubbles: true,
      cancelable: true,
      clientX: 40,
      clientY: 50,
    });
    card.dispatchEvent(event);
    return event;
  };
  let event!: MouseEvent;
  await act(async () => {
    event = rightClick();
  });
  expect(event.defaultPrevented).toBe(true);
  const items = () =>
    [...node.querySelectorAll('[role="menu"] [role="menuitem"]')] as HTMLButtonElement[];
  expect(items().map((b) => b.textContent)).toEqual([
    "Open transcript",
    "Open pull request #7",
    "End session",
  ]);
  expect(document.activeElement).toBe(items()[0]);
  // Escape closes it.
  await act(async () => {
    items()[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  });
  expect(node.querySelector('[role="menu"]')).toBeNull();
  // A choice closes it and does what it says.
  await act(async () => {
    rightClick();
  });
  await click(items()[1]);
  expect(went).toEqual(["/s/test-ns/?page=pull-requests&pr=7"]);
  expect(node.querySelector('[role="menu"]')).toBeNull();
  await act(async () => {
    rightClick();
  });
  await click(items()[2]);
  expect(posts()).toEqual([
    { path: "/s/test-ns/api/live-sessions/claude-code/s1/end", method: "POST", body: {} },
  ]);
  expect(node.querySelector(".local-toast-title")!.textContent).toBe("Ended the session");
});

it("opens a reply with r on a session waiting at its prompt, sends it with Enter and closes it with Escape", async () => {
  await render([
    session({
      state: "idle",
      waiting_reason: "turn_finished",
      waiting_since: ago(60),
      messageable: true,
    }),
    session({
      session_id: "s2",
      state: "idle",
      waiting_reason: "permission",
      waiting_since: ago(30),
    }),
  ]);
  // Nothing to type in until asked.
  expect(node.querySelector("[aria-label='Needs you'] input")).toBeNull();
  const key = (k: string) =>
    act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }),
      );
    });
  // The session asking a permission has no reply.
  await key("j");
  await key("j");
  await key("r");
  expect(node.querySelector("[aria-label='Needs you'] input")).toBeNull();
  await key("k");
  await key("r");
  const input = node.querySelector<HTMLInputElement>('[data-row-id="claude-code:s1"] input')!;
  expect(document.activeElement).toBe(input);
  expect(input.value).toBe("");
  // Escape closes it, and the keys go back to the card.
  await act(async () => {
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
  });
  expect(node.querySelector("[aria-label='Needs you'] input")).toBeNull();
  expect(document.activeElement).toBe(
    node.querySelector('[data-row-id="claude-code:s1"] a.local-feed-open'),
  );
  await key("r");
  const again = node.querySelector<HTMLInputElement>('[data-row-id="claude-code:s1"] input')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      again,
      " Open the PR ",
    );
    again.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // Enter submits the form.
  await act(async () => again.form!.requestSubmit());
  expect(posts()).toEqual([
    {
      path: "/s/test-ns/api/live-sessions/claude-code/s1/message",
      method: "POST",
      body: { text: "Open the PR" },
    },
  ]);
  expect(node.querySelector("[aria-label='Needs you'] input")).toBeNull();
  expect(node.querySelector(".local-toast-title")!.textContent).toBe("Replied");
  expect(node.querySelector(".local-toast-detail")!.textContent).toBe("Open the PR");
});

it("answers the focused question or permission with its number keys, Enter submitting several", async () => {
  const s = session({ state: "idle", waiting_reason: "input", waiting_since: ago(5) });
  const key = (k: string) =>
    act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }),
      );
    });
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
        question: "Open the PR?",
        header: "Open PR",
        options: [{ label: "Yes" }, { label: "Not yet" }],
      },
    ],
  };
  await render([s], requests({ requests: [question] }));
  expect(button("Not yet")!.querySelector("kbd")!.textContent).toBe("2");
  await key("j");
  await key("2");
  expect(posts()[0]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/q1/answer",
    body: { behavior: "allow", answers: { "Open the PR?": "Not yet" } },
  });

  // Several choices: the numbers toggle them, Enter submits.
  const several: AgentRequest = {
    ...question,
    id: "q2",
    questions: [
      {
        question: "Which checks?",
        multi_select: true,
        options: [{ label: "Unit" }, { label: "E2E" }],
      },
    ],
  };
  await render([s], requests({ requests: [several] }));
  await key("1");
  await key("2");
  expect(button("E2E")!.getAttribute("aria-pressed")).toBe("true");
  await key("Enter");
  expect(posts()[1]).toMatchObject({ body: { answers: { "Which checks?": "Unit, E2E" } } });

  // A permission: 1 allows once, the last number denies.
  await render([s], requests({ requests: [permission({ id: "p1" })] }));
  const deny = button("Deny")!.querySelector("kbd")!.textContent!;
  await key(deny);
  expect(posts()[2]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/p1/answer",
    body: { behavior: "deny" },
  });
});

it("takes an answer typed in place of a choice", async () => {
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
    questions: [{ question: "Open the PR?", options: [{ label: "Yes" }, { label: "Not yet" }] }],
  };
  await render([s], requests({ requests: [question] }));
  const field = () => node.querySelector<HTMLInputElement>(".local-feed-card input");
  const type = async (text: string) => {
    const input = field()!;
    expect(document.activeElement).toBe(input);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => input.form!.requestSubmit());
  };
  // Its own number, after the choices.
  expect(button("Type an answer")!.querySelector("kbd")!.textContent).toBe("3");
  await click(button("Type an answer"));
  // Escape closes it unanswered.
  await act(async () => {
    field()!.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
  });
  expect(field()).toBeNull();
  await click(button("Type an answer"));
  await type("After the toast lands");
  expect(posts()[0]).toMatchObject({
    path: "/s/test-ns/api/agent-requests/q1/answer",
    body: { behavior: "allow", answers: { "Open the PR?": "After the toast lands" } },
  });

  // Among several, it joins the choices until Submit.
  await render(
    [s],
    requests({
      requests: [
        {
          ...question,
          id: "q2",
          questions: [
            { question: "Which checks?", multi_select: true, options: [{ label: "Unit" }] },
          ],
        },
      ],
    }),
  );
  await click(button("Unit"));
  await click(button("Type an answer"));
  await type("Lint");
  expect(button("“Lint”")!.getAttribute("aria-pressed")).toBe("true");
  await click(button("Submit"));
  expect(posts()[1]).toMatchObject({ body: { answers: { "Which checks?": "Unit, Lint" } } });
});
