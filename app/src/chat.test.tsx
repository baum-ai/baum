import { mockStateFetch } from "./ui-state-test-helper";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import {
  fixJob,
  jobName,
  reviewJob,
  type ChatConversation,
  type ChatTurn,
  type Entry,
  type Job,
} from "./api";
import { EarlierQuestions, focusAnchors, sectionFocus, stopFocus, turnReasonText } from "./chat";
import { LocalReview } from "./review";
import { resetSessionPanel, useSessionPanel } from "./session-panel-state";

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
let posted: string[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  resetSessionPanel();
  conversations = [];
  posted = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string, init?: RequestInit) => {
      const url = new URL(path, "http://x");
      if (init?.method && init.method !== "GET") posted.push(`${init.method} ${url.pathname}`);
      if (url.pathname.endsWith("/api/prs/chat")) return Response.json({ conversations });
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
  resetSessionPanel();
});
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 30)));
const button = (name: string | RegExp) =>
  Array.from(node.querySelectorAll("button")).find((b) =>
    typeof name === "string" ? b.textContent === name : name.test(b.textContent ?? ""),
  ) as HTMLButtonElement | undefined;

/** The session panel's state, as a component on the page reads it. */
let panelState: ReturnType<typeof useSessionPanel> | null = null;
function PanelProbe() {
  panelState = useSessionPanel();
  return null;
}

async function earlier(extra: Partial<Parameters<typeof EarlierQuestions>[0]> = {}) {
  await act(async () =>
    root.render(
      <>
        <EarlierQuestions
          repository="example-org/taskline"
          pr={212}
          pullRequestUrl="https://github.com/example-org/taskline/pull/212"
          conversations={conversations}
          document={doc}
          patches={null}
          close={() => {}}
          {...extra}
        />
        <PanelProbe />
      </>,
    ),
  );
  await settle();
}

it("renders an earlier answer as markdown, never raw HTML, with its citations and their lines", async () => {
  conversations = [conversation({ turns: [answered()] })];
  await earlier();
  expect(node.querySelector(".ask-answer strong")!.textContent).toBe("retries");
  expect(node.querySelector(".ask-answer b")).toBeNull();
  expect(node.textContent).toContain("<b>raw html</b>");
  const cites = Array.from(node.querySelectorAll(".ask-cite")).map((c) => c.textContent);
  expect(cites).toEqual(["queue/retry.go:31–42", "docs/else where.md:3"]);
  expect(node.textContent).not.toContain("Cost $");
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
  await earlier({ pullRequestUrl: "https://ghe.example.com/example-org/taskline/pull/212" });
  await act(async () => (node.querySelectorAll(".ask-cite")[1] as HTMLElement).click());
  expect(node.querySelector(".ask-cite-note")!.textContent).toContain("no excerpt to show");
  expect(node.querySelector(".ask-cite-note a")).toBeNull();
});

it("is read-only: no box, no actions, and Ask goes to the session panel", async () => {
  conversations = [
    conversation({
      turns: [answered({ answer: { ...answered().answer!, suggests_fix: { summary: "Cap it" } } })],
    }),
  ];
  await earlier();
  expect(node.querySelector("textarea")).toBeNull();
  expect(button("Send")).toBeUndefined();
  expect(button("Fix this")).toBeUndefined();
  expect(button("Add as comment")).toBeUndefined();
  expect(node.textContent).toContain("Suggested change: Cap it");
  expect(node.textContent).toContain("read-only");
  await act(async () => button("Ask in the session panel")!.click());
  expect(panelState?.ask).toMatchObject({
    repository: "example-org/taskline",
    pull_request: 212,
    focus: null,
  });
  expect(posted).toEqual([]);
});

it("lists the conversations by the review version they were about", async () => {
  const shown = structuredClone(doc);
  shown.review.version = 2;
  conversations = [
    conversation({ id: "newer", version: 4, turns: [answered({ question: "Newer question" })] }),
    conversation({ id: "now", version: 2, turns: [answered({ question: "Now question" })] }),
    conversation({ id: "old", version: 1, turns: [answered({ question: "Old question" })] }),
  ];
  await earlier({ document: shown });
  const headings = Array.from(node.querySelectorAll(".ask-convos h3")).map((h) => h.textContent);
  expect(headings).toEqual(["About review v4", "About this review v2", "About review v1"]);
  // The newest opens first; another opens on a click.
  expect(node.textContent).toContain("Newer question");
  await act(async () => button("Old question")!.click());
  expect(node.querySelector('[aria-current="true"]')!.textContent).toBe("Old question");
  // Its citations are named, not drawn from the hunks of the review on screen.
  await act(async () => (node.querySelectorAll(".ask-cite")[0] as HTMLElement).click());
  expect(node.querySelector(".ask-cite-note")).not.toBeNull();
});

