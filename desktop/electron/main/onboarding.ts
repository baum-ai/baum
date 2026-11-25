// First run and the "Command Line and Hooks" screen. Agent hooks run `baum`
// by name, so on first run the app shows a bundled setup screen that
// installs its bundled `baum` as the user's command (see install.ts) and adds
// baum's marketplace plugins through the Claude Code and Codex CLIs (the
// bundled `baum hooks install --user`). The user starts each change on that
// screen; the main process makes it. When the user continues, the window
// attaches to the daemon and opens its inbox. The menu item reopens the
// screen to see what is installed, update it or remove it.
//
// Whether setup ran and what the app installed live in the `setup` section
// of `desktop.json` in the app's configuration directory.
import { statSync } from "node:fs";
import { type CommandName, type Handler, registerCommand } from "./commands.ts";
import * as decode from "./decode.ts";
import { type Elevate, systemPrompt } from "./elevation.ts";
import * as install from "./install.ts";
import type { InstallRecord, LoginPath, Plan, Status, Target } from "./install.ts";
import { isAbsolute, join, joinPaths, parent, samePath, splitPaths } from "./paths.ts";
import {
  type AppProcess,
  childPath,
  cliOverridden,
  type Output,
  runBaum,
  runningAsAppImage,
} from "./run-baum.ts";
import { loadSection, readSection, updateSection } from "./settings.ts";

/** The bundled setup screen. */
export const SETUP_PAGE = "setup.html";
/** The bundled connecting screen. */
export const CONNECTING_PAGE = "index.html";
/** The menu item's id and label in the window's menu bar. */
export const MENU_ID = "command-line-and-hooks";
export const MENU_LABEL = "Command Line and Hooks…";

/** `baum daemon login-path` runs the login shell (10 s at most) and the
 *  `--version` of another baum (5 s). */
const LOGIN_PATH_TIMEOUT = 30_000;
// Login PATH resolution (30 s), two native actions (90 s each), and overhead.
const HOOKS_TIMEOUT = 240_000;
/** Starting a GitHub sign-in asks GitHub for a device code. */
const GITHUB_TIMEOUT = 45_000;
/** client.rs's POLL_TIMEOUT. */
const POLL_TIMEOUT = 5_000;
const REPOSITORIES_TIMEOUT = 130_000;
/** Where a GitHub sign-in's code is entered. Fixed rather than the daemon's
 *  verification URL, as on the setup screen. */
const GITHUB_DEVICE_PAGE = "https://github.com/login/device";

/** A daemon response, as client.rs's `Response`. */
export interface DaemonResponse {
  status: number;
  body: Uint8Array;
}

/** The loopback client to the attached daemon (client.rs). `L` is the
 *  launch the daemon port hands out (its URL and capability). */
export interface DaemonClient<L> {
  /** One request with a deadline; rejects when the daemon cannot be reached. */
  request(
    launch: L,
    method: string,
    path: string,
    json: string | null,
    timeoutMs: number,
  ): Promise<DaemonResponse>;
  /** Queues automatic reviews of the latest open pull requests. */
  catchUp(launch: L): Promise<void>;
}

export interface SetupDeps<L> {
  app: AppProcess;
  /** The user's home directory; empty when unknown. */
  home: string;
  /** desktop.json in the app's configuration directory. */
  settingsFile: string;
  /** Tauri's app_local_data_dir: `~/.local/share/com.usebaum.desktop`,
   *  `~/Library/Application Support/com.usebaum.desktop` or
   *  `%LOCALAPPDATA%\com.usebaum.desktop`. */
  localDataDir: string;
  /** The baum bundled next to the app's executable. */
  bundledCli: string;
  /** Why the app cannot run baum from where it is installed (daemon.rs's
   *  `misplaced`), if it cannot. */
  misplaced(): string | null;
  /** The daemon the app is attached to, once it is. */
  launch(): L | null;
  client: DaemonClient<L>;
  /** The native folder picker; null when the user cancels. */
  chooseFolder(title: string): Promise<string | null>;
  /** Opens an address in the default browser. */
  openExternal(url: string): void;
  /** Sends the main window to the connecting screen and attaches to the
   *  daemon, opening its inbox. */
  leaveSetup(): void;
  /** The administrator prompt; the system's by default. */
  elevate?: Elevate;
}

/** The `setup` section of desktop.json. */
export interface Saved {
  /** The user finished first-run setup, whatever they chose. */
  onboarded: boolean;
  /** Every link the app installed and has not removed yet: usually one,
   *  more when removing an earlier one failed (a cancelled password
   *  prompt), so that Remove can try it again. */
  commands: InstallRecord[];
}

