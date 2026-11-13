import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./app";
import { jobsEnded } from "./reload-events";
import type { Job } from "./api";

let root: Root;
let node: HTMLDivElement;
let hidden = false;
let paths: string[] = [];
let listeners = new Map<string, ((event: Event) => void)[]>();
let jobList: unknown[] = [];

const emit = (kind: string, data = "{}") => {
  for (const listener of listeners.get(kind) ?? [])
    listener(new MessageEvent(kind, { data }) as Event);
};
const visibility = (value: boolean) => {
  hidden = value;
  document.dispatchEvent(new Event("visibilitychange"));
};
/** The endpoints read since the last call, without their query. */
const read = () => {
  const seen = paths.map((p) => p.replace("/s/test-ns/", "").split("?")[0]!);
  paths = [];
  return [...new Set(seen)].sort();
};
/** The same, with every request counted. */
const counts = () => {
  const seen = new Map<string, number>();
  for (const p of paths) {
    const key = p.replace("/s/test-ns/", "").split("?")[0]!;
    seen.set(key, (seen.get(key) ?? 0) + 1);
  }
  paths = [];
  return seen;
};
const settle = () => act(async () => {});

beforeEach(async () => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=jobs");
  localStorage.clear();
  hidden = false;
  jobList = [];
  listeners = new Map();
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener(kind: string, listener: (event: Event) => void) {
        listeners.set(kind, [...(listeners.get(kind) ?? []), listener]);
      }
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (path: string) => {
      paths.push(path);
      const p = path.split("?")[0]!;
      return Response.json(
        p.endsWith("/api/index")
          ? { entries: [], problems: [] }
          : p.endsWith("/api/repos") || p.endsWith("/api/repositories") || p.endsWith("/api/prs")
            ? []
            : p.endsWith("/api/jobs")
              ? { jobs: jobList }
              : p.endsWith("/api/github-auth")
                ? { state: "disconnected", host: "github.com", saved: false, override: false }
                : {},
      );
    }),
  );
  await act(async () => root.render(<App />));
  await settle();
  paths = [];
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
  delete (document as { hidden?: boolean }).hidden;
});

it("reads everything once at startup, and again only on a reconnect", async () => {
  // The mount read was made in beforeEach; the first connection adds none.
  await act(async () => emit("open"));
  await settle();
  expect(read()).toEqual([]);
  await act(async () => emit("open"));
  await settle();
  expect(read()).toEqual(
    expect.arrayContaining([
      "api/automatic",
      "api/background",
      "api/clones/found",
      "api/groups",
      "api/index",
      "api/inbox",
      "api/jobs",
      "api/live-sessions",
      "api/prs",
      "api/repos",
      "api/repositories",
      "api/settings",
    ]),
  );
});

it("reads only the jobs and groups for a progress event", async () => {
  await act(async () => emit("progress", '{"job":{"id":"j1"}}'));
  await settle();
  expect(read()).toEqual(["api/groups", "api/jobs"]);
});

it("reads the index, jobs, discovery, clones and groups, not the settings, for an index event", async () => {
  await act(async () => emit("index"));
  await settle();
  const got = read();
  expect(got).toEqual(
    expect.arrayContaining([
      "api/background",
      "api/groups",
      "api/index",
      "api/inbox",
      "api/prs",
      "api/repos",
    ]),
  );
  expect(got).toContain("api/jobs");
  expect(got).not.toContain("api/settings");
});

it("reads only the automatic state, settings and discovery for an automatic event", async () => {
  await act(async () => emit("automatic"));
  await settle();
  const got = read();
  expect(got).toContain("api/automatic");
  expect(got).toContain("api/settings");
  expect(got).not.toContain("api/index");
  expect(got).not.toContain("api/jobs");
  expect(got).not.toContain("api/groups");
});

it("reads the index again when a progress read shows a job that ended", async () => {
  await act(async () => emit("progress"));
  await settle();
  read();
  jobList = [{ id: "j1", state: "completed", kind: "review", repository: "acme/demo", pr: 1 }];
  await act(async () => emit("progress"));
  await settle();
  const got = read();
  expect(got).toContain("api/jobs");
  expect(got).toContain("api/index");
  expect(got).toContain("api/prs");
});

it("reads nothing while hidden, then once for everything that arrived", async () => {
  visibility(true);
  await act(async () => {
    for (let i = 0; i < 5; i++) {
      emit("progress");
      emit("index");
      emit("live");
      emit("automatic");
      emit("job", '{"job":{"id":"j1"}}');
      emit("decisions");
      emit("timeline");
    }
  });
  await settle();
  expect(read()).toEqual([]);
  visibility(false);
  await settle();
  // One read of the union, one read of the live sessions.
  const got = counts();
  for (const path of ["api/index", "api/automatic", "api/live-sessions"])
    expect(got.get(path)).toBe(1);
  // Twice: the current jobs, and the Jobs page's own history page.
  expect(got.get("api/jobs")).toBe(2);
  await settle();
  expect(read()).toEqual([]);
});

it("reads the live sessions only if a live event arrived while hidden", async () => {
  visibility(true);
  await act(async () => emit("index"));
  visibility(false);
  await settle();
  const got = read();
  expect(got).toContain("api/index");
  expect(got).not.toContain("api/live-sessions");
});

it("reads one more time on a reconnect that happens while hidden, once shown", async () => {
  await act(async () => emit("open"));
  visibility(true);
  await act(async () => emit("open"));
  await settle();
  expect(read()).toEqual([]);
  visibility(false);
  await settle();
  const got = read();
  expect(got).toContain("api/index");
  expect(got).toContain("api/jobs");
  expect(got).toContain("api/live-sessions");
});

it("finds a job that ended", () => {
  const job = (id: string, state: string) => ({ id, state }) as unknown as Job;
  expect(jobsEnded([job("a", "running")], [job("a", "completed")])).toBe(true);
  expect(jobsEnded([job("a", "completed")], [job("a", "completed")])).toBe(false);
  expect(jobsEnded([job("a", "running")], [job("a", "running")])).toBe(false);
  expect(jobsEnded([], [job("a", "failed")])).toBe(true);
});

it("reads a part whose read failed again with the next read", async () => {
  const ok = vi.mocked(fetch).getMockImplementation()!;
  let failIndex = true;
  vi.stubGlobal(
    "fetch",
    vi.fn((path: string, init?: RequestInit) =>
      failIndex && path.includes("api/index")
        ? Promise.resolve(new Response("boom", { status: 500 }))
        : ok(path, init),
    ),
  );
  await act(async () => emit("index"));
  await settle();
  failIndex = false;
  paths = [];
  await act(async () => emit("progress"));
  await settle();
  expect(read()).toContain("api/index");
});
