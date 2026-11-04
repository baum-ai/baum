import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import { type Check, liveDetailsChecks } from "./checks";

const document_ = () => structuredClone(example) as unknown as ReviewDocument;
const pr = (head: string) => ({
  repository: "baum-ai/example",
  number: 7,
  head_sha: head,
  url: "https://github.com/baum-ai/example/pull/7",
  canRetry: false,
});
const check = (name: string, conclusion: string): Check => ({
  name,
  status: "completed",
  conclusion,
});

let node: HTMLDivElement;
let root: Root;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
});

it("shows the head's checks as GitHub lists them now, over the review's snapshot", async () => {
  const doc = document_();
  const live = liveDetailsChecks(doc, pr(doc.review.head), {
    checks: [check("build", "success"), check("test", "success")],
  });
  expect(live.failing).toBe(false);
  await act(async () => root.render(<>{live.content}</>));
  expect(node.querySelector("[role=status]")!.textContent).toBe("2/2 passing");
  expect(node.textContent).toContain("build");
});

it("flags the tab when a live check fails", () => {
  const doc = document_();
  const live = liveDetailsChecks(doc, pr(doc.review.head), {
    checks: [check("build", "success"), check("test", "failure")],
  });
  expect(live.failing).toBe(true);
});

it("keeps the snapshot for an earlier head, and until the live read is back", () => {
  const doc = document_();
  const earlier = liveDetailsChecks(doc, pr("0".repeat(40)), {
    checks: [check("build", "success")],
  });
  expect(earlier.content).toBeUndefined();
  const loading = liveDetailsChecks(doc, pr(doc.review.head), {});
  expect(loading.content).toBeUndefined();
  const failed = liveDetailsChecks(doc, pr(doc.review.head), { error: "rate limited" });
  expect(failed.content).toBeUndefined();
});
