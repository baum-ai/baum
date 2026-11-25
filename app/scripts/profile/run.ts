// Runs one scenario (scenarios.ts) against the Electron desktop app through
// Electron's chromedriver: a warm-up, then N runs, reading window.__baumPerf
// and recording a CPU profile (electron.ts) in each, and writes metrics.json
// and summary.md. `just desktop::profile` (oss/desktop/scripts/profile.sh)
// runs it inside the headless isolated container; see the desktop README's
// Profiling section.
// With --side, it is one side of a comparison (profile.sh --baseline): two
// runners, one per side in containers of their own, take turns through
// files in --out (compare.ts's schedule), and head's writes the comparison.
// usage: node run.ts <scenario> --app DIR --out DIR [--runs N] [--dist DIR]
//        [--side base|head] [--headline METRIC]
import { spawn } from "node:child_process";
import {
  createWriteStream,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  writeFileSync,
} from "node:fs";
import { cpus, homedir, loadavg } from "node:os";
import { dirname, join } from "node:path";
import { parseArgs } from "node:util";
import type { Navigation, Snapshot } from "../../src/perf-collector.ts";
import { readChunks } from "./bundle.ts";
import {
  comparisonJson,
  daemonFailure,
  schedule,
  summariseComparison,
  type Side,
} from "./compare.ts";
import {
  CHROMEDRIVER,
  CHROMEDRIVER_ARGS,
  CpuRecorder,
  electron,
  launchElectron,
} from "./electron.ts";
import { scenarios, Skip, type Context } from "./scenarios.ts";
import { standardMetrics, summarise, type Profile, type Run } from "./summary.ts";
import { waitForDriver, WebDriverError, type Session } from "./webdriver.ts";

const { values, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    app: { type: "string" },
    out: { type: "string" },
    runs: { type: "string", default: "5" },
    dist: { type: "string" },
    port: { type: "string", default: "4444" },
    "run-timeout": { type: "string", default: "600" },
    env: { type: "string", multiple: true, default: [] },
    side: { type: "string" },
    headline: { type: "string" },
  },
});
const name = positionals[0] ?? "";
const scenario = scenarios.find((s) => s.name === name);
if (!scenario || !values.out || !values.app) {
  console.error(
    `usage: run.ts <scenario> --app DIR --out DIR [--runs N]\nscenarios: ${scenarios.map((s) => s.name).join(", ")}`,
  );
  process.exit(2);
}
const runs = Number(values.runs);
if (!Number.isInteger(runs) || runs < 1) {
  console.error(`profile: --runs must be a whole number from 1: ${values.runs}`);
  process.exit(2);
}
const runTimeout = Number(values["run-timeout"]) * 1000;
if (values.side !== undefined && values.side !== "base" && values.side !== "head") {
  console.error(`profile: --side is base or head: ${values.side}`);
  process.exit(2);
}
const side = values.side as Side | undefined;
const headline = values.headline ?? scenario.headline;
if (side && !headline) {
  console.error(
    `profile: ${scenario.name} has no headline yet, so it cannot be compared; --headline names one`,
  );
  process.exit(2);
}
/** A comparison's directory, shared by both sides' containers. */
const shared = values.out;
/** This side's metrics.json and summary.md. */
const out = side ? join(shared, side) : shared;
mkdirSync(out, { recursive: true });
const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
const log = (text: string) => console.log(`profile: ${text}`);

// The instance is set up already (isolated.sh), so the app opens on the
// connecting screen and the daemon's page rather than first-run setup. A
// seeded home's desktop.json says so already and is left as it is; in
// another, only the flag is added.
const desktopConfig = join(homedir(), ".config/com.usebaum.desktop/desktop.json");
{
  const saved = existsSync(desktopConfig)
    ? (JSON.parse(readFileSync(desktopConfig, "utf8")) as Record<string, Record<string, unknown>>)
    : {};
  if (saved.setup?.onboarded !== true) {
    mkdirSync(dirname(desktopConfig), { recursive: true });
    writeFileSync(
      desktopConfig,
      JSON.stringify({ ...saved, setup: { ...saved.setup, onboarded: true } }, null, 2),
    );
  }
}

