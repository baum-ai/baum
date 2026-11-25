// The shell harness's pieces: a mock daemon on 127.0.0.1, "attacker"
// servers standing for other sites (`attacker.example`, which Chromium
// resolves to 127.0.0.1 here, and another loopback port), a fake `baum` that
// answers `baum daemon ensure --json` with the mock's status and leaves a
// private credential, so the app attaches through its real path, and the
// built app launched under Playwright with `shell.openExternal` recorded
// instead of opening a browser.
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { _electron, type ElectronApplication, type Page } from "@playwright/test";

/** oss/desktop: the app Playwright launches (package.json's `main`). */
export const DESKTOP = resolve(import.meta.dirname, "../..");
const ELECTRON = createRequire(import.meta.url)("electron") as unknown as string;

/** The daemon's own Content-Security-Policy for its pages. */
export const DAEMON_CSP =
  "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'";
/** A tile frame's policy, as the frame route sends it. */
export const FRAME_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts";
/** Fills the window, so a click anywhere hits it. */
export const BIG =
  "position:fixed;inset:0;display:block;font:40px sans-serif;background:#fd0;border:0;width:100%;height:100%";
export const NAMESPACE = "/s/ns/";

export interface Served {
  status?: number;
  type?: string;
  csp?: string;
  headers?: Record<string, string>;
  body?: string;
}

export type Route = (url: URL, body: string) => Served | undefined;

/** A small HTTP server on 127.0.0.1 that records every request it gets. */
export class Server {
  readonly requests: string[] = [];
  readonly #server: http.Server;
  #port = 0;
  route: Route;

  constructor(route: Route = () => ({ body: "hit" })) {
    this.route = route;
    this.#server = http.createServer((req, res) => {
      let body = "";
      req.setEncoding("utf8");
      req.on("data", (chunk: string) => {
        body += chunk;
      });
      req.on("end", () => {
        const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "127.0.0.1"}`);
        this.requests.push(`${req.method} ${url.host}${url.pathname}${url.search}`);
        const served = this.route(url, body) ?? { status: 404, body: "not found" };
        res.statusCode = served.status ?? 200;
        res.setHeader("Content-Type", served.type ?? "text/html; charset=utf-8");
        res.setHeader("Cache-Control", "no-store");
        if (served.csp) res.setHeader("Content-Security-Policy", served.csp);
        for (const [name, value] of Object.entries(served.headers ?? {})) {
          res.setHeader(name, value);
        }
        res.end(served.body ?? "");
      });
    });
  }

  get port(): number {
    return this.#port;
  }

  async listen(): Promise<void> {
    await new Promise<void>((done) => this.#server.listen(0, "127.0.0.1", done));
    const address = this.#server.address();
    if (address === null || typeof address === "string") throw new Error("no port");
    this.#port = address.port;
  }

  async close(): Promise<void> {
    this.#server.closeAllConnections();
    await new Promise<void>((done) => this.#server.close(() => done()));
  }
}

/** The mock daemon: `/` redirects into the namespace as the daemon's launch
 *  page does, `/api/health` answers, and each spec adds its own pages under
 *  `/s/ns/`. Heartbeats (`/s/ns/beat?win=`) and reports (`/s/ns/report`)
 *  are recorded. */
export class MockDaemon extends Server {
  readonly token = randomBytes(32).toString("base64url");
  readonly pages = new Map<string, Served | Route>();
  readonly beats = new Map<string, number[]>();
  readonly reports: Record<string, unknown>[] = [];

  constructor() {
    super();
    this.route = (url, body) => this.#route(url, body);
  }

  get origin(): string {
    return `http://127.0.0.1:${this.port}`;
  }

  /** A page of the daemon's, by its path under the namespace. */
  url(path = ""): string {
    return `${this.origin}${NAMESPACE}${path}`;
  }

  beatsOf(window: string): number[] {
    return this.beats.get(window) ?? [];
  }

  #route(url: URL, body: string): Served | undefined {
    if (url.pathname === "/") return { status: 302, headers: { Location: NAMESPACE } };
    if (url.pathname === "/api/health")
      return { type: "application/json", body: '{"status":"ok"}' };
    if (url.pathname === `${NAMESPACE}beat`) {
      const window = url.searchParams.get("win") ?? "";
      this.beats.set(window, [...this.beatsOf(window), Date.now()]);
      return { body: "ok" };
    }
    if (url.pathname === `${NAMESPACE}report`) {
      this.reports.push(JSON.parse(body) as Record<string, unknown>);
      return { body: "ok" };
    }
    const page =
      this.pages.get(`${url.pathname}${url.search}`) ?? this.pages.get(url.pathname) ?? undefined;
    if (typeof page === "function") return page(url, body);
    if (page) return page;
    if (url.pathname === NAMESPACE) return { csp: DAEMON_CSP, body: "<!doctype html><p>app page" };
    return undefined;
  }
}

export interface Shell {
  app: ElectronApplication;
  /** The main window. */
  main: Page;
  /** Everything the app wrote to its standard output and error. */
  logs: string[];
  /** The URLs the app handed to `shell.openExternal`. */
  opened(): Promise<string[]>;
  close(): Promise<void>;
}

/** Launches the built app with its own home, attached through a fake
 *  `baum` to `daemon` (when `onboarded`; otherwise it opens on the setup
 *  screen; with `failing`, the fake `baum` cannot start the daemon and the
 *  connecting screen stays). `attacker.example` resolves to 127.0.0.1. */
