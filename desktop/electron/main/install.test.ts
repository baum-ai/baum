import {
  chmodSync,
  closeSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  unlinkSync,
  utimesSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { capture, type Elevate, scriptFailure } from "./elevation.ts";
import {
  endsHash,
  Fnv1a64,
  type Found,
  type InstallRecord,
  type LoginPath,
  refreshCopy,
  type Status,
  stableTarget,
  stampPath,
} from "./install.ts";
import {
  INSTALL_SCRIPT,
  install,
  linksAt,
  mayElevate,
  packageManaged,
  repoint,
  repointAsRoot,
  type Rules,
  status,
  statusWith,
  UNINSTALL_SCRIPT,
  uninstall,
  uninstallWith,
  writable,
} from "./install-unix.ts";

const unix = process.platform !== "win32";
const runningAsRoot = process.getuid?.() === 0;

/** Runs the administrator scripts as this user, so tests exercise them in
 *  directories this user can write. */
const shell: Elevate = {
  async run(script, args) {
    const output = await capture("/bin/sh", ["-c", script, "sh", ...args]);
    if (output.code === 0) return;
    throw new Error(scriptFailure(output.code, output.stderr));
  },
};

/** Fails the test if a privileged script would run. */
const noPrompt: Elevate = {
  async run() {
    throw new Error("asked for an administrator");
  },
};

const lexists = (p: string) => {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
};

const exists = (p: string) => {
  try {
    statSync(p);
    return true;
  } catch {
    return false;
  }
};

async function failure(promise: Promise<unknown>): Promise<string> {
  try {
    await promise;
  } catch (e) {
    return e instanceof Error ? e.message : String(e);
  }
  throw new Error("expected a failure");
}

function executable(path: string, body: string) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `#!/bin/sh\necho ${body}\n`);
  chmodSync(path, 0o755);
}

const worlds: World[] = [];
afterEach(() => {
  for (const w of worlds.splice(0)) rmSync(w.root, { recursive: true, force: true });
});

class World {
  readonly root: string;
  readonly home: string;
  readonly app: string;

  constructor(name: string) {
    this.root = mkdtempSync(join(tmpdir(), `baum-install-${name}-`));
    this.home = join(this.root, "home");
    this.app = join(this.root, "Applications/baum.app/Contents/MacOS/baum");
    mkdirSync(this.home, { recursive: true });
    executable(this.app, "app");
    worlds.push(this);
  }

  localBin(): string {
    return join(this.home, ".local/bin");
  }

  /** What login-path reports for this PATH: the first executable baum. */
  login(path: string[]): LoginPath {
    const baum = path
      .map((dir) => join(dir, "baum"))
      .find((p) => {
        try {
          const m = statSync(p);
          return m.isFile() && (m.mode & 0o111) !== 0;
        } catch {
          return false;
        }
      });
    return {
      path,
      baum:
        baum === undefined ? null : { path: baum, resolved: realpathSync(baum), version: "v0.2.9" },
      error: null,
      warning: null,
    };
  }

  status(path: string[], record?: InstallRecord): Status {
    return this.statusFor(this.app, path, record);
  }

  statusFor(target: string, path: string[], record?: InstallRecord): Status {
    return status({ path: target }, this.login(path), record ? [record] : [], this.home);
  }

  /** As `status`, with every directory outside the home one root may
   *  change, so the administrator scripts can be exercised. */
  statusElevating(path: string[]): Status {
    const rules: Rules = {
      home: this.home,
      target: this.app,
      writable,
      mayElevate: (dir) => !dir.startsWith(this.home),
    };
    return statusWith({ path: this.app }, this.login(path), [], rules);
  }
}

function planOf(s: Status) {
  if (s.plan === null) throw new Error("no plan");
  return s.plan;
}

