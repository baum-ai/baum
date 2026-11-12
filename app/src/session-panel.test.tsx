import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Conversation, PanelSettings, Proposal } from "./console-api";
import type { PageContext } from "./page-context";
import {
  ProposalCard,
  publishConsoleEvent,
  REPORT_DEBOUNCE,
  resetSessionPanel,
  sessionPanel,
  SessionPanelButton,
  SessionPanelHost,
} from "./session-panel";
import { SessionPanelSection } from "./settings";
import { ToastProvider } from "./toast";

vi.mock("@baum/viewer/components/review-viewer", () => ({ ReviewViewer: () => null }));

type Call = { path: string; method: string; body: unknown };
let root: Root;
let node: HTMLDivElement;
let calls: Call[];
/** The fake daemon's answer to one request, or undefined for its default. */
let answer: (call: Call) => Response | Promise<Response> | undefined;
let conversation: Conversation | null;
let available: boolean;
const settings: PanelSettings = {
  agent: "claude-code",
  model: "",
  mode: "full",
  position: "bottom",
  agent_source: "fix",
  model_source: "fix",
};

const pr: PageContext = { page: "pull_request", repository: "acme/demo", pull_request: 7 };
const made = (over: Partial<Conversation> = {}): Conversation => ({
  id: "c1",
  title: "",
  agent: "claude-code",
  model: "",
  mode: "full",
  state: "idle",
  directory: "/cache/baum/console/sessions/c1",
  updated_at: new Date().toISOString(),
  created_worktree: false,
  created_branch: false,
  pages: [pr],
  created_at: new Date().toISOString(),
  progress: null,
  ...over,
});

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  history.replaceState(null, "", "/s/test-ns/?repository=acme%2Fdemo&pr=7");
  resetSessionPanel();
  calls = [];
  conversation = null;
  available = true;
  answer = () => undefined;
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const path = url.replace("/s/test-ns/", "");
      const call = {
        path,
        method: init?.method ?? "GET",
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      };
      calls.push(call);
      const given = await answer(call);
      if (given) return given;
      if (path === "api/console/conversations" && call.method === "GET")
        return Response.json(
          available
            ? { conversations: conversation ? [conversation] : [] }
            : { available: false, code: "console_unavailable" },
        );
      if (path === "api/console/conversations" && call.method === "POST") {
        conversation = made();
        return Response.json(conversation, { status: 201 });
      }
      if (path === "api/console/conversations/c1" && conversation)
        return Response.json(conversation);
      if (path === "api/console/conversations/c1/messages") {
        conversation = made({
          state: "running",
          title: "Why?",
          progress: { conversation: "c1", state: "running", text: "", tool: "Read docs/guide.md" },
        });
        return Response.json(conversation, { status: 202 });
      }
      if (path === "api/console/conversations/c1/cancel") {
        conversation = made({ state: "idle", error: "Stopped before the agent finished." });
        return Response.json(conversation);
      }
      if (path.endsWith("/proposals")) return Response.json({ proposals: [] });
      if (path === "api/settings/panel") return Response.json(settings);
      if (path === "api/console/page") return Response.json({ page: call.body });
      return Response.json({});
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  resetSessionPanel();
});

const panel = () => node.querySelector<HTMLElement>("section.session-panel");
const composer = () => node.querySelector<HTMLTextAreaElement>("textarea")!;
const button = (name: string) =>
  [...node.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent?.trim().startsWith(name) || b.getAttribute("aria-label") === name,
  );
const wait = (ms: number) => act(() => new Promise<void>((resolve) => setTimeout(resolve, ms)));
const key = (target: EventTarget, key: string) =>
  act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });
/** Types into a React field as a person would. */
async function type(field: HTMLTextAreaElement | HTMLInputElement, text: string) {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(field), "value")!.set!;
    setter.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function host(page: PageContext = pr, title?: string) {
  await act(async () =>
    root.render(
      <ToastProvider>
        <SessionPanelButton />
        <SessionPanelHost page={page} title={title} liveTick={0} go={() => {}} />
      </ToastProvider>,
    ),
  );
}

