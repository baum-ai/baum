#!/usr/bin/env node
// The running half of scripts/bridge-test.sh (which see): runs the apps it
// prepared under Xvfb, each with a home of its own and a stand-in daemon,
// and serves their updates on 127.0.0.1.
//
//   node scripts/bridge-test.mjs DIR PORT
//
// 1. Tauri to Electron: the Tauri AppImage (1.2.2), with the bundled baum
//    and a real daemon, reads DIR/release's latest.json, downloads the
//    Electron AppImage, replaces itself and restarts. The file must then be
//    the Electron AppImage, the app that runs must be Electron (it answers
//    on the remote debugging port the Tauri app was started with), it must
//    open the daemon's page, not setup, with the zoom desktop.json saved,
//    and keep the Tauri app's stable copy of baum and menu entry.
// 2. Electron to Electron: 1.2.3-test.1 checks DIR/feed (Settings' "Check
//    now" and "Update and restart", called as the daemon's page calls them),
//    replaces itself with 1.2.4-test.1 and starts it.
// 3. A damaged signature: 1.2.3-test.1 rejects DIR/tampered's download,
//    remembers it in desktop.json and keeps its file.
//
// Uses only Node.js (its WebSocket for the DevTools protocol), Xvfb and
// dbus-daemon, as the desktop-electron image has them.
import { execFileSync, spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  chmodSync,
  copyFileSync,
  createReadStream,
  existsSync,
  mkdirSync,
  openSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { join, normalize } from "node:path";

const [dir, portText] = process.argv.slice(2);
if (!dir || !portText) {
  console.error("usage: node scripts/bridge-test.mjs DIR PORT");
  process.exit(2);
}
const PORT = Number(portText);
const APPIMAGE = "baum-desktop-linux-amd64.AppImage";
const IDENTIFIER = "com.usebaum.desktop";
const run = join(dir, "run");
const results = [];

function ok(message) {
  results.push(`ok   ${message}`);
  console.log(`ok   ${message}`);
}

function fail(message) {
  console.error(`FAIL ${message}`);
  for (const line of results) console.error(line);
  cleanup();
  process.exit(1);
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(what, timeout, test) {
  const end = Date.now() + timeout;
  for (;;) {
    const value = await test();
    if (value) return value;
    if (Date.now() > end) fail(`timed out after ${timeout / 1000} s waiting for ${what}`);
    await sleep(500);
  }
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

// Every process the test starts, and the apps' own children, which the
// AppImage updater starts in a session of their own.
const started = [];
function cleanup() {
  for (const child of started.reverse()) {
    try {
      process.kill(-child.pid, "SIGKILL");
    } catch {}
  }
  for (const pattern of ["appimage_extracted_", `${IDENTIFIER}/bin/baum`]) {
    try {
      execFileSync("pkill", ["-KILL", "-f", pattern], { stdio: "ignore" });
    } catch {}
  }
}
process.on("exit", cleanup);

function start(name, command, args, env, log) {
  const out = log ? openSync(log, "a") : "ignore";
  const child = spawn(command, args, { env, detached: true, stdio: ["ignore", out, out] });
  started.push(child);
  child.on("error", (e) => fail(`${name}: ${e.message}`));
  return child;
}

// --- the display and the session bus -------------------------------------

rmSync(run, { recursive: true, force: true });
mkdirSync(run, { recursive: true });
const DISPLAY = ":77";
start("Xvfb", "Xvfb", [DISPLAY, "-screen", "0", "1280x1024x24", "-nolisten", "tcp"], process.env);
await waitFor("Xvfb", 20_000, () => existsSync("/tmp/.X11-unix/X77"));
const bus = `unix:path=${join(run, "bus")}`;
start("dbus-daemon", "dbus-daemon", ["--session", "--nofork", `--address=${bus}`], process.env);
await waitFor("the session bus", 20_000, () => existsSync(join(run, "bus")));

// --- the update server ------------------------------------------------------

/** Requests the update server answered, in order. */
const served = [];
const manifest = JSON.parse(readFileSync(join(dir, "release", "latest.json"), "utf8"));
const updateServer = createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  const agent = req.headers["user-agent"] ?? "";
  served.push(`${req.method} ${url.pathname} (${agent})`);
  // The release's latest.json, byte for byte but for each URL's origin:
  // https://downloads.usebaum.com/update/vVERSION/FILE is served from here.
  if (url.pathname === "/latest.json") {
    const local = structuredClone(manifest);
    for (const platform of Object.values(local.platforms)) {
      platform.url = platform.url.replace("https://downloads.usebaum.com", `http://127.0.0.1:${PORT}`);
    }
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(local));
    return;
  }
  const routes = [
    [`/update/v${manifest.version}/`, join(dir, "release")],
    ["/feed/", join(dir, "feed")],
    ["/tampered/", join(dir, "tampered")],
  ];
  for (const [prefix, root] of routes) {
    if (!url.pathname.startsWith(prefix)) continue;
    const name = normalize(decodeURIComponent(url.pathname.slice(prefix.length)));
    const path = join(root, name);
    if (name.includes("/") || !existsSync(path) || !statSync(path).isFile()) break;
    res.setHeader("Content-Length", statSync(path).size);
    createReadStream(path).pipe(res);
    return;
  }
  res.statusCode = 404;
  res.end("not found");
});
await new Promise((resolve) => updateServer.listen(PORT, "127.0.0.1", resolve));

