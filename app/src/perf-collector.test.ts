import { describe, expect, it } from "vitest";
import {
  createCollector,
  keyLabel,
  LIMIT,
  requestPath,
  targetLabel,
  type CollectorEnv,
} from "./perf-collector";

function setup() {
  const clock = { now: 0, page: "inbox" };
  const env: CollectorEnv = {
    now: () => clock.now,
    page: () => clock.page,
    userAgent: "test",
    dom: () => 42,
    heap: () => null,
  };
  return { clock, collector: createCollector(env) };
}

describe("navigations", () => {
  it("are shown once React committed and no loader is left", () => {
    const { clock, collector } = setup();
    collector.navigation("?page=pulls");
    // A frame before React committed the new page says nothing about it.
    clock.now = 5;
    collector.frame(5);
    const loaded = collector.loading();
    clock.now = 8;
    collector.render("main", "update", 3, 10);
    collector.frame(16);
    expect(collector.snapshot().navigations[0]!.content).toBeUndefined();
    loaded();
    collector.frame(40);
    expect(collector.snapshot().navigations[0]).toMatchObject({ content: 40, settled: 40 });
  });

  it("settle only once no request is in flight", () => {
    const { clock, collector } = setup();
    collector.navigation("?page=jobs");
    const answered = collector.request("GET", "api/jobs");
    clock.now = 2;
    collector.render("main", "update", 1, 1);
    collector.frame(16);
    expect(collector.snapshot().navigations[0]).toMatchObject({ content: 16 });
    expect(collector.snapshot().navigations[0]!.settled).toBeUndefined();
    clock.now = 20;
    answered(200, 512);
    collector.frame(33);
    expect(collector.snapshot().navigations[0]).toMatchObject({ content: 16, settled: 33 });
    expect(collector.snapshot().requests).toEqual([
      { method: "GET", path: "api/jobs", status: 200, start: 0, duration: 20, bytes: 512 },
    ]);
  });

  it("left before they settled stop there, and the next page does not complete them", () => {
    const { clock, collector } = setup();
    collector.navigation("?page=jobs");
    const jobsLoading = collector.loading();
    clock.now = 5;
    collector.render("main", "update", 1, 1);
    clock.now = 10;
    // Leaving Jobs while it still loads unmounts its loader.
    collector.navigation("?page=sessions");
    jobsLoading();
    clock.now = 12;
    collector.render("main", "update", 1, 1);
    collector.frame(20);
    const [jobs, sessions] = collector.snapshot().navigations;
    expect(jobs).toEqual({ search: "?page=jobs", start: 0, superseded: true });
    expect(sessions).toMatchObject({
      search: "?page=sessions",
      start: 10,
      content: 10,
      settled: 10,
    });
  });

  it("continue through a redirect", () => {
    const { clock, collector } = setup();
    collector.navigation("");
    clock.now = 3;
    collector.navigation("?inbox=1", true);
    collector.render("app", "mount", 1, 1);
    collector.frame(16);
    expect(collector.snapshot().navigations).toEqual([
      { search: "?inbox=1", start: 0, content: 16, settled: 16 },
    ]);
    // Once settled, a replaced entry is a navigation of its own.
    clock.now = 20;
    collector.navigation("?inbox=2", true);
    expect(collector.snapshot().navigations).toHaveLength(2);
  });

  it("count a loader released twice once", () => {
    const { collector } = setup();
    const first = collector.loading();
    collector.loading()();
    first();
    first();
    collector.navigation("");
    collector.render("app", "mount", 1, 1);
    collector.frame(1);
    expect(collector.snapshot().navigations[0]!.content).toBe(1);
  });
});

describe("frames", () => {
  it("bucket the gaps between frames and keep the long ones", () => {
    const { collector } = setup();
    for (const at of [0, 16, 32, 100, 116, 300]) collector.frame(at);
    const { frames } = collector.snapshot();
    expect(frames.count).toBe(5);
    expect(frames.buckets).toEqual({ "≤17": 3, "≤33": 0, "≤50": 0, "≤100": 1, ">100": 1 });
    expect(frames.long).toEqual([
      { start: 32, duration: 68 },
      { start: 116, duration: 184 },
    ]);
  });
});

describe("renders", () => {
  it("total each region and log each commit with its page", () => {
    const { clock, collector } = setup();
    collector.render("sidebar", "mount", 4, 4);
    clock.page = "pulls";
    collector.render("sidebar", "update", 2, 5);
    const { renders, commits } = collector.snapshot();
    expect(renders.sidebar).toEqual({ commits: 2, mounts: 1, actual: 6, max: 4, base: 5 });
    expect(commits.map((c) => c.page)).toEqual(["inbox", "pulls"]);
  });
});

describe("snapshot and reset", () => {
  it("bounds every list, keeping the newest", () => {
    const { collector } = setup();
    for (let i = 0; i < LIMIT + 3; i++) collector.mark(String(i));
    const { marks, dropped } = collector.snapshot();
    expect(marks).toHaveLength(LIMIT);
    expect(marks[0]!.name).toBe("3");
    expect(dropped).toEqual({ marks: 3 });
  });

  it("is a copy, and reset forgets what was recorded", () => {
    const { clock, collector } = setup();
    collector.mark("a");
    const before = collector.snapshot();
    before.marks.push({ name: "b", at: 0 });
    expect(collector.snapshot().marks).toHaveLength(1);
    clock.now = 50;
    collector.reset();
    const after = collector.snapshot();
    expect(after).toMatchObject({ since: 50, marks: [], renders: {}, dom: 42 });
  });
});

describe("labels", () => {
  it("strip the origin and secret namespace from request paths", () => {
    const base = "http://127.0.0.1:4167/s/secret-ns/";
    expect(requestPath("api/prs?state=open", base)).toBe("api/prs?state=open");
    expect(requestPath("http://127.0.0.1:4167/s/secret-ns/api/jobs", base)).toBe("api/jobs");
    expect(requestPath("assets/app.js", base)).toBe("/assets/app.js");
  });

  it("name keys with their modifiers", () => {
    expect(keyLabel({ key: "j", metaKey: false, ctrlKey: false, altKey: false })).toBe("j");
    expect(keyLabel({ key: "k", metaKey: true, ctrlKey: true, altKey: false })).toBe("Ctrl+Meta+k");
  });

  it("name a click by the control around it", () => {
    document.body.innerHTML = `<button aria-label="Open inbox"><svg><path id="p"/></svg></button><p id="text">  Some   words </p>`;
    expect(targetLabel(document.getElementById("p"))).toBe('button "Open inbox"');
    expect(targetLabel(document.getElementById("text"))).toBe('p "Some words"');
    expect(targetLabel(null)).toBe("?");
  });
});
