import { afterEach, describe, expect, it, vi } from "vitest";
import {
  checkWaitingJobs,
  checkWindowHere,
  inPageChecksHere,
  onTileCheckEvent,
  setInPageRunner,
} from "./check-host";
import {
  AT_ONCE,
  heartbeat,
  matrix,
  measured,
  runJob,
  type CheckJob,
  type FrameSpec,
} from "./check-runner";
import type { ReadyMetrics } from "./protocol";

const job = (fields: Partial<CheckJob> = {}): CheckJob => ({
  id: "chk_1",
  kind: "check",
  digest: `sha256:${"a".repeat(64)}`,
  kit: 1,
  title: "Findings",
  sizes: ["half", "third"],
  samples: [
    { name: "data", kind: "data", data: {} },
    { name: "example", kind: "sample", data: {}, refs: { glance: {}, detail: {} } },
    { name: "minimal", kind: "minimal", data: {}, refs: { glance: {}, detail: {} } },
  ],
  ...fields,
});

const metrics = (fields: Partial<ReadyMetrics> = {}): ReadyMetrics => ({
  height: 100,
  text_chars: 10,
  elements: 2,
  overflow_x: false,
  overflow_y: false,
  media: 0,
  empty_state: false,
  failed: false,
  unresolved: 0,
  render_ms: 10,
  ...fields,
});

describe("the matrix", () => {
  it("draws each sample at each size and width, and the data dark too", () => {
    const specs = matrix(job());
    const of = (name: string) =>
      specs
        .filter((s) => s.sample.name === name)
        .map((s) => `${s.face}:${s.size}:${s.width}:${s.theme}`);
    expect(of("example")).toEqual([
      "glance:half:488:light",
      "glance:third:310:light",
      "detail:full:720:light",
      "detail:full:360:light",
    ]);
    expect(of("data")).toContain("glance:half:488:dark");
    expect(of("data")).toContain("detail:full:720:dark");
    expect(of("minimal").some((s) => s.endsWith("dark"))).toBe(false);
  });

  it("draws a preflight once as a light glance and a detail", () => {
    const specs = matrix(
      job({
        kind: "preflight",
        sizes: ["third"],
        samples: [{ name: "data", kind: "data", data: {}, refs: { glance: {}, detail: {} } }],
      }),
    );
    expect(specs.map((s) => `${s.face}:${s.size}:${s.theme}`)).toEqual([
      "glance:third:light",
      "detail:full:light",
    ]);
  });
});

describe("what a ready frame shows", () => {
  const glance = { face: "glance", width: 310, height: 164 } as const;
  it("finds nothing wrong with a tile that drew", () => {
    expect(measured(glance, metrics())).toEqual([]);
  });
  it("names each problem", () => {
    const codes = (m: Partial<ReadyMetrics>) => measured(glance, metrics(m)).map((p) => p.code);
    expect(codes({ text_chars: 0 })).toEqual(["empty"]);
    expect(codes({ text_chars: 0, media: 1 })).toEqual([]);
    expect(codes({ text_chars: 0, empty_state: true })).toEqual([]);
    expect(codes({ failed: true })).toEqual(["render_failed"]);
    expect(codes({ overflow_y: true })).toEqual(["overflow_glance"]);
    expect(codes({ overflow_x: true })).toEqual(["overflow_x"]);
    expect(codes({ unresolved: 2 })).toEqual(["unresolved_citation"]);
    expect(codes({ render_ms: 900 })).toEqual(["slow"]);
    // A detail's height is its content's, never an overflow.
    expect(
      measured({ face: "detail", width: 720, height: 120 }, metrics({ overflow_y: true })),
    ).toEqual([]);
  });
});

describe("running a job", () => {
  it("draws at most AT_ONCE frames at a time and labels each problem", async () => {
    let open = 0;
    let most = 0;
    const draw = async (_stage: HTMLElement, spec: FrameSpec) => {
      open++;
      most = Math.max(most, open);
      await new Promise((resolve) => setTimeout(resolve, 5));
      open--;
      return {
        problems: spec.face === "glance" ? [{ code: "unresolved_citation", message: "x" }] : [],
        run: {
          sample: spec.sample.name,
          face: spec.face,
          size: spec.size,
          theme: spec.theme,
          width: spec.width,
          height: spec.height,
          ready_ms: 1,
          content_height: 1,
          text_chars: 1,
        },
      };
    };
    // The data's version cannot be read: its citations are not counted.
    let frames = 0;
    const result = await runJob(job({ context: null }), document.body, draw, () => frames++);
    expect(most).toBe(AT_ONCE);
    // Each frame drawn is a sign of life for the daemon.
    expect(frames).toBe(matrix(job()).length);
    expect(result.runs).toHaveLength(matrix(job()).length);
    expect(new Set(result.problems.map((p) => p.sample))).toEqual(new Set(["example", "minimal"]));
    expect(result.problems[0]).toMatchObject({ code: "unresolved_citation", face: "glance" });
  });
});