function readSaved(section: decode.Fields): Saved {
  return {
    onboarded: decode.boolOr(section, "onboarded"),
    commands: decode.arrayOr(section, "commands", install.readRecord),
  };
}

/** An unreadable file reads as a first run, which only shows setup again. */
export function loadFrom(path: string): Saved {
  try {
    return readSaved(loadSection(path, "setup"));
  } catch (e) {
    console.error(`baum desktop: setup settings unreadable, using defaults: ${decode.message(e)}`);
    return { onboarded: false, commands: [] };
  }
}

export function readFrom(path: string): Saved {
  try {
    return readSaved(readSection(path, "setup"));
  } catch (e) {
    throw new Error(`Cannot read ${path}: ${decode.message(e)}`);
  }
}

/** Replaces the section with the changed record, as serde wrote it. */
export function updateAt(path: string, change: (saved: Saved) => void): void {
  try {
    updateSection(path, "setup", (section) => {
      const saved = readSaved(section);
      change(saved);
      for (const key of Object.keys(section)) delete section[key];
      section.onboarded = saved.onboarded;
      section.commands = saved.commands.map((r) => ({
        link: r.link,
        target: r.target,
        backup: r.backup,
        admin: r.admin,
      }));
    });
  } catch (e) {
    throw new Error(`Cannot save ${path}: ${decode.message(e)}`);
  }
}

/** After installing `added`, removes the app's other recorded links
 *  (`remove` is `install.uninstall`) and returns the records to keep, `added`
 *  first, with what happened. A link that could not be removed stays
 *  recorded. */
export async function afterInstall(
  previous: InstallRecord[],
  added: InstallRecord,
  remove: (record: InstallRecord) => Promise<string>,
  platform: NodeJS.Platform,
): Promise<{ kept: InstallRecord[]; lines: string[] }> {
  const kept = [added];
  const lines: string[] = [];
  for (const record of previous.filter((r) => !samePath(r.link, added.link, platform))) {
    try {
      lines.push(await remove(record));
    } catch (e) {
      lines.push(
        `The app's earlier link at ${record.link} stays (${decode.message(e)}); Remove tries it again.`,
      );
      kept.push(record);
    }
  }
  return { kept, lines };
}

/** Removes every recorded link; returns those that could not be removed,
 *  which stay recorded, with what happened. */
export async function removeAll(
  records: InstallRecord[],
  remove: (record: InstallRecord) => Promise<string>,
): Promise<{ left: InstallRecord[]; lines: string[] }> {
  const left: InstallRecord[] = [];
  const lines: string[] = [];
  for (const record of records) {
    try {
      lines.push(await remove(record));
    } catch (e) {
      lines.push(`${record.link} stays: ${decode.message(e)}`);
      left.push(record);
    }
  }
  return { left, lines };
}

/** `'<cli>' desktop-setup`, quoted for POSIX shells and fish. Windows has no
 *  login shell to miss an interactive PATH. */
export function terminalSetup(cli: string, platform: NodeJS.Platform): string | null {
  if (platform === "win32") return null;
  return `'${cli.replaceAll("'", "'\\''")}' desktop-setup`;
}

/** Native marketplace plugin installation and enablement, from `baum hooks
 *  status --user --json`. */
export interface Hooks {
  agents: AgentHooks[];
  error: string | null;
}

/** As serde wrote it: the optional CLI details are left out when unknown. */
export interface AgentHooks {
  trust: { state: string } | null;
  agent: string;
  name: string;
  detected: boolean;
  /** Whether the detected CLI is signed in; absent when baum could not tell. */
  signed_in?: boolean;
  /** The detected CLI and its version, the oldest version baum runs,
   *  whether this one is that or newer (absent when its version is
   *  unknown), and whether `baum desktop-setup` recorded its directory. */
  cli_path?: string;
  cli_version?: string;
  minimum_version?: string;
  supported?: boolean;
  recorded: boolean;
  plugin: string;
  installed: boolean;
  enabled: boolean;
  version: string | null;
  /** The plugin version the bundled baum shipped with, and whether the
   *  installed version is older, so Settings offers Update only then. */
  latest: string | null;
  update_available: boolean;
  error: string | null;
}

/** A field serde leaves out when it is None. */
function present<K extends string, V>(key: K, value: V | null): { [k in K]?: V } {
  return (value === null ? {} : { [key]: value }) as { [k in K]?: V };
}

