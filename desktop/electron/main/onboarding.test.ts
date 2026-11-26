import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readlinkSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { clearCommands, COMMANDS, dispatch, isCommand } from "./commands.ts";
import type { InstallRecord } from "./install.ts";
import {
  afterInstall,
  CONNECTING_PAGE,
  hooksStatus,
  lastJson,
  loadFrom,
  loginPath,
  readFrom,
  readHooks,
  readReviewSettings,
  registerSetupCommands,
  removeAll,
  repositoryRequest,
  reviewSetArgs,
  runHooks,
  SETUP_PAGE,
  type SetupDeps,
  terminalSetup,
  updateAt,
} from "./onboarding.ts";
import type { AppProcess } from "./run-baum.ts";
import { childPath, dropForeignMounts, inForeignMount, runBaum, withoutDir } from "./run-baum.ts";
import { FILE } from "./settings.ts";

const unix = process.platform !== "win32";
const app: AppProcess = {
  platform: process.platform,
  env: process.env,
  execPath: process.execPath,
};

function tempDir(name: string): string {
  return mkdtempSync(join(tmpdir(), `baum-setup-${name}-`));
}

/** A stand-in for the bundled baum, running `script` with sh. */
function fakeCli(dir: string, script: string): string {
  const path = join(dir, "baum");
  writeFileSync(path, `#!/bin/sh\n${script}\n`);
  chmodSync(path, 0o755);
  return path;
}

function record(link: string): InstallRecord {
  return { link, target: "/Applications/baum.app/Contents/MacOS/baum", backup: null, admin: false };
}

async function failure(promise: Promise<unknown>): Promise<string> {
  try {
    await promise;
  } catch (e) {
    return e instanceof Error ? e.message : String(e);
  }
  throw new Error("expected a failure");
}

describe("the repository relay", () => {
  it("allows only named actions and safe identities", () => {
    expect(() => repositoryRequest("DELETE", null, null, null, null, false)).toThrow();
    for (const name of [
      "../repo",
      "owner/../repo",
      "owner/repo\r\nX: a",
      "owner/repo?x",
      "owner/",
    ]) {
      expect(() => repositoryRequest("clone", name, null, null, null, false), name).toThrow();
    }
    let request = repositoryRequest("list", null, "x\r\nHeader: bad", null, null, false);
    expect(request.route).not.toMatch(/[\r\n]/);
    request = repositoryRequest("retry-list", null, "acme widget", null, null, false);
    expect(request.method).toBe("GET");
    expect(request.route).toBe("/api/github/repositories?query=acme+widget&refresh=1&more=0");
    request = repositoryRequest("more-list", null, "acme", null, null, false);
    expect(request.route).toBe("/api/github/repositories?query=acme&refresh=0&more=1");
    request = repositoryRequest("attach", "owner/repo", null, "/work/code/fork", null, false);
    expect(request.method).toBe("POST");
    expect(request.route).toBe("/api/repos");
    expect((request.body as { repository: string }).repository).toBe("owner/repo");
    request = repositoryRequest("refresh", null, null, null, null, true);
    expect((request.body as { include_documents: boolean }).include_documents).toBe(true);
  });
});