describe.skipIf(!unix)("the baum command on Unix", () => {
  it("installs into ~/.local/bin when no baum is on the PATH", async () => {
    const w = new World("fresh");
    const path = [w.localBin(), "/usr/bin"];
    const before = w.status(path);
    expect(before.installed || before.found !== null).toBe(false);
    const plan = planOf(before);
    expect(plan.link).toBe(join(w.localBin(), "baum"));
    expect(plan.replaces === null && !plan.admin && plan.on_path && plan.blocked === null).toBe(
      true,
    );

    const record = await install(plan, w.app, null, noPrompt);
    expect(readlinkSync(record.link)).toBe(w.app);
    const after = w.status(path, record);
    expect(after.installed && after.plan === null && after.shadowed === null).toBe(true);
    expect(after.record).toEqual(record);
  });

  it("prefers a personal directory and falls back to /usr/local/bin", () => {
    const w = new World("choice");
    const homeBin = join(w.home, "bin");
    const usrLocal = "/usr/local/bin";
    let plan = planOf(w.status([usrLocal, homeBin]));
    expect(plan.link).toBe(join(homeBin, "baum"));
    plan = planOf(w.status([usrLocal]));
    expect(plan.link).toBe(join(usrLocal, "baum"));
    expect(plan.admin).toBe(!writable(usrLocal));
    // Nothing suitable on the PATH: ~/.local/bin, flagged as off the PATH.
    plan = planOf(w.status(["/usr/bin"]));
    expect(plan.link).toBe(join(w.localBin(), "baum"));
    expect(plan.on_path).toBe(false);
  });

  it("lets root change only system directories", () => {
    const w = new World("elevate");
    expect(mayElevate(w.localBin(), w.home)).toBe(false);
    expect(mayElevate(join(w.home, "anything/bin"), w.home)).toBe(false);
    // Owned by this user, not root (unless the tests run as root).
    const mine = join(w.root, "opt/bin");
    mkdirSync(mine, { recursive: true });
    expect(mayElevate(mine, w.home)).toBe(runningAsRoot);
    // Reached through a symbolic link.
    const linked = join(w.root, "linked");
    symlinkSync("/usr", linked);
    expect(mayElevate(join(linked, "bin"), w.home)).toBe(false);
    expect(mayElevate("relative/bin", w.home)).toBe(false);
    // /usr/bin and its parents are root's, canonical and writable by no
    // one else on every Unix the app supports.
    expect(mayElevate("/usr/bin", w.home)).toBe(true);
    // A directory to create needs an existing parent.
    expect(mayElevate("/usr/no-such-dir/bin", w.home)).toBe(false);
    // Below a directory this user owns (unless root runs the tests).
    expect(mayElevate(join(mine, "bin"), w.home)).toBe(runningAsRoot);
  });

  it.skipIf(runningAsRoot)("refuses a home directory that would need a password", async () => {
    const w = new World("home-admin");
    const locked = join(w.home, "tools/bin");
    executable(join(locked, "baum"), "theirs");
    chmodSync(locked, 0o555);
    const plan = planOf(w.status([locked]));
    chmodSync(locked, 0o755);
    expect(plan.admin).toBe(true);
    const why = plan.blocked ?? "";
    expect(why).toContain("system directories");
    expect(await failure(install(plan, w.app, join(locked, "baum"), noPrompt))).toBe(why);
  });

  it.skipIf(runningAsRoot)(
    "the administrator script refuses a directory root does not own",
    async () => {
      const w = new World("admin");
      const locked = join(w.root, "locked");
      const other = join(locked, "baum");
      executable(other, "other");
      chmodSync(locked, 0o555);
      const plan = planOf(w.statusElevating([locked]));
      chmodSync(locked, 0o755);
      expect(plan.admin && plan.blocked === null).toBe(true);
      // Had the app's check passed for a directory that is not root's (it
      // was swapped meanwhile), the script still refuses it. The scripts'
      // work as root is exercised by a harness run as root in a container.
      expect(await failure(install(plan, w.app, other, shell))).toBe(scriptFailure(4, ""));
      expect(readFileSync(other, "utf8")).toBe("#!/bin/sh\necho other\n");
      const record: InstallRecord = { link: other, target: w.app, backup: null, admin: true };
      unlinkSync(other);
      symlinkSync(w.app, other);
      const refused = await failure(
        uninstallWith(
          record,
          w.app,
          shell,
          () => false,
          () => true,
        ),
      );
      expect(refused).toBe(scriptFailure(4, ""));
      expect(lexists(other)).toBe(true);
    },
  );

  /** The scripts root runs check the paths themselves, whatever the app
   *  passes them. */
  it("the administrator scripts refuse other paths", async () => {
    const w = new World("scripts");
    const dir = join(w.root, "bin");
    mkdirSync(dir, { recursive: true });
    const victim = join(w.root, "victim");
    writeFileSync(victim, "keep");
    const none = "";
    const refused = scriptFailure(4, "");
    const run = (script: string, args: string[]) => failure(shell.run(script, args));
    const link = join(dir, "baum");
    expect(await run(INSTALL_SCRIPT, [dir, join(dir, "sh"), none, w.app])).toBe(refused);
    expect(await run(INSTALL_SCRIPT, [w.root, link, none, w.app])).toBe(refused);
    expect(await run(UNINSTALL_SCRIPT, [victim, w.app, none])).toBe(refused);
    for (const backup of [
      victim,
      `${link}.replaced-by-baum-app.1/../../victim`,
      `${link}.replaced-by-baum-app.1\nevil`,
      `${link}.replaced-by-baum-app.`,
      `${link}.replaced-by-baum-app.1x`,
    ]) {
      expect(await run(INSTALL_SCRIPT, [dir, link, backup, w.app]), backup).toBe(refused);
      expect(await run(UNINSTALL_SCRIPT, [link, w.app, backup]), backup).toBe(refused);
    }
    // An option as the target, and relative paths.
    expect(await run(INSTALL_SCRIPT, [dir, link, none, "-t/etc"])).toBe(refused);
    const relative = "rel";
    expect(await run(INSTALL_SCRIPT, [relative, `${relative}/baum`, none, w.app])).toBe(refused);
    expect(await run(UNINSTALL_SCRIPT, [`${relative}/baum`, w.app, none])).toBe(refused);
    expect(await run(UNINSTALL_SCRIPT, [link, "rel/baum", none])).toBe(refused);
    // An empty target never removes a regular file.
    writeFileSync(link, "regular");
    expect(await run(UNINSTALL_SCRIPT, [link, none, none])).toBe(refused);
    expect(readFileSync(link, "utf8")).toBe("regular");
    expect(readFileSync(victim, "utf8")).toBe("keep");
  });

  /** A record edited to name other paths is never acted on. */
  it("refuses a tampered record", async () => {
    const w = new World("tampered");
    const path = [w.localBin()];
    const record = await install(planOf(w.status(path)), w.app, null, noPrompt);
    const victim = join(w.root, "victim");
    writeFileSync(victim, "keep");
    for (const tampered of [
      { ...record, link: victim },
      { ...record, backup: victim },
      { ...record, backup: join(w.localBin(), "baum.replaced-by-baum-app.x") },
      { ...record, target: "baum" },
      // Written out: node:path's join would resolve the `..`.
      { ...record, target: `${w.root}/Applications/../victim/baum` },
      { ...record, target: join(w.root, "Applications/sh") },
      { ...record, target: join(w.root, "binaries/baum-x86_64-unknown-linux-gnu") },
    ]) {
      const err = await failure(uninstallWith(tampered, w.app, noPrompt, writable, () => true));
      expect(err).toContain("not what the app installs");
      expect(w.status(path, tampered).record).toBeNull();
    }
    // A link the user cannot write, for another target: no prompt.
    const elsewhere = { ...record, target: join(w.root, "other-app/baum") };
    symlinkSync(elsewhere.target, join(w.root, "dangling"));
    unlinkSync(record.link);
    symlinkSync(elsewhere.target, record.link);
    const err = await failure(
      uninstallWith(
        elsewhere,
        w.app,
        noPrompt,
        () => false,
        () => true,
      ),
    );
    expect(err).toContain("Remove it yourself");
    expect(readFileSync(victim, "utf8")).toBe("keep");
  });

  /** A baum from `go install` comes first on the PATH: it is shown, and
   *  replaced only once the user confirmed that path; uninstalling puts it
   *  back. */
  it("replaces a go-installed baum only after confirmation", async () => {
    const w = new World("go");
    const goBin = join(w.home, "go/bin");
    const theirs = join(goBin, "baum");
    executable(theirs, "go");
    const path = [goBin, w.localBin()];
    const before = w.status(path);
    expect(before.installed).toBe(false);
    expect(before.found?.path).toBe(theirs);
    const plan = planOf(before);
    expect(plan.link).toBe(theirs);
    expect(plan.blocked).toBeNull();
    expect(plan.replaces?.version).toBe("v0.2.9");
    expect(plan.backup).toBe(join(goBin, "baum.replaced-by-baum-app"));

    expect(await failure(install(plan, w.app, null, noPrompt))).toContain("Confirm");
    await failure(install(plan, w.app, join(w.localBin(), "baum"), noPrompt));
    expect(readFileSync(theirs, "utf8")).toBe("#!/bin/sh\necho go\n");

    const record = await install(plan, w.app, theirs, noPrompt);
    expect(readlinkSync(theirs)).toBe(w.app);
    expect(w.status(path, record).installed).toBe(true);

    await uninstall(record, w.app, w.home, noPrompt);
    expect(readFileSync(theirs, "utf8")).toBe("#!/bin/sh\necho go\n");
    expect(lexists(join(goBin, "baum.replaced-by-baum-app"))).toBe(false);
  });

  /** A baum a package manager owns is not replaced in place: the screen
   *  says to remove it with that manager. */
  it("leaves package-managed baums to their manager", () => {
    const found = (path: string, resolved: string): Found => ({ path, resolved, version: "" });
    for (const managed of [
      found("/opt/homebrew/bin/baum", "/opt/homebrew/Cellar/baum/0.3.0/bin/baum"),
      found("/usr/local/bin/baum", "/usr/local/Cellar/baum/0.3.0/bin/baum"),
      found("/run/current-system/sw/bin/baum", "/nix/store/abc-baum/bin/baum"),
      found("/usr/bin/baum", "/usr/bin/baum"),
      found("/sbin/baum", "/sbin/baum"),
      found("/opt/u/.asdf/shims/baum", "/opt/u/.asdf/shims/baum"),
      found("/opt/u/.local/share/mise/shims/baum", "/opt/u/.local/share/mise/shims/baum"),
      found("/opt/local/bin/baum", "/opt/local/bin/baum"),
      found("/snap/bin/baum", "/snap/baum/12/bin/baum"),
      found("/opt/homebrew/bin/baum", "/opt/homebrew/Caskroom/baum/0.3.0/baum"),
      found(
        "/opt/u/.local/share/aquaproj-aqua/bin/baum",
        "/opt/u/.local/share/aquaproj-aqua/pkgs/baum",
      ),
      found("/opt/u/.volta/bin/baum", "/opt/u/.volta/tools/image/baum"),
    ]) {
      expect(packageManaged(managed), managed.path).toBe(true);
    }
    for (const own of [
      found("/opt/u/go/bin/baum", "/opt/u/go/bin/baum"),
      found("/usr/local/bin/baum", "/usr/local/bin/baum"),
      found("/opt/u/.local/bin/baum", "/opt/baum/baum"),
    ]) {
      expect(packageManaged(own), own.path).toBe(false);
    }

    const w = new World("brew");
    const cellar = join(w.root, "homebrew/Cellar/baum/0.3.0/bin/baum");
    executable(cellar, "brew");
    const bin = join(w.root, "homebrew/bin");
    mkdirSync(bin, { recursive: true });
    symlinkSync(cellar, join(bin, "baum"));
    const plan = planOf(w.status([bin]));
    expect(plan.blocked).toContain("brew uninstall baum");
  });

  /** After the app is deleted, its link points nowhere: no baum is found,
   *  and installing again (with or without the old record) replaces the
   *  broken link without asking. */
  it("replaces a link left by a deleted app without asking", async () => {
    const w = new World("deleted");
    const path = [w.localBin()];
    const record = await install(planOf(w.status(path)), w.app, null, noPrompt);
    rmSync(join(w.root, "Applications"), { recursive: true });
    const link = join(w.localBin(), "baum");
    expect(lexists(link) && !exists(link)).toBe(true);

    const moved = join(w.root, "Elsewhere/baum.app/Contents/MacOS/baum");
    executable(moved, "moved");
    for (const r of [record, undefined]) {
      unlinkSync(link);
      symlinkSync(w.app, link);
      const s = w.statusFor(moved, path, r);
      expect(!s.installed && s.found === null).toBe(true);
      const plan = planOf(s);
      expect(plan.link).toBe(link);
      expect(plan.replaces).toBeNull();
      await install(plan, moved, null, noPrompt);
      expect(readlinkSync(link)).toBe(moved);
    }
  });

  /** After the app replaced a go-installed baum and then moved, updating
   *  its link keeps the backup, so removing the command still puts the
   *  original back. */
  it("keeps the backup of what it replaced when updating its link", async () => {
    const w = new World("keep-backup");
    const goBin = join(w.home, "go/bin");
    const theirs = join(goBin, "baum");
    executable(theirs, "go");
    const path = [goBin];
    const first = await install(planOf(w.status(path)), w.app, theirs, noPrompt);
    const backup = first.backup ?? "";
    expect(backup).not.toBe("");

    const moved = join(w.root, "Moved/baum.app/Contents/MacOS/baum");
    executable(moved, "moved");
    const plan = planOf(w.statusFor(moved, path, first));
    expect(plan.replaces === null && plan.backup === null).toBe(true);
    expect(plan.kept_backup).toBe(backup);
    const updated = await install(plan, moved, null, noPrompt);
    expect(updated.backup).toBe(backup);
    expect(readlinkSync(theirs)).toBe(moved);
    expect(readFileSync(backup, "utf8")).toBe("#!/bin/sh\necho go\n");

    await uninstall(updated, moved, w.home, noPrompt);
    expect(readFileSync(theirs, "utf8")).toBe("#!/bin/sh\necho go\n");
    expect(lexists(backup)).toBe(false);
  });

  /** The app's own link to an older copy of the app is updated in place
   *  without confirmation. */
  it("updates its own link without asking", async () => {
    const w = new World("update");
    const path = [w.localBin()];
    const record = await install(planOf(w.status(path)), w.app, null, noPrompt);
    const newer = join(w.root, "New/baum.app/Contents/MacOS/baum");
    executable(newer, "new");
    const s = w.statusFor(newer, path, record);
    expect(s.installed).toBe(false);
    const plan = planOf(s);
    expect(plan.replaces === null && plan.link === join(w.localBin(), "baum")).toBe(true);
    await install(plan, newer, null, noPrompt);
    expect(readlinkSync(join(w.localBin(), "baum"))).toBe(newer);
  });

  /** Uninstalling removes only the link the app recorded, and leaves a link
   *  or file someone put there since. */
  it("uninstall removes only the app's link", async () => {
    const w = new World("uninstall");
    const path = [w.localBin()];
    const record = await install(planOf(w.status(path)), w.app, null, noPrompt);
    let text = await uninstall(record, w.app, w.home, noPrompt);
    expect(text.startsWith("Removed the baum command")).toBe(true);
    expect(lexists(record.link)).toBe(false);
    expect(exists(w.app)).toBe(true);

    // Someone replaced the link with their own baum: it stays, and a
    // backup the app made is named.
    executable(record.link, "mine");
    const backup = join(w.localBin(), "baum.replaced-by-baum-app");
    writeFileSync(backup, "old");
    text = await uninstall({ ...record, backup }, w.app, w.home, noPrompt);
    expect(text).toContain("left alone");
    expect(text).toContain(backup);
    expect(readFileSync(record.link, "utf8")).toBe("#!/bin/sh\necho mine\n");
    // And the record no longer describes it.
    expect(w.status(path, record).record).toBeNull();
  });

  /** A file named baum in the chosen directory that is not a working baum
   *  on the PATH is still replaced only with confirmation. */
  it("needs confirmation for something else at the link", async () => {
    const w = new World("occupied");
    const bin = w.localBin();
    executable(join(bin, "baum"), "off-path");
    const plan = planOf(w.status(["/usr/bin"]));
    expect(plan.link).toBe(join(bin, "baum"));
    expect(plan.replaces !== null && !plan.on_path).toBe(true);
    await failure(install(plan, w.app, null, noPrompt));
  });

  it("refreshes the copy only when the app or the copy changed", () => {
    const w = new World("copy");
    const copy = join(w.root, "data/bin/baum");
    const refresh = () => refreshCopy(w.app, copy, process.platform);
    expect(refresh()).toBe(true);
    expect(refresh()).toBe(false);
    expect(statSync(copy).mode & 0o777).toBe(0o755);
    expect(statSync(dirname(copy)).mode & 0o777).toBe(0o755);
    // A temporary file a crash left behind does not stop the next copy.
    const stale = join(dirname(copy), `.baum-${process.pid}`);
    writeFileSync(stale, "stale");
    // An updated app: a different size.
    executable(w.app, "updated");
    expect(refresh()).toBe(true);
    expect(readFileSync(copy)).toEqual(readFileSync(w.app));
    expect(lexists(stale)).toBe(false);
    // An update of the same size landing at once, within the same tick
    // of the file clock, and with its old times put back.
    const times = statSync(w.app);
    writeFileSync(w.app, readFileSync(w.app, "utf8").replace("updated", "patched"));
    utimesSync(w.app, times.atime, times.mtime);
    expect(refresh()).toBe(true);
    expect(readFileSync(copy)).toEqual(readFileSync(w.app));
    expect(refresh()).toBe(false);
    // The same size and a new modification time.
    utimesSync(w.app, times.atime, 0);
    expect(refresh()).toBe(true);
    expect(refresh()).toBe(false);
    // A copy someone rewrote heals, whatever its size, and so does a
    // missing stamp.
    writeFileSync(copy, readFileSync(w.app, "utf8").replace("patched", "changed"));
    expect(refresh()).toBe(true);
    expect(readFileSync(copy)).toEqual(readFileSync(w.app));
    unlinkSync(stampPath(copy, process.platform));
    expect(refresh()).toBe(true);
    expect(refresh()).toBe(false);
    // A running daemon keeps the file it started from.
    const running = openSync(copy, "r");
    executable(w.app, "newer again");
    expect(refresh()).toBe(true);
    closeSync(running);
    expect(stableTarget(copy, process.platform)).toEqual({ path: copy });
  });

  /** A baum in the home directory never gets a link that needs a password:
   *  the link goes in ~/.local/bin, and when a baum in a system directory
   *  would still come first, the screen says so. */
  it("never links a baum in the home directory as root", () => {
    const w = new World("home-target");
    const copy = join(w.home, ".local/share/app/bin/baum");
    executable(copy, "copy");
    const rules: Rules = {
      home: w.home,
      target: copy,
      writable: (dir) => !dir.startsWith("/usr"),
      mayElevate: () => true,
    };
    const s = (path: string[], baum: Found | null) =>
      statusWith({ path: copy }, { path, baum, error: null, warning: null }, [], rules);
    // Only /usr/local/bin on the PATH: ~/.local/bin, not on the PATH.
    let plan = planOf(s(["/usr/local/bin"], null));
    expect(plan.link).toBe(join(w.localBin(), "baum"));
    expect(!plan.admin && !plan.on_path && plan.blocked === null).toBe(true);
    // A baum in a system directory first: ~/.local/bin works only before it.
    const theirs: Found = {
      path: "/usr/local/bin/baum",
      resolved: "/usr/local/bin/baum",
      version: "",
    };
    plan = planOf(s([w.localBin(), "/usr/local/bin"], theirs));
    expect(plan.link).toBe(join(w.localBin(), "baum"));
    expect(plan.blocked === null && !plan.admin).toBe(true);
    plan = planOf(s(["/usr/local/bin", w.localBin()], theirs));
    expect(plan.blocked).toContain("home folder");
  });

  it("is installed when the PATH already runs the bundled baum", () => {
    // A package install (the deb puts the bundled baum in /usr/bin).
    const w = new World("package");
    const bin = join(w.root, "usr/bin");
    mkdirSync(bin, { recursive: true });
    symlinkSync(w.app, join(bin, "baum"));
    const s = w.status([bin]);
    expect(s.installed && s.record === null && s.plan === null).toBe(true);
  });
});

