// Checks in WebKitGTK that the desktop shell's tile check window runs in a
// web process of its own: a tile spinning in it leaves the main window's
// heartbeat running, closing the window ends the spinning process within a
// second, and a window opened again works. Also that the check window's page
// can call no command and cannot leave the daemon's origin. A mock daemon
// serves the main page (which drives the commands as the server asks) and
// the check page, and records each page's heartbeat; WebKit's processes are
// sampled from /proc.
// usage: node check.mjs <harness binary>   (in the navigation-check image)
import http from "node:http";
import { readdirSync, readFileSync } from "node:fs";
import { spawn } from "node:child_process";

const harness = process.argv[2];
const ATT = "http://attacker.example:4301/";
const DAEMON_CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'";
const TILE_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts";
const SPIN_AFTER_MS = 1500;

const MAIN_JS = `
const post = (path, body) => fetch(path, { method: "POST", body: JSON.stringify(body ?? {}) });
setInterval(() => post("/s/ns/beat?win=main").catch(() => {}), 100);
(async () => {
  for (;;) {
    let action;
    try { action = await (await fetch("/s/ns/next")).text(); } catch { await new Promise((r) => setTimeout(r, 200)); continue; }
    if (action === "none") continue;
    const command = action === "close" ? "close_check_window" : "open_check_window";
    const result = await window.__TAURI_INTERNALS__.invoke(command).then(() => "ok", (e) => "error " + e);
    await post("/s/ns/done", { action, result }).catch(() => {});
  }
})();`;
const CHECK_JS = `
const gen = document.documentElement.dataset.gen;
const post = (path, body) => fetch(path, { method: "POST", body: JSON.stringify(body ?? {}) }).catch(() => {});
setInterval(() => post("/s/ns/beat?win=check" + gen), 250);
(async () => {
  const report = { gen, declaration: JSON.stringify(window.__BAUM_DESKTOP__), visibility: document.visibilityState };
  for (const command of ["open_check_window", "close_check_window"]) {
    try { await window.__TAURI_INTERNALS__.invoke(command); report[command] = "ran"; } catch (e) { report[command] = "refused: " + e; }
  }
  await post("/s/ns/report", report);
  if (gen !== "1") setTimeout(() => {
    post("/s/ns/report", { gen, leaving: true });
    open(${JSON.stringify(ATT + "?via=open")});
    location.href = ${JSON.stringify(ATT + "?via=location")};
  }, 1000);
})();`;
const tile = (mode) =>
  `<!doctype html><body style="margin:0;font:15px sans-serif"><p>${mode} tile</p>` +
  (mode === "spin" ? `<script>setTimeout(() => { for (;;) {} }, ${SPIN_AFTER_MS})</script>` : "") +
  "</body>";

const beats = new Map();
const reports = [];
let checkLoads = 0;
let attackerHits = 0;
const actions = [];
let poller = null;
let onDone = null;
let received = null;

http
  .createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1:4300");
    let body = "";
    req.on("data", (d) => (body += d));
    req.on("end", () => {
      const html = (csp, text) => {
        res.setHeader("Content-Security-Policy", csp);
        res.setHeader("Content-Type", "text/html");
        res.end(text);
      };
      const js = (text) => (res.setHeader("Content-Type", "text/javascript"), res.end(text));
      switch (url.pathname) {
        case "/s/ns/":
          if (url.searchParams.get("page") === "tile-check") {
            const gen = ++checkLoads;
            return html(
              DAEMON_CSP,
              `<!doctype html><html data-gen="${gen}"><body style="margin:0"><iframe sandbox="allow-scripts" style="width:720px;height:200px;border:0" src="/s/ns/tile?mode=${gen === 1 ? "spin" : "idle"}"></iframe><script src="/s/ns/check.js"></script></body></html>`,
            );
          }
          return html(DAEMON_CSP, `<!doctype html><p>main window</p><script src="/s/ns/main.js"></script>`);
        case "/s/ns/main.js":
          return js(MAIN_JS);
        case "/s/ns/check.js":
          return js(CHECK_JS);
        case "/s/ns/tile":
          return html(TILE_CSP, tile(url.searchParams.get("mode")));
        case "/s/ns/beat": {
          const win = url.searchParams.get("win");
          if (!beats.has(win)) beats.set(win, []);
          beats.get(win).push(Date.now());
          return res.end("ok");
        }
        case "/s/ns/report":
          reports.push(JSON.parse(body));
          return res.end("ok");
        case "/s/ns/next": {
          const send = () => {
            const action = actions.shift();
            if (action) received = Date.now();
            res.end(action ?? "none");
          };
          if (actions.length) return send();
          poller = send;
          setTimeout(() => poller === send && ((poller = null), send()), 5000);
          return;
        }
        case "/s/ns/done":
          res.end("ok");
          return onDone?.(JSON.parse(body));
      }
      res.statusCode = 404;
      res.end();
    });
  })
  .listen(4300, "127.0.0.1");
