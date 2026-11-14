import { afterEach, describe, expect, it, vi } from "vitest";
import { checkWindowHere, inPageChecksHere, onTileCheckEvent, setInPageRunner } from "./check-host";
import { AT_ONCE, matrix, measured, runJob, type CheckJob, type FrameSpec } from "./check-runner";
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
    const result = await runJob(job({ context: null }), document.body, draw);
    expect(most).toBe(AT_ONCE);
    expect(result.runs).toHaveLength(matrix(job()).length);
    expect(new Set(result.problems.map((p) => p.sample))).toEqual(new Set(["example", "minimal"]));
    expect(result.problems[0]).toMatchObject({ code: "unresolved_citation", face: "glance" });
  });
});

describe("where checks draw", () => {
  type Bridge = { __TAURI_INTERNALS__?: unknown };
  afterEach(() => {
    vi.useRealTimers();
    setInPageRunner(null);
  });
  const shell = (declared: unknown, invoke: unknown = vi.fn(() => Promise.resolve())) =>
    ({ __TAURI_INTERNALS__: { invoke }, __BAUM_DESKTOP__: declared }) as unknown as Window;

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

  it("opens the check window, and closes it on a hang and when idle", () => {
    vi.useFakeTimers();
    const invoke = vi.fn((_command: string, _args?: unknown) => Promise.resolve());
    const win = shell({ tileFrames: true, checkWindow: true }, invoke);
    const previous = (window as Bridge).__TAURI_INTERNALS__;
    (window as Bridge).__TAURI_INTERNALS__ = { invoke };
    try {
      const run = vi.fn();
      setInPageRunner(run);
      onTileCheckEvent({ id: "chk_1", state: "queued" }, win);
      expect(invoke).toHaveBeenLastCalledWith("open_check_window", undefined);
      onTileCheckEvent({ id: "chk_1", state: "hung" }, win);
      expect(invoke).toHaveBeenLastCalledWith("close_check_window", undefined);
      onTileCheckEvent({ id: "chk_2", state: "queued" }, win);
      expect(invoke).toHaveBeenLastCalledWith("open_check_window", undefined);
      vi.advanceTimersByTime(2 * 60 * 1000);
      expect(invoke).toHaveBeenLastCalledWith("close_check_window", undefined);
      expect(run).not.toHaveBeenCalled();
    } finally {
      (window as Bridge).__TAURI_INTERNALS__ = previous;
    }
  });
});
