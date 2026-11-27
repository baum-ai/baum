// A tile that spins before it says hello, checked end to end by the app's
// own render check code: the main page's check host (oss/app's
// `check-host.ts`) opens the check window on a `tile_check` event, the check
// page's runner (`check-runner.tsx`, as `?page=tile-check` runs it) claims
// the job and draws the tile's frame with the real kit (the bootstrap the
// frame route inlines), and the frame spins in a process of its own while the
// check page runs on. The runner must find the frame unresponsive and report
// a hang, the mock daemon then sends `hung` as the daemon does, and the host
// must close the check window, which ends the spinning process, while the
// main window never stalls. The app's modules are bundled from source with
// React and `api.ts` stubbed (the runner uses only `appURL` from it); the
// mock daemon implements the runner's routes and the event stream.
import { readFileSync } from "node:fs";
import type http from "node:http";
import { resolve } from "node:path";
import { expect, test } from "@playwright/test";
import { build } from "esbuild";
import {
  type Contents,
  contents,
  CpuSampler,
  cpuTicks,
  DAEMON_CSP,
  FRAME_CSP,
  launch,
  MockDaemon,
  maxGap,
  NAMESPACE,
  type Shell,
  sleep,
  waitFor,
} from "./harness.ts";

const APP = resolve(import.meta.dirname, "../../../app/src");
const KIT = resolve(import.meta.dirname, "../../../tiles/kit/testdata/v1.inlined.js");

const daemon = new MockDaemon();
let shell: Shell;

/** The modules the bundles stub: React (the check page's component is not
 *  rendered; `drain` is what it runs) and `api.ts`, whose `appURL` builds
 *  every URL under the namespace. */
const STUBS: Record<string, string> = {
  react: `export const useEffect = () => {}, useRef = () => ({ current: null }),
    useState = (v) => [v, () => {}], useSyncExternalStore = () => undefined;
    export const jsx = () => null, jsxs = () => null, Fragment = null;`,
  api: `export function appURL(path = "") {
      return (/^\\/s\\/[^/]+\\//.exec(location.pathname)?.[0] ?? "/") + path;
    }
    export function tilesAnswer() { throw new Error("not in the harness"); }`,
};

async function bundle(contents: string): Promise<string> {
  const result = await build({
    stdin: { contents, resolveDir: APP, loader: "ts", sourcefile: "entry.ts" },
    bundle: true,
    write: false,
    format: "iife",
    platform: "browser",
    target: "es2022",
    logLevel: "silent",
    plugins: [
      {
        name: "stubs",
        setup(b) {
          b.onResolve({ filter: /^react(\/.*)?$/ }, () => ({ path: "react", namespace: "stub" }));
          b.onResolve({ filter: /^(\.\.\/)+api$/ }, () => ({ path: "api", namespace: "stub" }));
          b.onLoad({ filter: /.*/, namespace: "stub" }, (args) => ({
            contents: STUBS[args.path],
            loader: "js",
          }));
        },
      },
    ],
  });
  return result.outputFiles[0]?.text ?? "";
}

/** The main page: the app's check host on the daemon's event stream, as
 *  `app.tsx` wires it, and a 100 ms heartbeat. */
const HOST = `
import { checkWaitingJobs, onTileCheckEvent } from "./tiles/custom/check-host.ts";
const source = new EventSource("${NAMESPACE}api/events");
source.addEventListener("tile_check", (event) => onTileCheckEvent(event.data));
void checkWaitingJobs();
setInterval(() => fetch("${NAMESPACE}beat?win=main", { method: "POST" }).catch(() => {}), 100);`;

/** The check page: the runner as \`TileCheckPage\` runs it, and a heartbeat. */
const RUNNER = `
import { drain } from "./tiles/custom/check-runner.tsx";
const stage = document.querySelector("[data-tile-check-stage]");
void drain(stage, { wait: 25, forever: true });
setInterval(() => fetch("${NAMESPACE}beat?win=check", { method: "POST" }).catch(() => {}), 250);`;

