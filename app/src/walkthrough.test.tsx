import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import type { Entry } from "./api";
import { Walkthrough, type WalkView } from "./walkthrough";
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
  expect(delay.before).toBeTruthy();
  expect(delay.question).toBeTruthy();
  expect(delay.place).toEqual({ path: "queue/retry.go", line: 42, side: "RIGHT", start_line: 31 });
  expect(delay.where).toBe("retry.go:42");
  expect(delay.tests).toContain("retry_test.go");
  const budget = tour.stops.find((stop) => stop.id === "beh_budget")!;
  expect(budget.after).toBe(budget.text);
  expect(budget.question).toBeNull();
  // A section without behaviour steps is one stop of its summary.
  const docs = tour.stops.find((stop) => stop.id === "sec_docs")!;
  expect(docs.title).toBe("Docs and dependency bump");
  expect(docs.after).toBe(doc.sections.find((s) => s.id === "sec_docs")!.summary.text);
});

it("takes a step's first sentence as its title", () => {
  expect(firstSentence("Retries back off. Then they stop.")).toBe("Retries back off");
  expect(firstSentence("No full stop")).toBe("No full stop");
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

let root: Root, node: HTMLDivElement, view: WalkView, posted: unknown[], answer: Response;
const changed = vi.fn(async () => {});
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  view = "tour";
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
        view={view}
        setView={(next) => {
          view = next;
          render(authored);
        }}
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

it("walks the stops, marks what was passed as looked at and skips it next time", () => {
  render();
  expect(node.textContent).toContain("The route · riskiest first");
  click("Start the tour");
  expect(node.querySelector("h2.text-2xl")?.textContent).toBe(buildTour(doc).stops[0]!.title);
  key("ArrowRight");
  expect(node.textContent).toContain("1 of");
  key("ArrowLeft");
  // Next from the first stop jumps over the one already looked at.
  key("ArrowRight");
  expect(node.textContent).toContain("Skipped 1 stop you already looked at");
});

it("sends one review with each comment on its line", async () => {
  render();
  click("Start the tour");
  key("c");
  type(node.querySelector("textarea")!, "Trusts a client header");
  click("Save comment");
  expect(node.textContent).toContain("Your comment on");
  key("e");
  expect(view).toBe("all");
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
  view = "all";
  render(true);
  expect(button("Approve").getAttribute("aria-disabled")).toBe("true");
  expect(node.textContent).toContain("GitHub doesn’t let you approve your own pull request");
});

it("offers to fold comments into the body when GitHub can't place them", async () => {
  view = "all";
  render();
  act(() => exact("Comment").click());
  type(node.querySelector("textarea[id^='walk-note-']")!, "Odd");
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

it("returns to the tour after sending a review", async () => {
  view = "all";
  render();
  act(() => exact("Comment").click());
  type(node.querySelector("textarea[id^='walk-note-']")!, "Odd");
  await act(async () => exact("Send comments").click());
  expect(node.textContent).toContain("Your review is on GitHub.");
  act(() => exact("Guided tour").click());
  expect(view).toBe("tour");
});