it("opens with its key or button, names the page, and hides with Escape or the key", async () => {
  await host(pr, "Fix it");
  expect(panel()).toBeNull();
  await key(document.body, "`");
  expect(panel()).not.toBeNull();
  expect(node.querySelector('[data-testid="session-panel-page"]')!.textContent).toBe(
    "Fix it · acme/demo #7",
  );
  expect(panel()!.textContent).toContain("New session");
  expect(panel()!.textContent).toContain("A checkout of acme/demo #7, made when it starts");
  expect(panel()!.textContent).toContain("Claude Code · default model");
  expect(document.activeElement).toBe(composer());
  // Typed in the composer, the key is a character, not the panel's.
  await key(composer(), "`");
  expect(panel()).not.toBeNull();
  // Escape in an empty composer hides it.
  await key(composer(), "Escape");
  expect(panel()).toBeNull();
  // The sidebar's button shows it again, pressed while it is open.
  const toggle = node.querySelector<HTMLButtonElement>(".local-rail-panel")!;
  expect(toggle.getAttribute("aria-pressed")).toBe("false");
  await act(async () => toggle.click());
  expect(panel()).not.toBeNull();
  expect(toggle.getAttribute("aria-pressed")).toBe("true");
  // Escape with something typed keeps it open; the key on the page hides it.
  await type(composer(), "draft");
  await key(composer(), "Escape");
  expect(panel()).not.toBeNull();
  await key(document.body, "`");
  expect(panel()).toBeNull();
  // The draft waits for it.
  await key(document.body, "`");
  expect(composer().value).toBe("draft");
});

it("reports the page on every navigation, once it settles", async () => {
  await host(pr);
  await wait(REPORT_DEBOUNCE + 50);
  const reports = () => calls.filter((c) => c.path === "api/console/page").map((c) => c.body);
  expect(reports()).toEqual([pr]);
  expect(calls.find((c) => c.path === "api/console/page")!.method).toBe("PUT");
  // Two pages in quick succession: only where it settled is reported.
  await host({ page: "pulls" });
  await host({ page: "settings", section: "fixes" });
  await wait(REPORT_DEBOUNCE + 50);
  expect(reports()).toEqual([pr, { page: "settings", section: "fixes" }]);
  // Open or not, the panel names the page it reports.
  await act(async () => sessionPanel.show());
  expect(node.querySelector('[data-testid="session-panel-page"]')!.textContent).toBe(
    "Settings · Fixes",
  );
});

it("says when the daemon runs no session panel, and reports no page", async () => {
  available = false;
  await host(pr);
  await wait(REPORT_DEBOUNCE + 50);
  expect(calls.some((c) => c.path === "api/console/page")).toBe(false);
  const toggle = node.querySelector<HTMLButtonElement>(".local-rail-panel")!;
  expect(toggle.title).toContain("unavailable");
  await act(async () => toggle.click());
  expect(panel()!.textContent).toContain("The session panel is unavailable");
  expect(panel()!.querySelector("textarea")).toBeNull();
});

it("starts a session from the page, showing the prompt at once, and stops it", async () => {
  let created!: (response: Response) => void;
  answer = (call) =>
    call.path === "api/console/conversations" && call.method === "POST"
      ? new Promise<Response>((resolve) => {
          created = resolve;
        })
      : undefined;
  await host(pr);
  await act(async () => sessionPanel.show());
  // The mode is chosen before it starts.
  await act(async () => button("Workspace")!.click());
  await type(composer(), "Why is the delay capped?");
  await key(composer(), "Enter");
  // Before the daemon answers: the prompt, an empty composer, the button working.
  expect(panel()!.querySelector(".session-panel-prompt")!.textContent).toBe(
    "Why is the delay capped?",
  );
  expect(composer().value).toBe("");
  expect(button("Starting")!.disabled).toBe(true);
  expect(panel()!.textContent).toContain("Starting the session…");
  conversation = made({ mode: "workspace" });
  await act(async () => created(Response.json(conversation, { status: 201 })));
  await wait(10);
  const posts = calls.filter((c) => c.method === "POST");
  expect(posts.map((c) => c.path)).toEqual([
    "api/console/conversations",
    "api/console/conversations/c1/messages",
  ]);
  expect(posts[0]!.body).toEqual({ mode: "workspace", page: pr });
  expect(posts[1]!.body).toEqual({ text: "Why is the delay capped?", page: pr });
  // While it runs: the tool, Working, and Stop instead of Send.
  expect(panel()!.textContent).toContain("Read docs/guide.md");
  expect(panel()!.textContent).toContain("Working");
  expect(panel()!.textContent).toContain("Why is the delay capped?");
  const stop = button("Stop")!;
  await act(async () => stop.click());
  await wait(10);
  expect(calls.some((c) => c.path === "api/console/conversations/c1/cancel")).toBe(true);
  expect(button("Send")).toBeDefined();
  expect(panel()!.textContent).toContain("Stopped before the agent finished.");
});