const DIGEST = `sha256:${"ab".repeat(32)}`;
const JOB = {
  id: "chk_spin",
  kind: "preflight",
  digest: DIGEST,
  kit: 1,
  title: "Spinner",
  sizes: ["half"],
  samples: [{ name: "data", kind: "data", data: {}, refs: { glance: {}, detail: {} } }],
};

/** The frame document: the kit's bootstrap in the head, as the frame route
 *  composes it, then a script that never yields, before the kit can say
 *  hello. */
const frameDocument = () =>
  `<!doctype html><html lang="en" data-theme="light" data-face=""><head><meta charset="utf-8"><script id="baum-kit-script">${readFileSync(KIT, "utf8")}</script></head><body><div id="baum-root"></div><script>for (;;) {}</script></body></html>`;

/** The mock daemon's side of the render check. */
const queue: (typeof JOB)[] = [];
let running: string | null = null;
const results: { id: string; at: number; body: { problems: { code: string }[] } }[] = [];
const alive: number[] = [];
const streams = new Set<http.ServerResponse>();
const events: { id: string; state: string; at: number }[] = [];
function publish(id: string, state: string) {
  events.push({ id, state, at: Date.now() });
  for (const stream of streams)
    stream.write(`event: tile_check\ndata: ${JSON.stringify({ id, state })}\n\n`);
}

test.beforeAll(async () => {
  await daemon.listen();
  const [host, runner] = await Promise.all([bundle(HOST), bundle(RUNNER)]);
  daemon.pages.set(NAMESPACE, (url) =>
    url.searchParams.get("page") === "tile-check"
      ? {
          csp: DAEMON_CSP,
          body: `<!doctype html><main><p>Checking tiles</p><div data-tile-check-stage style="position:fixed;inset:0;opacity:0;pointer-events:none;z-index:-1;overflow:hidden;display:flex;flex-wrap:wrap;align-items:flex-start;gap:8px"></div></main><script src="${NAMESPACE}runner.js"></script>`,
        }
      : {
          csp: DAEMON_CSP,
          body: `<!doctype html><p>main window</p><script src="${NAMESPACE}host.js"></script>`,
        },
  );
  daemon.pages.set(`${NAMESPACE}host.js`, { type: "text/javascript", body: host });
  daemon.pages.set(`${NAMESPACE}runner.js`, { type: "text/javascript", body: runner });
  daemon.pages.set(`${NAMESPACE}api/events`, {
    type: "text/event-stream",
    stream: (response) => {
      streams.add(response);
      response.on("close", () => streams.delete(response));
      response.write(": hello\n\n");
    },
  });
  daemon.pages.set(`${NAMESPACE}api/tile-checks/state`, () => ({
    type: "application/json",
    body: JSON.stringify({ waiting: queue.length, running: running !== null }),
  }));
  daemon.pages.set(`${NAMESPACE}api/tile-checks/next`, async () => {
    // A long poll, held for up to 2 seconds.
    for (let i = 0; i < 40 && (queue.length === 0 || running !== null); i++) await sleep(50);
    const job = running === null ? queue.shift() : undefined;
    if (!job) return { status: 204 };
    running = job.id;
    publish(job.id, "running");
    return { type: "application/json", body: JSON.stringify(job) };
  });
  daemon.route = ((route) => (url, body, headers) => {
    const check = /^\/s\/ns\/api\/tile-checks\/([^/]+)\/(alive|result)$/.exec(url.pathname);
    if (!check) return route(url, body, headers);
    if (check[1] !== running) return { status: 404 };
    if (check[2] === "alive") {
      alive.push(Date.now());
      return { status: 204 };
    }
    // As the daemon's Report: a reported hang ends the job as one, and the
    // page is told.
    const report = JSON.parse(body) as (typeof results)[number]["body"];
    results.push({ id: running, at: Date.now(), body: report });
    publish(running, report.problems.some((p) => p.code === "hang") ? "hung" : "done");
    running = null;
    return { status: 204 };
  })(daemon.route);
  daemon.pages.set(`/tile-frames/${DIGEST.slice("sha256:".length)}`, () => ({
    csp: FRAME_CSP,
    body: frameDocument(),
  }));
  shell = await launch(daemon);
  await shell.main.waitForURL((url) => url.href.startsWith(daemon.url()), { timeout: 60_000 });
});

