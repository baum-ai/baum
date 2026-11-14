import { describe, expect, it } from "vitest";
import {
  clampHeight,
  HEIGHT_MAX,
  HEIGHT_MIN,
  HeightGuard,
  MAX_ERROR,
  PROTOCOL,
  RATE,
  RateGuard,
  readMessage,
} from "./protocol";

const m = (fields: Record<string, unknown>) => ({ protocol: PROTOCOL, ...fields });

describe("readMessage", () => {
  it("reads every kind a frame may send", () => {
    expect(readMessage(m({ kind: "hello", kit: 1, registered: true }))).toEqual({
      kind: "hello",
      kit: 1,
      registered: true,
    });
    expect(
      readMessage(
        m({
          kind: "ready",
          metrics: {
            height: 300,
            text_chars: 40,
            elements: 9,
            overflow_x: false,
            overflow_y: true,
          },
        }),
      ),
    ).toEqual({
      kind: "ready",
      metrics: {
        height: 300,
        text_chars: 40,
        elements: 9,
        overflow_x: false,
        overflow_y: true,
        media: 0,
        empty_state: false,
        failed: false,
        unresolved: 0,
        render_ms: 0,
      },
    });
    expect(
      readMessage(
        m({
          kind: "ready",
          metrics: { height: 200, media: 2, empty_state: true, failed: "yes", unresolved: 1, render_ms: 40 },
        }),
      ),
    ).toMatchObject({
      metrics: { media: 2, empty_state: true, failed: false, unresolved: 1, render_ms: 40 },
    });
    expect(readMessage(m({ kind: "resize", height: 480 }))).toEqual({
      kind: "resize",
      height: 480,
    });
    expect(readMessage(m({ kind: "error", code: "script_error", message: "x", line: 3 }))).toEqual({
      kind: "error",
      code: "script_error",
      message: "x",
      line: 3,
    });
    expect(readMessage(m({ kind: "jump", key: '["hunk","h_000000000001",null]' }))).toEqual({
      kind: "jump",
      key: '["hunk","h_000000000001",null]',
    });
    expect(readMessage(m({ kind: "ask" }))).toEqual({ kind: "ask" });
    expect(readMessage(m({ kind: "open" }))).toEqual({ kind: "open" });
    expect(readMessage(m({ kind: "key", key: "Escape", shift: true }))).toEqual({
      kind: "key",
      key: "Escape",
      shift: true,
    });
  });

  // What a hostile frame might post: each is dropped whole.
  const hostile: [string, unknown][] = [
    ["undefined", undefined],
    ["null", null],
    ["a string", "hello"],
    ["a number", 7],
    ["an array", [PROTOCOL, "hello"]],
    ["no protocol", { kind: "hello", kit: 1, registered: true }],
    ["another protocol", { protocol: "baum.tile/2", kind: "hello", kit: 1, registered: true }],
    ["a kind that is not a string", m({ kind: 1 })],
    ["an unknown kind", m({ kind: "navigate", url: "http://outside.invalid" })],
    ["init, which only the page sends", m({ kind: "init", face: "detail" })],
    ["a kind on the prototype", m({ kind: "toString" })],
    ["hello with a kit as text", m({ kind: "hello", kit: "1", registered: true })],
    ["hello with kit 0", m({ kind: "hello", kit: 0, registered: true })],
    ["hello with a fractional kit", m({ kind: "hello", kit: 1.5, registered: true })],
    ["hello without registered", m({ kind: "hello", kit: 1 })],
    ["ready without metrics", m({ kind: "ready" })],
    ["ready with metrics as an array", m({ kind: "ready", metrics: [300] })],
    ["ready with a negative height", m({ kind: "ready", metrics: { height: -1 } })],
    ["ready with a height as text", m({ kind: "ready", metrics: { height: "300" } })],
    ["resize as text", m({ kind: "resize", height: "999999" })],
    ["resize not a number", m({ kind: "resize", height: Number.NaN })],
    ["resize infinite", m({ kind: "resize", height: Number.POSITIVE_INFINITY })],
    ["error with markup for a code", m({ kind: "error", code: "<b>x</b>", message: "x" })],
    ["error without a message", m({ kind: "error", code: "script_error" })],
    ["jump without a key", m({ kind: "jump" })],
    ["jump with a key as an object", m({ kind: "jump", key: { type: "hunk" } })],
    ["jump with a key over 1 KiB", m({ kind: "jump", key: "k".repeat(1025) })],
    ["a key the page does not take", m({ kind: "key", key: "Enter", shift: false })],
    ["a key as an object", m({ kind: "key", key: { key: "Escape" } })],
    [
      "a message whose prototype is not Object's",
      Object.assign(new (class Fake {})(), m({ kind: "ask" })),
    ],
  ];
  for (const [name, data] of hostile)
    it(`drops ${name}`, () => {
      expect(readMessage(data)).toBeNull();
    });

  it("caps an error's message and keeps it as text", () => {
    const read = readMessage(
      m({
        kind: "error",
        code: "script_error",
        message: "<img src=x onerror=alert(1)>".repeat(10000),
      }),
    );
    expect(read?.kind).toBe("error");
    if (read?.kind !== "error") return;
    expect(read.message.length).toBe(MAX_ERROR);
    expect(read.message.startsWith("<img")).toBe(true);
  });

  it("ignores fields it does not know, however large", () => {
    expect(readMessage(m({ kind: "ask", padding: "y".repeat(4 * 1024 * 1024) }))).toEqual({
      kind: "ask",
    });
  });

  it("clamps the height a ready reports, as a resize's", () => {
    expect(readMessage(m({ kind: "ready", metrics: { height: 1e9 } }))).toMatchObject({
      metrics: { height: HEIGHT_MAX },
    });
    expect(readMessage(m({ kind: "ready", metrics: { height: 0 } }))).toMatchObject({
      metrics: { height: HEIGHT_MIN },
    });
  });

  it("clamps a detail's height to its bounds", () => {
    expect(readMessage(m({ kind: "resize", height: 1e9 }))).toEqual({
      kind: "resize",
      height: HEIGHT_MAX,
    });
    expect(readMessage(m({ kind: "resize", height: -50 }))).toEqual({
      kind: "resize",
      height: HEIGHT_MIN,
    });
    expect(clampHeight(480.2)).toBe(481);
    expect(clampHeight("480")).toBeNull();
  });
});

