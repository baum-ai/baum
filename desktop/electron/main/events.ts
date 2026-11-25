// The daemon's shared native events stream, read from the main process while
// the window is closed or hidden. The reader is hand-written over Node's
// `node:http` (an EventSource cannot send the Authorization header): it parses
// the server-sent-events lines itself, in bounded memory.
//
// Authentication. The daemon accepts one bearer credential, the launch
// capability the app already holds in `credential.json` and sends to
// `/api/health`; it has no read-only or events-only scope. The app sends it
// only in an `Authorization` header to the two routes it uses: `GET
// /api/events` (read-only) and `POST /api/auth/exchange` (to learn the app
// path for a click). It never goes in a URL, a log line or a page.
//
// API:
//   Endpoint.fromLaunch(launch) / Endpoint.create(host, port, token) -> a
//     loopback endpoint or null; it never prints its credential.
//   EventStreamReader: the long-lived reader the tray and notifications share.
//     attach(launch) opens a stream (replacing any old one at once), detach()
//     closes it, close() stops for good. It reconnects with a backoff from
//     1 s to 30 s, starts beyond the latest event and resumes after the last
//     event seen. It forwards change notices to `onChange` and full `notice`
//     events to `onNotice`; `onOpen` and `onClosed` mark a stream coming up
//     and going down.
//   stream(...): one connection's worth of events (what the reader loops on).
//   appUrl(endpoint): the daemon's app path as an absolute URL.
//   backoff(attempt), NO_HISTORY, READ_TIMEOUT_MS.
import { type ClientRequest, request as httpRequest } from "node:http";
import { inspect } from "node:util";
import { address, type Launch } from "./client.ts";

/** The daemon pings every 20 s. A stream silent for this long is dead. */
export const READ_TIMEOUT_MS = 70_000;

const CONNECT_TIMEOUT_MS = 5_000;
const MAX_LINE = 64 << 10;
/** A cursor no event reaches: the daemon answers with a `reset` carrying the
 *  latest id and then sends only new events, so history is never replayed as
 *  news. Event ids are unsigned 64-bit numbers, hence bigint. */
export const NO_HISTORY = 2n ** 64n - 1n;

const BACKOFF_FIRST_MS = 1_000;
const BACKOFF_MAX_MS = 30_000;

function isLoopback(host: string): boolean {
  const bare = host.startsWith("[") ? host.slice(1, -1) : host;
  return /^127\.\d+\.\d+\.\d+$/.test(bare) || bare === "::1";
}

/** A daemon's app transport and the credential for it. */
export class Endpoint {
  readonly authority: string;
  readonly host: string;
  readonly port: number;
  readonly #token: string;

  private constructor(authority: string, host: string, port: number, token: string) {
    this.authority = authority;
    this.host = host;
    this.port = port;
    this.#token = token;
  }

  static fromLaunch(launch: Launch): Endpoint | null {
    const { url } = launch;
    if (url.port === "" || !url.hash.startsWith("#token=")) return null;
    return Endpoint.create(url.hostname, Number(url.port), url.hash.slice("#token=".length));
  }

  static create(host: string, port: number, token: string): Endpoint | null {
    let url: URL;
    try {
      url = new URL(`http://${host}:${port}/`);
    } catch {
      return null;
    }
    const addr = address(url);
    // The credential is only ever sent to this machine.
    if (addr === null || !isLoopback(addr.host)) return null;
    return new Endpoint(addr.authority, addr.host, addr.port, token);
  }

  get origin(): string {
    return new URL(`http://${this.authority}/`).origin;
  }

  /** The credential, for an `Authorization` header only. Never log it. */
  bearer(): string {
    return this.#token;
  }

  toString(): string {
    return `Endpoint(${this.authority})`;
  }

  toJSON(): { authority: string } {
    return { authority: this.authority };
  }

  [inspect.custom](): string {
    return this.toString();
  }
}

/** One server-sent event. */
export interface SseEvent {
  id: bigint | null;
  name: string;
  data: string;
}