test.afterAll(async () => {
  for (const stream of streams) stream.end();
  await shell?.close();
  await daemon.close();
});

const isCheck = (c: Contents) => c.url.includes("page=tile-check");

test("a tile that spins before its hello is reported as a hang and its window closed", async () => {
  const numbers: Record<string, unknown> = {};
  await waitFor("the main window's heartbeat", 30_000, () => daemon.beatsOf("main").length > 5);
  await waitFor("the main page's event stream", 15_000, () => streams.size > 0);

  const queued = Date.now();
  queue.push(JOB);
  publish(JOB.id, "queued");

  // The host opens the check window; its runner claims the job and mounts
  // the tile's frame, which spins in a process of its own.
  const spinning = await waitFor("the tile's frame in the check window", 30_000, async () => {
    const check = (await contents(shell.app)).find(isCheck);
    return check?.frames.find((f) => f.url.includes("/tile-frames/"));
  });
  const check = (await contents(shell.app)).find(isCheck) as Contents;
  numbers.processes = { checkPage: check.pid, tileFrame: spinning.pid };
  expect(spinning.pid, "the tile runs in the check page's process").not.toBe(check.pid);
  const sampler = new CpuSampler(spinning.pid);
  try {
    await waitFor("the tile's process at full CPU", 15_000, () =>
      sampler.samples.slice(-3).some((s) => (s.cpu ?? 0) >= 80),
    );
    const spinFrom = Date.now();

    // The runner reports a hang, not a timeout: no hello, no answer to its
    // ping, and the document never loaded.
    const result = await waitFor("the runner's report", 30_000, () => results[0]);
    numbers.queuedToReportMs = result.at - queued;
    numbers.reported = result.body.problems.map((p) => p.code);
    expect(result.id).toBe(JOB.id);
    expect(numbers.reported).toContain("hang");
    expect(numbers.reported).not.toContain("no_hello");
    expect(numbers.reported).not.toContain("timeout");
    // The check page kept running while its frame spun.
    numbers.checkBeatsWhileSpinning = daemon
      .beatsOf("check")
      .filter((t) => t > spinFrom && t <= result.at).length;
    expect(numbers.checkBeatsWhileSpinning as number).toBeGreaterThan(0);

    // `hung` reaches the host, which closes the check window: the spinning
    // process stops within a second and is gone a second later.
    await waitFor("the check window to close", 5_000, async () => {
      return !(await contents(shell.app)).some(isCheck);
    });
    numbers.reportToClosedMs = Date.now() - result.at;
    await sleep(2000);
    const after = sampler.samples.filter((s) => s.t > result.at);
    const quiet = (s: { cpu: number | null }) => s.cpu === null || s.cpu < 10;
    const from = after.findIndex((_, i) => after.slice(i).every(quiet));
    expect(from, "the spinning process kept running").toBeGreaterThanOrEqual(0);
    numbers.reportToCpuDropMs = (after[from]?.t ?? Number.POSITIVE_INFINITY) - result.at;
    expect(numbers.reportToCpuDropMs as number).toBeLessThanOrEqual(2000);
    numbers.spinningProcessGone = cpuTicks(spinning.pid) === null;
    expect(numbers.spinningProcessGone).toBe(true);
    expect(shell.logs.some((l) => l.includes("close_check_window"))).toBe(true);
  } finally {
    sampler.stop();
  }
  // The main window never stalled.
  numbers.mainMaxBeatGapMs = maxGap(daemon.beatsOf("main"), queued, Date.now());
  expect(numbers.mainMaxBeatGapMs as number).toBeLessThan(1000);
  numbers.events = events.map((e) => e.state);
  console.log(JSON.stringify(numbers, null, 2));
});
