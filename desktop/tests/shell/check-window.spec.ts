// The tile check window runs in renderer processes other than the main
// window's: a tile spinning in it leaves the main window's heartbeat
// running, closing the window ends the spinning process within a second, and
// a window opened again works. Its page can call no command and cannot leave
// the daemon's origin (README "Tile check window"; the port of the WebKitGTK
// harness's tests/check-window/check.mjs). Processes are sampled from /proc.
import { expect, test } from "@playwright/test";
import {
  type Contents,
  contents,
  CpuSampler,
  cpuTicks,
  DAEMON_CSP,
  FRAME_CSP,
  invoke,
  launch,
  MockDaemon,
  maxGap,
  Server,
  type Shell,
  sleep,
  waitFor,
} from "./harness.ts";

const daemon = new MockDaemon();
const attacker = new Server();
let shell: Shell;
let checkLoads = 0;
const SPIN_AFTER_MS = 1500;

const MAIN_JS = `
setInterval(() => fetch("/s/ns/beat?win=main", { method: "POST" }).catch(() => {}), 100);`;

const checkJs = () => `
const gen = document.documentElement.dataset.gen;
const post = (path, body) => fetch(path, { method: "POST", body: JSON.stringify(body ?? {}) }).catch(() => {});
setInterval(() => post("/s/ns/beat?win=check" + gen), 250);
let frames = 0;
const count = () => { frames++; requestAnimationFrame(count); };
requestAnimationFrame(count);
(async () => {
  const report = { gen, declaration: JSON.stringify(window.__BAUM_DESKTOP__), visibility: document.visibilityState, bridge: typeof window.baumDesktop };
  for (const command of ["open_check_window", "close_check_window", "zoom_state", "open_link"]) {
    try {
      await window.baumDesktop.invoke(command, command === "open_link" ? { url: "http://attacker.example:${attacker.port}/?via=open_link" } : undefined);
      report[command] = "ran";
    } catch (e) { report[command] = "refused: " + e; }
  }
  setTimeout(() => {
    report.animationFrames = frames;
    post("/s/ns/report", report);
  }, 500);
  if (gen !== "1") setTimeout(() => {
    post("/s/ns/report", { gen, leaving: true });
    open("http://attacker.example:${attacker.port}/?via=open");
    location.href = "http://attacker.example:${attacker.port}/?via=location";
  }, 1000);
})();`;

const tile = (mode: string | null) =>
  `<!doctype html><body style="margin:0;font:15px sans-serif"><p>${mode} tile</p>${
    mode === "spin" ? `<script>setTimeout(() => { for (;;) {} }, ${SPIN_AFTER_MS})</script>` : ""
  }</body>`;

test.beforeAll(async () => {
  await Promise.all([daemon.listen(), attacker.listen()]);
  daemon.pages.set("/s/ns/", (url) => {
    if (url.searchParams.get("page") === "tile-check") {
      const gen = ++checkLoads;
      return {
        csp: DAEMON_CSP,
        body: `<!doctype html><html data-gen="${gen}"><body style="margin:0"><iframe sandbox="allow-scripts" style="width:720px;height:200px;border:0" src="/s/ns/tile?mode=${gen === 1 ? "spin" : "idle"}"></iframe><script src="/s/ns/check.js"></script></body></html>`,
      };
    }
    // The main window's page, with an idle tile frame of its own.
    return {
      csp: DAEMON_CSP,
      body: `<!doctype html><p>main window</p><iframe sandbox="allow-scripts" src="/s/ns/tile?mode=main"></iframe><script src="/s/ns/main.js"></script>`,
    };
  });
  daemon.pages.set("/s/ns/main.js", { type: "text/javascript", body: MAIN_JS });
  daemon.pages.set("/s/ns/check.js", () => ({ type: "text/javascript", body: checkJs() }));
  daemon.pages.set("/s/ns/tile", (url) => ({
    csp: FRAME_CSP,
    body: tile(url.searchParams.get("mode")),
  }));
  shell = await launch(daemon);
  await shell.main.waitForURL((url) => url.href.startsWith(daemon.url()), { timeout: 60_000 });
});

test.afterAll(async () => {
  await shell?.close();
  await Promise.all([daemon.close(), attacker.close()]);
});

const isCheck = (c: Contents) => c.url.includes("page=tile-check");