function readAgentHooks(value: unknown): AgentHooks {
  const o = decode.object(value, "an agent's hooks");
  return {
    trust: decode.optional(o, "trust", (t) => ({
      state: decode.string(decode.object(t, "hook trust"), "state"),
    })),
    agent: decode.string(o, "agent"),
    name: decode.string(o, "name"),
    detected: decode.boolOr(o, "detected"),
    ...present("signed_in", decode.optionalBool(o, "signed_in")),
    ...present("cli_path", decode.optionalString(o, "cli_path")),
    ...present("cli_version", decode.optionalString(o, "cli_version")),
    ...present("minimum_version", decode.optionalString(o, "minimum_version")),
    ...present("supported", decode.optionalBool(o, "supported")),
    recorded: decode.boolOr(o, "recorded"),
    plugin: decode.string(o, "plugin"),
    installed: decode.boolOr(o, "installed"),
    enabled: decode.boolOr(o, "enabled"),
    version: decode.optionalString(o, "version"),
    latest: decode.optionalString(o, "latest"),
    update_available: decode.boolOr(o, "update_available"),
    error: decode.optionalString(o, "error"),
  };
}

export function readHooks(value: unknown): Hooks {
  const o = decode.object(value, "the hooks status");
  return {
    agents: decode.arrayOr(o, "agents", readAgentHooks),
    error: decode.optionalString(o, "error"),
  };
}

/** What a change printed, for the setup screen. */
export interface Outcome {
  ok: boolean;
  lines: string[];
}

/** What the setup screen shows. */
export interface View {
  first_run: boolean;
  home: string | null;
  command: Status;
  hooks: Hooks;
  /** What the user runs in a terminal so baum records the agents that
   *  terminal runs: the app's own baum by its full path, since the `baum`
   *  command may not be installed yet. */
  terminal_setup: string | null;
}

/** The last line of output that is not blank, read with `read`. */
export function lastJson<T>(lines: readonly string[], read: (value: unknown) => T): T {
  const line = lines.findLast((l) => l.trim() !== "");
  if (line === undefined) throw new Error("baum printed nothing.");
  try {
    return read(JSON.parse(line));
  } catch (e) {
    throw new Error(`baum printed an unreadable answer: ${decode.message(e)}`);
  }
}

/** The login shell's PATH and the baum on it, as the bundled baum reads it.
 *  If that fails, the app's own PATH stands in, with the reason. */
export async function loginPath(app: AppProcess, cli: string): Promise<LoginPath> {
  try {
    const out = await runBaum(app, cli, ["daemon", "login-path", "--json"], LOGIN_PATH_TIMEOUT);
    if (!out.ok) throw new Error(out.stderr.join(" "));
    return lastJson(out.stdout, install.readLoginPath);
  } catch (e) {
    const inherited = childPath(app) ?? app.env.PATH;
    return {
      path: inherited === undefined ? [] : splitPaths(inherited, app.platform),
      baum: null,
      error: `The baum command could not read your PATH: ${decode.message(e)}`,
      warning: null,
    };
  }
}

export async function hooksStatus(app: AppProcess, cli: string): Promise<Hooks> {
  try {
    const out = await runHooks(app, cli, ["hooks", "status", "--user", "--json"]);
    if (!out.ok) throw new Error(out.stderr.join(" "));
    const hooks = lastJson(out.stdout, readHooks);
    if (out.stderr.length > 0) hooks.error = out.stderr.join(" ");
    return hooks;
  } catch (e) {
    return { agents: [], error: decode.message(e) };
  }
}

// GUI launchers often omit shell/npm/Homebrew paths. Use the same login PATH
// resolution as command setup, without mutating the app's global environment.
export async function runHooks(
  app: AppProcess,
  cli: string,
  args: readonly string[],
): Promise<Output> {
  const started = Date.now();
  const login = await loginPath(app, cli);
  let path: string;
  try {
    path = joinPaths(login.path, app.platform);
  } catch (e) {
    throw new Error(`Cannot use the login shell's PATH for agent plugins: ${decode.message(e)}`);
  }
  const out = await runBaum(
    app,
    cli,
    args,
    Math.max(0, HOOKS_TIMEOUT - (Date.now() - started)),
    path,
  );
  if (login.error !== null) {
    out.stderr.unshift(`warning: ${login.error} Using the app's inherited PATH for agent plugins.`);
  }
  return out;
}