/** Items shared by the tray and notification consumers. */
export type StreamItem = { kind: "open" } | { kind: "event"; event: SseEvent } | { kind: "ping" };

/** The id of the last event seen, which a reconnect resumes after. */
export interface Cursor {
  last: bigint;
}

/** Interrupts a blocked read without a polling timer. */
export class Control {
  #request: ClientRequest | null = null;

  cancel(): void {
    const request = this.#request;
    this.#request = null;
    request?.destroy();
  }

  track(request: ClientRequest): void {
    this.#request = request;
  }

  release(request: ClientRequest): void {
    if (this.#request === request) this.#request = null;
  }
}

function asError(e: unknown): Error {
  return e instanceof Error ? e : new Error(String(e));
}

/** `u64::from_str`: digits with an optional plus, within 64 bits. */
function parseId(value: string): bigint | null {
  if (!/^\+?[0-9]+$/.test(value)) return null;
  const id = BigInt(value);
  return id <= NO_HISTORY ? id : null;
}

/** One connection's worth of events. Resolves when `stop` asked the stream to
 *  end and rejects when the connection ends otherwise. */
export function stream(
  endpoint: Endpoint,
  cursor: Cursor,
  timeoutMs: number,
  stop: () => boolean,
  control: Control,
  onItem: (item: StreamItem) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    let done = false;
    const req = httpRequest({
      host: endpoint.host,
      port: endpoint.port,
      method: "GET",
      path: "/api/events",
      headers: {
        Host: endpoint.authority,
        Authorization: `Bearer ${endpoint.bearer()}`,
        Accept: "text/event-stream",
        "Last-Event-ID": String(cursor.last),
        Connection: "close",
      },
      agent: false,
      timeout: CONNECT_TIMEOUT_MS,
    });
    const finish = (error?: Error) => {
      if (done) return;
      done = true;
      control.release(req);
      req.destroy();
      if (error === undefined) resolve();
      else reject(error);
    };
    control.track(req);
    req.on("timeout", () => req.destroy(new Error("timed out")));
    req.on("error", finish);
    req.on("socket", (socket) => {
      const connected = () => {
        if (stop()) {
          finish();
          return;
        }
        req.setTimeout(timeoutMs);
      };
      if (socket.connecting) socket.once("connect", connected);
      else connected();
    });
    req.on("response", (res) => {
      if (done) return;
      if (stop()) {
        finish();
        return;
      }
      if (res.statusCode !== 200) {
        const status = `HTTP/${res.httpVersion} ${res.statusCode} ${res.statusMessage}`;
        finish(new Error(`the daemon answered ${JSON.stringify(status)}`));
        return;
      }
      let pending: Buffer = Buffer.alloc(0);
      let id: bigint | null = null;
      let name = "";
      let data = "";
      let dataBytes = 0;

      const line = (text: string) => {
        if (text === "") {
          if (name !== "" || data !== "") {
            const event: SseEvent = { id, name: name === "" ? "message" : name, data };
            if (event.id !== null) cursor.last = event.id;
            onItem({ kind: "event", event });
          }
          [id, name, data, dataBytes] = [null, "", "", 0];
          return;
        }
        if (text.startsWith(":")) {
          onItem({ kind: "ping" });
          return;
        }
        const colon = text.indexOf(":");
        const field = colon < 0 ? text : text.slice(0, colon);
        let value = colon < 0 ? "" : text.slice(colon + 1);
        if (value.startsWith(" ")) value = value.slice(1);
        switch (field) {
          case "id":
            id = parseId(value);
            break;
          case "event":
            name = value;
            break;
          case "data":
            if (data !== "") {
              data += "\n";
              dataBytes += 1;
            }
            data += value;
            dataBytes += Buffer.byteLength(value);
            break;
        }
        if (dataBytes > MAX_LINE) throw new Error("event too large");
      };

      // Decode only complete lines, including multibyte characters split
      // across socket reads.
      const consume = (chunk: Buffer) => {
        pending = Buffer.concat([pending, chunk]);
        for (;;) {
          if (stop()) {
            finish();
            return;
          }
          const newline = pending.indexOf(0x0a);
          // A peer that never sends a newline cannot grow the buffer past this.
          if (newline < 0) {
            if (pending.length >= MAX_LINE) throw new Error("event line too long");
            return;
          }
          if (newline + 1 > MAX_LINE) throw new Error("event line too long");
          const raw = pending.subarray(0, newline + 1);
          pending = pending.subarray(newline + 1);
          if (stop()) {
            finish();
            return;
          }
          line(raw.toString("utf8").replace(/[\r\n]+$/, ""));
          if (done) return;
        }
      };

      res.on("data", (chunk: Buffer) => {
        if (done) return;
        try {
          consume(chunk);
        } catch (e) {
          finish(asError(e));
        }
      });
      res.on("error", finish);
      res.on("close", () => finish(new Error("unexpected end of stream")));
      try {
        onItem({ kind: "open" });
      } catch (e) {
        finish(asError(e));
      }
    });
    req.end();
  });
}