it("says in words why an earlier question was not answered", async () => {
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
        turn({ seq: 8, state: "queued" }),
      ],
    }),
  ];
  await earlier();
  const text = node.textContent!;
  expect(text).toContain("didn't pass baum's checks");
  expect(text).toContain("time or size limit");
  expect(text).toContain("provider was unavailable");
  expect(text).toContain("Something went wrong inside baum");
  expect(text).toContain("no local clone");
  expect(text).toContain("The question was cancelled");
  expect(text).toContain("because baum stopped or restarted");
  expect(text).toContain("The agent kept no session.");
  expect(text).toContain("Not answered");
  expect(text).not.toContain("Cost $");
  expect(turnReasonText(turn({ state: "failed", reason: "unsupported_backend" }))).toContain(
    "Claude Code or Codex",
  );
});

it("notes that a seeded conversation started without the review's context", async () => {
  conversations = [
    conversation({ seeded: true, turns: [answered({ note: "Seeded from disk." })] }),
  ];
  await earlier();
  expect(node.textContent).toContain("Started without the review's own context.");
  expect(node.textContent).toContain("Seeded from disk.");
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

it("does not take earlier chat jobs for reviews or fixes", () => {
  const entry = {
    pull_request: { repository: "Acme/Demo", number: 7 },
  } as Entry;
  const base = {
    clone: "c",
    repository: "acme/demo",
    pr: 7,
    phase: "finished",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
  };
  const chat: Job = { ...base, id: "chat", state: "completed", kind: "chat" };
  const review: Job = { ...base, id: "review", state: "completed" };
  const fix: Job = { ...base, id: "fix", state: "completed", kind: "fix" };
  expect(reviewJob(entry, [review, fix, chat])?.id).toBe("review");
  expect(reviewJob(entry, [chat])).toBeUndefined();
  expect(fixJob(entry, [chat])).toBeUndefined();
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
      <PanelProbe />
    </UrlStateProvider>
  );
}
const press = (target: EventTarget, key: string) =>
  act(async () => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });

it("asks in the session panel with a and the Ask button, ignoring a typed in a text field", async () => {
  await act(async () => root.render(<Harness search="" />));
  await settle();
  expect(panelState?.ask).toBeNull();
  await press(node.querySelector("input")!, "a");
  expect(panelState?.ask).toBeNull();
  await press(document.body, "a");
  expect(panelState?.ask).toMatchObject({
    repository: "example-org/taskline",
    pull_request: 212,
    focus: null,
    seq: 1,
  });
  // No panel of its own, and nothing in the address.
  expect(node.querySelector(".ask-panel")).toBeNull();
  expect(node.querySelector("[data-search]")!.textContent).toBe("");
  await act(async () => button("Ask")!.click());
  expect(panelState?.ask?.seq).toBe(2);
  // Without earlier questions there is nothing to open them with.
  expect(button("Earlier questions")).toBeUndefined();
  expect(posted).toEqual([]);
});

it("opens the earlier questions read-only, keeps them in the URL and closes them with Escape", async () => {
  conversations = [conversation({ turns: [answered()] })];
  await act(async () => root.render(<Harness search="" />));
  await settle();
  await act(async () => button("Earlier questions")!.click());
  expect(node.querySelector(".ask-panel")).not.toBeNull();
  expect(node.querySelector("[data-search]")!.textContent).toBe("questions=1");
  expect(node.querySelector(".ask-panel textarea")).toBeNull();
  await press(node.querySelector(".ask-panel")!, "Escape");
  await settle();
  expect(node.querySelector(".ask-panel")).toBeNull();
  expect(document.activeElement).toBe(node.querySelector("[data-questions-button]"));
  expect(node.querySelector("[data-search]")!.textContent).toBe("");
});

it("starts with the earlier questions open when the URL says questions=1", async () => {
  conversations = [conversation({ turns: [answered()] })];
  await act(async () => root.render(<Harness search="walk=full&questions=1" />));
  await settle();
  expect(node.querySelector(".ask-panel")).not.toBeNull();
});
