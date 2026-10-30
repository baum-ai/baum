import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import {
  chatJobs,
  fixJob,
  jobName,
  reviewJob,
  type ChatConversation,
  type ChatTurn,
  type Entry,
  type Job,
} from "./api";
import { AskPanel, focusAnchors, sectionFocus, stopFocus, turnReasonText } from "./chat";
import { LocalReview } from "./review";

vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, header, renderSectionAside }: ReviewViewerProps) => (
    <div>
      {header?.beside}
      {document ? <p>{document.summary.text}</p> : null}
      {document && renderSectionAside ? renderSectionAside("sec_retry") : null}
    </div>
  ),
}));

const doc = example as unknown as ReviewDocument;
const turn = (rest: Partial<ChatTurn> = {}): ChatTurn => ({
  seq: 1,
  question: "Why is the delay capped?",
  job_id: "job-1",
  state: "completed",
  created: "2026-10-28T10:00:00Z",
  finished: "2026-10-28T10:00:20Z",
  ...rest,
});
const conversation = (rest: Partial<ChatConversation> = {}): ChatConversation => ({
  id: "c1",
  version: doc.review.version,
  backend: "claude",
  model: "claude-sonnet-4-5",
  seeded: false,
  created: "2026-10-28T10:00:00Z",
  updated: "2026-10-28T10:00:20Z",
  turns: [turn()],
  ...rest,
});
const answered = (rest: Partial<ChatTurn> = {}) =>
  turn({
    answer: {
      answer_markdown: "The cap keeps **retries** bounded.\n\n<b>raw html</b>",
      citations: [
        { path: "queue/retry.go", start_line: 31, end_line: 42 },
        { path: "docs/else where.md", start_line: 3, end_line: 3 },
      ],
      suggests_fix: null,
    },
    cost_usd: 0.0123,
    ...rest,
  });

let root: Root;
let node: HTMLDivElement;
let conversations: ChatConversation[];
let posted: unknown[];
let postAnswer: () => Response;
let cancelled: string[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  conversations = [];
  posted = [];
  cancelled = [];
  postAnswer = () =>
    Response.json(
      { conversation_id: "c1", turn: 1, job: {} },
      { status: 202, headers: { "Content-Type": "application/json" } },
    );
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      const url = new URL(path, "http://x");
      if (url.pathname.endsWith("/api/prs/chat")) {
        if (init?.method === "POST") {
          posted.push(JSON.parse(String(init.body)));
          return postAnswer();
        }
        return Response.json({ conversations });
      }
      if (url.pathname.endsWith("/cancel")) {
        cancelled.push(url.pathname);
        return Response.json({});
      }
      if (url.pathname.endsWith("/transcript"))
        return Response.json({
          entries: [
            { seq: 1, time: "t", kind: "message", text: "Reading the retry code." },
            { seq: 2, time: "t", kind: "tool", id: "t1", tool: "Read", title: "queue/retry.go" },
          ],
          next: 2,
          more: false,
          done: false,
        });
      if (url.pathname.endsWith("/api/document")) return Response.json(doc);
      if (url.pathname.endsWith("/api/versions")) return Response.json([]);
      return Response.json({ available: true, sessions: [], commits: [] });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 30)));
const button = (name: string | RegExp) =>
  Array.from(node.querySelectorAll("button")).find((b) =>
    typeof name === "string" ? b.textContent === name : name.test(b.textContent ?? ""),
  ) as HTMLButtonElement | undefined;

async function panel(extra: Partial<Parameters<typeof AskPanel>[0]> = {}) {
  await act(async () =>
    root.render(
      <AskPanel
        clone="clone"
        pr={212}
        pullRequestUrl="https://github.com/example-org/taskline/pull/212"
        latestVersion={doc.review.version}
        document={doc}
        patches={null}
        focus={null}
        clearFocus={() => {}}
        close={() => {}}
        {...extra}
      />,
    ),
  );
  await settle();
}
const type = async (text: string) => {
  const box = node.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
};

it("explains the agent when there are no conversations yet", async () => {
  await panel();
  expect(node.textContent).toContain("answers questions about the code");
  expect(node.textContent).toContain("it changes nothing");
  expect(button("Send")!.disabled).toBe(true);
});