/** The daemon's app path, `/s/<secret>/`, as an absolute URL. The window's
 *  session cookie is scoped to it; the exchange hands out the path again and
 *  changes nothing else. */
export function appUrl(endpoint: Endpoint): Promise<URL> {
  const body = Buffer.from(JSON.stringify({ token: endpoint.bearer() }), "utf8");
  return new Promise((resolve, reject) => {
    let done = false;
    const fail = (e: unknown) => {
      if (done) return;
      done = true;
      reject(asError(e));
    };
    const req = httpRequest({
      host: endpoint.host,
      port: endpoint.port,
      method: "POST",
      path: "/api/auth/exchange",
      headers: {
        Host: endpoint.authority,
        Origin: `http://${endpoint.authority}`,
        "Content-Type": "application/json",
        "Content-Length": String(body.length),
        Connection: "close",
      },
      agent: false,
      timeout: CONNECT_TIMEOUT_MS,
    });
    req.on("timeout", () => req.destroy(new Error("timed out")));
    req.on("error", fail);
    req.on("response", (res) => {
      const chunks: Buffer[] = [];
      let size = 0;
      const finish = () => {
        if (done) return;
        if (res.statusCode !== 200) {
          fail(new Error("the daemon refused the credential"));
          return;
        }
        try {
          done = true;
          resolve(exchanged(endpoint, Buffer.concat(chunks).toString("utf8")));
        } catch (e) {
          done = false;
          fail(e);
        }
      };
      res.on("data", (chunk: Buffer) => {
        chunks.push(chunk.subarray(0, 8192 - size));
        size += chunk.length;
        if (size >= 8192) {
          finish();
          req.destroy();
        }
      });
      res.on("end", finish);
      res.on("error", fail);
    });
    req.end(body);
  });
}

function exchanged(endpoint: Endpoint, text: string): URL {
  let path: unknown;
  try {
    path = (JSON.parse(text) as { path?: unknown } | null)?.path;
  } catch (e) {
    throw new Error(`unreadable exchange: ${asError(e).message}`);
  }
  if (typeof path !== "string") throw new Error("unreadable exchange: missing field `path`");
  const url = new URL(path, `http://${endpoint.authority}/`);
  const namespaced = url.pathname.startsWith("/s/") && url.pathname.endsWith("/");
  const query = (url.href.split("#")[0] ?? "").includes("?");
  if (url.origin !== endpoint.origin || !namespaced || query) {
    throw new Error("the daemon named an unexpected app path");
  }
  return url;
}

/** The wait before reconnecting after the `attempt`th failure in a row. */
export function backoff(attempt: number): number {
  return Math.min(BACKOFF_FIRST_MS * 2 ** Math.min(attempt, 8), BACKOFF_MAX_MS);
}