describe("RateGuard", () => {
  it("lets an honest frame through", () => {
    const guard = new RateGuard();
    for (let i = 0; i < 20; i++) expect(guard.admit(1000 + i * 40)).toBe("ok");
  });

  it("drops what passes the rate in a second, without unmounting a short burst", () => {
    const guard = new RateGuard();
    const seen = Array.from({ length: 50 }, () => guard.admit(1500));
    expect(seen.filter((admission) => admission === "ok")).toHaveLength(RATE.PER_SECOND);
    expect(seen.filter((admission) => admission === "drop")).toHaveLength(50 - RATE.PER_SECOND);
    // The next second starts afresh.
    expect(guard.admit(2100)).toBe("ok");
  });

  it("finds a flood sustained over seconds", () => {
    const guard = new RateGuard();
    const results: string[] = [];
    for (let second = 0; second <= RATE.OVER_SECONDS; second++)
      for (let i = 0; i < 40; i++) results.push(guard.admit(10_000 + second * 1000 + i * 20));
    expect(results.at(-1)).toBe("flood");
    expect(guard.admit(30_000)).toBe("flood");
  });

  it("finds a flood over the rate now and then, in a sliding window", () => {
    // Over the rate every third second: never twice in a row, but four
    // times within ten seconds.
    const guard = new RateGuard();
    let last = "";
    for (const second of [0, 3, 6, 9])
      for (let i = 0; i < 40; i++) last = guard.admit(second * 1000 + i);
    expect(last).toBe("flood");
  });

  it("lets a frame over the rate rarely, and one just under it always", () => {
    const rare = new RateGuard();
    for (const second of [0, 5, 10, 15, 20, 25])
      for (let i = 0; i < 40; i++) expect(rare.admit(second * 1000 + i)).not.toBe("flood");
    const steady = new RateGuard();
    for (let second = 0; second < 60; second++)
      for (let i = 0; i < RATE.PER_SECOND; i++)
        expect(steady.admit(second * 1000 + i * 30)).toBe("ok");
  });

  it("finds a burst in one second", () => {
    const guard = new RateGuard();
    let last = "";
    for (let i = 0; i <= RATE.BURST; i++) last = guard.admit(5000);
    expect(last).toBe("flood");
  });
});

describe("HeightGuard", () => {
  it("follows a frame's height", () => {
    const guard = new HeightGuard();
    expect(guard.next(300, 0)).toBe(300);
    expect(guard.next(280, 10)).toBe(280);
    expect(guard.next(600, 20)).toBe(600);
  });

  it("stops following a height that bounces", () => {
    const guard = new HeightGuard();
    let applied: number | null = 0;
    for (let i = 0; i < 12; i++) applied = guard.next(i % 2 ? 400 : 600, i * 50);
    expect(applied).toBeNull();
    expect(guard.frozen).toBe(true);
    // Changes of direction spread out are an ordinary tile reflowing.
    const calm = new HeightGuard();
    for (let i = 0; i < 12; i++) expect(calm.next(i % 2 ? 400 : 600, i * 1000)).not.toBeNull();
  });

  it("stops following a height that grows with the frame", () => {
    const guard = new HeightGuard();
    let height = 200;
    let applied: number | null = 0;
    for (let i = 0; i < 12; i++) {
      height += 50;
      applied = guard.next(height, i * 16);
    }
    expect(applied).toBeNull();
    expect(guard.frozen).toBe(true);
  });
});