/** How the app generates reviews, as `baum internal reviews show` prints it. */
export interface ReviewSettings {
  backend: string;
  model: string;
  effort: string;
  automatic: boolean;
  pull_requests: string;
  /** How automatic reviews follow later pushes: always, on_open or
   *  on_change; empty leaves the saved choice. */
  updates: string;
  /** The fixes' agent, model and effort; an empty agent follows reviews. */
  fix_backend: string;
  fix_model: string;
  fix_effort: string;
  /** The extraction model and effort. Setup sends them only when it
   *  changes the review choice, which extraction then follows. */
  decisions_model?: string;
  decisions_effort?: string;
  /** The data extraction harness Settings chose, empty while it follows
   *  the reviews'; read only, never saved. */
  decisions_chosen_backend: string;
  /** The harness and model decision capture uses (empty when unknown),
   *  and whether it ignores `decisions_model`; read only, never saved. */
  decisions_backend: string;
  decisions_effective_model: string;
  decisions_model_ignored: boolean;
}

export function readReviewSettings(value: unknown): ReviewSettings {
  const o = decode.object(value, "review settings");
  return {
    backend: decode.string(o, "backend"),
    model: decode.string(o, "model"),
    effort: decode.stringOr(o, "effort"),
    automatic: decode.bool(o, "automatic"),
    pull_requests: decode.string(o, "pull_requests"),
    updates: decode.stringOr(o, "updates"),
    fix_backend: decode.stringOr(o, "fix_backend"),
    fix_model: decode.stringOr(o, "fix_model"),
    fix_effort: decode.stringOr(o, "fix_effort"),
    ...present("decisions_model", decode.optionalString(o, "decisions_model")),
    ...present("decisions_effort", decode.optionalString(o, "decisions_effort")),
    decisions_chosen_backend: decode.stringOr(o, "decisions_chosen_backend"),
    decisions_backend: decode.stringOr(o, "decisions_backend"),
    decisions_effective_model: decode.stringOr(o, "decisions_effective_model"),
    decisions_model_ignored: decode.boolOr(o, "decisions_model_ignored"),
  };
}

/** The `reviews set` flags for setup's choices. Extraction, when sent,
 *  follows the reviews' harness. */
export function reviewSetArgs(settings: ReviewSettings): string[] {
  // `--flag=value` keeps a value starting with `-` from reading as a flag.
  const args = [
    `--backend=${settings.backend}`,
    `--model=${settings.model}`,
    `--effort=${settings.effort}`,
    `--automatic=${settings.automatic}`,
    `--pull-requests=${settings.pull_requests}`,
    `--fix-backend=${settings.fix_backend}`,
  ];
  if (settings.updates !== "") args.push(`--updates=${settings.updates}`);
  if (settings.fix_backend !== "") {
    args.push(`--fix-model=${settings.fix_model}`, `--fix-effort=${settings.fix_effort}`);
  }
  if (settings.decisions_model !== undefined && settings.decisions_effort !== undefined) {
    args.push(
      "--decisions-backend=",
      `--decisions-model=${settings.decisions_model}`,
      `--decisions-effort=${settings.decisions_effort}`,
    );
  }
  return args;
}

/** The bundled repository step reaches only these local API actions; it
 *  cannot provide an arbitrary HTTP path, method or credential. */
export function repositoryRequest(
  action: string,
  repository: string | null,
  query: string | null,
  path: string | null,
  dir: string | null,
  documents: boolean,
): { method: string; route: string; body: unknown } {
  const identity = (): string => {
    if (repository === null) throw new Error("Choose a GitHub repository.");
    const parts = repository.split("/");
    if (
      parts.length !== 2 ||
      parts.some((p) => p === "" || p === "." || p === ".." || !/^[A-Za-z0-9_.-]+$/.test(p))
    ) {
      throw new Error("Invalid GitHub repository.");
    }
    return repository;
  };
  const base = () => `/api/repositories/${identity()}`;
  switch (action) {
    case "list":
    case "retry-list":
    case "more-list": {
      const params = new URLSearchParams();
      params.append("query", query ?? "");
      params.append("refresh", action === "retry-list" ? "1" : "0");
      params.append("more", action === "more-list" ? "1" : "0");
      return { method: "GET", route: `/api/github/repositories?${params}`, body: undefined };
    }
    case "picked":
      return { method: "GET", route: "/api/repositories", body: undefined };
    case "found":
      return { method: "GET", route: "/api/clones/found", body: undefined };
    case "refresh":
      return {
        method: "POST",
        route: "/api/clones/found/refresh",
        body: { include_documents: documents },
      };
    case "pick":
      return { method: "POST", route: "/api/repositories", body: { repository: identity() } };
    case "unpick":
      return { method: "DELETE", route: base(), body: {} };
    case "target":
      return { method: "GET", route: `${base()}/clone-target`, body: undefined };
    case "status":
      return { method: "GET", route: `${base()}/clone`, body: undefined };
    case "clone":
      return { method: "POST", route: `${base()}/clone`, body: dir !== null ? { dir } : {} };
    case "attach": {
      if (path === null) throw new Error("Choose a local folder.");
      const body: Record<string, unknown> = { path };
      if (repository !== null) body.repository = identity();
      return { method: "POST", route: "/api/repos", body };
    }
    default:
      throw new Error("Unknown repository action.");
  }
}

