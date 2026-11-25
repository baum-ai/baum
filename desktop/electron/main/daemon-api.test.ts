import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { inspect } from "node:util";
import { afterEach, describe, expect, it } from "vitest";
import { appUrlOf, clientLaunch, EventFeed, noticeRoute, trayDaemon } from "./daemon-api.ts";
import { Launch } from "./daemon.ts";
import type { DaemonEvent } from "./tray.ts";

const TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";
const closers: (() => void)[] = [];

afterEach(() => {
  for (const close of closers.splice(0)) close();
});

function launchOn(port: number): Launch {
  return new Launch(`http://127.0.0.1:${port}/#token=${TOKEN}`, TOKEN, {
    version: "1",
    pid: 1,
    app: null,
    state: "/state",
  });
}

/** A daemon answering every request with `answer(request)`. */
async function serve(
  answer: (req: IncomingMessage, body: string) => [number, string],
): Promise<{ port: number; seen: { method: string; url: string; body: string; auth: string }[] }> {
  const seen: { method: string; url: string; body: string; auth: string }[] = [];
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const body = Buffer.concat(chunks).toString("utf8");
      seen.push({
        method: req.method ?? "",
        url: req.url ?? "",
        body,
        auth: req.headers.authorization ?? "",
      });
      const [status, text] = answer(req, body);
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(text);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  closers.push(() => server.close());
  return { port: (server.address() as AddressInfo).port, seen };
}

describe("the daemon's launch for the client", () => {
  it("reads the launch URL and capability, and prints neither", () => {
    const launch = clientLaunch(launchOn(4100));
    expect(launch.url.href).toBe(`http://127.0.0.1:4100/#token=${TOKEN}`);
    expect(launch.bearer()).toBe(TOKEN);
    expect(JSON.stringify(launch)).not.toContain(TOKEN);
    expect(inspect(launch)).not.toContain(TOKEN);
    expect(inspect({ launch })).not.toContain(TOKEN);
  });
});

describe("the tray's daemon", () => {
  it("resolves the body of a successful answer and rejects any other", async () => {
    const { port, seen } = await serve((req) =>
      req.url === "/api/jobs"
        ? [200, '{"jobs":[]}']
        : req.url === "/api/automatic/catch-up"
          ? [202, "{}"]
          : [500, "{}"],
    );
    const daemon = trayDaemon(clientLaunch(launchOn(port)), new EventFeed());
    await expect(daemon.get("/api/jobs")).resolves.toBe('{"jobs":[]}');
    await expect(daemon.get("/api/automatic")).rejects.toThrow("/api/automatic answered 500");
    await expect(daemon.put("/api/automatic", '{"enabled":false}')).rejects.toThrow("500");
    await expect(daemon.post("/api/automatic/catch-up", "{}")).resolves.toBe("{}");
    expect(seen.map((s) => `${s.method} ${s.url}`)).toEqual([
      "GET /api/jobs",
      "GET /api/automatic",
      "PUT /api/automatic",
      "POST /api/automatic/catch-up",
    ]);
    expect(seen[2]?.body).toBe('{"enabled":false}');
    expect(seen.every((s) => s.auth === `Bearer ${TOKEN}`)).toBe(true);
  });

  it("reads the app's one event stream", async () => {
    const feed = new EventFeed();
    const daemon = trayDaemon(clientLaunch(launchOn(1)), feed);
    const seen: string[] = [];
    const handlers = {
      onOpen: () => seen.push("open"),
      onEvent: (e: DaemonEvent) => seen.push(`${e.name}:${String(e.id)}`),
    };
    const done = daemon.stream(handlers, new AbortController().signal, { lastId: undefined });
    feed.change({ id: 1n, name: "progress", data: "{}" });
    feed.opened();
    feed.change({ id: 2n, name: "progress", data: "{}" });
    feed.change({ id: null, name: "live", data: "{}" });
    feed.closed();
    await done;
    expect(seen).toEqual(["progress:1", "open", "progress:2", "live:undefined"]);
  });
});

describe("the event feed", () => {
  const quiet = { onOpen: () => {}, onEvent: () => {} };

  it("opens a subscription at once while the stream is up", async () => {
    const feed = new EventFeed();
    feed.opened();
    expect(feed.up).toBe(true);
    let opened = 0;
    const done = feed.subscribe({ ...quiet, onOpen: () => opened++ }, new AbortController().signal);
    expect(opened).toBe(1);
    feed.closed();
    await done;
    expect(feed.up).toBe(false);
  });

  it("ends a subscription when its signal aborts, and sends it nothing after", async () => {
    const feed = new EventFeed();
    const abort = new AbortController();
    const events: string[] = [];
    const done = feed.subscribe({ ...quiet, onEvent: (e) => events.push(e.name) }, abort.signal);
    feed.change({ id: 1n, name: "index", data: "" });
    abort.abort();
    await done;
    feed.change({ id: 2n, name: "live", data: "" });
    expect(events).toEqual(["index"]);
    const aborted = new AbortController();
    aborted.abort();
    await feed.subscribe(quiet, aborted.signal);
  });

  it("ends every subscription when the stream closes", async () => {
    const feed = new EventFeed();
    const first = feed.subscribe(quiet, new AbortController().signal);
    const second = feed.subscribe(quiet, new AbortController().signal);
    feed.closed();
    await Promise.all([first, second]);
  });
});

describe("the notifications' app path", () => {
  it("is asked of the attached daemon", async () => {
    const { port, seen } = await serve(() => [200, '{"path":"/s/secret/"}']);
    const url = await appUrlOf(clientLaunch(launchOn(port)));
    expect(url.href).toBe(`http://127.0.0.1:${port}/s/secret/`);
    expect(seen[0]?.url).toBe("/api/auth/exchange");
  });

  it("is an error without a daemon", async () => {
    await expect(appUrlOf(null)).rejects.toThrow("not attached");
  });
});

describe("tray routes", () => {
  it("open the same pages as the notifications", () => {
    expect(noticeRoute({ kind: "jobs" })).toEqual({ type: "jobs" });
    expect(noticeRoute({ kind: "job", id: "j1" })).toEqual({ type: "job", id: "j1" });
    expect(noticeRoute({ kind: "diagnostics" })).toEqual({ type: "diagnostics" });
    expect(noticeRoute({ kind: "session", key: "claude:1" })).toEqual({
      type: "session",
      key: "claude:1",
    });
  });
});