http.createServer((req, res) => (attackerHits++, res.end("hit"))).listen(4301, "0.0.0.0");

// WebKit web processes, every 100 ms: CPU % over the interval, by PID.
const HZ = 100;
const samples = [];
let previous = new Map();
const webProcesses = () => {
  const now = Date.now();
  const seen = new Map();
  for (const d of readdirSync("/proc")) {
    if (!/^\d+$/.test(d)) continue;
    try {
      if (!readFileSync(`/proc/${d}/comm`, "utf8").startsWith("WebKitWebProces")) continue;
      const stat = readFileSync(`/proc/${d}/stat`, "utf8").split(") ")[1].split(" ");
      if (stat[0] === "Z") continue;
      seen.set(Number(d), { ticks: Number(stat[11]) + Number(stat[12]), t: now });
    } catch {}
  }
  return seen;
};
setInterval(() => {
  const seen = webProcesses();
  const cpu = new Map();
  for (const [pid, s] of seen) {
    const p = previous.get(pid);
    cpu.set(pid, p ? ((s.ticks - p.ticks) / HZ / ((s.t - p.t) / 1000)) * 100 : 0);
  }
  samples.push({ t: Date.now(), cpu });
  previous = seen;
}, 100);
const latest = () => samples.at(-1)?.cpu ?? new Map();

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(what, timeout, test) {
  const end = Date.now() + timeout;
  for (;;) {
    const value = test();
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(50);
  }
}
function act(action) {
  return new Promise((resolve) => {
    const queued = Date.now();
    onDone = (done) => resolve({ ...done, queued, received, done: Date.now() });
    actions.push(action);
    if (poller) {
      const p = poller;
      poller = null;
      p();
    }
  });
}
const beatsOf = (win) => beats.get(win) ?? [];
const maxGap = (times, from, to) => {
  const within = times.filter((t) => t >= from && t <= to);
  let gap = 0;
  let last = from;
  for (const t of [...within, to]) (gap = Math.max(gap, t - last)), (last = t);
  return gap;
};

const out = [];
const child = spawn(harness, [], { env: { ...process.env, HARNESS_URL: "http://127.0.0.1:4300/s/ns/" } });
child.stderr.on("data", (d) => out.push(String(d)));
child.stdout.on("data", (d) => out.push(String(d)));