// --- the stand-in daemon ------------------------------------------------------

const TOKEN = randomBytes(32).toString("base64url");
/** User agents of the daemon page's loads: Electron's names the app's version. */
const pageLoads = [];
const daemon = createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  const json = (body) => {
    res.setHeader("Content-Type", "application/json");
    res.end(body);
  };
  if (url.pathname === "/") {
    pageLoads.push(req.headers["user-agent"] ?? "");
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    res.end("<!doctype html><title>baum</title><p>the stand-in daemon's page");
  } else if (url.pathname === "/api/health") json('{"status":"ok"}');
  else if (url.pathname === "/api/jobs") json('{"jobs":[]}');
  else {
    res.statusCode = 404;
    json("{}");
  }
});
await new Promise((resolve) => daemon.listen(0, "127.0.0.1", resolve));
const origin = `http://127.0.0.1:${daemon.address().port}`;

// --- the apps --------------------------------------------------------------------

/** A home with desktop.json as an installed copy left it, a fake `baum`
 *  whose daemon is the stand-in, and the AppImage in ~/Applications. */
function home(name, image) {
  const root = join(run, name);
  const config = join(root, ".config", IDENTIFIER);
  const state = join(root, "state");
  const apps = join(root, "Applications");
  for (const d of [config, apps]) mkdirSync(d, { recursive: true });
  mkdirSync(state, { mode: 0o700 });
  writeFileSync(
    join(config, "desktop.json"),
    JSON.stringify({ setup: { onboarded: true, commands: [] }, window: { zoom: 1.25 } }),
  );
  writeFileSync(join(state, "credential.json"), JSON.stringify({ url: `${origin}/#token=${TOKEN}` }), {
    mode: 0o600,
  });
  const status = JSON.stringify({ version: "0.0.0-bridge", pid: process.pid, app: origin, state });
  const baum = join(root, "baum");
  writeFileSync(
    baum,
    `#!/bin/sh\necho "$*" >>"${join(root, "baum.log")}"\ncase "$1 $2" in\n"daemon ensure"|"daemon status") echo '${status}' ;;\nesac\n`,
  );
  chmodSync(baum, 0o755);
  const app = join(apps, APPIMAGE);
  copyFileSync(image, app);
  chmodSync(app, 0o755);
  return { root, config, app, baum };
}

/** Starts the AppImage in `h`. With `real`, it runs its bundled baum and a
 *  real daemon; otherwise the fake `baum` and the stand-in daemon. */