describe("setup", () => {
  it("sends fixes and changed extraction with the review settings", () => {
    const settings = (json: string) => readReviewSettings(JSON.parse(json));
    const base = `"backend":"codex","model":"gpt-6.1-sol","effort":"high","automatic":true,"pull_requests":"all"`;
    expect(reviewSetArgs(settings(`{${base}}`)).slice(5)).toEqual(["--fix-backend="]);
    expect(reviewSetArgs(settings(`{${base},"updates":"on_change"}`)).slice(5)).toEqual([
      "--fix-backend=",
      "--updates=on_change",
    ]);
    expect(
      reviewSetArgs(
        settings(
          `{${base},"fix_backend":"claude","fix_model":"claude-opus-5-5","fix_effort":"max","decisions_model":"gpt-6.1-sol","decisions_effort":"high"}`,
        ),
      ).slice(5),
    ).toEqual([
      "--fix-backend=claude",
      "--fix-model=claude-opus-5-5",
      "--fix-effort=max",
      "--decisions-backend=",
      "--decisions-model=gpt-6.1-sol",
      "--decisions-effort=high",
    ]);
  });

  it("quotes the app's baum for the terminal", () => {
    expect(terminalSetup("/home/user/.local/share/ai.baum/bin/baum", "linux")).toBe(
      "'/home/user/.local/share/ai.baum/bin/baum' desktop-setup",
    );
    expect(terminalSetup("/Applications/Ada's Baum.app/Contents/MacOS/baum", "darwin")).toBe(
      "'/Applications/Ada'\\''s Baum.app/Contents/MacOS/baum' desktop-setup",
    );
    expect(terminalSetup("C:\\baum.exe", "win32")).toBeNull();
  });

  it("round-trips the first-run state", () => {
    const path = join(tempDir("state"), "config", FILE);
    expect(loadFrom(path)).toEqual({ onboarded: false, commands: [] });
    updateAt(path, (current) => {
      current.onboarded = true;
      current.commands = [record("/home/user/.local/bin/baum")];
    });
    expect(loadFrom(path).onboarded).toBe(true);
    expect(loadFrom(path).commands).toEqual([record("/home/user/.local/bin/baum")]);
    if (unix) expect(statSync(path).mode & 0o777).toBe(0o600);
    writeFileSync(path, "not json");
    expect(loadFrom(path)).toEqual({ onboarded: false, commands: [] });
  });

  it("keeps finishing setup and recording commands apart", async () => {
    const path = join(tempDir("concurrent-setup"), FILE);
    await Promise.all([
      Promise.resolve().then(() =>
        updateAt(path, (saved) => {
          saved.onboarded = true;
        }),
      ),
      Promise.resolve().then(() =>
        updateAt(path, (saved) => {
          saved.commands = [record("/home/user/.local/bin/baum")];
        }),
      ),
    ]);
    const saved = loadFrom(path);
    expect(saved.onboarded).toBe(true);
    expect(saved.commands).toEqual([record("/home/user/.local/bin/baum")]);
  });

  it("cannot replace a damaged setup file", () => {
    const path = join(tempDir("corrupt-setup"), FILE);
    for (const setup of ["[]", "null", "true", `"bad"`]) {
      const data = `{"setup":${setup},"notifications":{"permission":"denied"}}`;
      writeFileSync(path, data);
      expect(() => readFrom(path)).toThrow();
      expect(() =>
        updateAt(path, (saved) => {
          saved.onboarded = true;
        }),
      ).toThrow();
      expect(readFileSync(path, "utf8")).toBe(data);
    }
  });

  /** An earlier link that cannot be removed (a cancelled password prompt)
   *  stays recorded beside the new one, and Remove tries every one. */
  it("keeps earlier links recorded until they are removed", async () => {
    const old = record("/usr/local/bin/baum");
    const other = record("/opt/tools/bin/baum");
    const added = record("/home/user/.local/bin/baum");
    const cancelled = async (r: InstallRecord) => {
      if (r.link === old.link) throw new Error("Cancelled.");
      return `Removed ${r.link}.`;
    };
    const { kept, lines } = await afterInstall([old, other, added], added, cancelled, "linux");
    expect(kept).toEqual([added, old]);
    expect(
      lines.some((l) =>
        l.includes("/usr/local/bin/baum stays (Cancelled.); Remove tries it again"),
      ),
    ).toBe(true);
    expect(lines).toContain("Removed /opt/tools/bin/baum.");

    const removed = await removeAll(kept, cancelled);
    expect(removed.left).toEqual([old]);
    expect(removed.lines).toHaveLength(2);
    const again = await removeAll(removed.left, async () => "");
    expect(again.left).toEqual([]);
  });

  // The Rust test also checked that navigation allows the bundled page;
  // that rule belongs to the navigation port.
  it("bundles the setup page", () => {
    expect(SETUP_PAGE).toBe("setup.html");
    expect(CONNECTING_PAGE).toBe("index.html");
  });

  it("preserves Codex hook trust status", () => {
    const hooks = lastJson(
      [
        `{"agents":[{"agent":"codex","name":"Codex","detected":true,"plugin":"baum@baum","installed":true,"enabled":true,"trust":{"state":"untrusted"}}]}`,
      ],
      readHooks,
    );
    expect(hooks.agents[0]?.trust?.state).toBe("untrusted");
  });

  it("reads the hooks status", () => {
    const hooks = lastJson(
      [
        `{"agents":[{"agent":"claude-code","name":"Claude Code","detected":true,"plugin":"baum@baum","installed":true,"enabled":false},{"agent":"codex","name":"Codex","detected":false,"plugin":"baum@baum","installed":false,"error":"not valid JSON"}]}`,
      ],
      readHooks,
    );
    expect(hooks.agents).toHaveLength(2);
    expect(hooks.agents[0]?.installed && hooks.agents[0]?.detected).toBe(true);
    expect(hooks.agents[0]?.enabled).toBe(false);
    expect(hooks.agents[0]?.plugin).toBe("baum@baum");
    expect(hooks.agents[1]?.error).toBe("not valid JSON");
    expect(() => lastJson([], readHooks)).toThrow();
  });

  it("writes hooks and review settings as serde did", () => {
    const hooks = readHooks({
      agents: [
        { agent: "codex", name: "Codex", plugin: "baum@baum", cli_version: "0.166", extra: 1 },
      ],
    });
    expect(JSON.stringify(hooks)).toBe(
      `{"agents":[{"trust":null,"agent":"codex","name":"Codex","detected":false,"cli_version":"0.166","recorded":false,"plugin":"baum@baum","installed":false,"enabled":false,"version":null,"latest":null,"update_available":false,"error":null}],"error":null}`,
    );
    expect(() => readHooks({ agents: [{ agent: "codex", name: "Codex" }] })).toThrow();
    expect(() =>
      readReviewSettings({ backend: "codex", model: "", automatic: null, pull_requests: "all" }),
    ).toThrow();
  });
});

