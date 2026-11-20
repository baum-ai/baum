// Runs one scenario (scenarios.ts) against the desktop app through
// WebKitWebDriver: a warm-up, then N runs, reading window.__baumPerf after
// each, and writes metrics.json and summary.md. `just desktop::profile`
// (oss/desktop/scripts/profile.sh) runs it inside the headless isolated
// container; see the desktop README's Profiling section.
// usage: node run.ts <scenario> --app BINARY --out DIR [--runs N] [--dist DIR]
import { execFileSync, spawn } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { parseArgs } from "node:util";
import type { Navigation, Snapshot } from "../../src/perf-collector.ts";
import { readChunks } from "./bundle.ts";
import { scenarios, Skip, type Context } from "./scenarios.ts";
import { standardMetrics, summarise, type Profile, type Run } from "./summary.ts";
import { newSession, waitForDriver, WebDriverError, type Session } from "./webdriver.ts";

const { values, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    app: { type: "string" },
    out: { type: "string" },
    runs: { type: "string", default: "5" },
    dist: { type: "string" },
    port: { type: "string", default: "4444" },
    env: { type: "string", multiple: true, default: [] },
  },
});
const name = positionals[0] ?? "";
const scenario = scenarios.find((s) => s.name === name);
if (!scenario || !values.app || !values.out) {
  console.error(
    `usage: run.ts <scenario> --app BINARY --out DIR [--runs N]\nscenarios: ${scenarios.map((s) => s.name).join(", ")}`,
  );
  process.exit(2);
}
const runs = Number(values.runs);
if (!Number.isInteger(runs) || runs < 1) {
  console.error(`profile: --runs must be a whole number from 1: ${values.runs}`);
  process.exit(2);
}
const out = values.out;
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

// WebKitGTK lets the app's first web context be automated only with this set.
process.env.TAURI_WEBVIEW_AUTOMATION = "true";
const driverLog = createWriteStream(join(out, "driver.log"));
const driver = spawn("WebKitWebDriver", [`--port=${values.port}`], {
  stdio: ["ignore", "pipe", "pipe"],
});
driver.stdout.pipe(driverLog);
driver.stderr.pipe(driverLog);
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
  session = await newSession(driverBase, values.app!);
  await session.setTimeouts({ script: 600_000 });
  const end = Date.now() + 120_000;
  for (;;) {
    const url = await onDaemonPage();
    if (url) {
      base = url.match(/^https?:\/\/[^/]+\/s\/[^/]+\//)![0];
      return performance.now() - started;
    }
    if (Date.now() > end) {
      const at = await session.url().catch(() => "?");
      throw new Error(
        `the app did not open the daemon's page with the profiling collector within two minutes; it shows ${at.replace(/\/s\/[^/]+/, "/s/…")}. ` +
          "Was the frontend built with BAUM_FRONTEND_MODE=profile (just desktop::profile does)?",
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

const notes: string[] = [];
const context: Context = {
  launch,
  open,
  execute,
  waitFor,
  settled,
  press,
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
  entries.CPUs = read("/sys/fs/cgroup/cpuset.cpus.effective") || "unrestricted";
  // The user agent's WebKit version is frozen; the package's is not.
  let webkit = "";
  try {
    webkit = execFileSync("pkg-config", ["--modversion", "webkit2gtk-4.1"], { encoding: "utf8" });
  } catch {}
  entries.WebKitGTK =
    webkit.trim() || (snapshot.userAgent.match(/AppleWebKit\/[\d.]+/)?.[0] ?? snapshot.userAgent);
  const marker = read(join(homedir(), ".local/state/baum/seeddata.json"));
  if (marker) {
    const seeded = JSON.parse(marker) as { seed?: number; digest?: string };
    entries.data = `seed ${seeded.seed} (digest ${String(seeded.digest).slice(0, 12)})`;
  } else {
    entries.data = "this instance's own (not seeded, so not comparable across machines)";
  }
  return entries;
}

async function main() {
  await waitForDriver(driverBase);
  log(`launching the app for ${scenario!.name}`);
  await launch();
  const state = scenario!.prepare ? await scenario!.prepare(context) : undefined;
  const collected: Run[] = [];
  for (let index = 0; index <= runs; index++) {
    log(index === 0 ? "warm-up" : `run ${index} of ${runs}`);
    const started = performance.now();
    const metrics = await scenario!.run(context, state as never);
    const wall = performance.now() - started;
    const snapshot = await execute<Snapshot>("return window.__baumPerf.snapshot();");
    collected.push({
      index,
      wall,
      metrics: { ...metrics, ...scenario!.after?.(snapshot), ...standardMetrics(snapshot) },
      snapshot,
    });
  }
  const [warmup, ...measured] = collected;
  const profile: Profile = {
    scenario: scenario!.name,
    description: scenario!.description,
    started: new Date().toISOString(),
    environment: environment(warmup!.snapshot),
    notes,
    warmup: warmup!,
    runs: measured,
    chunks: values.dist && existsSync(values.dist) ? readChunks(values.dist) : [],
  };
  writeFileSync(join(out, "metrics.json"), `${JSON.stringify(profile, null, 2)}\n`);
  writeFileSync(join(out, "summary.md"), summarise(profile));
}

let code = 0;
try {
  await main();
} catch (error) {
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
    if (session) {
      const shot = join(out, "failure.png");
      await session
        .screenshot()
        .then((png) => {
          writeFileSync(shot, png);
          console.error("profile: failure.png beside the summary shows the window then");
        })
        .catch(() => {});
    }
    code = 1;
  }
} finally {
  if (session) await session.close().catch(() => {});
  driver.kill();
}
process.exit(code);
