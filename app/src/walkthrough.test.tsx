import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import type { Entry } from "./api";
import { Walkthrough, type WalkMode } from "./walkthrough";
import {
  buildTour,
  firstSentence,
  loadProgress,
  progressKey,
  reviewSubmission,
  saveProgress,
} from "./walkthrough-model";

const doc = example as unknown as ReviewDocument;
const entry = (authored = false): Entry =>
  ({
    pull_request: {
      repository: doc.review.repository,
      number: doc.review.pull_request,
      head_sha: doc.review.head,
      title: "Example",
      url: "https://github.com/acme/demo/pull/7",
      state: "OPEN",
      draft: false,
      fork: false,
      authored_by_me: authored,
      review_requested_from_me: true,
    },
  }) as Entry;

it("orders chapters riskiest first and leaves out mechanical and obsolete sections", () => {
  const tour = buildTour(doc);
  expect(tour.chapters.map((chapter) => chapter.id)).toEqual([
    "sec_api",
    "sec_retry",
    "sec_deadletter",
    "sec_ackfix",
    "sec_docs",
  ]);
  expect(tour.chapters[0]!.meaning).toBe("Critical impact");
  expect(tour.risks[0]).toEqual({ text: expect.stringContaining("RoleFromRequest"), stop: 0 });
});

it("reads a step's tour fields and falls back for steps and sections without them", () => {
  const tour = buildTour(doc);
  const delay = tour.stops.find((stop) => stop.id === "beh_delay")!;
  expect(delay.title).toBe("Retries wait longer after each failure, up to a cap");
  expect(delay.question).toBeTruthy();
  expect(delay.place).toEqual({ path: "queue/retry.go", line: 42, side: "RIGHT", start_line: 31 });
  expect(delay.where).toBe("retry.go:42");
  expect(delay.evidence.map((item) => item.id)).toEqual(["ev_retry_tests"]);
  const budget = tour.stops.find((stop) => stop.id === "beh_budget")!;
  expect(budget.question).toBeNull();
  // A chapter carries its section's intent.
  expect(tour.chapters[delay.chapter]!.intent).toContain("wait longer after each failure");
  // A section without behaviour steps is one stop of its summary.
  const docs = tour.stops.find((stop) => stop.id === "sec_docs")!;
  expect(docs.title).toBe("Docs and dependency bump");
  expect(docs.text).toBe(doc.sections.find((s) => s.id === "sec_docs")!.summary.text);
  expect(docs.evidence).toEqual([]);
});

it("takes a step's first sentence as its title", () => {
  expect(firstSentence("Retries back off. Then they stop.")).toBe("Retries back off");
  expect(firstSentence("No full stop")).toBe("No full stop");
  expect(firstSentence("A hook waits; the daemon answers. Later.")).toBe("A hook waits");
});

it("puts placed comments on their lines and the rest in the body", () => {
  const tour = buildTour(doc);
  const progress = {
    seen: [],
    notes: { beh_delay: "Is the cap enforced?", sec_docs: "", beh_guard: " Trusts a header " },
    note: "Thanks",
  };
  const review = reviewSubmission(doc, tour, progress, "REQUEST_CHANGES");
  expect(review).toMatchObject({
    repository: doc.review.repository,
    pr: doc.review.pull_request,
    head_sha: doc.review.head,
    event: "REQUEST_CHANGES",
    body: "Thanks",
  });
  expect(review.comments.map((comment) => comment.body)).toEqual([
    "Trusts a header",
    "Is the cap enforced?",
  ]);
  const folded = reviewSubmission(doc, tour, progress, "COMMENT", false);
  expect(folded.comments).toEqual([]);
  expect(folded.body).toContain("Is the cap enforced?");
  expect(folded.body).toContain("`retry.go:42`");
});

it("keeps progress per review version and survives junk in storage", () => {
  localStorage.setItem("baum.walkthrough", "not json");
  const key = progressKey(doc);
  expect(loadProgress(key)).toEqual({ seen: [], notes: {}, note: "" });
  saveProgress(key, { seen: ["beh_delay"], notes: { beh_delay: "x" }, note: "" });
  expect(loadProgress(key).seen).toEqual(["beh_delay"]);
  expect(loadProgress(`${key}x`).seen).toEqual([]);
});