describe("where checks draw in the Electron shell", () => {
  const bridge = "baumDesktop";
  type Bridge = Record<string, unknown>;
  afterEach(() => {
    vi.useRealTimers();
    setInPageRunner(null);
  });
  const shell = (declared: unknown, invoke: unknown = vi.fn(() => Promise.resolve())) =>
    ({ [bridge]: { invoke }, __BAUM_DESKTOP__: declared }) as unknown as Window;

  it("uses the check window where the shell declares one", () => {
    expect(checkWindowHere(shell({ tileFrames: true, checkWindow: true }))).toBe(true);
    expect(checkWindowHere(shell({ tileFrames: true, checkWindow: false }))).toBe(false);
    expect(checkWindowHere(shell({ tileFrames: false, checkWindow: true }))).toBe(false);
    expect(inPageChecksHere(shell({ tileFrames: true }))).toBe(true);
    expect(inPageChecksHere(shell({}))).toBe(false);
    expect(inPageChecksHere({} as Window)).toBe(true);
  });

  it("draws in the page on a queued check in a browser", () => {
    const run = vi.fn();
    setInPageRunner(run);
    onTileCheckEvent('{"id":"chk_1","state":"queued"}', {} as Window);
    onTileCheckEvent('{"id":"chk_1","state":"running"}', {} as Window);
    onTileCheckEvent("not json", {} as Window);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("opens the check window, and closes it on a hang and when idle", async () => {
    const flush = async () => {
      for (let i = 0; i < 10; i++) await Promise.resolve();
    };
    vi.useFakeTimers();
    const invoke = vi.fn((_command: string, _args?: unknown) => Promise.resolve());
    const win = shell({ tileFrames: true, checkWindow: true }, invoke);
    const previous = (window as unknown as Bridge)[bridge];
    (window as unknown as Bridge)[bridge] = { invoke };
    try {
      const run = vi.fn();
      setInPageRunner(run);
      onTileCheckEvent({ id: "chk_1", state: "queued" }, win);
      await flush();
      expect(invoke).toHaveBeenLastCalledWith("open_check_window", undefined);
      // A hang, then a check that was waiting announced again: the shell
      // gets the close, then the open, in that order.
      onTileCheckEvent({ id: "chk_1", state: "hung" }, win);
      onTileCheckEvent({ id: "chk_2", state: "queued" }, win);
      await flush();
      expect(invoke.mock.calls.map((call) => call[0])).toEqual([
        "open_check_window",
        "close_check_window",
        "open_check_window",
      ]);
      vi.advanceTimersByTime(2 * 60 * 1000);
      await flush();
      expect(invoke).toHaveBeenLastCalledWith("close_check_window", undefined);
      expect(run).not.toHaveBeenCalled();
    } finally {
      (window as unknown as Bridge)[bridge] = previous;
    }
  });

  it("asks once on load whether checks wait, and opens the window only if so", async () => {
    const flush = async () => {
      for (let i = 0; i < 10; i++) await Promise.resolve();
    };
    vi.useFakeTimers();
    const invoke = vi.fn((_command: string, _args?: unknown) => Promise.resolve());
    const win = shell({ tileFrames: true, checkWindow: true }, invoke);
    const previous = (window as unknown as Bridge)[bridge];
    (window as unknown as Bridge)[bridge] = { invoke };
    const answer = (body: unknown) =>
      vi.fn((_url: string) => Promise.resolve({ ok: true, json: () => Promise.resolve(body) }));
    try {
      const fetched = answer({ waiting: 0, running: false });
      vi.stubGlobal("fetch", fetched);
      await checkWaitingJobs(win);
      await flush();
      expect(String(fetched.mock.calls[0]?.[0])).toContain("api/tile-checks/state");
      expect(invoke).not.toHaveBeenCalled();
      vi.stubGlobal("fetch", answer({ waiting: 2, running: false }));
      await checkWaitingJobs(win);
      await flush();
      expect(invoke).toHaveBeenCalledTimes(1);
      expect(invoke).toHaveBeenLastCalledWith("open_check_window", undefined);
      vi.stubGlobal(
        "fetch",
        vi.fn(() => Promise.reject(new Error("offline"))),
      );
      await checkWaitingJobs(win);
      expect(invoke).toHaveBeenCalledTimes(1);
    } finally {
      (window as unknown as Bridge)[bridge] = previous;
      vi.unstubAllGlobals();
      onTileCheckEvent({ id: "x", state: "hung" }, win);
    }
  });
});

describe("the heartbeat", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("says the runner is alive, and reports the job abandoned if the page goes", () => {
    vi.useFakeTimers();
    const fetched = vi.fn((_url: string, _init?: RequestInit) => Promise.resolve(new Response()));
    vi.stubGlobal("fetch", fetched);
    const stop = heartbeat("chk_7");
    const urls = () => fetched.mock.calls.map((call) => String(call[0]).replace(/^.*api\//, ""));
    vi.advanceTimersByTime(4100);
    expect(urls()).toEqual(["tile-checks/chk_7/alive", "tile-checks/chk_7/alive"]);
    // A frame just after a beat sends nothing more; one a second later does.
    stop.frame();
    expect(fetched).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(1000);
    stop.frame();
    expect(fetched).toHaveBeenCalledTimes(3);
    window.dispatchEvent(new Event("pagehide"));
    const [url, init] = fetched.mock.calls.at(-1)!;
    expect(String(url)).toContain("tile-checks/chk_7/result");
    expect(init).toMatchObject({ method: "PUT", keepalive: true });
    expect(JSON.parse(String(init?.body))).toMatchObject({ abandoned: true });
    stop();
    const count = fetched.mock.calls.length;
    vi.advanceTimersByTime(10000);
    window.dispatchEvent(new Event("pagehide"));
    expect(fetched).toHaveBeenCalledTimes(count);
  });
});