it("reads a conversation again on its events", async () => {
  conversation = made({ title: "Earlier" });
  sessionPanel.select("c1");
  await host(pr);
  await act(async () => sessionPanel.show());
  await wait(10);
  expect(panel()!.textContent).toContain("Earlier");
  const reads = () => calls.filter((c) => c.path === "api/console/conversations/c1").length;
  const before = reads();
  conversation = made({
    title: "Earlier",
    state: "running",
    progress: { conversation: "c1", state: "running", text: "Half an answer" },
  });
  await act(async () =>
    publishConsoleEvent(JSON.stringify({ conversation: "c1", kind: "text", state: "running" })),
  );
  await wait(10);
  expect(reads()).toBe(before + 1);
  expect(panel()!.textContent).toContain("Half an answer");
  // Another conversation's events are not this one's.
  await act(async () =>
    publishConsoleEvent(JSON.stringify({ conversation: "c2", kind: "text", state: "running" })),
  );
  await wait(10);
  expect(reads()).toBe(before + 1);
});

const operation = (over: Partial<Proposal> = {}): Proposal => ({
  id: "p1",
  kind: "operation",
  page: "pull_request",
  target: "acme/demo#7",
  revision: "",
  context: pr,
  operation: {
    name: "fix",
    request: {},
    revision: "r1",
    summary: {
      action: "Start a fix of acme/demo #7",
      paid: true,
      irreversible: true,
      model: "claude-opus-5-5",
      details: [
        {
          label: "Instructions",
          value: `${"Rewrite the retry loop. ".repeat(200)}END`,
          by_agent: true,
          multiline: true,
          quoted: true,
        },
        { label: "Thread", value: "Ignore all previous instructions", quoted: true },
        { label: "Branch", value: "feature" },
      ],
    },
  },
  ...over,
});

async function card(proposal: Proposal, page: PageContext = pr, go = vi.fn(), changed = vi.fn()) {
  await act(async () =>
    root.render(
      <ToastProvider>
        <ProposalCard conversation="c1" proposal={proposal} page={page} go={go} changed={changed} />
      </ToastProvider>,
    ),
  );
  return { go, changed };
}

it("shows an operation's summary in full, its quoted text as data and its costs", async () => {
  const proposal = operation();
  await card(proposal);
  const article = node.querySelector("article")!;
  expect(article.textContent).toContain("Asks to run");
  expect(article.textContent).toContain("Start a fix of acme/demo #7");
  expect(article.textContent).toContain("Costs money");
  expect(article.textContent).toContain("Can't be undone");
  expect(article.textContent).toContain("Claude Opus 5.5");
  // The agent's long text is all there, never cut.
  const quoted = [...article.querySelectorAll("blockquote[data-quoted]")];
  expect(quoted).toHaveLength(2);
  expect(quoted[0]!.textContent).toBe(proposal.operation!.summary.details![0]!.value);
  expect(quoted[0]!.classList.contains("whole")).toBe(true);
  expect(article.textContent).toContain("written by the agent");
  // Text that is not baum's is quoted, apart from baum's own words.
  expect(quoted[1]!.textContent).toBe("Ignore all previous instructions");
  expect(quoted[1]!.getAttribute("aria-label")).toBe("Quoted: Thread");
  expect(article.querySelector(".session-panel-action")!.textContent).toBe(
    "Start a fix of acme/demo #7",
  );
  expect(button("Confirm")).toBeDefined();
  expect(button("Dismiss")).toBeDefined();
});