/** One change at a time, as the Rust shell's busy mutex. */
class Lock {
  private tail: Promise<unknown> = Promise.resolve();

  run<T>(work: () => Promise<T>): Promise<T> {
    const result = this.tail.then(work);
    this.tail = result.catch(() => undefined);
    return result;
  }
}

function text(body: Uint8Array): string {
  return Buffer.from(body).toString("utf8");
}

/** A command argument as Tauri deserialized it. */
class Args {
  constructor(
    private readonly command: CommandName,
    private readonly values: Record<string, unknown>,
  ) {}

  private invalid(key: string, expected: string): Error {
    return new Error(
      `invalid args \`${key}\` for command \`${this.command}\`: expected ${expected}`,
    );
  }

  string(key: string): string {
    const v = this.values[key];
    if (typeof v !== "string") throw this.invalid(key, "a string");
    return v;
  }

  optionalString(key: string): string | null {
    const v = this.values[key];
    if (v === undefined || v === null) return null;
    if (typeof v !== "string") throw this.invalid(key, "a string");
    return v;
  }

  optionalBool(key: string): boolean | null {
    const v = this.values[key];
    if (v === undefined || v === null) return null;
    if (typeof v !== "boolean") throw this.invalid(key, "a boolean");
    return v;
  }

  read<T>(key: string, read: (value: unknown) => T): T {
    try {
      return read(this.values[key]);
    } catch (e) {
      throw this.invalid(key, decode.message(e));
    }
  }
}

export type SetupPage = typeof SETUP_PAGE | typeof CONNECTING_PAGE;

export type SetupHandlers = Record<
  | "setup_state"
  | "install_command"
  | "uninstall_command"
  | "install_hooks"
  | "uninstall_hooks"
  | "update_hooks"
  | "choose_repository"
  | "add_repository"
  | "review_settings"
  | "save_review_settings"
  | "github_connection"
  | "repositories"
  | "finish_setup",
  Handler
>;

/** The setup screen's state while the app runs, and its commands. */
export class Setup<L> {
  /** The setup screen shows, so attaching to the daemon must not navigate
   *  away from it. */
  private open = false;
  private readonly busy = new Lock();
  /** Where the app keeps its stable copy of baum, once `start` set it up. */
  private stable: (() => string) | null = null;
  private readonly elevate: Elevate;
  readonly handlers: SetupHandlers;

  constructor(private readonly deps: SetupDeps<L>) {
    this.elevate = deps.elevate ?? systemPrompt(deps.app.platform);
    this.handlers = this.makeHandlers();
  }

  private get platform(): NodeJS.Platform {
    return this.deps.app.platform;
  }

  /** Prepares setup when the app starts and returns the bundled page the
   *  window opens on: the setup screen on first run, else the connecting
   *  screen. Where the app keeps a stable copy of baum (an AppImage,
   *  Windows), it refreshes it here, before anything runs baum, and runs that
   *  copy from then on, so updating the app updates the command and the
   *  daemon. */
  start(): SetupPage {
    const copy = this.managedCopy();
    if (copy !== null) {
      const bundled = this.deps.bundledCli;
      this.stable = () => {
        try {
          install.refreshCopy(bundled, copy, this.platform);
          return copy;
        } catch (e) {
          throw new Error(
            `Cannot copy baum out of the app to ${copy}: ${decode.message(e)}. Check that the folder can be written and the disk has room, then try again.`,
          );
        }
      };
    }
    // An app that cannot run baum shows why on the connecting screen.
    if (loadFrom(this.deps.settingsFile).onboarded || this.deps.misplaced() !== null) {
      return CONNECTING_PAGE;
    }
    this.open = true;
    return SETUP_PAGE;
  }

  /** Whether the setup screen shows, so the window must stay on it: the
   *  main process checks this before it sends the window to the daemon. */
  isOpen(): boolean {
    return this.open;
  }

