import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Job } from "./api";
import { groupEntries, JobTranscript, mergeEntries, type TranscriptEntry } from "./job-transcript";

const job = (overrides: Partial<Job> = {}): Job => ({
  id: "fix-1",
  clone: "clone",
  repository: "acme/demo",
  pr: 2,
  state: "running",
  phase: "fixing",
  calls: 0,
  tokens: 0,
  repairs: 0,
  actual_cost_usd: null,
  kind: "fix",
  has_transcript: true,
  ...overrides,
});
const entry = (seq: number, rest: Partial<TranscriptEntry>): TranscriptEntry => ({
  seq,
  time: "2026-10-27T10:00:00Z",
  kind: "message",
  ...rest,
});

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  window.scrollTo = () => {};
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));

it("shows a tool's update in the place of its first entry", () => {
  const merged = mergeEntries(
    [entry(1, { kind: "tool", id: "t", tool: "Bash", status: "running" }), entry(2, { text: "x" })],
    [entry(3, { kind: "tool", id: "t", status: "completed", text: "ok" })],
  );
  expect(merged.map((e) => [e.seq, e.status, e.text])).toEqual([
    [1, "completed", "ok"],
    [2, undefined, "x"],
  ]);
  expect(merged[0]!.tool).toBe("Bash");
});

it("gathers consecutive tool calls into one run, broken only by what the agent says", () => {
  const items = groupEntries([
    entry(1, { text: "Looking." }),
    entry(2, { kind: "tool", id: "a", tool: "Read" }),
    entry(3, { kind: "reasoning", text: "hm" }),
    entry(4, { kind: "tool", id: "b", tool: "Edit" }),
    entry(5, { kind: "tool", id: "c", tool: "Grep" }),
    entry(6, { text: "Done." }),
    entry(7, { kind: "tool", id: "d", tool: "Bash" }),
  ]);
  expect(
    items.map((item) =>
      item.kind === "tools" ? item.tools.map((t) => t.id) : `${item.entry.kind}:${item.entry.seq}`,
    ),
  ).toEqual(["message:1", "reasoning:3", ["a", "b", "c"], "message:6", ["d"]]);
});

it("shows a run's latest tool call and folds the earlier ones", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () =>
      Response.json({
        entries: [
          entry(1, { text: "Some **bold** words." }),
          entry(2, {
            kind: "tool",
            id: "a",
            tool: "Read",
            title: "Read a.go",
            status: "completed",
          }),
          entry(3, { kind: "tool", id: "b", tool: "Edit", status: "failed" }),
          entry(4, { kind: "tool", id: "c", tool: "Grep", status: "running" }),
        ],
        next: 4,
        more: false,
        done: true,
      }),
    ),
  );
  await act(async () => root.render(<JobTranscript job={job()} />));
  await wait(20);
  expect(node.querySelector(".local-job-message strong")!.textContent).toBe("bold");
  const run = node.querySelector(".local-job-tools")!;
  const earlier = run.querySelector<HTMLDetailsElement>(".local-job-tools-earlier")!;
  expect(earlier.open).toBe(false);
  expect(earlier.querySelector("summary")!.textContent).toBe("2 earlier tool calls, 1 failed");
  expect([...earlier.querySelectorAll(".local-job-tool strong")].map((s) => s.textContent)).toEqual(
    ["Read", "Edit"],
  );
  expect(run.querySelector(":scope > .local-job-tool strong")!.textContent).toBe("Grep");
  // Claude's titles repeat the tool's name; it is shown once.
  expect(earlier.querySelector(".local-job-tool-title")!.textContent).toBe("a.go");
});

it("reads on at once while there is more, then stops when done", async () => {
  const pages: Record<string, unknown> = {
    "0": {
      entries: [
        entry(1, { text: "Looking at the failing test.\nIt is flaky." }),
        entry(2, { kind: "reasoning", text: "maybe a race" }),
        entry(3, { kind: "tool", id: "t", tool: "Bash", title: "go test", status: "running" }),
      ],
      next: 3,
      more: true,
      done: false,
    },
    "3": {
      entries: [
        entry(4, { kind: "tool", id: "t", status: "completed", text: "PASS" }),
        entry(5, { kind: "error", text: "push refused" }),
      ],
      next: 5,
      more: false,
      done: true,
    },
  };
  const fetched: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      fetched.push(path);
      return Response.json(pages[new URL(path, "http://x").searchParams.get("after")!]);
    }),
  );
  await act(async () => root.render(<JobTranscript job={job()} />));
  await wait(50);
  expect(fetched).toEqual([
    "/s/test-ns/api/jobs/fix-1/transcript?after=0",
    "/s/test-ns/api/jobs/fix-1/transcript?after=3",
  ]);
  expect(node.querySelector(".local-job-message")!.textContent).toBe(
    "Looking at the failing test.\nIt is flaky.",
  );
  expect(node.querySelectorAll(".local-job-tool")).toHaveLength(1);
  expect(node.querySelector(".local-job-tool pre")!.textContent).toBe("PASS");
  expect(node.querySelector(".local-job-tool .local-tool-done")).not.toBeNull();
  expect(node.querySelector(".local-job-reasoning")).not.toBeNull();
  expect(node.textContent).toContain("push refused");
  expect(node.textContent).toContain("Fix CI: The agent is fixing it");
  // Done: nothing more is asked for.
  await wait(1200);
  expect(fetched).toHaveLength(2);
});

it("says so when the job left no transcript", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response("not found", { status: 404 })),
  );
  await act(async () =>
    root.render(<JobTranscript job={job({ state: "failed", reason: "signing" })} />),
  );
  await wait(20);
  expect(node.textContent).toContain("No transcript to show");
  expect(node.textContent).toContain("Git couldn't sign a commit");
});

it("stops reading when it is closed", async () => {
  const fetched = vi.fn(async () =>
    Response.json({ entries: [], next: 0, more: false, done: false }),
  );
  vi.stubGlobal("fetch", fetched);
  await act(async () => root.render(<JobTranscript job={job()} />));
  await wait(20);
  await act(async () => root.render(<div />));
  const calls = fetched.mock.calls.length;
  await wait(1200);
  expect(fetched.mock.calls.length).toBe(calls);
});