let root: Root, node: HTMLDivElement, mode: WalkMode, posted: unknown[], answer: Response;
const changed = vi.fn(async () => {});
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  mode = "landing";
  posted = [];
  answer = Response.json({ url: "https://github.com/acme/demo/pull/7#pullrequestreview-1" });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_path: string, init?: RequestInit) => {
      posted.push(JSON.parse(String(init?.body)));
      return answer;
    }),
  );
});
afterEach(() => {
  act(() => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

const render = (authored = false) =>
  act(() =>
    root.render(
      <Walkthrough
        entry={entry(authored)}
        document={doc}
        mode={mode}
        setMode={(next) => {
          mode = next;
          render(authored);
        }}
        back={{ href: "/s/test-ns/", label: "Inbox" }}
        close={() => {}}
        patches={async () => null}
        changed={changed}
      />,
    ),
  );
const button = (name: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent?.trim().startsWith(name))!;
const exact = (name: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent?.trim() === name)!;
const click = (name: string) => act(() => button(name).click());
const type = (field: HTMLTextAreaElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(
      field,
      value,
    );
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
const key = (k: string) =>
  act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));

it("walks chapter by chapter and behaviour by behaviour, marking what was passed", () => {
  render();
  expect(node.textContent).toContain("The tour · riskiest first");
  click("Start the review");
  const tour = buildTour(doc);
  const [first, second] = tour.chapters;
  const title = () => node.querySelector("h2.walk-title")?.textContent;
  const picked = () => node.querySelector("li[aria-current]")?.id;
  const id = (at: number) => `walk-behaviour-${tour.stops[at]!.id}`;
  expect(title()).toBe(first!.title);
  expect(picked()).toBe(id(first!.stops[0]!));
  // j moves the pick down the chapter, and past its end to the next one.
  for (const at of first!.stops.slice(1)) {
    key("j");
    expect(picked()).toBe(id(at));
  }
  expect(loadProgress(progressKey(doc)).seen).toContain(tour.stops[first!.stops[0]!]!.id);
  key("j");
  expect(title()).toBe(second!.title);
  expect(picked()).toBe(id(second!.stops[0]!));
  // Leaving a chapter counts all of it as looked at.
  expect(loadProgress(progressKey(doc)).seen).toEqual(
    expect.arrayContaining(first!.stops.map((at) => tour.stops[at]!.id)),
  );
  // k goes back to the previous chapter's last behaviour; arrows move pages.
  key("k");
  expect(picked()).toBe(id(first!.stops[first!.stops.length - 1]!));
  key("ArrowRight");
  expect(title()).toBe(second!.title);
  key("ArrowLeft");
  key("ArrowLeft");
  expect(node.textContent).toContain("The tour · riskiest first");
});

// Comments on the first behaviour, then goes to the wrap-up.
const commentAndWrapUp = (text: string) => {
  click("Start the review");
  key("c");
  type(node.querySelector("textarea")!, text);
  click("Save comment");
  click("Send review");
};

it("sends one review with each comment on its line", async () => {
  render();
  click("Start the review");
  key("c");
  type(node.querySelector("textarea")!, "Trusts a client header");
  click("Save comment");
  expect(node.textContent).toContain("Your comment on");
  click("Send review");
  expect(button("Approve").getAttribute("aria-disabled")).toBe("true");
  expect(button("Request changes").getAttribute("aria-disabled")).toBe("false");
  click("Request changes");
  await act(async () => exact("Request changes").click());
  expect(posted).toEqual([
    expect.objectContaining({
      event: "REQUEST_CHANGES",
      head_sha: doc.review.head,
      comments: [expect.objectContaining({ body: "Trusts a client header", side: "RIGHT" })],
    }),
  ]);
  expect(node.textContent).toContain("Your review is on GitHub.");
  expect(Object.values(loadProgress(progressKey(doc)).notes)).toEqual([]);
  expect(changed).toHaveBeenCalled();
});

it("offers neither a verdict on the reader's own pull request", () => {
  render(true);
  click("Start the review");
  click("Send review");
  expect(button("Approve").getAttribute("aria-disabled")).toBe("true");
  expect(node.textContent).toContain("GitHub doesn’t let you approve your own pull request");
});

it("offers to fold comments into the body when GitHub can't place them", async () => {
  render();
  commentAndWrapUp("Odd");
  answer = Response.json(
    { code: "invalid_line", message: "Some comments are on lines outside the diff." },
    { status: 422 },
  );
  await act(async () => exact("Send comments").click());
  expect(node.textContent).toContain("Some comments are on lines outside the diff.");
  answer = Response.json({ url: "https://github.com/x" });
  await act(async () => button("Put the comments in the review body instead").click());
  expect(posted[1]).toMatchObject({ event: "COMMENT", comments: [] });
  expect((posted[1] as { body: string }).body).toContain("Odd");
});

it("keeps what was written while the review was being sent", async () => {
  render();
  commentAndWrapUp("Odd");
  let respond!: (answer: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise<Response>((resolve) => (respond = resolve))),
  );
  act(() => exact("Send comments").click());
  type(node.querySelector("textarea")!, "Thanks");
  await act(async () => respond(Response.json({ url: "https://github.com/x" })));
  expect(node.textContent).toContain("Your review is on GitHub.");
  const kept = loadProgress(progressKey(doc));
  expect(Object.values(kept.notes)).toEqual([]);
  expect(kept.note).toBe("Thanks");
});
