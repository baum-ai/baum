import { describe, expect, it, vi } from "vitest";
import {
  type AttachDeps,
  Attacher,
  HEALTH_INTERVAL,
  HEALTH_INTERVAL_STREAMING,
  HealthPause,
} from "./attach.ts";
import { Failure, type Health, Launch } from "./daemon.ts";

const TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";

function launchOn(port: number, state = "/s"): Launch {
  return new Launch(`http://127.0.0.1:${port}/#token=${TOKEN}`, TOKEN, {
    version: "v1",
    pid: port,
    app: null,
    state,
  });
}

/** Lets a test step the health watch: each pause waits for `tick`. */
function harness(overrides: Partial<AttachDeps> = {}) {
  const ticks: (() => void)[] = [];
  const healths: Health[] = [];
  const events: string[] = [];
  let credential: Launch | null = null;
  const deps: AttachDeps = {
    misplaced: () => null,
    cliPath: async () => "/baum",
    ensure: async () => launchOn(4100),
    health: async (launch) => {
      events.push(`health ${launch.origin}`);
      return healths.shift() ?? "ok";
    },
    readCredential: () => {
      if (!credential) throw new Error("missing");
      return credential;
    },
    logTail: () => ["── daemon.log", "panic"],
    frontend: (url) => url,
    setupOpen: () => false,
    load: (url) => events.push(`load ${url.replace(TOKEN, "T")}`),
    showScreen: () => events.push("screen"),
    pause: () => new Promise((resolve) => ticks.push(resolve)),
    sleep: () => new Promise((resolve) => ticks.push(resolve)),
    log: () => {},
    hooks: {
      connecting: () => events.push("connecting"),
      attached: (l) => events.push(`attached ${l.origin}`),
      pageAttached: (l) => events.push(`page ${l.origin}`),
      detached: () => events.push("detached"),
    },
    ...overrides,
  };
  const attacher = new Attacher(deps);
  const tick = async (results: Health[] = []) => {
    healths.push(...results);
    await vi.waitFor(() => expect(ticks.length).toBeGreaterThan(0));
    ticks.shift()?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  };
  return {
    attacher,
    events,
    tick,
    setCredential: (l: Launch | null) => {
      credential = l;
    },
  };
}

describe("attaching", () => {
  it("loads the daemon and reports it connected", async () => {
    const { attacher, events } = harness();
    expect(attacher.connection()).toEqual({ phase: "connecting" });
    await attacher.connect();
    expect(attacher.connection()).toEqual({ phase: "connected" });
    expect(attacher.daemon()).toBe("http://127.0.0.1:4100");
    expect(events).toEqual([
      "connecting",
      "screen",
      "load http://127.0.0.1:4100/#token=T",
      "page http://127.0.0.1:4100",
      "attached http://127.0.0.1:4100",
    ]);
  });

  it("leaves the setup screen showing", async () => {
    const { attacher, events } = harness({ setupOpen: () => true });
    await attacher.connect();
    expect(attacher.connection()).toEqual({ phase: "connected" });
    expect(events).toEqual(["connecting", "screen", "attached http://127.0.0.1:4100"]);
  });

  it("uses the development proxy's origin", async () => {
    const { attacher } = harness({ frontend: (url) => url.replace(":4100", ":1420") });
    await attacher.connect();
    expect(attacher.daemon()).toBe("http://127.0.0.1:1420");
  });

  it("shows why ensure failed", async () => {
    const { attacher, events } = harness({
      ensure: async () => {
        throw new Failure("baum daemon ensure failed (exit status: 1).", ["locked"]);
      },
    });
    await attacher.connect();
    expect(attacher.connection()).toEqual({
      phase: "failed",
      message: "baum daemon ensure failed (exit status: 1).",
      details: ["locked"],
    });
    expect(attacher.daemon()).toBeNull();
    expect(events.slice(-2)).toEqual(["detached", "screen"]);
  });

  it("asks to move an app it cannot run baum from, and runs nothing", async () => {
    const ensure = vi.fn();
    const { attacher } = harness({ misplaced: () => "Move baum.", ensure });
    await attacher.connect();
    expect(attacher.connection()).toEqual({ phase: "misplaced", message: "Move baum." });
    expect(ensure).not.toHaveBeenCalled();
  });

  it("does not start a second attempt while one is connecting", async () => {
    let release: (l: Launch) => void = () => {};
    const ensure = vi.fn(() => new Promise<Launch>((resolve) => (release = resolve)));
    const { attacher } = harness({ ensure });
    const first = attacher.connect();
    await attacher.connect();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(ensure).toHaveBeenCalledTimes(1);
    release(launchOn(4100));
    await first;
    expect(attacher.connection()).toEqual({ phase: "connected" });
  });
});

