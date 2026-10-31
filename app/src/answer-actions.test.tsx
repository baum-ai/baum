import { mockStateFetch } from "./ui-state-test-helper";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import { UrlStateProvider } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import type { ChatConversation, ChatTurn, Entry } from "./api";
import type { FixChat } from "./fix";
import { LocalReview } from "./review";
import {
  answerComment,
  appendToDraft,
  buildTour,
  loadProgress,
  progressKey,
} from "./walkthrough-model";

vi.mock("@baum/viewer/components/review-viewer", () => ({
  ReviewViewer: ({ document, header }: ReviewViewerProps) => (
    <div>
      {header?.beside}
      {document ? <p>{document.summary.text}</p> : null}
    </div>
  ),
}));

const doc = example as unknown as ReviewDocument;
const tour = buildTour(doc);
const key = progressKey(doc);

const answered = (rest: Partial<ChatTurn> = {}): ChatTurn => ({
  seq: 1,
  question: "Why is the delay capped?",
  job_id: "job-1",
  state: "completed",
  created: "2026-10-28T10:00:00Z",
  finished: "2026-10-28T10:00:20Z",
  focus: { kind: "stop", id: "beh_delay", title: "Retries wait longer" },
  answer: {
    answer_markdown: "The cap keeps **retries** bounded.",
    citations: [
      { path: "queue/retry.go", start_line: 31, end_line: 42 },
      { path: "queue/retry.go", start_line: 7, end_line: 7 },
    ],
    suggests_fix: { summary: "Lower the cap to 30s" },
  },
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
  turns: [answered()],
  ...rest,
});

let root: Root;
let node: HTMLDivElement;
let conversations: ChatConversation[];
let calls: string[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  conversations = [conversation()];
  calls = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string, init?: RequestInit) => {
      const url = new URL(path, "http://x");
      calls.push(`${init?.method ?? "GET"} ${url.pathname}`);
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
});
const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 30)));
const button = (name: string | RegExp) =>
  Array.from(node.querySelectorAll("button")).find((b) =>
    typeof name === "string" ? b.textContent === name : name.test(b.textContent ?? ""),
  ) as HTMLButtonElement | undefined;

