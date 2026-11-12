import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReviewDocument } from "@baum/viewer/review-document";
import example from "../../schema/review-example.json" with { type: "json" };
import { type Check, PullRequestChecks, liveDetailsChecks } from "./checks";

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

const failing = (over: Partial<Check> = {}): Check => ({
  name: "CI / Platform",
  status: "completed",
  conclusion: "failure",
  id: 21,
  job_id: 21,
  has_log: true,
  url: "https://github.com/baum-ai/example/actions/runs/1/job/21",
  ...over,
});

const render = (checks: Check[]) =>
  act(async () =>
    root.render(
      <PullRequestChecks
        repository="baum-ai/example"
        number={7}
        head="abc"
        url="https://github.com/baum-ai/example/pull/7"
        canRetry={false}
        checks={{ checks }}
      />,
    ),
  );

const button = (name: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent === name) as
    | HTMLButtonElement
    | undefined;

it("shows a failing check's log tail only when asked, collapsed by default", async () => {
  const fetchMock = vi.fn(async () =>
    Response.json({
      id: 21,
      name: "CI / Platform",
      available: true,
      log: "ok\nFAIL: TestX",
      truncated: true,
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  try {
    await render([failing()]);
    const show = button("Show log")!;
    expect(show.getAttribute("aria-expanded")).toBe("false");
    expect(show.hasAttribute("aria-controls")).toBe(false);
    expect(node.querySelector("pre")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();

    await act(async () => show.click());
    const calls = fetchMock.mock.calls as unknown as [string][];
    expect(calls).toHaveLength(1);
    expect(String(calls[0]![0])).toContain(
      "api/prs/check-log?repository=baum-ai%2Fexample&pr=7&check=21",
    );
    const log = node.querySelector("pre")!;
    expect(log.textContent).toBe("ok\nFAIL: TestX");
    expect(log.getAttribute("aria-label")).toBe("Log of CI / Platform");
    expect(log.tabIndex).toBe(0);
    expect(node.textContent).toContain("The end of the log");
    expect(button("Hide log")!.getAttribute("aria-expanded")).toBe("true");
    expect(button("Copy")).toBeDefined();
    // The copy status is for screen readers only (the PR page loads no
    // desktop stylesheet that hides `visually-hidden`).
    expect(node.querySelector("[role=status].sr-only")).not.toBeNull();
    expect(node.querySelector(".visually-hidden")).toBeNull();
    expect(button("Hide log")!.getAttribute("aria-controls")).toBe(log.closest("div[id]")!.id);

    await act(async () => button("Hide log")!.click());
    expect(node.querySelector("pre")).toBeNull();
  } finally {
    vi.unstubAllGlobals();
  }
});

it("says why a check has no log, and what went wrong reading one", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        id: 21,
        name: "x",
        available: false,
        reason: "expired",
        message: "GitHub no longer has this log.",
      }),
    ),
  );
  try {
    await render([failing()]);
    await act(async () => button("Show log")!.click());
    expect(node.textContent).toContain("GitHub no longer has this log.");
    expect(node.querySelector("pre")).toBeNull();
  } finally {
    vi.unstubAllGlobals();
  }
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ code: "failed", message: "baum could not reach GitHub." }), {
          status: 502,
        }),
    ),
  );
  try {
    await render([failing({ name: "other", id: 22 })]);
    await act(async () => button("Show log")!.click());
    const alert = node.querySelector("[role=alert]")!;
    expect(alert.textContent).toContain("baum could not reach GitHub.");
    expect(alert.querySelector("a")!.getAttribute("href")).toContain("/job/21");
  } finally {
    vi.unstubAllGlobals();
  }
});

it("offers a log only for a failing check that has one", async () => {
  await render([
    failing({ has_log: undefined, name: "ci/circle", id: undefined }),
    failing({ name: "passing", conclusion: "success" }),
  ]);
  expect(button("Show log")).toBeUndefined();
});