describe("the health watch", () => {
  it("returns to the connecting screen after three failures, with the log", async () => {
    const { attacher, tick, events } = harness();
    await attacher.connect();
    await tick(["down"]);
    await tick(["down"]);
    expect(attacher.connection().phase).toBe("connected");
    await tick(["down"]);
    expect(attacher.connection()).toEqual({
      phase: "failed",
      message: "The baum daemon stopped responding.",
      details: ["── daemon.log", "panic"],
    });
    expect(events.at(-1)).toBe("screen");
  });

  it("a passing check resets the count", async () => {
    const { attacher, tick } = harness();
    await attacher.connect();
    for (const h of ["down", "down", "ok", "down", "down"] as const) await tick([h]);
    expect(attacher.connection().phase).toBe("connected");
  });

  it("attaches to a replacing daemon once", async () => {
    const launches = [launchOn(4100), launchOn(4200)];
    const ensure = vi.fn(async () => launches.shift() as Launch);
    const { attacher, tick } = harness({ ensure });
    await attacher.connect();
    await tick(["replaced"]);
    await vi.waitFor(() => expect(attacher.daemon()).toBe("http://127.0.0.1:4200"));
    // The replacement's credential fails too: the app shows it.
    await tick(["replaced"]);
    expect(attacher.connection()).toMatchObject({
      phase: "failed",
      message: "The new baum daemon did not accept the app's credential.",
    });
    expect(ensure).toHaveBeenCalledTimes(2);
  });

  it("while the screen shows, attaches to a daemon another client started", async () => {
    const ensure = vi.fn(async () => launchOn(4100));
    const { attacher, tick, setCredential } = harness({ ensure });
    await attacher.connect();
    for (let i = 0; i < 3; i++) await tick(["down"]);
    expect(attacher.connection().phase).toBe("failed");
    // The same daemon's credential is not a replacement.
    setCredential(launchOn(4100));
    await tick();
    expect(attacher.connection().phase).toBe("failed");
    ensure.mockImplementation(async () => launchOn(4300));
    setCredential(launchOn(4300));
    await tick(["ok"]);
    await vi.waitFor(() => expect(attacher.daemon()).toBe("http://127.0.0.1:4300"));
  });

  it("a newer attempt ends the old watch", async () => {
    const { attacher, tick, events } = harness();
    await attacher.connect();
    const before = events.length;
    await attacher.connect("first");
    await tick(["down"]); // the old watch's pause: it stops
    expect(events.slice(before).filter((e) => e.startsWith("health"))).toEqual([]);
  });
});

describe("the health pause", () => {
  it("waits 5 seconds, 30 while the stream is up, and wakes at once when it goes down", async () => {
    vi.useFakeTimers();
    try {
      const pause = new HealthPause();
      let done = false;
      void pause.pause().then(() => (done = true));
      await vi.advanceTimersByTimeAsync(HEALTH_INTERVAL);
      expect(done).toBe(true);
      pause.setStreamUp(true);
      done = false;
      void pause.pause().then(() => (done = true));
      await vi.advanceTimersByTimeAsync(HEALTH_INTERVAL_STREAMING - 1);
      expect(done).toBe(false);
      pause.setStreamUp(false);
      await vi.advanceTimersByTimeAsync(0);
      expect(done).toBe(true);
      done = false;
      void pause.pause().then(() => (done = true));
      pause.wake();
      await vi.advanceTimersByTimeAsync(0);
      expect(done).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