const driverLog = createWriteStream(join(out, "driver.log"));
const cpu = new CpuRecorder(out, values.dist);
if (!existsSync(CHROMEDRIVER)) {
  console.error(`profile: ${CHROMEDRIVER} is not in this image; run through just desktop::profile`);
  process.exit(2);
}
const driver = spawn(CHROMEDRIVER, [`--port=${values.port}`, ...CHROMEDRIVER_ARGS], {
  stdio: ["ignore", "pipe", "pipe"],
});
driver.on("error", (error) => {
  console.error(`profile: the WebDriver server did not start: ${error.message}`);
  process.exit(1);
});
/** The daemon's capability, in a launch URL's fragment, is never logged. */
const scrub = (text: string) => text.replace(/#token=[^\s"'\\]+/g, "#token=…");
for (const stream of [driver.stdout, driver.stderr])
  stream.on("data", (chunk: Buffer) => driverLog.write(scrub(chunk.toString())));
const driverBase = `http://127.0.0.1:${values.port}`;

let session: Session | undefined;
let base = "";

const page = () => {
  if (!session) throw new Error("the app is not running");
  return session;
};

async function execute<T>(body: string, args: unknown[] = []): Promise<T> {
  const result = await page().executeAsync<{ ok: T } | { error: string }>(
    `const done = arguments[arguments.length - 1];
     (async function () { ${body} }).apply(null, Array.prototype.slice.call(arguments, 0, -1))
       .then((ok) => done({ ok: ok === undefined ? null : ok }), (e) => done({ error: String(e) + (e && e.stack ? " at " + e.stack : "") }));`,
    args,
  );
  if ("error" in result)
    throw new Error(`in the page: ${result.error}\n  running: ${body.trim().slice(0, 200)}`);
  return result.ok;
}

async function waitFor<T>(body: string, what: string, timeout = 30_000, args: unknown[] = []) {
  const found = await execute<T | null>(
    `const end = performance.now() + ${timeout};
     const test = async function () { ${body} };
     const args = arguments;
     for (;;) {
       let value = null;
       try { value = await test.apply(null, args); } catch {}
       if (value) return value;
       if (performance.now() > end) return null;
       await new Promise((resolve) => setTimeout(resolve, 20));
     }`,
    args,
  );
  if (!found) throw new Error(`timed out after ${timeout} ms waiting for ${what}`);
  return found;
}

async function settled(timeout = 30_000): Promise<Navigation> {
  const end = Date.now() + timeout;
  const navigation = await waitFor<Navigation>(
    `const n = window.__baumPerf.progress().navigation;
     return n && (n.settled !== undefined || n.superseded) ? n : null;`,
    "the page to settle",
    timeout,
  ).catch(async () => {
    const n = await execute<Navigation | null>("return window.__baumPerf.progress().navigation;");
    if (!n) throw new Error(`no navigation recorded within ${timeout} ms`);
    return n;
  });
  if (Date.now() > end) log(`the page had not settled after ${timeout} ms`);
  return navigation;
}

/** The app's window is on the daemon's page with the collector running. */
async function onDaemonPage() {
  const url = await page()
    .url()
    .catch(() => "");
  if (!/^https?:\/\/[^/]+\/s\/[^/]+\//.test(url)) return null;
  const perf = await page()
    .executeAsync<boolean>("arguments[0](!!window.__baumPerf);")
    .catch(() => false);
  return perf ? url : null;
}

async function launch() {
  if (session) {
    await session.close().catch(() => {});
    session = undefined;
  }
  const started = performance.now();
  // The wall clock the page's performance.timeOrigin shares.
  const startedAt = Date.now();
  session = await launchElectron(driverBase, values.app!, dirname(desktopConfig), cpu);
  await session.setTimeouts({ script: 600_000 });
  const end = Date.now() + 120_000;
  for (;;) {
    const url = await onDaemonPage();
    if (url) {
      base = url.match(/^https?:\/\/[^/]+\/s\/[^/]+\//)![0];
      return { wall: performance.now() - started, startedAt };
    }
    if (Date.now() > end) {
      const at = await session.url().catch(() => "?");
      if (/^https?:\/\/[^/]+\/s\/[^/]+\//.test(at))
        throw new Error(
          "the daemon's page has no profiling collector: the sidecar holds a build of the " +
            "frontend that is not the profiling build. just desktop::profile builds it; a recipe " +
            "since (check, isolated, build) may have rebuilt it for production",
        );
      let said: string | undefined;
      try {
        said = daemonFailure(
          readFileSync(join(homedir(), ".local/state/baum/daemon/daemon.out"), "utf8"),
          side,
        );
      } catch {}
      throw new Error(
        `the app did not open the daemon's page within two minutes; it shows ${scrub(at.replace(/\/s\/[^/]+/, "/s/…"))}` +
          (said ? `. ${said}` : ""),
      );
    }
    await sleep(50);
  }
}

async function open(search: string) {
  await page().navigate(base + search);
  await waitFor("return !!window.__baumPerf;", "the profiling collector", 60_000);
  const tour = await execute<boolean>(
    `return !!document.querySelector(".tour-card") || location.pathname.includes("/sample/");`,
  );
  if (tour) {
    log("closing the first-run tour");
    await execute(`
      const { revision } = await (await fetch("${base}api/ui-state/tour")).json();
      await fetch("${base}api/ui-state/tour", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ revision, mutation_id: crypto.randomUUID() + ":1", value: { seen: true } }),
      });`);
    await page().navigate(base + search);
    await waitFor("return !!window.__baumPerf;", "the profiling collector", 60_000);
  }
  const navigation = await settled(60_000);
  // Let what the page does after it settled (fonts, images, idle work) end.
  await sleep(500);
  return navigation;
}

async function press(key: string, modifiers: string[] = []) {
  const before = await execute<number>("return window.__baumPerf.progress().interactions;");
  await page().press([key], modifiers);
  // Held modifiers are key presses of their own.
  await waitFor(
    "return window.__baumPerf.progress().interactions >= arguments[0];",
    `the page to record the key ${JSON.stringify(key)}`,
    10_000,
    [before + modifiers.length + 1],
  );
  await sleep(50);
}

/** What the app keeps between runs that a journey can change: the daemon's
 *  records of the Inbox (what was seen today, snoozes) and of typed filters,
 *  and the window's storage (the last inbox, the sidebar, the session
 *  panel). Saved once before the warm-up and put back before every run, so
 *  each starts from the same state; filters always go back to none. */
type Saved = { inbox: unknown; local: Record<string, string> };
let saved: Saved | undefined;

const putRecord = (path: string, value: unknown): Promise<void> =>
  execute<void>(
    `const [path, value] = arguments;
     for (let attempt = 0; attempt < 3; attempt++) {
       const { revision } = await (await fetch(path)).json();
       const written = await fetch(path, {
         method: "PUT",
         headers: { "Content-Type": "application/json" },
         body: JSON.stringify({ revision, mutation_id: crypto.randomUUID() + ":1", value }),
       });
       if (written.ok) return;
       if (written.status !== 409) throw new Error("writing " + path + " answered " + written.status);
     }
     throw new Error(path + " could not be written after three revisions");`,
    [`${base}${path}`, value],
  );

/** A page that holds none of those records, so none is written while they
 *  are put back. */
async function quietPage() {
  await page().navigate(`${base}?page=settings`);
  await waitFor("return !!window.__baumPerf;", "the settings page", 60_000);
}

async function saveState() {
  await quietPage();
  saved = await execute<Saved>(
    `const record = await (await fetch(arguments[0])).json();
     const local = {};
     for (let i = 0; i < localStorage.length; i++) {
       const key = localStorage.key(i);
       local[key] = localStorage.getItem(key);
     }
     return { inbox: record.value, local };`,
    [`${base}api/ui-state/inbox`],
  );
}

async function restoreState() {
  await quietPage();
  await putRecord("api/ui-state/filters", { pages: {}, tabs: [] });
  if (saved?.inbox) await putRecord("api/ui-state/inbox", saved.inbox);
  await execute(
    `localStorage.clear();
     sessionStorage.clear();
     for (const [key, value] of Object.entries(arguments[0])) localStorage.setItem(key, value);`,
    [saved?.local ?? {}],
  );
}

const notes: string[] = [];
const context: Context = {
  launch,
  open,
  execute,
  waitFor,
  settled,
  press,
  clearFilters: () => putRecord("api/ui-state/filters", { pages: {}, tabs: [] }),
  reset: () => execute("window.__baumPerf.reset();"),
  mark: (mark) => execute("window.__baumPerf.mark(arguments[0]);", [mark]),
  sleep,
  note: (text) => {
    if (!notes.includes(text)) notes.push(text);
  },
};

function environment(snapshot: Snapshot) {
  const read = (path: string) => {
    try {
      return readFileSync(path, "utf8").trim();
    } catch {
      return "";
    }
  };
  const entries: Record<string, string> = {};
  for (const item of values.env ?? []) {
    const at = item.indexOf("=");
    if (at > 0) entries[item.slice(0, at)] = item.slice(at + 1);
  }
  // cgroup v2, then v1's cpuset controller.
  entries.CPUs =
    read("/sys/fs/cgroup/cpuset.cpus.effective") ||
    read("/sys/fs/cgroup/cpuset/cpuset.effective_cpus") ||
    "unknown";
  entries["host CPUs"] = String(cpus().length);
  entries.Electron = electron(values.app!).version;
  entries.Chromium =
    cpu.version || (snapshot.userAgent.match(/Chrome\/([\d.]+)/)?.[1] ?? snapshot.userAgent);
  const marker = read(join(homedir(), ".local/state/baum/seeddata.json"));
  if (marker) {
    const seeded = JSON.parse(marker) as { seed?: number; digest?: string };
    entries.data = `seed ${seeded.seed} (digest ${String(seeded.digest).slice(0, 12)})`;
  } else {
    entries.data = "this instance's own (not seeded, so not comparable across machines)";
  }
  return entries;
}

/** `work`, or a clear failure when it hangs, as a stuck driver or app does. */
function limited<T>(work: Promise<T>, what: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () =>
        reject(
          new Error(
            `${what} did not finish within ${runTimeout / 1000} s; chromedriver or the app ` +
              "may be hung (see driver.log)",
          ),
        ),
      runTimeout,
    );
  });
  return Promise.race([work, timeout]).finally(() => clearTimeout(timer));
}

// Taking turns with the other side of a comparison, through files in the
// shared directory: `turn` holds the place in the order whose run may go,
// `failed` why a side stopped, `<side>.ready` the data a side runs on.
const other: Side = side === "head" ? "base" : "head";
const order = schedule(runs);
const sharedFile = (name: string) => join(shared, name);
const readShared = (name: string) => {
  try {
    return readFileSync(sharedFile(name), "utf8");
  } catch {
    return undefined;
  }
};
const writeShared = (name: string, text: string) => {
  writeFileSync(sharedFile(`${name}.${side}.tmp`), text);
  renameSync(sharedFile(`${name}.${side}.tmp`), sharedFile(name));
};

/** The other side stopped, so this one does too. */
class OtherSideFailed extends Error {}

/** Waits until `ready` returns something, or the other side fails. */
async function waitOther<T>(ready: () => T | undefined, what: string): Promise<T> {
  // The other side may be launching, or preparing, or running two runs.
  const end = Date.now() + 3 * runTimeout;
  for (;;) {
    const failed = readShared("failed");
    if (failed !== undefined) throw new OtherSideFailed(`the ${other} side failed: ${failed}`);
    const value = ready();
    if (value !== undefined) return value;
    if (Date.now() > end)
      throw new Error(`the ${other} side did not ${what} within ${(3 * runTimeout) / 1000} s`);
    await sleep(50);
  }
}

/** Both sides launched and prepared, on the same seeded dataset. */
async function bothReady() {
  const data = (path: string) => {
    try {
      return JSON.parse(readFileSync(path, "utf8")) as { seed?: number; digest?: string };
    } catch {
      return undefined;
    }
  };
  const mine = data(join(homedir(), ".local/state/baum/seeddata.json"));
  if (!mine?.digest)
    throw new Error("a comparison runs on the seeded dataset, and this side's home has none");
  writeShared(`${side}.ready`, JSON.stringify({ seed: mine.seed, digest: mine.digest }));
  const theirs = await waitOther(() => {
    const text = readShared(`${other}.ready`);
    return text === undefined
      ? undefined
      : (JSON.parse(text) as { seed?: number; digest?: string });
  }, "launch and prepare");
  if (theirs.digest !== mine.digest)
    throw new Error(
      `the sides' seeded datasets differ (${side}: seed ${mine.seed}, digest ${mine.digest}; ` +
        `${other}: seed ${theirs.seed}, digest ${theirs.digest}), so their numbers would not compare`,
    );
}

const turn = () => Number(readShared("turn") ?? "0");
/** Waits for the host (profile.sh) to say this side may run its place in
 *  the order, which it does once the turn reached it and it has frozen the
 *  other side's container (docker pause), so the waiting side's daemon, app
 *  and compositor take no CPU from the side measured. */
const myTurn = (at: number) =>
  waitOther(() => (readShared("go") === String(at) ? true : undefined), "finish its run");

/** Leaves the app on a page with no script while the other side runs, so
 *  it does no work then: one of the daemon's JSON records, since the app
 *  refuses to navigate off the daemon (about:blank too). */
const idle = () => page().navigate(`${base}api/ui-state/tour`);

/** Hands the turn on, the app idle. */
async function passTurn(at: number) {
  await idle();
  writeShared("turn", String(at + 1));
}

/** Milliseconds of CPU this container has used, from its cgroup (v2, then
 *  v1's cpuacct), or undefined where neither can be read. */
function containerCpu(): number | undefined {
  for (const [path, parse] of [
    ["/sys/fs/cgroup/cpu.stat", (t: string) => Number(t.match(/^usage_usec (\d+)/m)?.[1]) / 1000],
    ["/sys/fs/cgroup/cpuacct/cpuacct.usage", (t: string) => Number(t) / 1e6],
  ] as const) {
    try {
      const value = parse(readFileSync(path, "utf8"));
      if (Number.isFinite(value)) return value;
    } catch {}
  }
  return undefined;
}

/** MiB the app's web content processes hold resident (WebKit's
 *  WebKitWebProcess, beside the runner in the container), or undefined
 *  where there is none, as in Chromium. Read before each run: a page
 *  process that grows from run to run carries something over. */
function webProcessMemory(): number | undefined {
  let kib = 0;
  let found = false;
  for (const pid of readdirSync("/proc").filter((name) => /^\d+$/.test(name))) {
    try {
      const status = readFileSync(`/proc/${pid}/status`, "utf8");
      if (!/^Name:\s+WebKitWebProces/m.test(status)) continue;
      kib += Number(status.match(/^VmRSS:\s+(\d+)/m)?.[1] ?? 0);
      found = true;
    } catch {}
  }
  return found ? Math.round(kib / 1024) : undefined;
}

/** BAUM_PROFILE_SETTLE=0 turns settling off, to measure without it. */
const settling = process.env.BAUM_PROFILE_SETTLE !== "0";
/** A quarter second in which the container used under this share of one
 *  CPU counts as quiet. */
const QUIET_SHARE = 0.1;

/** Waits, at most `cap` ms, until the container (the app, its daemon and
 *  compositor) has been quiet for a second: four quarter seconds each under
 *  QUIET_SHARE of a CPU. A new home's daemon works through its clones for a
 *  while after it starts, and a side just thawed catches up on what came
 *  due while it was frozen; either would land in the runs. Returns the
 *  milliseconds it waited. */
async function settle(cap: number): Promise<number> {
  const start = Date.now();
  if (!settling || containerCpu() === undefined) return 0;
  let quiet = 0;
  let last = containerCpu()!;
  while (quiet < 4 && Date.now() - start < cap) {
    await sleep(250);
    const now = containerCpu()!;
    quiet = now - last < 250 * QUIET_SHARE ? quiet + 1 : 0;
    last = now;
  }
  return Date.now() - start;
}

const collected: Run[] = [];
let environmentFrom: Snapshot | undefined;

/** Writes metrics.json and summary.md with the runs so far: after every
 *  run, so a failure keeps the runs before it. */
function write(partial?: string) {
  const [warmup, ...measured] = collected;
  if (!warmup) return;
  const profile: Profile = {
    scenario: scenario!.name,
    description: scenario!.description,
    started: new Date().toISOString(),
    environment: environment(environmentFrom ?? warmup.snapshot),
    notes,
    ...(partial ? { partial } : {}),
    warmup,
    runs: measured,
    chunks: values.dist && existsSync(values.dist) ? readChunks(values.dist) : [],
  };
  if (side) profile.side = side;
  writeFileSync(join(out, "metrics.json"), `${JSON.stringify(profile, null, 2)}\n`);
  writeFileSync(join(out, "summary.md"), summarise(profile) + cpu.section());
}

const done = () => `${Math.max(0, collected.length - 1)} of ${runs} runs`;

async function main() {
  await limited(waitForDriver(driverBase), "chromedriver's start");
  log(`launching the app for ${scenario!.name}`);
  await limited(launch(), "the app's launch");
  const state = await limited(
    (async () => {
      await context.clearFilters();
      const prepared = scenario!.prepare ? await scenario!.prepare(context) : undefined;
      await saveState();
      return prepared;
    })(),
    "preparing the scenario",
  );
  const steps = side
    ? order.flatMap((step, at) => (step.side === side ? [{ index: step.index, at }] : []))
    : Array.from({ length: runs + 1 }, (_, index) => ({ index, at: undefined }));
  if (side) {
    await idle();
    // Before the other side may run, so each side's daemon finishes its
    // start on its own time.
    const waited = await settle(300_000);
    if (waited > 2_000) log(`waited ${Math.round(waited / 1000)} s for the instance to go quiet`);
    await bothReady();
  }
  for (const { index, at } of steps) {
    if (at !== undefined) await myTurn(at);
    const label = side ? `${side} ` : "";
    log(index === 0 ? `${label}warm-up` : `${label}run ${index} of ${runs}`);
    const what = index === 0 ? "the warm-up" : `run ${index}`;
    await limited(
      (async () => {
        const settled = await settle(15_000);
        const load = loadavg()[0]!;
        await restoreState();
        if (index > 0) await cpu.start(page());
        const cpuBefore = containerCpu();
        const memory = webProcessMemory();
        const started = performance.now();
        const metrics = await scenario!.run(context, state as never);
        const wall = performance.now() - started;
        const snapshot = await execute<Snapshot>("return window.__baumPerf.snapshot();");
        await cpu.stop(index, snapshot, execute);
        // After the snapshot, so it is not measured.
        await scenario!.teardown?.(context, state as never);
        environmentFrom ??= snapshot;
        collected.push({
          index,
          wall,
          metrics: { ...metrics, ...scenario!.after?.(snapshot), ...standardMetrics(snapshot) },
          snapshot,
          ...(side ? { side, order: at } : {}),
          conditions: {
            load,
            settle: settled,
            ...(cpuBefore === undefined ? {} : { cpu: containerCpu()! - cpuBefore }),
            ...(memory === undefined ? {} : { web_memory: memory }),
          },
        });
      })(),
      what,
    );
    write(index < runs ? `${done()} so far` : undefined);
    if (at !== undefined) await passTurn(at);
  }
}

/** The baseline as it was named, and its commit unless that is the name. */
function baseLabel(ref = "", commit = "unknown") {
  return !ref || commit.startsWith(ref) || ref.startsWith(commit) ? commit : `${ref} at ${commit}`;
}

/** Head's last step: once base has run all its runs, or stopped, the
 *  comparison of the two. */
async function compare(partial?: string) {
  if (side !== "head") return;
  if (!partial)
    await waitOther(() => (turn() >= order.length ? true : undefined), "finish its runs").catch(
      (error: Error) => {
        partial = error.message;
      },
    );
  const read = (which: Side) => {
    try {
      return JSON.parse(readFileSync(join(shared, which, "metrics.json"), "utf8")) as Profile;
    } catch {
      return undefined;
    }
  };
  const base = read("base");
  const head = read("head");
  if (!base || !head) {
    writeFileSync(
      join(shared, "summary.md"),
      `# Profile comparison: ${scenario!.name}\n\nNo comparison: ${partial ?? "a side wrote no runs"}.\n`,
    );
    return;
  }
  const reason = partial ?? base.partial ?? head.partial;
  const comparison = {
    scenario: scenario!.name,
    description: scenario!.description,
    headline: headline!,
    baseLabel: baseLabel(base.environment.ref, base.environment.commit),
    headLabel: head.environment.commit ?? "unknown",
    base,
    head,
    ...(reason ? { partial: reason } : {}),
  };
  writeFileSync(join(shared, "summary.md"), summariseComparison(comparison));
  writeFileSync(
    join(shared, "metrics.json"),
    `${JSON.stringify(comparisonJson(comparison), null, 2)}\n`,
  );
}

let code = 0;
try {
  await main();
  await compare();
} catch (error) {
  if (side && !(error instanceof OtherSideFailed))
    writeShared(
      "failed",
      `${side}: ${error instanceof Error ? error.message.split("\n")[0] : String(error)}`,
    );
  if (error instanceof Skip) {
    writeFileSync(
      join(out, "summary.md"),
      `# Profile: ${scenario.name}\n\nSkipped: ${error.message}.\n`,
    );
    console.error(`profile: skipped ${scenario.name}: ${error.message}`);
    code = 3;
  } else {
    const message =
      error instanceof WebDriverError || error instanceof Error ? error.message : String(error);
    console.error(`profile: ${scenario.name} failed: ${message}`);
    if (collected.length) {
      write(`${done()}; run ${collected.length} failed: ${message.split("\n")[0]}`);
      console.error(`profile: summary.md keeps the ${done()} that finished`);
    }
    if (session) {
      const shot = join(out, "failure.png");
      // Best effort, and bounded: after a run timeout the driver may be hung.
      await Promise.race([
        session
          .screenshot()
          .then((png) => {
            writeFileSync(shot, png);
            console.error("profile: failure.png beside the summary shows the window then");
          })
          .catch(() => {}),
        sleep(5_000),
      ]);
    }
    code = 1;
  }
  await compare(error instanceof Error ? error.message.split("\n")[0] : String(error)).catch(
    () => {},
  );
} finally {
  if (session) await Promise.race([session.close().catch(() => {}), sleep(10_000)]);
  driver.kill();
  // The driver's last words, before the process ends.
  await Promise.race([
    new Promise<void>((resolve) => driver.once("close", () => resolve())),
    sleep(5_000),
  ]);
  await new Promise<void>((resolve) => driverLog.end(() => resolve()));
}
process.exit(code);