// The Tauri app linked the command to baum.app/Contents/MacOS/baum, the
// Electron app's own executable now; the app moves those links to its
// bundled baum when it starts.
describe.skipIf(!unix)("moving the Tauri app's links", () => {
  /** An app whose old baum path (`from`) is the executable now, its bundled
   *  baum (`to`), and the record of a link to `from` in `dir`. */
  function moved(w: World, dir: string) {
    const from = w.app;
    const to = join(w.root, "Applications/baum.app/Contents/Resources/bin/baum");
    executable(to, "bundled");
    const link = join(dir, "baum");
    mkdirSync(dir, { recursive: true });
    symlinkSync(from, link);
    const record: InstallRecord = {
      link,
      target: from,
      backup: join(dir, "baum.replaced-by-baum-app"),
      admin: false,
    };
    return { from, to, record };
  }

  /** Records each prompt instead of showing it. */
  function prompts() {
    const asked: string[][] = [];
    const elevate: Elevate = {
      async run(script, args) {
        expect(script).toBe(INSTALL_SCRIPT);
        asked.push([...args]);
      },
    };
    return { asked, elevate };
  }

  it("moves a link in a folder the user can write without asking", () => {
    const w = new World("tauri-writable");
    const { from, to, record } = moved(w, w.localBin());
    // Recorded as needing an administrator back then, but writable now.
    const found = linksAt([{ ...record, admin: true }], from);
    expect(found).toEqual({ writable: [{ ...record, admin: true }], system: [] });
    const after = repoint(record, to);
    expect(readlinkSync(record.link)).toBe(to);
    // The backup of a baum the link replaced back then stays recorded.
    expect(after).toEqual({ ...record, target: to, admin: false });
    // Moved, and so no longer found.
    expect(linksAt([record], from)).toEqual({ writable: [], system: [] });
  });

  it("leaves links someone changed, and other records", () => {
    const w = new World("tauri-changed");
    const { from, record } = moved(w, w.localBin());
    unlinkSync(record.link);
    executable(record.link, "theirs");
    const other = moved(w, join(w.root, "elsewhere/bin"));
    const shapeless = { ...other.record, link: join(w.root, "elsewhere/bin/notbaum") };
    expect(linksAt([record, shapeless], from)).toEqual({ writable: [], system: [] });
    expect(linksAt([other.record], join(w.root, "some/other/baum"))).toEqual({
      writable: [],
      system: [],
    });
  });

  it("asks for a password only for a system folder", async () => {
    const w = new World("tauri-system");
    const dir = join(w.root, "usr/local/bin");
    const { from, to, record } = moved(w, dir);
    expect(linksAt([record], from, () => false).system).toEqual([record]);
    const { asked, elevate } = prompts();
    const after = await repointAsRoot(record, to, w.home, elevate, () => true);
    expect(asked).toEqual([[dir, record.link, "", to]]);
    expect(after).toEqual({ ...record, target: to, admin: true });
    // Not a folder root may change, or a baum in the home folder: no prompt.
    expect(await failure(repointAsRoot(record, to, w.home, elevate, () => false))).toContain(
      "does not ask for an administrator's password",
    );
    const inHome = join(w.home, "Applications/baum.app/Contents/Resources/bin/baum");
    expect(await failure(repointAsRoot(record, inHome, w.home, elevate, () => true))).toContain(
      "does not ask for an administrator's password",
    );
    expect(asked).toHaveLength(1);
  });
});

describe("the copy's stamp", () => {
  it("hashes with 64-bit FNV-1a", () => {
    const hash = new Fnv1a64();
    expect(hash.hex()).toBe("cbf29ce484222325");
    hash.feed(Buffer.from("a"));
    expect(hash.hex()).toBe("af63dc4c8601ec8c");
    const foobar = new Fnv1a64();
    foobar.feed(Buffer.from("foobar"));
    expect(foobar.hex()).toBe("85944171f73967e8");
  });

  it("hashes a file's first and last 64 KiB", () => {
    const dir = mkdtempSync(join(tmpdir(), "baum-hash-"));
    const file = join(dir, "big");
    const data = Buffer.alloc(200 * 1024, 7);
    writeFileSync(file, data);
    const before = endsHash(file);
    // The middle is not read; the end is.
    data[100 * 1024] = 9;
    writeFileSync(file, data);
    expect(endsHash(file)).toBe(before);
    data[150 * 1024] = 9;
    writeFileSync(file, data);
    expect(endsHash(file)).not.toBe(before);
    rmSync(dir, { recursive: true });
  });
});