it("shows a running question with its live transcript and cancels it", async () => {
  conversations = [conversation({ turns: [turn({ state: "running" })] })];
  await panel();
  expect(node.textContent).toContain("Why is the delay capped?");
  expect(node.textContent).toContain("The agent is answering");
  expect(node.querySelector(".local-job-message")!.textContent).toBe("Reading the retry code.");
  // Tool calls are collapsed disclosures.
  expect(node.querySelector("details.local-job-tool")).not.toBeNull();
  expect((node.querySelector("details.local-job-tool") as HTMLDetailsElement).open).toBe(false);
  // The box is closed to a new question meanwhile.
  await type("And the floor?");
  expect(button("Send")!.disabled).toBe(true);
  expect(node.textContent).toContain("Waiting for the answer");
  await act(async () => button("Cancel")!.click());
  expect(cancelled).toEqual(["/s/test-ns/api/jobs/job-1/cancel"]);
});

it("renders an answer as markdown, never raw HTML, with its citations and their lines", async () => {
  conversations = [conversation({ turns: [answered()] })];
  await panel();
  expect(node.querySelector(".ask-answer strong")!.textContent).toBe("retries");
  expect(node.querySelector(".ask-answer b")).toBeNull();
  expect(node.textContent).toContain("<b>raw html</b>");
  const cites = Array.from(node.querySelectorAll(".ask-cite")).map((c) => c.textContent);
  expect(cites).toEqual(["queue/retry.go:31–42", "docs/else where.md:3"]);
  expect(node.textContent).toContain("Cost $0.0123");
  // The slot under the answer is there for the next step's actions.
  expect(node.querySelector("[data-answer-actions]")).not.toBeNull();
  // A citation in one of the review's hunks opens that hunk's lines.
  await act(async () => (node.querySelectorAll(".ask-cite")[0] as HTMLElement).click());
  const evidence = node.querySelector("details")!;
  expect(evidence.hasAttribute("open")).toBe(true);
  expect(evidence.textContent).toContain("excerpt");
  // One outside them says so, and links where the review's head shows it.
  await act(async () => (node.querySelectorAll(".ask-cite")[1] as HTMLElement).click());
  expect(node.querySelector(".ask-cite-note")!.textContent).toContain("no excerpt to show");
  expect(node.querySelector(".ask-cite-note a")!.getAttribute("href")).toBe(
    `https://github.com/example-org/taskline/blob/${doc.review.head}/docs/else%20where.md#L3-L3`,
  );
});

it("leaves out the GitHub link for another host", async () => {
  conversations = [conversation({ turns: [answered()] })];
  await panel({ pullRequestUrl: "https://ghe.example.com/example-org/taskline/pull/212" });
  await act(async () => (node.querySelectorAll(".ask-cite")[1] as HTMLElement).click());
  expect(node.querySelector(".ask-cite-note")!.textContent).toContain("no excerpt to show");
  expect(node.querySelector(".ask-cite-note a")).toBeNull();
});

it("keeps questions tied to the version on screen and says which conversations are outdated", async () => {
  const shown = structuredClone(doc);
  shown.review.version = 2;
  conversations = [
    conversation({ id: "newer", version: 4, turns: [answered({ question: "Newer question" })] }),
    conversation({ id: "now", version: 2, turns: [answered({ question: "Now question" })] }),
    conversation({ id: "old", version: 1, turns: [answered({ question: "Old question" })] }),
  ];
  // Version 2 is on screen; version 4 is the latest review.
  await panel({ document: shown, latestVersion: 4 });
  const headings = Array.from(node.querySelectorAll(".ask-convos h3")).map((h) => h.textContent);
  expect(headings).toEqual([
    "Review v2",
    "Asked about review v4",
    "Outdated: asked about review v1",
  ]);
  expect(node.textContent).toContain("Now question");
  // A newer review's conversation is read-only here, and not called outdated.
  await act(async () => button("Newer question")!.click());
  expect(node.querySelector("textarea")).toBeNull();
  expect(node.textContent).toContain("about a newer review, so it is read-only");
  expect(node.textContent).not.toContain("Outdated: asked about review v4");
  await act(async () => button("Old question")!.click());
  expect(node.textContent).toContain("about an older review, so it is read-only");
  // A question goes to the version on screen.
  await act(async () => button("Now question")!.click());
  await type("And here?");
  await act(async () => button("Send")!.click());
  expect(posted).toEqual([
    { clone: "clone", pr: 212, version: 2, conversation_id: "now", question: "And here?" },
  ]);
});