test("the check window isolates a spinning tile and closing it ends the process", async () => {
  const numbers: Record<string, unknown> = {};
  await waitFor("the main window's heartbeat", 30_000, () => daemon.beatsOf("main").length > 5);

  // Open, and open again while it is open: one window.
  const opened = Date.now();
  expect(await invoke(shell.main, "open_check_window")).toBe("ok null");
  expect(await invoke(shell.main, "open_check_window")).toBe("ok null");
  const firstBeat = await waitFor(
    "the check window's heartbeat",
    15_000,
    () => daemon.beatsOf("check1")[0],
  );
  numbers.openToFirstBeatMs = firstBeat - opened;
  await sleep(500);
  expect(checkLoads, "check page loads for two opens").toBe(1);

  // Where each page runs.
  const all = await contents(shell.app);
  const main = all.find((c) => c.url.startsWith(daemon.url()) && !isCheck(c)) as Contents;
  const check = all.find(isCheck) as Contents;
  const spinFrame = check.frames.find((f) => f.url.includes("mode=spin"));
  const mainTile = main.frames.find((f) => f.url.includes("mode=main"));
  numbers.processes = {
    mainWindow: main.pid,
    mainWindowTileFrame: mainTile?.pid,
    checkWindow: check.pid,
    checkWindowTileFrame: spinFrame?.pid,
  };
  expect(spinFrame, "the check window's tile frame").toBeDefined();
  const spinning = spinFrame?.pid as number;
  const mainPids = new Set(main.frames.map((f) => f.pid));
  expect(check.pid, "the check page runs in the main window's process").not.toBe(main.pid);
  expect(mainPids.has(check.pid), "the check page shares a process with the main window").toBe(
    false,
  );
  expect(mainPids.has(spinning), "the tile shares a process with the main window").toBe(false);

  // The tile spins: the check page's heartbeat stops (where the tile shares
  // its process) and the tile's process is busy.
  const sampler = new CpuSampler(spinning);
  try {
    await waitFor("the tile's process at full CPU", 15_000, () =>
      sampler.samples.slice(-3).some((s) => (s.cpu ?? 0) >= 80),
    );
    const spinFrom = Date.now();
    await sleep(3000);
    const spinTo = Date.now();
    const during = sampler.samples.filter((s) => s.t > spinFrom && s.t <= spinTo);
    numbers.spinningCpuPercent = Math.round(Math.min(...during.map((s) => s.cpu ?? 0)));
    numbers.checkBeatsWhileSpinning = daemon
      .beatsOf("check1")
      .filter((t) => t > spinFrom + 500).length;
    numbers.mainMaxBeatGapWhileSpinningMs = maxGap(daemon.beatsOf("main"), spinFrom, spinTo);
    expect(numbers.spinningCpuPercent as number).toBeGreaterThanOrEqual(50);
    expect(numbers.mainMaxBeatGapWhileSpinningMs as number).toBeLessThan(1000);

    // Close: the spinning process stops within a second, and is gone a
    // second later.
    const closing = Date.now();
    expect(await invoke(shell.main, "close_check_window")).toBe("ok null");
    numbers.closeCommandMs = Date.now() - closing;
    const stopped = await waitFor("the spinning process to stop", 10_000, () =>
      sampler.samples.find((s) => s.t > closing && (s.cpu === null || s.cpu < 10)),
    );
    numbers.closeToCpuDropMs = stopped.t - closing;
    expect(numbers.closeToCpuDropMs as number).toBeLessThanOrEqual(1000);
    await sleep(1000);
    numbers.spinningProcessGoneAfter1s = cpuTicks(spinning) === null;
    expect(numbers.spinningProcessGoneAfter1s).toBe(true);
    expect((await contents(shell.app)).some(isCheck), "the check window is still open").toBe(false);
  } finally {
    sampler.stop();
  }

  // Open again: a fresh window works, and its page cannot leave.
  const reopened = Date.now();
  expect(await invoke(shell.main, "open_check_window")).toBe("ok null");
  const secondBeat = await waitFor(
    "the reopened check window's heartbeat",
    15_000,
    () => daemon.beatsOf("check2")[0],
  );
  numbers.reopenToFirstBeatMs = secondBeat - reopened;
  await waitFor("the reopened page's attempt to leave", 15_000, () =>
    daemon.reports.find((r) => r.gen === "2" && r.leaving),
  );
  await sleep(3000);
  numbers.checkBeatsAfterLeavingAttempt = daemon
    .beatsOf("check2")
    .filter((t) => t > Date.now() - 2500).length;
  expect(numbers.checkBeatsAfterLeavingAttempt as number).toBeGreaterThan(0);
  expect(attacker.requests, "requests from the check window to another site").toEqual([]);
  expect(await shell.opened(), "links the check window opened").toEqual([]);

  // What each check page saw: the declaration, and every command refused.
  const pages = daemon.reports.filter((r) => "declaration" in r);
  expect(pages.map((r) => r.gen).sort()).toEqual(["1", "2"]);
  for (const r of pages) {
    numbers[`checkPage${r.gen}`] = r;
    expect(r.declaration).toBe('{"tileFrames":true,"checkWindow":true,"openLinks":false}');
    for (const command of ["open_check_window", "close_check_window", "zoom_state", "open_link"]) {
      expect(r[command], command).toBe(`refused: command ${command} not allowed`);
    }
  }

  // Close and open at once: the open builds a new window.
  const swapped = Date.now();
  const both = await shell.main.evaluate(async () => {
    const bridge = (window as unknown as { baumDesktop: { invoke(n: string): Promise<unknown> } })
      .baumDesktop;
    const closing = bridge.invoke("close_check_window");
    const opening = bridge.invoke("open_check_window");
    return [await closing, await opening];
  });
  expect(both).toEqual([null, null]);
  const thirdBeat = await waitFor(
    "the check window opened right after close",
    15_000,
    () => daemon.beatsOf("check3")[0],
  );
  numbers.closeThenOpenToFirstBeatMs = thirdBeat - swapped;
  expect(checkLoads).toBe(3);

  const beforeReload = Date.now();
  // The main window loading another page closes it.
  await shell.main.goto(daemon.url("?again"));
  await waitFor(
    "the check window to close",
    5_000,
    async () => !(await contents(shell.app)).some(isCheck),
  );
  expect(await invoke(shell.main, "close_check_window")).toBe("ok null");
  expect(attacker.requests).toEqual([]);
  numbers.mainMaxBeatGapUntilReloadMs = maxGap(
    daemon.beatsOf("main"),
    daemon.beatsOf("main")[5] as number,
    beforeReload,
  );
  expect(numbers.mainMaxBeatGapUntilReloadMs as number).toBeLessThan(1000);
  const refusals = shell.logs.filter((l) => l.includes("refused"));
  numbers.refusals = refusals;
  console.log(JSON.stringify(numbers, null, 2));
  expect(refusals).toContain("baum desktop: refused a navigation to http://attacker.example");
});
