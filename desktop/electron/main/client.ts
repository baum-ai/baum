// The tray's requests to the attached daemon's app API, over loopback HTTP.
//
// They authenticate like the health check: the launch capability from
// `credential.json` as a bearer token, which the daemon accepts on `/api/`
// routes only and for browsers never sends ambiently. The app holds that
// capability already (the window loads it), so this adds no privilege. A
// request that changes state also names the daemon's own origin and sends
// JSON, which the daemon requires of every non-GET request. The capability
// is only ever sent in an `Authorization` header, never in a URL or a log.
//
// Every request sends `Connection: close` and reads the body to the end.
//
// API (every function rejects with an `Error` whose message is safe to show):
//   request(launch, method, path, json, timeoutMs) -> { status, body }
//   get(launch, path, timeoutMs) -> body of a 200 answer
//   jobs, waiting, automatic, planUsage(refresh), liveSessions, diagnostics
//     -> the raw JSON body for the tray to parse
//   setAutomatic(launch, enabled), jobSeen(launch, id), catchUp(launch)
//   POLL_TIMEOUT_MS, DIAGNOSTICS_TIMEOUT_MS, PLAN_USAGE_TIMEOUT_MS
import { request as httpRequest } from "node:http";
import { isIP } from "node:net";

/** The daemon the app is attached to. `url` holds the credential in its
 *  fragment and `bearer()` returns the capability: never log either. */
export interface Launch {
  readonly url: URL;
  bearer(): string;
}

/** Large enough for a long job journal, small enough to bound memory. */
const MAX_BODY = 16 << 20;

export const POLL_TIMEOUT_MS = 5_000;
/** Diagnostics run the doctor's checks; the daemon gives them up to 45 s. */
export const DIAGNOSTICS_TIMEOUT_MS = 50_000;
/** A plan usage reading may ask Claude's usage endpoint first. */
export const PLAN_USAGE_TIMEOUT_MS = 20_000;

export interface Response {
  status: number;
  body: Buffer;
}

/** The socket address a URL names, as Node connects to it and as the Host
 *  and Origin headers spell it; null unless it is an IP literal with a port. */
export function address(url: URL): { host: string; port: number; authority: string } | null {
  const host = url.hostname;
  const bare = host.startsWith("[") ? host.slice(1, -1) : host;
  if (url.port === "" || isIP(bare) === 0) return null;
  return { host: bare, port: Number(url.port), authority: `${host}:${url.port}` };
}

/** One request with a deadline per connect, read and write. */
export function request(
  launch: Launch,
  method: string,
  path: string,
  json: string | null,
  timeoutMs: number,
): Promise<Response> {
  const addr = address(launch.url);
  if (addr === null) return Promise.reject(new Error("the daemon's address is unusable"));
  const headers: Record<string, string> = {
    Host: addr.authority,
    Authorization: `Bearer ${launch.bearer()}`,
    Connection: "close",
  };
  const body = json === null ? null : Buffer.from(json, "utf8");
  if (body !== null) {
    headers.Origin = `http://${addr.authority}`;
    headers["Content-Type"] = "application/json";
    headers["Content-Length"] = String(body.length);
  }
  return new Promise((resolve, reject) => {
    let settled = false;
    const fail = (e: unknown) => {
      if (settled) return;
      settled = true;
      const reason = e instanceof Error ? e.message : String(e);
      reject(new Error(`cannot reach the baum daemon: ${reason}`));
    };
    const req = httpRequest({
      host: addr.host,
      port: addr.port,
      method,
      path,
      headers,
      agent: false,
      timeout: timeoutMs,
    });
    req.on("timeout", () => req.destroy(new Error("timed out")));
    req.on("error", fail);
    req.on("response", (res) => {
      const chunks: Buffer[] = [];
      let size = 0;
      const finish = () => {
        if (settled) return;
        settled = true;
        resolve({ status: res.statusCode ?? 0, body: Buffer.concat(chunks) });
      };
      res.on("data", (chunk: Buffer) => {
        chunks.push(chunk.subarray(0, MAX_BODY - size));
        size += chunk.length;
        if (size >= MAX_BODY) {
          finish();
          req.destroy();
        }
      });
      res.on("end", finish);
      res.on("error", fail);
      res.on("aborted", () => fail(new Error("connection closed early")));
    });
    req.end(body);
  });
}

/** A GET whose answer must be 200; resolves to its body. */
export async function get(launch: Launch, path: string, timeoutMs: number): Promise<Buffer> {
  const response = await request(launch, "GET", path, null, timeoutMs);
  if (response.status !== 200) throw new Error(`${path} answered ${response.status}`);
  return response.body;
}

export function jobs(launch: Launch): Promise<Buffer> {
  return get(launch, "/api/jobs", POLL_TIMEOUT_MS);
}

/** How many pull requests wait on the user, which the daemon counts so the
 *  shell need not download the whole index every few seconds. */
export function waiting(launch: Launch): Promise<Buffer> {
  return get(launch, "/api/index?view=waiting", POLL_TIMEOUT_MS);
}

export function automatic(launch: Launch): Promise<Buffer> {
  return get(launch, "/api/automatic", POLL_TIMEOUT_MS);
}

/** What remains of the agents' plans. Unforced, the daemon serves its cached
 *  reading (up to five minutes old); `refresh` asks for a newer one, which it
 *  reads at most once a minute. */
export function planUsage(launch: Launch, refresh: boolean): Promise<Buffer> {
  const path = refresh ? "/api/plan-usage?refresh=1" : "/api/plan-usage";
  return get(launch, path, PLAN_USAGE_TIMEOUT_MS);
}

/** The agent sessions in progress, for those waiting on the user. */
export function liveSessions(launch: Launch): Promise<Buffer> {
  return get(launch, "/api/live-sessions", POLL_TIMEOUT_MS);
}

export function diagnostics(launch: Launch): Promise<Buffer> {
  return get(launch, "/api/diagnostics", DIAGNOSTICS_TIMEOUT_MS);
}

async function expectStatus(
  launch: Launch,
  method: string,
  path: string,
  json: string,
  status: number,
): Promise<void> {
  const response = await request(launch, method, path, json, POLL_TIMEOUT_MS);
  if (response.status !== status) throw new Error(`${path} answered ${response.status}`);
}

/** Sets the global automatic generation switch, which the daemon treats as the
 *  master switch: off cancels queued automatic jobs and stops new ones. */
export function setAutomatic(launch: Launch, enabled: boolean): Promise<void> {
  return expectStatus(launch, "PUT", "/api/automatic", JSON.stringify({ enabled }), 200);
}

/** Records in the daemon that the user has seen a failed job, so no client
 *  flags it again. */
export async function jobSeen(launch: Launch, id: string): Promise<void> {
  if (!/^[A-Za-z0-9_-]+$/.test(id)) throw new Error(`not a job id: ${JSON.stringify(id)}`);
  await expectStatus(launch, "POST", `/api/jobs/${id}/seen`, "{}", 200);
}

/** Asks the daemon to queue automatic reviews of the latest pull requests
 *  open when setup finished; it answers before it polls the repositories. */
export function catchUp(launch: Launch): Promise<void> {
  return expectStatus(launch, "POST", "/api/automatic/catch-up", "{}", 202);
}