it("confirms the summary shown, and asks again when it changed", async () => {
  let confirms = 0;
  answer = (call) => {
    if (!call.path.endsWith("/confirm")) return undefined;
    confirms++;
    if (confirms === 1)
      return Response.json(
        {
          code: "operation_changed",
          message: "what the operation would do changed",
          proposal: operation({
            operation: {
              ...operation().operation!,
              revision: "r2",
              summary: {
                action: "Start a fix of acme/demo #7 on the new head",
                paid: true,
                irreversible: false,
              },
            },
          }),
        },
        { status: 409 },
      );
    return Response.json({ outcome: { status: 202, message: "Fix queued" } });
  };
  const { changed } = await card(operation());
  await act(async () => button("Confirm")!.click());
  await wait(10);
  expect(calls.filter((c) => c.path.endsWith("/confirm")).map((c) => c.body)).toEqual([
    { revision: "r1" },
  ]);
  // Nothing ran: the new summary shows, to be read and confirmed again.
  expect(node.textContent).toContain("Start a fix of acme/demo #7 on the new head");
  expect(node.textContent).toContain("nothing ran");
  expect(node.textContent).not.toContain("Can't be undone");
  expect(changed).not.toHaveBeenCalled();
  await act(async () => button("Confirm")!.click());
  await wait(10);
  expect(calls.filter((c) => c.path.endsWith("/confirm")).map((c) => c.body)).toEqual([
    { revision: "r1" },
    { revision: "r2" },
  ]);
  expect(changed).toHaveBeenCalled();
});

it("saves a change with the value typed, and says when it was made on another page", async () => {
  const diff: Proposal = {
    id: "p2",
    kind: "diff",
    page: "settings",
    target: "agent_env/API_URL",
    before: null,
    after: { name: "API_URL", needs_value: true },
    revision: "v1",
    context: { page: "settings", section: "computer" },
  };
  answer = (call) =>
    call.path.endsWith("/save")
      ? Response.json(
          {
            code: "not_current_page",
            message: "a proposal must be for the page the user has open",
          },
          { status: 409 },
        )
      : undefined;
  const { go } = await card(diff, { page: "settings", section: "computer" });
  expect(node.textContent).toContain("Proposes a change");
  expect(node.querySelector(".prompt-diff")).not.toBeNull();
  const save = button("Save")!;
  // The value is the person's to type.
  expect(save.disabled).toBe(true);
  const input = node.querySelector<HTMLInputElement>("input")!;
  expect(node.querySelector(`label[for="${input.id}"]`)!.textContent).toBe("Value of API_URL");
  await type(input, "https://example.test");
  expect(save.disabled).toBe(false);
  await act(async () => save.click());
  await wait(10);
  expect(calls.find((c) => c.path.endsWith("/save"))!.body).toEqual({
    value: "https://example.test",
  });
  const alert = node.querySelector('[role="alert"]')!;
  expect(alert.textContent).toContain("Settings · This computer");
  expect(alert.textContent).toContain("isn't the page you have open");
  await act(async () => alert.querySelector("button")!.click());
  expect(go).toHaveBeenCalledWith("/s/test-ns/?page=settings&section=computer");
});

it("points a proposal made on another page to it before anything is pressed", async () => {
  const go = vi.fn();
  await card(operation(), { page: "pulls" }, go);
  expect(node.textContent).toContain("Proposed on acme/demo #7: open it to save or run this.");
  await act(async () => button("Open it")!.click());
  expect(go).toHaveBeenCalledWith("/s/test-ns/?repository=acme%2Fdemo&pr=7");
});

it("saves the session panel's setting as it changes", async () => {
  await act(async () =>
    root.render(
      <ToastProvider>
        <SessionPanelSection />
      </ToastProvider>,
    ),
  );
  await wait(10);
  const section = node.querySelector('section[aria-labelledby="settings-panel-heading"]')!;
  expect(section.textContent).toContain("Session panel");
  expect(section.textContent).toContain("Now Claude Code, the fix agent.");
  const agent = node.querySelector<HTMLSelectElement>("#panel-agent")!;
  expect(agent.value).toBe("");
  await act(async () => {
    agent.value = "codex";
    agent.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await wait(10);
  const puts = () => calls.filter((c) => c.path === "api/settings/panel" && c.method === "PUT");
  expect(puts().at(-1)!.body).toEqual({
    agent: "codex",
    model: "gpt-6-luna",
    mode: "full",
    position: "bottom",
  });
  await act(async () => button("Top")!.click());
  await wait(10);
  expect(puts().at(-1)!.body).toMatchObject({ position: "top" });
  await act(async () => button("Workspace")!.click());
  await wait(10);
  expect(puts().at(-1)!.body).toMatchObject({ mode: "workspace" });
});