export async function launch(
  daemon: MockDaemon,
  { onboarded = true, failing = false } = {},
): Promise<Shell> {
  const home = mkdtempSync(join(tmpdir(), "baum-shell-"));
  const state = join(home, "state");
  mkdirSync(state, { mode: 0o700 });
  writeFileSync(
    join(state, "credential.json"),
    JSON.stringify({ url: `${daemon.origin}/#token=${daemon.token}` }),
    { mode: 0o600 },
  );
  const status = { version: "0.0.0-shell", pid: process.pid, app: daemon.origin, state };
  const baum = join(home, "baum");
  writeFileSync(
    baum,
    failing
      ? "#!/bin/sh\necho 'the test daemon cannot start' >&2\nexit 1\n"
      : `#!/bin/sh\nif [ "$1 $2" = "daemon ensure" ]; then echo '${JSON.stringify(status)}'; fi\n`,
    { mode: 0o755 },
  );
  const config = join(home, ".config");
  if (onboarded) {
    mkdirSync(join(config, "com.usebaum.desktop"), { recursive: true });
    writeFileSync(
      join(config, "com.usebaum.desktop", "desktop.json"),
      JSON.stringify({ setup: { onboarded: true } }),
    );
  }
  const env: Record<string, string> = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (value !== undefined && !name.startsWith("XDG_") && !name.startsWith("BAUM_")) {
      env[name] = value;
    }
  }
  Object.assign(env, {
    HOME: home,
    XDG_CONFIG_HOME: config,
    XDG_DATA_HOME: join(home, ".local", "share"),
    XDG_CACHE_HOME: join(home, ".cache"),
    BAUM_DESKTOP_BAUM: baum,
    BAUM_DESKTOP_LOG_COMMANDS: "1",
  });
  const app = await _electron.launch({
    executablePath: ELECTRON,
    args: ["--host-resolver-rules=MAP attacker.example 127.0.0.1", DESKTOP],
    cwd: DESKTOP,
    env,
    timeout: 60_000,
  });
  const logs: string[] = [];
  const collect = (chunk: Buffer) => logs.push(...String(chunk).split("\n").filter(Boolean));
  app.process().stdout?.on("data", collect);
  app.process().stderr?.on("data", collect);
  // No browser: record what would open.
  await app.evaluate(({ shell }) => {
    const opened: string[] = [];
    (globalThis as { __opened?: string[] }).__opened = opened;
    shell.openExternal = async (url: string) => {
      opened.push(url);
    };
  });
  const main = await app.firstWindow();
  return {
    app,
    main,
    logs,
    opened: () => app.evaluate(() => [...((globalThis as { __opened?: string[] }).__opened ?? [])]),
    close: async () => {
      await app.close().catch(() => {});
      rmSync(home, { recursive: true, force: true });
    },
  };
}

/** Calls a command from `page`'s main frame: "ok <value>" or "error
 *  <message>". */
export function invoke(page: Page, name: string, args?: Record<string, unknown>): Promise<string> {
  return page.evaluate(
    ([name, args]) => {
      const bridge = (
        window as { baumDesktop?: { invoke(n: string, a?: unknown): Promise<unknown> } }
      ).baumDesktop;
      if (!bridge) return Promise.resolve("error no bridge");
      return bridge.invoke(name as string, args).then(
        (value) => `ok ${JSON.stringify(value)}`,
        (error) => `error ${String(error)}`,
      );
    },
    [name, args] as const,
  );
}

export const sleep = (ms: number) => new Promise<void>((done) => setTimeout(done, ms));

export async function waitFor<T>(what: string, timeout: number, test: () => T | Promise<T>) {
  const end = Date.now() + timeout;
  for (;;) {
    const value = await test();
    if (value) return value;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(50);
  }
}

/** The CPU time a Linux process has used, in clock ticks, or null once it
 *  has exited. */
export function cpuTicks(pid: number): number | null {
  const path = `/proc/${pid}/stat`;
  if (!existsSync(path)) return null;
  try {
    const fields = (readFileSync(path, "utf8").split(") ")[1] ?? "").split(" ");
    if (fields[0] === "Z" || fields[0] === "X") return null;
    return Number(fields[11]) + Number(fields[12]);
  } catch {
    return null;
  }
}

/** Samples one process's CPU use every 100 ms, in percent of one core. */
export class CpuSampler {
  readonly samples: { t: number; cpu: number | null }[] = [];
  readonly #timer: NodeJS.Timeout;

  constructor(pid: number, hz = 100) {
    let last = { t: Date.now(), ticks: cpuTicks(pid) };
    this.#timer = setInterval(() => {
      const now = { t: Date.now(), ticks: cpuTicks(pid) };
      const cpu =
        now.ticks === null || last.ticks === null
          ? null
          : ((now.ticks - last.ticks) / hz / ((now.t - last.t) / 1000)) * 100;
      this.samples.push({ t: now.t, cpu });
      last = now;
    }, 100);
  }

  stop(): void {
    clearInterval(this.#timer);
  }
}

/** The longest stretch between `from` and `to` without a beat. */
export function maxGap(times: number[], from: number, to: number): number {
  let gap = 0;
  let last = from;
  for (const t of [...times.filter((t) => t >= from && t <= to), to]) {
    gap = Math.max(gap, t - last);
    last = t;
  }
  return gap;
}

/** Where each web contents of the app runs: its main frame's process and
 *  every frame's. */
export interface Contents {
  url: string;
  pid: number;
  frames: { url: string; pid: number }[];
}

export function contents(app: ElectronApplication): Promise<Contents[]> {
  return app.evaluate(({ webContents }) =>
    webContents
      .getAllWebContents()
      .filter((c) => !c.isDestroyed())
      .map((c) => ({
        url: c.getURL(),
        pid: c.getOSProcessId(),
        frames: c.mainFrame.framesInSubtree.map((f) => ({ url: f.url, pid: f.osProcessId })),
      })),
  );
}