it("says in words why a question was not answered", async () => {
  conversations = [
    conversation({
      turns: [
        turn({ seq: 1, state: "failed", reason: "validation", cost_usd: 0.5 }),
        turn({ seq: 2, state: "failed", reason: "budget" }),
        turn({ seq: 3, state: "failed", reason: "backend" }),
        turn({ seq: 4, state: "failed", reason: "internal" }),
        turn({ seq: 5, state: "failed", reason: "needs_clone" }),
        turn({ seq: 6, state: "cancelled" }),
        turn({ seq: 7, state: "interrupted", note: "The agent kept no session." }),
      ],
    }),
  ];
  await panel();
  const text = node.textContent!;
  expect(text).toContain("didn't pass baum's checks");
  expect(text).toContain("time or size limit");
  expect(text).toContain("provider was unavailable");
  expect(text).toContain("Something went wrong inside baum");
  expect(text).toContain("no local clone");
  expect(text).toContain("You cancelled this question");
  expect(text).toContain("because baum stopped or restarted");
  expect(text).toContain("The agent kept no session.");
  expect(text).toContain("Cost $0.5000");
  expect(turnReasonText(turn({ state: "failed", reason: "unsupported_backend" }))).toContain(
    "Claude Code or Codex",
  );
});

it("keeps older reviews' conversations read-only and starts new ones on the current review", async () => {
  conversations = [
    conversation({ id: "current", turns: [answered({ question: "Current question" })] }),
    conversation({ id: "old", version: 1, turns: [answered({ question: "Old question" })] }),
  ];
  await panel();
  expect(node.textContent).toContain("Review v3");
  expect(node.textContent).toContain("Outdated: asked about review v1");
  expect(node.querySelector("textarea")).not.toBeNull();
  await act(async () => button("Old question")!.click());
  expect(node.querySelector("textarea")).toBeNull();
  expect(node.textContent).toContain("older review, so it is read-only");
  // Its citations are named, not drawn from this review's hunks.
  await act(async () => (node.querySelectorAll(".ask-cite")[0] as HTMLElement).click());
  expect(node.querySelector(".ask-cite-note")).not.toBeNull();
  await act(async () => button("New conversation")!.click());
  expect(node.querySelector("textarea")).not.toBeNull();
  await type("A fresh one");
  await act(async () => button("Send")!.click());
  expect(posted).toEqual([
    { clone: "clone", pr: 212, version: doc.review.version, question: "A fresh one" },
  ]);
});

it("notes that a seeded conversation started without the review's context", async () => {
  conversations = [
    conversation({ seeded: true, turns: [answered({ note: "Seeded from disk." })] }),
  ];
  await panel();
  expect(node.textContent).toContain("Started without the review's own context.");
  expect(node.textContent).toContain("Seeded from disk.");
});

it("sends with Enter, keeps Shift+Enter for a new line and clears the focus it asked with", async () => {
  conversations = [conversation({ turns: [answered()] })];
  const cleared = vi.fn();
  const focus = sectionFocus(doc, "sec_retry")!;
  await panel({ focus, clearFocus: cleared });
  expect(node.querySelector(".ask-focus")!.textContent).toContain(focus.title);
  await type("What about jitter?");
  const box = node.querySelector("textarea")!;
  await act(async () => {
    box.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, bubbles: true }),
    );
  });
  expect(posted).toEqual([]);
  await act(async () => {
    box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  expect(posted).toEqual([
    {
      clone: "clone",
      pr: 212,
      version: doc.review.version,
      conversation_id: "c1",
      question: "What about jitter?",
      focus,
    },
  ]);
  expect(cleared).toHaveBeenCalled();
});

it("shows the daemon's words when the conversation is busy", async () => {
  conversations = [conversation({ turns: [answered()] })];
  postAnswer = () =>
    Response.json(
      {
        code: "busy",
        message: "The agent is still answering the last question of this conversation.",
      },
      { status: 409 },
    );
  await panel();
  await type("Another?");
  await act(async () => button("Send")!.click());
  await settle();
  expect(node.querySelector('[role="alert"]')!.textContent).toContain(
    "still answering the last question",
  );
  // The question stays for another try.
  expect(node.querySelector("textarea")!.value).toBe("Another?");
});

it("says so when chat is not available", async () => {
  // What the daemon answers without the capability.
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json(
        { code: "chat_unavailable", message: "Chat is not available." },
        { status: 503 },
      ),
    ),
  );
  await panel();
  expect(node.textContent).toContain("not available on this computer");
});