function Harness({
  search,
  pull = {},
  fix,
}: {
  search: string;
  pull?: Record<string, unknown>;
  fix?: (chat?: FixChat) => void;
}) {
  const [params, setParams] = useState(() => new URLSearchParams(search));
  const entry = {
    pull_request: {
      repository: "example-org/taskline",
      number: 212,
      head_sha: doc.review.head,
      title: "Example",
      url: "https://github.com/example-org/taskline/pull/212",
      state: "OPEN",
      fork: false,
      viewer_can_push: true,
      ...pull,
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
        fix={fix}
      />
      <output data-search>{params.toString()}</output>
    </UrlStateProvider>
  );
}
async function open(props: Parameters<typeof Harness>[0]) {
  await act(async () => root.render(<Harness {...props} />));
  await settle();
}

it("offers Fix this only on an answer about the current review, when the pull request can be fixed", async () => {
  const fix = vi.fn();
  await open({ search: "ask=1", fix });
  expect(button("Fix this")).toBeDefined();
  // The answer's suggestion is shown above the buttons.
  expect(node.querySelector(".ask-suggestion")!.textContent).toContain("Lower the cap to 30s");
  await act(async () => button("Fix this")!.click());
  expect(fix).toHaveBeenCalledWith({
    conversation_id: "c1",
    turn: 1,
    suggestion: "Lower the cap to 30s",
  });
  // A fork, a repository the person cannot push to, or no clone: no fix, but the comment stays.
  for (const pull of [{ fork: true }, { viewer_can_push: false }, { viewer_can_push: null }]) {
    await open({ search: "ask=1", fix: vi.fn(), pull });
    expect(button("Fix this")).toBeUndefined();
    expect(button("Add as comment")).toBeDefined();
  }
  await open({ search: "ask=1", fix: undefined });
  expect(button("Fix this")).toBeUndefined();
  await act(async () => root.render(null));
  // An answer about an older review has neither action.
  conversations = [conversation({ version: doc.review.version - 1 })];
  await act(async () => root.render(null));
  await open({ search: "ask=1", fix });
  await act(async () => button(/Why is the delay capped/)?.click());
  expect(node.textContent).toContain("The cap keeps");
  expect(button("Fix this")).toBeUndefined();
  expect(button("Add as comment")).toBeUndefined();
});

it("puts the answer in the stop's own comment, never sends it, and undoes", async () => {
  await open({ search: "ask=1", fix: vi.fn() });
  await act(async () => button("Add as comment")!.click());
  expect(node.querySelector(".ask-added")!.textContent).toContain("Added to your review draft");
  const expected = "The cap keeps **retries** bounded.\n\nqueue/retry.go:31–42\nqueue/retry.go:7";
  expect(answerComment(answered().answer!)).toBe(expected);
  expect(loadProgress(key).notes).toEqual({ beh_delay: expected });
  expect(loadProgress(key).note).toBe("");
  // Another answer to the same stop goes after what is there.
  appendToDraft(key, { kind: "stop", id: "beh_delay", title: "x" }, "Second.");
  expect(loadProgress(key).notes.beh_delay).toBe(`${expected}\n\nSecond.`);
  // Nothing was sent: no review was posted, and no fix was started.
  expect(calls.filter((call) => call.startsWith("POST"))).toEqual([]);
  // Undo takes back only what this answer added, and the text typed after it stays.
  await act(async () => button("Undo")!.click());
  expect(loadProgress(key).notes.beh_delay).toBe(`${expected}\n\nSecond.`);
  expect(node.querySelector(".ask-added")!.textContent).toContain("nothing was removed");
});

it("takes the undo back to the draft as it was", async () => {
  appendToDraft(key, { kind: "stop", id: "beh_delay", title: "x" }, "Mine.");
  await open({ search: "ask=1", fix: vi.fn() });
  await act(async () => button("Add as comment")!.click());
  expect(loadProgress(key).notes.beh_delay).toContain("Mine.\n\nThe cap keeps");
  await act(async () => button("Undo")!.click());
  expect(loadProgress(key).notes.beh_delay).toBe("Mine.");
  expect(node.querySelector(".ask-added")!.textContent).toContain("Removed from your review draft");
});

it("puts an answer about no stop in the overall comment, which an open walkthrough shows", async () => {
  conversations = [
    conversation({
      turns: [answered({ focus: { kind: "section", id: "sec_retry", title: "Retry" } })],
    }),
  ];
  await open({ search: "ask=1&walk=tour", fix: vi.fn() });
  await act(async () => button(/^Send review/)!.click());
  const overall = () =>
    node.querySelector<HTMLTextAreaElement>("section[aria-label='Your review'] textarea")!;
  expect(overall().value).toBe("");
  await act(async () => button("Add as comment")!.click());
  expect(node.querySelector(".ask-added")!.textContent).toContain("as the overall comment");
  expect(overall().value).toContain("The cap keeps **retries** bounded.");
  expect(loadProgress(key).note).toContain("queue/retry.go:31–42");
  // Undo empties it again, and the walkthrough follows.
  await act(async () => button("Undo")!.click());
  expect(overall().value).toBe("");
  expect(calls.filter((call) => call.startsWith("POST"))).toEqual([]);
});

it("opens the walkthrough's submit panel from the confirmation", async () => {
  await open({ search: "ask=1", fix: vi.fn() });
  await act(async () => button("Add as comment")!.click());
  await act(async () => button("Open the draft")!.click());
  await settle();
  expect(node.querySelector("[data-search]")!.textContent).toContain("walk=tour");
  expect(node.querySelector("section[aria-label='Your review']")!.textContent).toContain(
    "The cap keeps **retries** bounded.",
  );
  expect(tour.stops.some((stop) => stop.id === "beh_delay")).toBe(true);
});