function launch(name, h, debugPort, extra = {}, real = false) {
  const env = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v !== undefined && !k.startsWith("XDG_") && !k.startsWith("BAUM_")) env[k] = v;
  }
  Object.assign(env, {
    HOME: h.root,
    DISPLAY,
    DBUS_SESSION_BUS_ADDRESS: bus,
    // No FUSE in a container: the AppImage runtime extracts and runs.
    APPIMAGE_EXTRACT_AND_RUN: "1",
    // A host's AppImageLauncher registers itself for every AppImage
    // (binfmt_misc reaches into containers); it must not ask to integrate.
    APPIMAGELAUNCHER_DISABLE: "1",
    NO_AT_BRIDGE: "1",
    ...(real ? {} : { BAUM_DESKTOP_BAUM: h.baum }),
    // WebKitGTK's own sandbox needs user namespaces a container lacks.
    WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS: "1",
    WEBKIT_DISABLE_COMPOSITING_MODE: "1",
    ...extra,
  });
  return start(name, h.app, [`--remote-debugging-port=${debugPort}`], env, join(h.root, "app.log"));
}

async function devtools(port, path) {
  try {
    const response = await fetch(`http://127.0.0.1:${port}${path}`);
    return response.ok ? await response.json() : null;
  } catch {
    return null;
  }
}

/** Evaluates `expression` in the page at `url`, through the DevTools
 *  protocol, and returns its value. */
async function evaluate(port, url, expression) {
  const pages = (await devtools(port, "/json/list")) ?? [];
  const page = pages.find((p) => p.type === "page" && p.url.startsWith(url));
  if (!page) fail(`no page at ${url} on port ${port}`);
  const socket = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.onopen = resolve;
    socket.onerror = () => reject(new Error("cannot reach the page"));
  });
  const answer = new Promise((resolve) => {
    socket.onmessage = (event) => {
      const message = JSON.parse(String(event.data));
      if (message.id === 1) resolve(message);
    };
  });
  socket.send(
    JSON.stringify({
      id: 1,
      method: "Runtime.evaluate",
      params: { expression, awaitPromise: true, returnByValue: true },
    }),
  );
  const message = await answer;
  socket.close();
  if (message.result?.exceptionDetails) {
    throw new Error(message.result.exceptionDetails.exception?.description ?? "evaluation failed");
  }
  return message.result?.result?.value;
}

/** Calls a command as the daemon's page does. */
function invoke(port, name) {
  return evaluate(port, origin, `window.baumDesktop.invoke(${JSON.stringify(name)})`);
}

async function daemonPage(port, what, at = origin) {
  return waitFor(`${what} to open the daemon's page`, 120_000, async () => {
    const pages = (await devtools(port, "/json/list")) ?? [];
    return pages.some((p) => p.type === "page" && p.url.startsWith(at)) ? pages : null;
  });
}

function processes(pattern) {
  try {
    return execFileSync("pgrep", ["-af", pattern], { encoding: "utf8" }).trim().split("\n");
  } catch {
    return [];
  }
}

function desktopJson(h) {
  return JSON.parse(readFileSync(join(h.config, "desktop.json"), "utf8"));
}

function files(path) {
  return existsSync(path) ? readdirSync(path).sort() : [];
}

// --- 1. Tauri to Electron ------------------------------------------------------------

