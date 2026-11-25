import {
  createServer,
  type IncomingHttpHeaders,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import type { AddressInfo } from "node:net";
import { inspect } from "node:util";
import { afterEach, describe, expect, it } from "vitest";
import type { Launch } from "./client.ts";
import {
  appUrl,
  backoff,
  Control,
  Endpoint,
  EventStreamReader,
  NO_HISTORY,
  READ_TIMEOUT_MS,
  type SseEvent,
  stream,
  type StreamItem,
  type Timers,
} from "./events.ts";

const POLL = 2000;
const TOKEN = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ";

interface Head {
  method: string;
  url: string;
  headers: IncomingHttpHeaders;
}

type Handler = (req: IncomingMessage, res: ServerResponse, head: Head) => void;

const servers: Server[] = [];

afterEach(() => {
  for (const server of servers.splice(0)) {
    server.closeAllConnections();
    server.close();
  }
});

/** A test daemon on 127.0.0.1:0; `heads` collects every request head. */
async function serve(handler: Handler): Promise<{ endpoint: Endpoint; heads: Head[] }> {
  const heads: Head[] = [];
  const server = createServer((req, res) => {
    const head = { method: req.method ?? "", url: req.url ?? "", headers: req.headers };
    heads.push(head);
    handler(req, res, head);
  });
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = (server.address() as AddressInfo).port;
  const endpoint = Endpoint.create("127.0.0.1", port, TOKEN);
  if (endpoint === null) throw new Error("loopback endpoint");
  return { endpoint, heads };
}

function stream200(res: ServerResponse, body: string): void {
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  res.end(body);
}

const never = () => false;

/** Reads one connection's events; resolves to the error it ended with. */
async function readEvents(
  endpoint: Endpoint,
  cursor: { last: bigint },
  stop: () => boolean,
  onEvent: (event: SseEvent) => void,
): Promise<Error | null> {
  try {
    await stream(endpoint, cursor, POLL, stop, new Control(), (item) => {
      if (item.kind === "event") onEvent(item.event);
    });
    return null;
  } catch (e) {
    return e as Error;
  }
}

describe("events stream", () => {
  it("reads events and authenticates with a header", async () => {
    const { endpoint, heads } = await serve((_req, res) =>
      stream200(
        res,
        ': connected\n\nid: 5\nevent: reset\ndata: {}\n\nid: 6\nevent: notice\ndata: {"a":\ndata: 1}\n\n: ping\n\nevent: index\ndata: {}\n\n',
      ),
    );
    const got: SseEvent[] = [];
    const cursor = { last: NO_HISTORY };
    const result = await readEvents(endpoint, cursor, never, (e) => got.push(e));
    expect(result, "the server closed the stream").not.toBeNull();
    const head = heads[0];
    expect(head?.method).toBe("GET");
    expect(head?.url).toBe("/api/events");
    expect(head?.headers.authorization).toBe(`Bearer ${TOKEN}`);
    expect(head?.headers["last-event-id"]).toBe(String(2n ** 64n - 1n));
    expect(head?.headers.host).toMatch(/^127\.0\.0\.1:/);
    expect(got).toEqual([
      { id: 5n, name: "reset", data: "{}" },
      { id: 6n, name: "notice", data: '{"a":\n1}' },
      { id: null, name: "index", data: "{}" },
    ]);
    expect(cursor.last).toBe(6n);
  });

  it("one stream delivers liveness and full events", async () => {
    const { endpoint } = await serve((_req, res) =>
      stream200(res, ': ping\n\nid: 9\nevent: notice\ndata: {"kind":"review_finished"}\n\n'),
    );
    const items: StreamItem[] = [];
    const cursor = { last: NO_HISTORY };
    await stream(endpoint, cursor, POLL, never, new Control(), (item) => items.push(item)).catch(
      () => {},
    );
    expect(items).toEqual([
      { kind: "open" },
      { kind: "ping" },
      { kind: "event", event: { id: 9n, name: "notice", data: '{"kind":"review_finished"}' } },
    ]);
    expect(cursor.last).toBe(9n);
  });

  it("replacement stops before dispatching buffered events", async () => {
    const { endpoint } = await serve((_req, res) =>
      stream200(res, "id: 9\nevent: notice\ndata: {}\n\n"),
    );
    let stopped = false;
    const cursor = { last: NO_HISTORY };
    await stream(
      endpoint,
      cursor,
      POLL,
      () => stopped,
      new Control(),
      (item) => {
        expect(item).toEqual({ kind: "open" });
        stopped = true;
      },
    );
    expect(cursor.last).toBe(NO_HISTORY);
  });

  it("cancellation interrupts a quiet socket", async () => {
    let release: () => void = () => {};
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { endpoint } = await serve((_req, res) => {
      res.writeHead(200);
      res.flushHeaders();
      void released.then(() => res.end());
    });
    const control = new Control();
    const cursor = { last: NO_HISTORY };
    const reading = stream(endpoint, cursor, READ_TIMEOUT_MS, never, control, (item) => {
      expect(item).toEqual({ kind: "open" });
      control.cancel();
    }).catch(() => {});
    const result = await Promise.race([
      reading.then(() => "interrupted"),
      new Promise((resolve) => setTimeout(() => resolve("still reading"), 2000)),
    ]);
    // Release the server even if cancellation failed, so a failed test never
    // leaves a connection waiting for the full socket timeout.
    release();
    expect(result, "cancellation must interrupt the read before its 70-second timeout").toBe(
      "interrupted",
    );
  });

  it("a refused credential is an error", async () => {
    const { endpoint } = await serve((_req, res) => {
      res.writeHead(401);
      res.end("unauthorized\n");
    });
    const cursor = { last: NO_HISTORY };
    const error = await readEvents(endpoint, cursor, never, () => {
      throw new Error("no events");
    });
    expect(error?.message).toContain("401");
    expect(error?.message).not.toContain(TOKEN);
  });

  it("sends nothing once the daemon's process has gone", async () => {
    const { endpoint, heads } = await serve((_req, res) => stream200(res, ""));
    let running = false;
    const gone = Endpoint.fromLaunch({
      url: new URL(`http://${endpoint.authority}/#token=${TOKEN}`),
      bearer: () => TOKEN,
      running: () => running,
    });
    if (gone === null) throw new Error("loopback endpoint");
    const error = await readEvents(gone, { last: NO_HISTORY }, never, () => {
      throw new Error("no events");
    });
    expect(error?.message).toBe("the baum daemon has stopped");
    await expect(appUrl(gone)).rejects.toThrow("the baum daemon has stopped");
    expect(heads).toHaveLength(0);
    running = true;
    expect(gone.running()).toBe(true);
  });

  it("reconnects after the last event seen", async () => {
    let n = 0;
    const { endpoint, heads } = await serve((_req, res) => {
      stream200(res, `id: ${10 + n}\nevent: notice\ndata: {}\n\n`);
      n += 1;
    });
    const ids: (bigint | null)[] = [];
    const cursor = { last: NO_HISTORY };
    for (let i = 0; i < 2; i++) await readEvents(endpoint, cursor, never, (e) => ids.push(e.id));
    expect(ids).toEqual([10n, 11n]);
    expect(heads[0]?.headers["last-event-id"]).toBe(String(NO_HISTORY));
    expect(heads[1]?.headers["last-event-id"]).toBe("10");
  });

  it("exchange returns the app url", async () => {
    let body = "";
    const { endpoint, heads } = await serve((req, res) => {
      req.on("data", (c: Buffer) => {
        body += c.toString();
      });
      req.on("end", () => {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end('{"path":"/s/secret_ns/"}');
      });
    });
    const url = await appUrl(endpoint);
    expect(url.pathname).toBe("/s/secret_ns/");
    expect(url.origin).toBe(endpoint.origin);
    const head = heads[0];
    expect(head?.method).toBe("POST");
    expect(head?.url).toBe("/api/auth/exchange");
    expect(head?.headers.origin).toBe(`http://${endpoint.authority}`);
    expect(head?.headers["content-type"]).toBe("application/json");
    expect(head?.headers.authorization).toBeUndefined();
    expect(JSON.parse(body)).toEqual({ token: TOKEN });
  });

  it("exchange refuses paths off the daemon", async () => {
    for (const path of [
      "http://evil.example/s/x/",
      "/other/",
      "/s/x/?q=1",
      "//evil.example/s/x/",
    ]) {
      const { endpoint } = await serve((req, res) => {
        req.resume();
        req.on("end", () => res.end(JSON.stringify({ path })));
      });
      await expect(appUrl(endpoint), path).rejects.toThrow();
    }
  });

  it("a peer that never sends a newline is cut off", async () => {
    const { endpoint } = await serve((_req, res) => {
      res.writeHead(200);
      res.write("data: ");
      const chunk = "x".repeat(8192);
      const pump = () => {
        while (!res.destroyed && res.write(chunk)) {}
        if (!res.destroyed) res.once("drain", pump);
      };
      pump();
    });
    const cursor = { last: NO_HISTORY };
    const error = await readEvents(endpoint, cursor, never, () => {
      throw new Error("no events");
    });
    expect(error?.message).toContain("too long");
  });

  it("a character split by a pause is not lost", async () => {
    const { endpoint } = await serve((_req, res) => {
      const bytes = Buffer.from("event: notice\ndata: café\n\n");
      res.writeHead(200);
      res.write(bytes.subarray(0, bytes.length - 3));
      setTimeout(() => res.end(bytes.subarray(bytes.length - 3)), 50);
    });
    const got: string[] = [];
    const cursor = { last: NO_HISTORY };
    await readEvents(endpoint, cursor, never, (e) => got.push(e.data));
    expect(got).toEqual(["café"]);
  });

  it("only loopback endpoints are accepted", () => {
    expect(Endpoint.create("127.0.0.1", 4100, TOKEN)).not.toBeNull();
    expect(Endpoint.create("192.0.2.1", 4100, TOKEN)).toBeNull();
    expect(Endpoint.create("localhost", 4100, TOKEN)).toBeNull();
  });

  it("the credential is not in debug output", async () => {
    const { endpoint } = await serve(() => {});
    expect(inspect(endpoint)).not.toContain(TOKEN);
    expect(inspect(endpoint, { showHidden: true, depth: 5 })).not.toContain(TOKEN);
    expect(JSON.stringify(endpoint)).not.toContain(TOKEN);
    expect(String(endpoint)).not.toContain(TOKEN);
  });
});

function launchFor(endpoint: Endpoint): Launch {
  return {
    url: new URL(`http://${endpoint.authority}/#token=${TOKEN}`),
    bearer: () => TOKEN,
  };
}

/** A clock that fires only when the test says so. */
function manualTimers() {
  const pending: { callback: () => void; ms: number; live: boolean }[] = [];
  const timers: Timers = {
    setTimeout(callback, ms) {
      const timer = { callback, ms, live: true };
      pending.push(timer);
      return timer;
    },
    clearTimeout(handle) {
      (handle as { live: boolean }).live = false;
    },
  };
  return {
    timers,
    waits: () => pending.filter((t) => t.live).map((t) => t.ms),
    fire() {
      const timer = pending.find((t) => t.live);
      if (timer === undefined) throw new Error("no timer is waiting");
      timer.live = false;
      timer.callback();
    },
  };
}

async function until(condition: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 400; i++) {
    if (condition()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

describe("event stream reader", () => {
  it("backs off from one second to thirty", () => {
    expect(Array.from({ length: 8 }, (_, a) => backoff(a))).toEqual([
      1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000,
    ]);
    expect(backoff(Number.MAX_SAFE_INTEGER)).toBe(30_000);
  });

  it("forwards change notices and full notices separately", async () => {
    const { endpoint } = await serve((_req, res) =>
      stream200(
        res,
        'id: 1\nevent: progress\ndata: {}\n\nid: 2\nevent: notice\ndata: {"kind":"x"}\n\n',
      ),
    );
    const changes: string[] = [];
    const notices: string[] = [];
    let opened = 0;
    let closed = 0;
    const clock = manualTimers();
    const reader = new EventStreamReader({
      timers: clock.timers,
      log: () => {},
      onOpen: () => {
        opened += 1;
      },
      onClosed: () => {
        closed += 1;
      },
      onChange: (e) => changes.push(e.name),
      onNotice: (ep, e) => {
        expect(ep.authority).toBe(endpoint.authority);
        notices.push(e.data);
      },
    });
    reader.attach(launchFor(endpoint));
    await until(() => closed === 1, "the stream to end");
    reader.close();
    expect(changes).toEqual(["progress"]);
    expect(notices).toEqual(['{"kind":"x"}']);
    expect(opened).toBe(1);
  });

  it("reconnects with backoff, resuming after the last event", async () => {
    let n = 0;
    const { endpoint, heads } = await serve((_req, res) => {
      if (n === 0) stream200(res, "id: 10\nevent: progress\ndata: {}\n\n");
      else stream200(res, "");
      n += 1;
    });
    const clock = manualTimers();
    const reader = new EventStreamReader({ timers: clock.timers, log: () => {} });
    reader.attach(launchFor(endpoint));
    await until(() => clock.waits().length === 1, "the first backoff");
    expect(clock.waits()).toEqual([1000]);
    clock.fire();
    await until(() => clock.waits().length === 1 && heads.length === 2, "the second backoff");
    // The stream opened the second time, so the failure count starts over.
    expect(clock.waits()).toEqual([1000]);
    expect(heads[0]?.headers["last-event-id"]).toBe(String(NO_HISTORY));
    expect(heads[1]?.headers["last-event-id"]).toBe("10");
    reader.close();
  });

  it("grows the backoff while the daemon keeps failing", async () => {
    const { endpoint } = await serve((_req, res) => {
      res.writeHead(503);
      res.end();
    });
    const clock = manualTimers();
    const logs: string[] = [];
    const reader = new EventStreamReader({ timers: clock.timers, log: (m) => logs.push(m) });
    reader.attach(launchFor(endpoint));
    for (const expected of [1000, 2000, 4000]) {
      await until(() => clock.waits().length === 1, "a backoff");
      expect(clock.waits()).toEqual([expected]);
      clock.fire();
    }
    reader.close();
    expect(logs.length).toBeGreaterThan(0);
    expect(logs.join("\n")).not.toContain(TOKEN);
  });

  it("closes the stream immediately on detach and starts beyond the latest event on replace", async () => {
    const closedSockets: number[] = [];
    const { endpoint, heads } = await serve((req, res) => {
      res.writeHead(200);
      res.write("id: 3\nevent: progress\ndata: {}\n\n");
      req.socket.on("close", () => closedSockets.push(heads.length));
    });
    const clock = manualTimers();
    const changes: string[] = [];
    const reader = new EventStreamReader({
      timers: clock.timers,
      log: () => {},
      onChange: (e) => changes.push(e.name),
    });
    reader.attach(launchFor(endpoint));
    await until(() => changes.length === 1, "the first event");
    reader.detach();
    await until(() => closedSockets.length === 1, "the socket to close");
    expect(clock.waits().length).toBeLessThanOrEqual(1);
    reader.attach(launchFor(endpoint));
    await until(() => heads.length === 2, "the second connection");
    // A replacement daemon: its event ids restart, so resume from nothing.
    expect(heads[1]?.headers["last-event-id"]).toBe(String(NO_HISTORY));
    reader.close();
    await until(() => closedSockets.length === 2, "the replacement socket to close");
  });
});
