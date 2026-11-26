// The bridge release on macOS (scripts/bridge-test-mac.sh run DIR PORT).
//
// In a throwaway home under DIR/run, as an installed Tauri copy leaves it
// (desktop.json with setup finished and a 125% zoom, start at login on, and
// the command linked at ~/.local/bin/baum -> baum.app/Contents/MacOS/baum,
// on the login shell's PATH), it starts the Tauri app (1.2.2) through
// LaunchServices, as Finder does, and serves DIR/release. Then:
//
// - the Tauri updater replaces baum.app with the Electron app (1.2.3) and
//   restarts into it;
// - the Electron app opens the daemon's page, not setup, and keeps
//   desktop.json;
// - the Tauri app's daemon (1.2.2, run from Contents/MacOS/baum, which is
//   the Electron executable now) is replaced by the bundled baum 1.2.3, and
//   the window stays on the daemon's page;
// - the command link points at Contents/Resources/bin/baum, and `baum` on
//   the PATH reaches the new daemon; a command run through the old path is
//   forwarded to it;
// - start at login names the Electron app.
//
// The Tauri app's web view keeps its data where WebKit always does, in the
// real ~/Library/WebKit/com.usebaum.desktop: WebKit does not read HOME. It
// only loads the test daemon's page there, under a port of its own.
import { execFileSync } from "node:child_process";
import {
  createReadStream,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { dirname, join, normalize } from "node:path";

const dir = process.argv[2];
const PORT = Number(process.argv[3]);
const DEBUG_PORT = 9343;
const IDENTIFIER = "com.usebaum.desktop";
const LSREGISTER =
  "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let failures = 0;
const ok = (message) => console.log(`ok   ${message}`);
const note = (message) => console.log(`     ${message}`);
function fail(message) {
  failures++;
  console.log(`FAIL ${message}`);
}

async function waitFor(what, ms, check) {
  const end = Date.now() + ms;
  for (;;) {
    const value = await check();
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(500);
  }
}

/** A command's output, or null when it fails. */
function output(file, args, options = {}) {
  try {
    return execFileSync(file, args, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      ...options,
    }).trim();
  } catch {
    return null;
  }
}

const plist = (path, key) => output("/usr/libexec/PlistBuddy", ["-c", `Print :${key}`, path]);

/** pid and arguments of this test's processes whose arguments match. */
function processes(root, pattern) {
  return (output("ps", ["-axo", "pid=,args="]) ?? "")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.includes(root) && pattern.test(line))
    .map((line) => {
      const space = line.indexOf(" ");
      return { pid: Number(line.slice(0, space)), args: line.slice(space + 1) };
    });
}

// The daemon replaces itself only with a file no other user can change, in
// directories no other user can write (internal/daemon/trust.go), as in
// /Applications; under /tmp the test would see a daemon that never moves.
for (let d = dir; d !== dirname(d); d = dirname(d)) {
  const { mode } = lstatSync(d);
  if ((mode & 0o022) !== 0 && (mode & 0o1000) === 0) {
    console.error(`bridge-test-mac: ${d} is writable by others; use a DIR in your home folder`);
    process.exit(1);
  }
}

// --- the update server ------------------------------------------------------

const served = [];
const manifest = JSON.parse(readFileSync(join(dir, "release", "latest.json"), "utf8"));
const server = createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://127.0.0.1");
  served.push(`${req.method} ${url.pathname} (${req.headers["user-agent"] ?? ""})`);
  // The release's latest.json but for each URL's origin.
  if (url.pathname === "/latest.json") {
    const local = structuredClone(manifest);
    for (const platform of Object.values(local.platforms)) {
      platform.url = platform.url.replace("https://downloads.usebaum.com", `http://127.0.0.1:${PORT}`);
    }
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(local));
    return;
  }
  const prefix = `/update/v${manifest.version}/`;
  if (url.pathname.startsWith(prefix)) {
    const name = normalize(decodeURIComponent(url.pathname.slice(prefix.length)));
    const path = join(dir, "release", name);
    if (!name.includes("/") && existsSync(path) && statSync(path).isFile()) {
      res.setHeader("Content-Length", statSync(path).size);
      createReadStream(path).pipe(res);
      return;
    }
  }
  res.statusCode = 404;
  res.end("not found");
});
await new Promise((resolve) => server.listen(PORT, "127.0.0.1", resolve));

// --- the home a Tauri copy left -----------------------------------------------