console.log("== 1. the Tauri AppImage (1.2.2) updates to the Electron AppImage (1.2.3)");
const electron = sha256(join(dir, "release", APPIMAGE));
const tauri = home("tauri", join(dir, "tauri", APPIMAGE));
const tauriApp = sha256(tauri.app);
if (tauriApp === electron) fail("the Tauri AppImage is the Electron one");
launch("the Tauri app", tauri, 9333, {}, true);
// The Tauri app keeps a stable copy of its bundled baum, which runs the daemon.
const stable = join(tauri.root, ".local", "share", IDENTIFIER, "bin", "baum");
await waitFor("the Tauri app's stable copy of baum", 60_000, () => existsSync(stable));
const tauriDaemon = await waitFor("the daemon the Tauri app starts", 60_000, () => {
  const found = processes(stable);
  return found.length > 0 ? found : null;
});
console.log(`     the Tauri app's daemon: ${tauriDaemon.join(" | ")}`);
const entries = join(tauri.root, ".local", "share", "applications");
await sleep(3000);
const tauriEntries = files(entries);
const tauriEntry = tauriEntries.map((f) => readFileSync(join(entries, f), "utf8"));
if (tauriEntries.length === 0) fail("the Tauri app wrote no menu entry");
await waitFor("the Tauri app to replace its AppImage", 240_000, () => sha256(tauri.app) === electron);
ok(`the Tauri app replaced ${APPIMAGE} with the Electron AppImage of latest.json`);
for (const line of served) console.log(`     served ${line}`);
if (!served.some((s) => s.startsWith("GET /latest.json") && s.includes("tauri-plugin-updater"))) {
  fail("the Tauri updater did not read latest.json");
}
const version = await waitFor("an app on the Tauri app's remote debugging port", 120_000, () =>
  devtools(9333, "/json/version"),
);
if (!String(version["User-Agent"]).includes("Electron/")) {
  fail(`the restarted app is not Electron: ${version["User-Agent"]}`);
}
ok(`the Tauri app restarted into Electron (${version.Browser}, ${version["User-Agent"]})`);
const daemonAt = "http://127.0.0.1:";
const pages = await daemonPage(9333, "the Electron app", daemonAt);
if (pages.some((p) => p.url.includes("setup.html"))) fail("the Electron app showed setup");
// Origins only: the daemon's paths carry its namespace secret.
const shown = pages.filter((p) => p.type === "page").map((p) => new URL(p.url).origin);
ok(`the Electron app opened the daemon's page, not setup: ${shown.join(", ")}`);
const kept = desktopJson(tauri);
if (kept.setup?.onboarded !== true || kept.window?.zoom !== 1.25) {
  fail(`desktop.json lost what the Tauri app saved: ${JSON.stringify(kept)}`);
}
ok(`desktop.json kept setup.onboarded and the zoom: ${JSON.stringify(kept)}`);
// The window's zoom (125%) is the one saved there.
const ratio = await waitFor("the saved zoom", 20_000, async () => {
  const value = await evaluate(9333, daemonAt, "window.devicePixelRatio");
  return value === 1.25 ? value : null;
});
ok(`the Electron window applies the saved zoom (devicePixelRatio ${ratio})`);
if (!existsSync(join(tauri.config, "chromium"))) fail("Electron keeps no files beside desktop.json");
ok(`Electron's files went in ${IDENTIFIER}/chromium beside desktop.json`);
// The stable copy the Tauri app made is where the Electron app keeps its
// own (paths.ts), and still runs the daemon the Electron app attached to.
const electronRuns = processes(stable);
if (!existsSync(stable) || electronRuns.length === 0) {
  fail(`nothing runs the stable copy ${stable} any more`);
}
ok(`the stable copy of baum is in place and runs the daemon: ${electronRuns.join(" | ")}`);
await sleep(3000);
const electronEntries = files(entries);
const electronEntry = electronEntries.map((f) => readFileSync(join(entries, f), "utf8"));
console.log(`     menu entries: Tauri ${JSON.stringify(tauriEntries)}, Electron ${JSON.stringify(electronEntries)}`);
if (JSON.stringify(electronEntries) !== JSON.stringify(tauriEntries)) {
  fail("the Electron app's menu entry is not the Tauri app's file");
}
for (const text of electronEntry) {
  if (!text.includes(tauri.app)) fail(`the menu entry does not run ${tauri.app}:\n${text}`);
}
ok(`the menu entry (${electronEntries.join(", ")}) is the Tauri app's file and starts the same AppImage`);
console.log(`     Tauri's:\n${tauriEntry.join("\n")}\n     Electron's:\n${electronEntry.join("\n")}`);
cleanup();
started.length = 0;
await sleep(1000);
// Xvfb and the bus went with the cleanup; start them again.
start("Xvfb", "Xvfb", [DISPLAY, "-screen", "0", "1280x1024x24", "-nolisten", "tcp"], process.env);
rmSync(join(run, "bus"), { force: true });
start("dbus-daemon", "dbus-daemon", ["--session", "--nofork", `--address=${bus}`], process.env);
await waitFor("the session bus", 20_000, () => existsSync(join(run, "bus")));

// --- 2. Electron to Electron ------------------------------------------------------------

const pubkey = readFileSync(join(dir, "key", "pubkey.b64"), "utf8").trim();
const testEnv = (feed) => ({
  BAUM_DESKTOP_UPDATE_FEED: `http://127.0.0.1:${PORT}/${feed}/`,
  BAUM_DESKTOP_UPDATE_PUBKEY: pubkey,
});