describe.skipIf(!unix)("the bundled baum", () => {
  it("reads the login path", async () => {
    const cli = fakeCli(
      tempDir("login-path"),
      `[ "$*" = "daemon login-path --json" ] || exit 9
echo '{"shell":"/bin/zsh","login_shell":true,"path":["/home/user/.local/bin","/usr/bin"],"baum":{"path":"/home/user/go/bin/baum","resolved":"/home/user/go/bin/baum","version":"v0.2.9","self":false}}'`,
    );
    const login = await loginPath(app, cli);
    expect(login.path).toHaveLength(2);
    expect(login.baum?.version).toBe("v0.2.9");
    expect(login.error).toBeNull();
  });

  it("falls back to the app's PATH when baum cannot read it", async () => {
    const cli = fakeCli(tempDir("login-path-fails"), "echo 'unknown command' >&2; exit 1");
    const login = await loginPath(app, cli);
    expect(login.baum).toBeNull();
    expect(login.error).toContain("unknown command");
  });

  it("uses the login PATH for native plugin status, install and removal", async () => {
    const cli = fakeCli(
      tempDir("plugin-login-path"),
      `
if [ "$*" = "daemon login-path --json" ]; then
  echo '{"path":["/resolved/login/agents"]}'
  exit 0
fi
[ "$PATH" = "/resolved/login/agents" ] || { echo 'wrong plugin PATH' >&2; exit 9; }
case "$*" in
  'hooks status --user --json') echo '{"agents":[{"agent":"codex","name":"Codex","detected":true,"plugin":"baum@baum","installed":true,"enabled":true}]}' ;;
  'hooks install --user'|'hooks uninstall --user') echo 'native plugin command saw login PATH' ;;
  *) exit 8 ;;
esac
`,
    );
    const hooks = await hooksStatus(app, cli);
    expect(hooks.agents).toHaveLength(1);
    expect(hooks.agents[0]?.detected && hooks.agents[0]?.enabled).toBe(true);
    expect(hooks.error).toBeNull();
    for (const action of ["install", "uninstall"]) {
      const out = await runHooks(app, cli, ["hooks", action, "--user"]);
      expect(out.ok, out.stderr.join("\n")).toBe(true);
      expect(out.stdout).toEqual(["native plugin command saw login PATH"]);
    }
  });

  it("keeps native plugin status and shows the login PATH fallback", async () => {
    const cli = fakeCli(
      tempDir("plugin-login-path-fails"),
      `
if [ "$*" = "daemon login-path --json" ]; then
  echo 'login shell unavailable' >&2
  exit 1
fi
printf '%s\\n' "$PATH"
echo '{"agents":[{"agent":"codex","name":"Codex","detected":true,"plugin":"baum@baum","installed":false,"enabled":false}]}'
`,
    );
    const hooks = await hooksStatus(app, cli);
    expect(hooks.agents).toHaveLength(1);
    const warning = hooks.error ?? "";
    expect(warning).toContain("login shell unavailable");
    expect(warning).toContain("inherited PATH");
    for (const action of ["install", "uninstall"]) {
      const out = await runHooks(app, cli, ["hooks", action, "--user"]);
      expect(out.ok).toBe(true);
      expect(out.stdout[0]).toBe(process.env.PATH ?? "");
      expect(out.stderr[0]).toContain("login shell unavailable");
    }
  });

  it("times out a hanging command", async () => {
    const cli = fakeCli(tempDir("hangs"), "exec sleep 30");
    const started = Date.now();
    const err = await failure(runBaum(app, cli, ["hooks"], 300));
    expect(err).toContain("did not finish");
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it("reports a failing hooks status command", async () => {
    const cli = fakeCli(tempDir("hooks-fail"), "echo 'find the home directory: no' >&2; exit 1");
    const hooks = await hooksStatus(app, cli);
    expect(hooks.agents).toEqual([]);
    expect(hooks.error).toContain("home directory");
  });
});

describe.skipIf(!unix)("the setup commands", () => {
  afterEach(() => clearCommands());

  function deps(root: string, overrides: Partial<SetupDeps<string>> = {}): SetupDeps<string> {
    return {
      app,
      home: join(root, "home"),
      settingsFile: join(root, "config", FILE),
      localDataDir: join(root, "data"),
      bundledCli: fakeCli(root, "echo '{\"path\":[]}'"),
      misplaced: () => null,
      launch: () => null,
      client: {
        request: async () => ({ status: 200, body: Buffer.from("{}") }),
        catchUp: async () => undefined,
      },
      chooseFolder: async () => null,
      openExternal: () => undefined,
      leaveSetup: () => undefined,
      ...overrides,
    };
  }

  const context = { window: null, role: "setup-screen" as const, origin: "app://setup" };

  it("registers every setup command the table lists", () => {
    registerSetupCommands(deps(tempDir("register")));
    for (const name of [
      "setup_state",
      "install_command",
      "uninstall_command",
      "install_hooks",
      "uninstall_hooks",
      "update_hooks",
      "choose_repository",
      "add_repository",
      "review_settings",
      "save_review_settings",
      "github_connection",
      "repositories",
      "finish_setup",
    ]) {
      expect(isCommand(name) && Object.hasOwn(COMMANDS, name), name).toBe(true);
    }
  });

  it("opens setup on first run and connects once it is finished", async () => {
    const root = tempDir("first-run");
    let left = 0;
    const setup = registerSetupCommands(
      deps(root, {
        leaveSetup: () => {
          left++;
        },
      }),
    );
    expect(setup.start()).toBe(SETUP_PAGE);
    expect(setup.isOpen()).toBe(true);
    const view = (await dispatch("setup_state", {}, context)) as { first_run: boolean };
    expect(view.first_run).toBe(true);
    expect(await dispatch("finish_setup", {}, context)).toBeNull();
    expect(setup.isOpen()).toBe(false);
    expect(left).toBe(1);
    expect(setup.start()).toBe(CONNECTING_PAGE);
    expect(
      await failure(dispatch("install_hooks", { agent: "cursor" }, context) as Promise<unknown>),
    ).toBe("Choose Codex or Claude Code.");
    expect(await failure(dispatch("github_connection", {}, context) as Promise<unknown>)).toContain(
      "still starting",
    );
  });

  // setup.ts's tests, from before the onboarding port decided the first page.
  it("opens on the connecting screen once setup is finished or baum cannot run", () => {
    const root = tempDir("first-page");
    const settings = join(root, "config", FILE);
    mkdirSync(join(root, "config"), { recursive: true });
    const misplaced = registerSetupCommands(deps(root, { misplaced: () => "on a disk image" }));
    expect(misplaced.start()).toBe(CONNECTING_PAGE);
    expect(misplaced.isOpen()).toBe(false);
    clearCommands();
    writeFileSync(settings, JSON.stringify({ setup: { onboarded: "yes" } }));
    expect(registerSetupCommands(deps(root)).start()).toBe(SETUP_PAGE);
    clearCommands();
    writeFileSync(settings, JSON.stringify({ setup: { onboarded: true, commands: [] } }));
    expect(registerSetupCommands(deps(root)).start()).toBe(CONNECTING_PAGE);
  });

  it("runs an AppImage's baum from its copy, without the mount on the PATH", () => {
    const root = tempDir("appimage");
    const appdir = join(root, "mount");
    mkdirSync(join(appdir, "usr/bin"), { recursive: true });
    const appImage: AppProcess = {
      platform: "linux",
      env: {
        APPIMAGE: join(root, "baum.AppImage"),
        APPDIR: appdir,
        PATH: `${appdir}/usr/bin:/usr/bin`,
      },
      execPath: join(appdir, "usr/bin/baum-desktop"),
    };
    const setup = registerSetupCommands(deps(root, { app: appImage }));
    setup.start();
    const copy = join(root, "data/bin/baum");
    expect(setup.managedCopy()).toBe(copy);
    expect(setup.cliPath()).toBe(copy);
    expect(childPath(appImage)).toBe("/usr/bin");
    expect(statSync(copy).mode & 0o777).toBe(0o755);
  });

  it("drops the AppImage mount from the child's PATH", () => {
    expect(
      withoutDir(
        "/tmp/.mount_baum-dfeJFK/usr/bin:/home/user/.local/bin:/tmp/.mount_baum-dfeJFK:/tmp/.mount_baum-dfeJFKG/usr/bin:/usr/bin",
        "/tmp/.mount_baum-dfeJFK",
        "linux",
      ),
    ).toBe("/home/user/.local/bin:/tmp/.mount_baum-dfeJFKG/usr/bin:/usr/bin");
    const env = {
      APPIMAGE: "/x.AppImage",
      APPDIR: "/tmp/.mount_a",
      PATH: "/tmp/.mount_a/usr/bin:/usr/bin",
    };
    const app = (execPath: string, e: NodeJS.ProcessEnv = env): AppProcess => ({
      platform: "linux",
      env: e,
      execPath,
    });
    expect(childPath(app("/tmp/.mount_a/usr/bin/baum-desktop"))).toBe("/usr/bin");
    // A child of an AppImage inherits APPIMAGE but runs from elsewhere.
    expect(childPath(app("/usr/bin/code"))).toBeNull();
    expect(childPath(app("/usr/bin/baum-desktop", { PATH: "/usr/bin" }))).toBeNull();
  });

  it("drops another AppImage's mount, inherited from the app that started this one", () => {
    // The Tauri app's updater started the Electron app with its environment.
    const old = "/tmp/.mount_baum-dBdkACK";
    const own = "/tmp/.mount_baum-dLjg5nt";
    const env: NodeJS.ProcessEnv = {
      APPIMAGE: "/home/user/Applications/baum.AppImage",
      APPDIR: own,
      PATH: `${old}/usr/bin/:${old}/usr/sbin/:/home/user/.local/bin:/usr/bin`,
      LD_LIBRARY_PATH: `${old}/usr/lib/:${old}/usr/lib/x86_64-linux-gnu/`,
      PYTHONPATH: `${old}/usr/share/pyshared/:`,
      GTK_EXE_PREFIX: `${old}//usr`,
      XDG_DATA_DIRS: `${old}/usr/share/:${own}/usr/share:/usr/share`,
      HOME: "/home/user",
    };
    const app: AppProcess = { platform: "linux", env, execPath: `${own}/baum-desktop` };
    expect(dropForeignMounts(app).sort()).toEqual([
      "GTK_EXE_PREFIX",
      "LD_LIBRARY_PATH",
      "PATH",
      "PYTHONPATH",
      "XDG_DATA_DIRS",
    ]);
    expect(env).toEqual({
      APPIMAGE: "/home/user/Applications/baum.AppImage",
      APPDIR: own,
      PATH: "/home/user/.local/bin:/usr/bin",
      XDG_DATA_DIRS: `${own}/usr/share:/usr/share`,
      HOME: "/home/user",
    });
    expect(dropForeignMounts(app)).toEqual([]);
    expect(inForeignMount(app, `${old}/usr`)).toBe(true);
    expect(inForeignMount(app, own)).toBe(false);
    expect(inForeignMount(app, "/home/user")).toBe(false);
  });

  it("treats every mount as another's when the app is not an AppImage", () => {
    const env: NodeJS.ProcessEnv = { PATH: "/tmp/.mount_baum-x/usr/bin:/usr/bin" };
    expect(
      dropForeignMounts({ platform: "linux", env, execPath: "/usr/bin/baum-desktop" }),
    ).toEqual(["PATH"]);
    expect(env.PATH).toBe("/usr/bin");
    const mac: NodeJS.ProcessEnv = { PATH: "/tmp/.mount_baum-x/usr/bin:/usr/bin" };
    expect(dropForeignMounts({ platform: "darwin", env: mac, execPath: "/x" })).toEqual([]);
  });
});

describe.skipIf(!unix)("the Tauri app's command links on macOS", () => {
  afterEach(() => clearCommands());
  const runningAsRoot = process.getuid?.() === 0;

  /** The updated app at `root`: its executable is the Tauri app's baum
   *  path, its bundled baum is in Resources/bin, and desktop.json records
   *  the Tauri app's links to the old path, in `~/.local/bin` and, unless
   *  `system` is null, in `system`. */
  function updated(root: string, system: string | null) {
    const contents = join(root, "Applications/baum.app/Contents");
    const exe = join(contents, "MacOS/baum");
    mkdirSync(join(contents, "MacOS"), { recursive: true });
    writeFileSync(exe, "the app");
    mkdirSync(join(contents, "Resources/bin"), { recursive: true });
    const bundled = fakeCli(join(contents, "Resources/bin"), "exit 0");
    const links = [join(root, "home/.local/bin/baum")];
    if (system !== null) links.push(join(system, "baum"));
    for (const link of links) {
      mkdirSync(join(link, ".."), { recursive: true });
      symlinkSync(exe, link);
    }
    const settingsFile = join(root, "config", FILE);
    mkdirSync(join(root, "config"), { recursive: true });
    const commands = links.map((link) => ({ link, target: exe, backup: null, admin: false }));
    writeFileSync(settingsFile, JSON.stringify({ setup: { onboarded: true, commands } }));
    let prompts = 0;
    const deps: SetupDeps<string> = {
      app: { platform: "darwin", env: {}, execPath: exe },
      home: join(root, "home"),
      settingsFile,
      localDataDir: join(root, "data"),
      bundledCli: bundled,
      misplaced: () => null,
      launch: () => null,
      client: {
        request: async () => ({ status: 200, body: Buffer.from("{}") }),
        catchUp: async () => undefined,
      },
      chooseFolder: async () => null,
      openExternal: () => undefined,
      leaveSetup: () => undefined,
      elevate: {
        async run() {
          prompts++;
        },
      },
    };
    return { exe, bundled, links, settingsFile, deps, prompts: () => prompts };
  }

  it("moves a link the user can write at once, without a prompt", async () => {
    const w = updated(tempDir("tauri-links"), null);
    const setup = registerSetupCommands(w.deps);
    const moving = setup.moveTauriLinks();
    // Before the promise settles: nothing may run baum through it first.
    expect(readlinkSync(w.links[0] ?? "")).toBe(w.bundled);
    await moving;
    expect(w.prompts()).toBe(0);
    expect(readFrom(w.settingsFile).commands).toEqual([
      { link: w.links[0], target: w.bundled, backup: null, admin: false },
    ]);
    // Done: the next start finds nothing to move.
    clearCommands();
    await registerSetupCommands(w.deps).moveTauriLinks();
    expect(w.prompts()).toBe(0);
  });

  it.skipIf(runningAsRoot)(
    "prompts for no folder outside the system ones, and leaves its link",
    async () => {
      const root = tempDir("tauri-locked");
      const locked = join(root, "opt/bin");
      const w = updated(root, locked);
      chmodSync(locked, 0o555);
      try {
        await registerSetupCommands(w.deps).moveTauriLinks();
      } finally {
        chmodSync(locked, 0o755);
      }
      // ~/.local/bin moved; opt/bin is the user's, not root's: no prompt,
      // and setup offers Install for it.
      expect(w.prompts()).toBe(0);
      expect(readlinkSync(w.links[0] ?? "")).toBe(w.bundled);
      expect(readlinkSync(w.links[1] ?? "")).toBe(w.exe);
      expect(readFrom(w.settingsFile).commands.map((c) => c.target)).toEqual([w.bundled, w.exe]);
    },
  );

  it("does nothing elsewhere than macOS, or with a development baum", async () => {
    const w = updated(tempDir("tauri-other"), null);
    await registerSetupCommands({
      ...w.deps,
      app: { ...w.deps.app, platform: "linux" },
    }).moveTauriLinks();
    clearCommands();
    await registerSetupCommands({
      ...w.deps,
      app: { ...w.deps.app, env: { BAUM_DESKTOP_BAUM: "/dev/baum" } },
    }).moveTauriLinks();
    expect(readlinkSync(w.links[0] ?? "")).toBe(w.exe);
  });
});