const run = join(dir, "run");
rmSync(run, { recursive: true, force: true });
const root = join(run, "home");
mkdirSync(root, { recursive: true, mode: 0o700 });
const config = join(root, "Library", "Application Support", IDENTIFIER);
const desktopJson = join(config, "desktop.json");
const agent = join(root, "Library", "LaunchAgents", `${IDENTIFIER}.plist`);
const app = join(root, "Applications", "baum.app");
const tauriExe = join(app, "Contents", "MacOS", "baum-desktop");
// The Tauri app's baum; the Electron app's executable after the update.
const oldCli = join(app, "Contents", "MacOS", "baum");
const bundledCli = join(app, "Contents", "Resources", "bin", "baum");
const link = join(root, ".local", "bin", "baum");
const state = join(root, ".local", "state", "baum", "daemon");
for (const d of [config, dirname(agent), dirname(app), dirname(link)]) {
  mkdirSync(d, { recursive: true });
}
execFileSync("ditto", [join(dir, "tauri", "baum.app"), app]);
execFileSync("ln", ["-s", oldCli, link]);
writeFileSync(join(root, ".zprofile"), 'export PATH="$HOME/.local/bin:$PATH"\n');
writeFileSync(
  desktopJson,
  JSON.stringify({
    setup: { onboarded: true, commands: [{ link, target: oldCli, backup: null, admin: false }] },
    window: { zoom: 1.25 },
  }),
);
writeFileSync(
  agent,
  `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>${IDENTIFIER}</string>
<key>ProgramArguments</key><array><string>${tauriExe}</string><string>--hidden</string></array>
<key>RunAtLoad</key><true/></dict></plist>
`,
);

const env = {};
for (const [k, v] of Object.entries(process.env)) {
  if (v !== undefined && !k.startsWith("BAUM_")) env[k] = v;
}
env.HOME = root;
const daemonInfo = () => {
  try {
    return JSON.parse(readFileSync(join(state, "daemon.json"), "utf8"));
  } catch {
    return null;
  }
};
const devtools = async (path) => {
  try {
    const response = await fetch(`http://127.0.0.1:${DEBUG_PORT}${path}`);
    return response.ok ? await response.json() : null;
  } catch {
    return null;
  }
};
const pages = async () => ((await devtools("/json/list")) ?? []).filter((p) => p.type === "page");
const onDaemonPage = async () =>
  (await pages()).some((p) => p.url.startsWith("http://127.0.0.1:"));