  /** The menu item reopens the setup screen; the caller then shows the
   *  window and loads SETUP_PAGE. */
  markOpen(): void {
    this.open = true;
  }

  /** The `baum` binary the app runs (daemon.rs's `cli_path`):
   *  `BAUM_DESKTOP_BAUM` while developing, else the stable copy where the
   *  app keeps one (refreshed first; a copy that cannot be made is an error,
   *  never a fall back to the binary that would vanish), else the one
   *  bundled next to the executable. A macOS app on a read-only volume runs
   *  no baum. Throws the message for the user. */
  cliPath(): string {
    const override = this.deps.app.env.BAUM_DESKTOP_BAUM;
    if (override !== undefined) return override;
    const misplaced = this.deps.misplaced();
    if (misplaced !== null) throw new Error(misplaced);
    if (this.stable !== null) return this.stable();
    return this.deps.bundledCli;
  }

  /** Where the app keeps a stable copy of baum, if it does: an AppImage's
   *  files vanish when it quits, so the daemon and the command must not run
   *  them; on Windows the copy sits alone in a directory the user's PATH can
   *  name. */
  managedCopy(): string | null {
    const { app } = this.deps;
    if (cliOverridden(app) || !(app.platform === "win32" || runningAsAppImage(app))) return null;
    // Windows: the local, not the roaming, application data.
    const bin = join(this.deps.localDataDir, "bin", app.platform);
    return join(bin, app.platform === "win32" ? "baum.exe" : "baum", app.platform);
  }

  /** The stable path the command links to: the baum the app runs. */
  private target(cli: string): Target {
    const copy = this.managedCopy();
    if (copy !== null && !samePath(cli, copy, this.platform)) {
      return {
        error: `The app could not copy its baum to ${copy}, so the command cannot be installed. Restart the app to try again.`,
      };
    }
    return install.stableTarget(cli, this.platform);
  }

  private load(): Saved {
    return loadFrom(this.deps.settingsFile);
  }

  private async commandStatus(cli: string): Promise<Status> {
    const login = await loginPath(this.deps.app, cli);
    return install.status(
      this.platform,
      this.target(cli),
      login,
      this.load().commands,
      this.deps.home,
      this.deps.app.env,
    );
  }

  private async view(): Promise<View> {
    const cli = this.cliPath();
    return {
      first_run: !this.load().onboarded,
      home: this.deps.home === "" ? null : this.deps.home,
      command: await this.commandStatus(cli),
      hooks: await hooksStatus(this.deps.app, cli),
      terminal_setup: terminalSetup(cli, this.platform),
    };
  }

  private uninstallOne(record: InstallRecord, current: string | null): Promise<string> {
    const { home, app } = this.deps;
    return install.uninstall(this.platform, record, current, home, this.elevate, app.env);
  }

  /** Installs the command as the current plan says. `replace` names the
   *  different baum the user agreed to replace, which the plan must still
   *  name. */
  private installCommand(replace: string | null): Promise<Outcome> {
    return this.busy.run(async () => {
      const saved = readFrom(this.deps.settingsFile);
      const cli = this.cliPath();
      const status = await this.commandStatus(cli);
      if (status.installed) return { ok: true, lines: ["The baum command is installed."] };
      const { plan, target } = status;
      if (plan === null || target === null) {
        throw new Error(status.unavailable ?? "The baum command cannot be installed.");
      }
      const record = await install.install(
        this.platform,
        plan,
        target,
        replace,
        this.elevate,
        this.deps.app.env,
      );
      const lines = installedLines(plan, record, this.platform);
      // Links the app made elsewhere earlier (a baum earlier on the PATH
      // hid them) are removed, and stay recorded if that fails.
      const { kept, lines: removed } = await afterInstall(
        saved.commands,
        record,
        (r) => this.uninstallOne(r, target),
        this.platform,
      );
      lines.push(...removed);
      updateAt(this.deps.settingsFile, (s) => {
        s.commands = kept;
      });
      return { ok: true, lines };
    });
  }

  /** Removes the command the app installed, and only that. */
  private uninstallCommand(): Promise<Outcome> {
    return this.busy.run(async () => {
      const saved = readFrom(this.deps.settingsFile);
      if (saved.commands.length === 0) {
        return { ok: true, lines: ["The app has not installed a baum command."] };
      }
      // The copy the app runs stays: only the links go.
      let current: string | null = null;
      try {
        const target = this.target(this.cliPath());
        if ("path" in target) current = target.path;
      } catch {}
      const { left, lines } = await removeAll(saved.commands, (r) => this.uninstallOne(r, current));
      updateAt(this.deps.settingsFile, (s) => {
        s.commands = left;
      });
      return { ok: left.length === 0, lines };
    });
  }