const problems = [];
const numbers = {};
const expect = (ok, problem) => ok || problems.push(problem);
try {
  await waitFor("the main window's heartbeat", 30000, () => beatsOf("main").length > 5);
  await sleep(1000);
  const mainPids = new Set(latest().keys());
  numbers.mainWebProcesses = mainPids.size;

  // Open, and open again while it is open: one window.
  const opened = await act("open");
  expect(opened.result === "ok", `open_check_window: ${opened.result}`);
  const again = await act("open");
  expect(again.result === "ok", `a second open_check_window: ${again.result}`);
  const firstBeat = await waitFor("the check window's heartbeat", 15000, () => beatsOf("check1")[0]);
  numbers.openToFirstBeatMs = firstBeat - opened.received;
  await sleep(500);
  expect(checkLoads === 1, `the check page loaded ${checkLoads} times for two opens`);
  const newPids = [...latest().keys()].filter((p) => !mainPids.has(p));
  numbers.checkWebProcesses = newPids.length;
  expect(newPids.length >= 1, "the check window has no web process of its own");

  // The tile spins: the check page's heartbeat stops and its process is busy.
  const spinning = await waitFor("a web process of the check window at full CPU", 15000, () =>
    newPids.find((p) => (latest().get(p) ?? 0) >= 80),
  );
  const spinFrom = Date.now();
  await sleep(3000);
  const spinTo = Date.now();
  numbers.spinningCpuPercent = Math.round(
    Math.min(...samples.filter((s) => s.t > spinFrom && s.t <= spinTo).map((s) => s.cpu.get(spinning) ?? 0)),
  );
  numbers.checkBeatsWhileSpinning = beatsOf("check1").filter((t) => t > spinFrom + 500).length;
  numbers.mainMaxBeatGapWhileSpinningMs = maxGap(beatsOf("main"), spinFrom, spinTo);
  numbers.mainBeatsWhileSpinning = beatsOf("main").filter((t) => t > spinFrom && t <= spinTo).length;
  expect(numbers.checkBeatsWhileSpinning === 0, "the check page kept beating: the tile did not spin");
  expect(!mainPids.has(spinning), "the spinning process is the main window's");
  expect(numbers.mainMaxBeatGapWhileSpinningMs < 1000, `the main window stalled ${numbers.mainMaxBeatGapWhileSpinningMs} ms`);

  // Close: the spinning process stops within a second.
  const closed = await act("close");
  expect(closed.result === "ok", `close_check_window: ${closed.result}`);
  const stopped = await waitFor("the spinning process to stop", 10000, () => {
    const s = samples.find((s) => s.t > closed.received && (s.cpu.get(spinning) ?? 0) < 10);
    return s?.t;
  });
  numbers.closeToCpuDropMs = stopped - closed.received;
  numbers.closeCommandMs = closed.done - closed.received;
  expect(numbers.closeToCpuDropMs <= 1000, `the spinning process ran ${numbers.closeToCpuDropMs} ms after close`);
  await sleep(1000);
  numbers.spinningProcessGoneAfter1s = !latest().has(spinning);
  expect(numbers.spinningProcessGoneAfter1s, "the spinning process still exists a second after close");

  // Open again: a fresh window works.
  const reopened = await act("open");
  expect(reopened.result === "ok", `open_check_window after close: ${reopened.result}`);
  const secondBeat = await waitFor("the reopened check window's heartbeat", 15000, () => beatsOf("check2")[0]);
  numbers.reopenToFirstBeatMs = secondBeat - reopened.received;
  await waitFor("the reopened page's attempt to leave", 15000, () => reports.find((r) => r.gen === "2" && r.leaving));
  await sleep(3000);
  const after = reports.find((r) => r.gen === "2" && r.leaving);
  numbers.checkBeatsAfterLeavingAttempt = beatsOf("check2").filter((t) => t > Date.now() - 2500).length;
  expect(numbers.checkBeatsAfterLeavingAttempt > 0, "the reopened check page stopped after trying to leave");
  expect(attackerHits === 0, `${attackerHits} request(s) from the check window reached another server`);
  expect(after, "the reopened check page did not report");

  for (const r of reports.filter((r) => "declaration" in r)) {
    expect(r.declaration === '{"tileFrames":true,"checkWindow":true}', `check page ${r.gen} declaration: ${r.declaration}`);
    for (const command of ["open_check_window", "close_check_window"])
      expect(r[command]?.startsWith("refused"), `the check page could call ${command}: ${r[command]}`);
    numbers[`checkPage${r.gen}`] = { visibility: r.visibility, open: r.open_check_window.slice(0, 120) };
  }
  expect(reports.filter((r) => "declaration" in r).length === 2, "a check page did not report");
  const last = await act("close");
  expect(last.result === "ok", `final close_check_window: ${last.result}`);
  numbers.mainMaxBeatGapOverallMs = maxGap(beatsOf("main"), beatsOf("main")[5], Date.now());
} catch (e) {
  problems.push(String(e.message ?? e));
}
child.kill("SIGKILL");
console.log(JSON.stringify(numbers, null, 2));
for (const l of out.join("").split("\n").filter((l) => /^(OPEN|CLOSED)|panic|error/i.test(l) && !/dbind|libEGL/.test(l)).slice(0, 20))
  console.log(`  harness: ${l.trim().slice(0, 200)}`);
if (problems.length) {
  for (const p of problems) console.log(`FAIL ${p}`);
  process.exit(1);
}
console.log("check window: all checks passed");
process.exit(0);