/** The clock the reader sleeps on; tests supply their own. */
export interface Timers {
  setTimeout(callback: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

export interface ReaderOptions {
  timers?: Timers;
  readTimeoutMs?: number;
  log?: (message: string) => void;
  /** A connection (also a re-connection) is up: read what was missed. */
  onOpen?: () => void;
  /** A stream that was up has ended. */
  onClosed?: () => void;
  /** Any event but `notice`, which only announces that something changed. */
  onChange?: (event: SseEvent) => void;
  /** A full `notice` payload, for the notification worker. */
  onNotice?: (endpoint: Endpoint, event: SseEvent) => void;
}

/** Keeps the daemon's event stream open while a daemon is attached. It
 *  reconnects with backoff, and resumes after the last event it saw unless
 *  the daemon was replaced, which starts beyond the latest event again. */
export class EventStreamReader {
  readonly #options: ReaderOptions;
  readonly #timers: Timers;
  readonly #control = new Control();
  #launch: Launch | null = null;
  #generation = 0;
  #closed = false;
  #kickPending = false;
  #waiter: (() => void) | null = null;

  constructor(options: ReaderOptions = {}) {
    this.#options = options;
    this.#timers = options.timers ?? {
      setTimeout: (callback, ms) => setTimeout(callback, ms),
      clearTimeout: (handle) => clearTimeout(handle as NodeJS.Timeout),
    };
    void this.#run();
  }

  /** Reads this daemon's events, closing the old stream at once. */
  attach(launch: Launch): void {
    this.#launch = launch;
    this.#generation += 1;
    this.#control.cancel();
    this.#kick();
  }

  /** Closes the stream at once and reads nothing until the next attach. */
  detach(): void {
    this.#launch = null;
    this.#control.cancel();
  }

  /** Stops for good. */
  close(): void {
    this.#closed = true;
    this.#launch = null;
    this.#control.cancel();
    this.#kick();
  }

  #kick(): void {
    const waiter = this.#waiter;
    if (waiter === null) this.#kickPending = true;
    else waiter();
  }

  /** Waits for `ms` (forever if null), or until kicked. */
  #wait(ms: number | null): Promise<void> {
    if (this.#kickPending) {
      this.#kickPending = false;
      return Promise.resolve();
    }
    return new Promise((resolve) => {
      let handle: unknown;
      const wake = () => {
        this.#waiter = null;
        if (handle !== undefined) this.#timers.clearTimeout(handle);
        resolve();
      };
      this.#waiter = wake;
      if (ms !== null) handle = this.#timers.setTimeout(wake, ms);
    });
  }

  async #run(): Promise<void> {
    const log = this.#options.log ?? ((message) => console.error(message));
    const cursor: Cursor = { last: NO_HISTORY };
    let cursorGeneration = -1;
    let attempt = 0;
    while (!this.#closed) {
      const launch = this.#launch;
      const generation = this.#generation;
      if (launch === null) {
        attempt = 0;
        await this.#wait(null);
        continue;
      }
      if (cursorGeneration !== generation) {
        cursorGeneration = generation;
        cursor.last = NO_HISTORY;
      }
      const stop = () => this.#closed || this.#generation !== generation || this.#launch === null;
      const endpoint = Endpoint.fromLaunch(launch);
      let up = false;
      let failure: Error | null = null;
      try {
        if (endpoint === null) throw new Error("the daemon's address is unusable");
        await stream(
          endpoint,
          cursor,
          this.#options.readTimeoutMs ?? READ_TIMEOUT_MS,
          stop,
          this.#control,
          (item) => {
            if (item.kind === "open") {
              attempt = 0;
              up = true;
              this.#options.onOpen?.();
            } else if (item.kind === "event") {
              if (item.event.name === "notice") this.#options.onNotice?.(endpoint, item.event);
              else this.#options.onChange?.(item.event);
            }
          },
        );
      } catch (e) {
        failure = asError(e);
      }
      if (up) this.#options.onClosed?.();
      if (failure !== null && attempt > 0) {
        log(`baum desktop: event stream unavailable: ${failure.message}`);
      }
      if (this.#generation !== generation) {
        attempt = 0;
        continue;
      }
      const wait = backoff(attempt);
      attempt += 1;
      await this.#wait(wait);
    }
  }
}