  /** Runs the bundled `baum hooks <action> --user`: install for the agents
   *  it finds, update to the marketplace's latest version, or uninstall. */
  private changeHooks(action: string, agent: string | null): Promise<Outcome> {
    return this.busy.run(async () => {
      const args = ["hooks", action, "--user"];
      if (agent !== null) {
        if (agent !== "codex" && agent !== "claude-code") {
          throw new Error("Choose Codex or Claude Code.");
        }
        args.push("--agent", agent);
      }
      const out = await runHooks(this.deps.app, this.cliPath(), args);
      return { ok: out.ok, lines: [...out.stdout, ...out.stderr] };
    });
  }

  /** Register one existing clone without changing its agent or Git hooks. */
  private addRepository(path: string): Promise<Outcome> {
    return this.busy.run(async () => {
      let directory = false;
      try {
        directory = statSync(path).isDirectory();
      } catch {}
      if (!isAbsolute(path, this.platform) || !directory) {
        throw new Error("Choose an existing repository folder using an absolute path.");
      }
      const out = await runBaum(
        this.deps.app,
        this.cliPath(),
        ["internal", "repos", "add", "--", path],
        LOGIN_PATH_TIMEOUT,
      );
      return { ok: out.ok, lines: [...out.stdout, ...out.stderr] };
    });
  }

  private async reviewSettings(): Promise<ReviewSettings> {
    const out = await runBaum(
      this.deps.app,
      this.cliPath(),
      ["internal", "reviews", "show"],
      LOGIN_PATH_TIMEOUT,
    );
    if (!out.ok) throw new Error(out.stderr.join("\n"));
    try {
      return readReviewSettings(JSON.parse(out.stdout.join("\n")));
    } catch (e) {
      throw new Error(`Cannot read the review settings: ${decode.message(e)}`);
    }
  }

  /** Saves the review settings; the CLI validates every value. */
  private saveReviewSettings(settings: ReviewSettings): Promise<Outcome> {
    return this.busy.run(async () => {
      const out = await runBaum(
        this.deps.app,
        this.cliPath(),
        ["internal", "reviews", "set", ...reviewSetArgs(settings)],
        LOGIN_PATH_TIMEOUT,
      );
      return { ok: out.ok, lines: [...out.stdout, ...out.stderr] };
    });
  }

  private attached(): L {
    const launch = this.deps.launch();
    if (launch === null) throw new Error("baum is still starting. Try again in a moment.");
    return launch;
  }

  /** The GitHub step signs in through the attached daemon's own API, which
   *  runs the device flow and keeps the credential, so the screen never
   *  holds a token. It reads the connection, or starts, cancels or rechecks
   *  a sign-in; disconnecting stays in the app's settings. `open` opens
   *  GitHub's device page in the browser, a fixed address, a few seconds
   *  after a sign-in starts: the screen can no longer open a window then,
   *  and the window's navigations never open the browser. */
  private async githubConnection(action: string | null): Promise<unknown> {
    if (action === "open") {
      this.deps.openExternal(GITHUB_DEVICE_PAGE);
      return null;
    }
    const launch = this.attached();
    const { client } = this.deps;
    let response: DaemonResponse;
    if (action === null) {
      response = await client.request(launch, "GET", "/api/github-auth", null, POLL_TIMEOUT);
    } else if (action === "start" || action === "cancel" || action === "retry") {
      response = await client.request(
        launch,
        "POST",
        `/api/github-auth/${action}`,
        "{}",
        GITHUB_TIMEOUT,
      );
    } else {
      throw new Error(`Unknown GitHub action: ${action}`);
    }
    if (response.status !== 200) {
      throw new Error(`The baum daemon answered ${response.status}: ${text(response.body).trim()}`);
    }
    try {
      return JSON.parse(text(response.body));
    } catch (e) {
      throw new Error(`Cannot read the GitHub connection: ${decode.message(e)}`);
    }
  }