async function untilPhase(port, what, done) {
  return waitFor(what, 120_000, async () => {
    const view = await invoke(port, "update_state");
    return done(view) ? view : null;
  });
}

console.log("== 2. Electron 1.2.3-test.1 updates to 1.2.4-test.1");
const next = sha256(join(dir, "feed", APPIMAGE));
const first = home("electron", join(dir, "test-1.2.3", APPIMAGE));
pageLoads.length = 0;
launch("Electron 1.2.3-test.1", first, 9334, testEnv("feed"));
await daemonPage(9334, "Electron 1.2.3-test.1");
console.log(`     check_for_updates: ${JSON.stringify(await invoke(9334, "check_for_updates"))}`);
const settled = (v) => v.phase === "ready" || v.error || (v.phase === "idle" && v.checked_at);
const ready = await untilPhase(9334, "the update to be ready", settled);
if (ready.phase !== "ready") fail(`the update is not ready: ${JSON.stringify(ready)}`);
ok(`1.2.3-test.1 downloaded and verified ${ready.available}: ${JSON.stringify(ready)}`);
for (const line of served.filter((s) => s.includes("/feed/"))) console.log(`     served ${line}`);
console.log(`     install_update: ${JSON.stringify(await invoke(9334, "install_update"))}`);
await waitFor("the AppImage to be replaced", 60_000, () => sha256(first.app) === next);
ok(`1.2.3-test.1 replaced ${APPIMAGE} with 1.2.4-test.1`);
await waitFor("1.2.4-test.1 to open the daemon's page", 90_000, () =>
  pageLoads.some((ua) => ua.includes("1.2.4-test.1")),
);
ok(`1.2.4-test.1 started and opened the daemon's page (${pageLoads.find((ua) => ua.includes("1.2.4-test.1"))})`);
const after = desktopJson(first);
if (after.setup?.onboarded !== true) fail(`desktop.json lost setup: ${JSON.stringify(after)}`);
cleanup();
started.length = 0;
await sleep(1000);
start("Xvfb", "Xvfb", [DISPLAY, "-screen", "0", "1280x1024x24", "-nolisten", "tcp"], process.env);
rmSync(join(run, "bus"), { force: true });
start("dbus-daemon", "dbus-daemon", ["--session", "--nofork", `--address=${bus}`], process.env);
await waitFor("the session bus", 20_000, () => existsSync(join(run, "bus")));

// --- 3. a damaged signature ------------------------------------------------------------

console.log("== 3. Electron 1.2.3-test.1 rejects 1.2.4-test.1 with a damaged signature");
const tampered = home("tampered", join(dir, "test-1.2.3", APPIMAGE));
const before = sha256(tampered.app);
launch("Electron 1.2.3-test.1", tampered, 9335, testEnv("tampered"));
await daemonPage(9335, "Electron 1.2.3-test.1");
await invoke(9335, "check_for_updates");
const rejected = await untilPhase(9335, "the download to be rejected", settled);
if (rejected.phase === "ready" || rejected.installs !== false) {
  fail(`the damaged signature was accepted: ${JSON.stringify(rejected)}`);
}
ok(`the download was rejected and is only announced: ${JSON.stringify(rejected)}`);
const remembered = desktopJson(tampered).updates?.rejected_downloads ?? [];
if (!remembered.includes("1.2.4-test.1")) fail(`desktop.json does not remember it: ${JSON.stringify(desktopJson(tampered))}`);
ok(`desktop.json remembers it: updates.rejected_downloads = ${JSON.stringify(remembered)}`);
const install = await evaluate(
  9335,
  origin,
  'window.baumDesktop.invoke("install_update").then((v) => "ok " + JSON.stringify(v), (e) => "error " + String(e))',
);
if (!String(install).startsWith("error")) fail(`install_update did not refuse: ${install}`);
ok(`install_update refuses: ${install}`);
await sleep(2000);
if (sha256(tampered.app) !== before) fail("the AppImage changed");
ok("the AppImage is unchanged");

cleanup();
console.log("\nbridge-test: passed");
for (const line of results) console.log(line);
process.exit(0);