it("builds a focus from a section and a stop with the hunks' files and lines", () => {
  const section = sectionFocus(doc, "sec_retry")!;
  expect(section).toMatchObject({
    kind: "section",
    id: "sec_retry",
    title: "Retry policy and backoff",
  });
  expect(section.anchors).toContainEqual({ path: "queue/retry.go", start_line: 1, end_line: 43 });
  expect(sectionFocus(doc, "missing")).toBeNull();
  const stop = stopFocus(doc, {
    id: "beh_delay",
    title: "Delays",
    anchors: [
      { type: "hunk", id: "h_141757b19901", lines: [31, 42] },
      { type: "pr", number: 1 },
    ],
  });
  expect(stop).toEqual({
    kind: "stop",
    id: "beh_delay",
    title: "Delays",
    anchors: [{ path: "queue/retry.go", start_line: 31, end_line: 42 }],
  });
  const many = focusAnchors(
    doc,
    doc.index.hunks.flatMap((hunk) => Array(3).fill({ type: "hunk", id: hunk.id })),
  );
  expect(many.length).toBeLessThanOrEqual(20);
});

it("does not take chat jobs for reviews or fixes", () => {
  const entry = {
    pull_request: { repository: "Acme/Demo", number: 7 },
  } as Entry;
  const base = {
    clone: "c",
    repository: "acme/demo",
    pr: 7,
    phase: "answering",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
  };
  const chat: Job = { ...base, id: "chat", state: "running", kind: "chat" };
  const review: Job = { ...base, id: "review", state: "completed" };
  const fix: Job = { ...base, id: "fix", state: "completed", kind: "fix" };
  const jobs = [review, fix, chat];
  expect(reviewJob(entry, jobs)?.id).toBe("review");
  expect(reviewJob(entry, [chat])).toBeUndefined();
  expect(fixJob(entry, [chat])).toBeUndefined();
  expect(chatJobs(entry, jobs).map((job) => job.id)).toEqual(["chat"]);
  expect(jobName(chat)).toBe("Question on #7");
});

function Harness({ search }: { search: string }) {
  const [params, setParams] = useState(() => new URLSearchParams(search));
  const entry = {
    pull_request: {
      repository: "example-org/taskline",
      number: 212,
      head_sha: doc.review.head,
      title: "Example",
      url: "https://github.com/example-org/taskline/pull/212",
      state: "OPEN",
    },
    clone_id: "clone",
    status: "current",
    version_count: 3,
  } as unknown as Entry;
  return (
    <UrlStateProvider
      value={{
        search: params,
        update: (patch) => setParams(patch(new URLSearchParams(params))),
      }}
    >
      <LocalReview
        entry={entry}
        clone="clone"
        revision={0}
        changed={async () => {}}
        generate={() => {}}
      />
      <input aria-label="elsewhere" />
      <output data-search>{params.toString()}</output>
    </UrlStateProvider>
  );
}
const press = (target: EventTarget, key: string) =>
  act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });

it("opens the Ask panel with a, keeps it in the URL, and ignores a typed in a text field", async () => {
  await act(async () => root.render(<Harness search="" />));
  await settle();
  expect(node.querySelector(".ask-panel")).toBeNull();
  const field = node.querySelector("input")!;
  await press(field, "a");
  expect(node.querySelector(".ask-panel")).toBeNull();
  await press(document.body, "a");
  await settle();
  expect(node.querySelector(".ask-panel")).not.toBeNull();
  expect(node.querySelector("[data-search]")!.textContent).toBe("ask=1");
  // The box has the cursor, so typing an a there is typing.
  expect(document.activeElement).toBe(node.querySelector("textarea"));
  const before = node.querySelector(".ask-panel");
  await press(node.querySelector("textarea")!, "a");
  expect(node.querySelector(".ask-panel")).toBe(before);
  // Escape in the panel closes it.
  await press(node.querySelector("textarea")!, "Escape");
  await settle();
  expect(node.querySelector(".ask-panel")).toBeNull();
  // The keyboard goes back to the Ask button.
  expect(document.activeElement).toBe(button("Ask"));
  expect(node.querySelector("[data-search]")!.textContent).toBe("");
  // The button does the same, and a reload with ask=1 starts open.
  await act(async () => button("Ask")!.click());
  expect(node.querySelector(".ask-panel")).not.toBeNull();
});

it("starts open when the URL says ask=1, and asks about a section from the review", async () => {
  await act(async () => root.render(<Harness search="ask=1" />));
  await settle();
  expect(node.querySelector(".ask-panel")).not.toBeNull();
  await act(async () => button("Ask about this section")!.click());
  expect(node.querySelector(".ask-focus")!.textContent).toContain("Retry policy and backoff");
  await act(async () => button("Remove")!.click());
  expect(node.querySelector(".ask-focus")).toBeNull();
});