  private async repositories(
    action: string,
    repository: string | null,
    query: string | null,
    path: string | null,
    dir: string | null,
    includeDocuments: boolean,
  ): Promise<unknown> {
    const { method, route, body } = repositoryRequest(
      action,
      repository,
      query,
      path,
      dir,
      includeDocuments,
    );
    const launch = this.attached();
    const response = await this.deps.client.request(
      launch,
      method,
      route,
      body === undefined ? null : JSON.stringify(body),
      REPOSITORIES_TIMEOUT,
    );
    if (response.status < 200 || response.status >= 300) {
      let reason: unknown;
      try {
        reason = (JSON.parse(text(response.body)) as { message?: unknown } | null)?.message;
      } catch {}
      throw new Error(
        typeof reason === "string"
          ? reason
          : `The baum daemon answered ${response.status}: ${text(response.body).trim()}`,
      );
    }
    if (response.status === 204) return null;
    try {
      return JSON.parse(text(response.body));
    } catch (e) {
      throw new Error(`Cannot read repositories: ${decode.message(e)}`);
    }
  }

  /** Leaves the setup screen: records that first-run setup is done, returns
   *  to the connecting screen and attaches to the daemon, opening its inbox. */
  private finishSetup(): null {
    const first = !this.load().onboarded;
    updateAt(this.deps.settingsFile, (s) => {
      s.onboarded = true;
    });
    if (first) this.catchUp();
    this.open = false;
    this.deps.leaveSetup();
    return null;
  }

  /** Asks the daemon setup used to review the latest pull requests now, in
   *  the background, so reviews run while the tour shows. Best effort:
   *  without it automatic reviews start with the next push or review
   *  request. */
  private catchUp(): void {
    const launch = this.deps.launch();
    if (launch === null) return;
    this.deps.client.catchUp(launch).catch((error: unknown) => {
      console.error(
        `baum desktop: reviews of the open pull requests were not queued: ${decode.message(error)}`,
      );
    });
  }

  private makeHandlers(): SetupHandlers {
    const args = (command: CommandName, values: Record<string, unknown>) =>
      new Args(command, values);
    return {
      setup_state: () => this.view(),
      install_command: (a) =>
        this.installCommand(args("install_command", a).optionalString("replace")),
      uninstall_command: () => this.uninstallCommand(),
      install_hooks: (a) =>
        this.changeHooks("install", args("install_hooks", a).optionalString("agent")),
      update_hooks: (a) =>
        this.changeHooks("update", args("update_hooks", a).optionalString("agent")),
      uninstall_hooks: (a) =>
        this.changeHooks("uninstall", args("uninstall_hooks", a).optionalString("agent")),
      // The setup screen and the daemon's pages may call it: it changes
      // nothing and returns only the folder the user chose. With
      // `cloneTarget` it asks where a new clone should go rather than for an
      // existing clone.
      choose_repository: (a) =>
        this.deps.chooseFolder(
          args("choose_repository", a).optionalBool("cloneTarget")
            ? "Choose where to clone the repository"
            : "Choose a local GitHub repository",
        ),
      add_repository: (a) => this.addRepository(args("add_repository", a).string("path")),
      review_settings: () => this.reviewSettings(),
      save_review_settings: (a) =>
        this.saveReviewSettings(
          args("save_review_settings", a).read("settings", readReviewSettings),
        ),
      github_connection: (a) =>
        this.githubConnection(args("github_connection", a).optionalString("action")),
      // Tauri took this command's arguments in snake_case.
      repositories: (a) => {
        const r = args("repositories", a);
        return this.repositories(
          r.string("action"),
          r.optionalString("repository"),
          r.optionalString("query"),
          r.optionalString("path"),
          r.optionalString("dir"),
          r.optionalBool("include_documents") ?? false,
        );
      },
      finish_setup: () => this.finishSetup(),
    };
  }
}

function installedLines(plan: Plan, record: InstallRecord, platform: NodeJS.Platform): string[] {
  const lines = [`Installed the baum command: ${record.link} runs ${record.target}.`];
  if (record.backup !== null) {
    lines.push(
      `The baum that was there is kept as ${record.backup}; removing the command puts it back.`,
    );
  }
  if (!plan.on_path) {
    lines.push(
      `${parent(record.link, platform) ?? ""} is not on your PATH yet: add it in your shell's startup file, then open a new terminal.`,
    );
  }
  return lines;
}

/** Creates the setup state and registers the setup screen's commands. At
 *  startup the main process calls `start()` for the page the main window
 *  opens on and runs baum from `cliPath()` from then on. */
export function registerSetupCommands<L>(deps: SetupDeps<L>): Setup<L> {
  const setup = new Setup(deps);
  for (const [name, handler] of Object.entries(setup.handlers)) {
    registerCommand(name as CommandName, handler);
  }
  return setup;
}