try {
  // --- the Tauri app --------------------------------------------------------------

  console.log("== the Tauri app (1.2.2) updates to the Electron app (1.2.3)");
  const log = join(run, "tauri.log");
  // The debugging port is the Tauri app's argument, which its restart passes
  // on to the Electron app.
  execFileSync(
    "/usr/bin/open",
    ["-n", "--stdout", log, "--stderr", log, app, "--args", `--remote-debugging-port=${DEBUG_PORT}`],
    { env },
  );
  const tauri = await waitFor("the Tauri app", 20_000, () => processes(root, /MacOS\/baum-desktop/)[0]);
  ok(`the Tauri app runs (pid ${tauri.pid})`);
  const before = await waitFor("the Tauri app's daemon", 60_000, () => {
    const info = daemonInfo();
    return info?.version === "1.2.2" ? info : null;
  });
  const oldDaemon = processes(root, /daemon run/).find((p) => p.pid === before.pid);
  if (!oldDaemon?.args.startsWith(oldCli)) fail(`the Tauri app's daemon runs ${oldDaemon?.args}`);
  else ok(`the Tauri app's daemon 1.2.2 runs from Contents/MacOS/baum (pid ${before.pid})`);

  // --- the update -------------------------------------------------------------------

  await waitFor("the Tauri updater to replace baum.app", 240_000, () =>
    existsSync(join(app, "Contents", "Frameworks", "Electron Framework.framework")) &&
    !existsSync(tauriExe),
  );
  const info = join(app, "Contents", "Info.plist");
  ok(
    `baum.app is the Electron app: ${plist(info, "CFBundleShortVersionString")}, executable ${plist(info, "CFBundleExecutable")}, ${plist(info, "CFBundleIdentifier")}`,
  );
  if (!served.some((s) => s.startsWith("GET /latest.json") && s.includes("tauri-plugin-updater"))) {
    fail(`the Tauri updater did not read latest.json: ${served.join("; ")}`);
  }
  if (output("codesign", ["--verify", "--deep", "--strict", app]) === null) {
    fail("the installed app's signature does not verify");
  }
  await waitFor("the Tauri app to exit", 30_000, () => output("ps", ["-p", String(tauri.pid)]) === null);
  const electron = await waitFor("the Electron app", 60_000, () =>
    processes(root, /MacOS\/baum --remote-debugging-port/).find((p) => !p.args.includes("Helper")),
  );
  ok(`the Tauri app restarted into the Electron app, with its arguments (pid ${electron.pid})`);
  const version = await waitFor("the Electron app's DevTools", 60_000, () => devtools("/json/version"));
  if (!String(version["User-Agent"]).includes("Electron/")) fail(`not Electron: ${version["User-Agent"]}`);

  await waitFor("the daemon's page", 120_000, onDaemonPage);
  if ((await pages()).some((p) => p.url.includes("setup.html"))) fail("the Electron app showed setup");
  else ok("the Electron app opened the daemon's page, not setup");
  const kept = JSON.parse(readFileSync(desktopJson, "utf8"));
  if (kept.setup?.onboarded !== true || kept.window?.zoom !== 1.25) {
    fail(`desktop.json lost what the Tauri app saved: ${JSON.stringify(kept)}`);
  } else ok("desktop.json kept setup and the zoom");

  // --- what still named Contents/MacOS/baum ---------------------------------------

  if (output("readlink", [link]) !== bundledCli) {
    fail(`${link} points at ${output("readlink", [link])}, not the bundled baum`);
  } else if (kept.setup.commands?.[0]?.target !== bundledCli) {
    fail(`desktop.json records ${JSON.stringify(kept.setup.commands)}`);
  } else ok("the command link points at Contents/Resources/bin/baum, without a prompt");

  // The old daemon notices its replaced executable and restarts from that
  // path: the Electron executable, which forwards to the bundled baum.
  const after = await waitFor("the daemon 1.2.3", 120_000, () => {
    const d = daemonInfo();
    return d?.version === "1.2.3" ? d : null;
  });
  const newDaemon = processes(root, /daemon run/).find((p) => p.pid === after.pid);
  if (!newDaemon?.args.startsWith(bundledCli)) fail(`the daemon 1.2.3 runs ${newDaemon?.args}`);
  else ok(`the daemon 1.2.3 runs from Contents/Resources/bin/baum (pid ${after.pid})`);
  await sleep(15_000);
  if (!(await onDaemonPage())) {
    fail(`the window left the daemon's page: ${(await pages()).map((p) => p.url).join(", ")}`);
  } else ok("the window stays on the daemon's page");
  const apps = processes(root, /MacOS\/baum( |$)/).filter((p) => !p.args.includes("Helper"));
  if (apps.length !== 1) fail(`Electron processes: ${apps.map((p) => p.args).join(" | ")}`);
  if (daemonInfo()?.pid !== after.pid) fail("the daemon changed again");

  const status = output("/bin/zsh", ["-lc", "baum daemon status"], { env, timeout: 30_000 });
  if (!status?.includes("baum daemon 1.2.3 is running")) fail(`\`baum daemon status\`: ${status}`);
  else ok("`baum` on the login shell's PATH reaches the daemon 1.2.3");
  const forwarded = output(oldCli, ["daemon", "status"], { env, timeout: 30_000 });
  if (!forwarded?.includes("baum daemon 1.2.3 is running")) fail(`${oldCli} daemon status: ${forwarded}`);
  else ok("a baum command run through Contents/MacOS/baum is forwarded to the bundled baum");

  const login = (plist(agent, "ProgramArguments") ?? "").replace(/\s+/g, " ");
  if (!login.includes(`${oldCli} --hidden`)) fail(`start at login runs ${login}`);
  else ok("start at login runs the Electron app");
} catch (e) {
  fail(e instanceof Error ? e.message : String(e));
  note(`served: ${served.join("; ")}`);
} finally {
  output(bundledCli, ["daemon", "stop"], { env, timeout: 30_000 });
  output("pkill", ["-f", app]);
  await sleep(2000);
  output(LSREGISTER, ["-u", app]);
  rmSync(app, { recursive: true, force: true });
  server.close();
}

console.log(failures ? `\nbridge-test-mac: ${failures} check(s) failed; logs in ${run}` : "\nbridge-test-mac: ok");
process.exit(failures ? 1 : 0);
