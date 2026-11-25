import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import { FILE } from "./settings.ts";
import { releaseSignature, testKey } from "./testdata/minisign-key.ts";
import {
  CANDIDATE_FEED,
  CHECK_INTERVAL,
  CLOCK_TICK,
  copyInstalls,
  due,
  isTestVersion,
  enabled,
  FIRST_CHECK_DELAY,
  feedUrl,
  JOBS_POLL,
  RELEASE_PUBLIC_KEY,
  registerUpdaterCommands,
  releasePage,
  remember,
  remembered,
  STABLE_FEED,
  STALL_TIMEOUT,
  signatureUrl,
  TEST_FEED,
  TEST_PUBLIC_KEY,
  testOverrides,
  Updater,
  type UpdaterBackend,
  type UpdaterDeps,
  updaterKind,
} from "./updater.ts";

function scratch(): string {
  return mkdtempSync(join(tmpdir(), "baum-desktop-updates-"));
}

/** Lets promises and file reads settle without moving the fake clock, until
 *  `done` if given. */
async function settle(done?: () => boolean): Promise<void> {
  for (let i = 0; done ? !done() && i < 5000 : i < 20; i++) {
    await stat(tmpdir());
    await new Promise((resolve) => setImmediate(resolve));
  }
}

async function advance(ms: number, done?: () => boolean): Promise<void> {
  await vi.advanceTimersByTimeAsync(ms);
  await settle(done);
}

/** Whether a check or download is under way. */
function busy(updater: Updater): boolean {
  return ["checking", "downloading"].includes(updater.view().phase);
}

const KEY = testKey();
const BUNDLE = Buffer.from("baum 1.2.4");

/** electron-updater as the updater sees it: a feed offering `offered`, whose
 *  download is `bundle`. */
class FakeBackend implements UpdaterBackend {
  offered: string | null = "1.2.4";
  checks = 0;
  downloads = 0;
  installs = 0;
  checkError: Error | null = null;
  installError: Error | null = null;
  /** When set, the download waits for the test, which may feed it data. */
  hang: { onData: () => void } | null = null;

  constructor(readonly dir: string) {}

  async check(): Promise<string | null> {
    this.checks++;
    if (this.checkError) throw this.checkError;
    return this.offered;
  }

  async download(onData: () => void, signal: AbortSignal) {
    this.downloads++;
    if (this.hang) {
      this.hang.onData = onData;
      await new Promise((_resolve, reject) => {
        signal.addEventListener("abort", () => reject(signal.reason));
      });
    }
    const file = join(this.dir, "baum-desktop-linux-amd64.AppImage");
    writeFileSync(file, BUNDLE);
    return { version: this.offered ?? "", file };
  }

  async install(): Promise<void> {
    this.installs++;
    if (this.installError) throw this.installError;
  }
}

interface Harness {
  updater: Updater;
  backend: FakeBackend;
  deps: UpdaterDeps;
  fetched: string[];
  jobs: number[];
  calls: string[];
}

function harness(overrides: Partial<UpdaterDeps> = {}, signature?: string): Harness {
  const dir = scratch();
  const backend = new FakeBackend(dir);
  const fetched: string[] = [];
  const jobs: number[] = [];
  const calls: string[] = [];
  const deps: UpdaterDeps = {
    version: "1.2.3",
    packaged: true,
    setting: undefined,
    installs: true,
    settingsPath: join(dir, "config", FILE),
    backend,
    publicKey: KEY.publicFile,
    fetch: async (url) => {
      fetched.push(url);
      return new Response(signature ?? releaseSignature(KEY, BUNDLE, "1.2.4"));
    },
    runningJobs: async () => {
      calls.push("jobs");
      return jobs.shift() ?? 0;
    },
    stopDaemon: async () => {
      calls.push("stop");
    },
    restartDaemon: async () => {
      calls.push("restart");
    },
    log: () => {},
    ...overrides,
  };
  return { updater: new Updater(deps), backend, deps, fetched, jobs, calls };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
  clearCommands();
});

describe("configuration", () => {
  it("only packaged release builds check", () => {
    expect(enabled("1.2.3", true, undefined)).toBe(true);
    expect(enabled("1.2.3", true, "1")).toBe(true);
    expect(enabled("0.0.0", true, undefined)).toBe(false);
    expect(enabled("1.2.3", false, undefined)).toBe(false);
    for (const off of ["0", "false", "off", "no", " 0 "]) {
      expect(enabled("1.2.3", true, off), off).toBe(false);
    }
  });

  // Release candidates follow every release; full releases only full ones.
  it("release candidates read their own feed", () => {
    for (const candidate of ["1.2.3-rc.1", "1.2.3-rc.10+linux"]) {
      expect(feedUrl(candidate), candidate).toBe(CANDIDATE_FEED);
    }
    for (const release of ["1.2.3", "1.2.3+build-1"]) {
      expect(feedUrl(release), release).toBe(STABLE_FEED);
    }
    expect(STABLE_FEED).toBe("https://github.com/baum-ai/baum/releases/latest/download/");
    expect(CANDIDATE_FEED).toBe(
      "https://github.com/baum-ai/baum/releases/download/desktop-rc-updates/",
    );
  });

  // The signature comes from the update's own release whatever the channel:
  // the release-candidate channel holds only the update files.
  it("reads a signature from the release being installed", () => {
    expect(signatureUrl("1.2.4", "/cache/pending/baum-desktop-linux-amd64.AppImage")).toBe(
      "https://github.com/baum-ai/baum/releases/download/v1.2.4/baum-desktop-linux-amd64.AppImage.sig",
    );
    expect(signatureUrl("1.2.4-rc.1", "/cache/baum desktop.zip")).toBe(
      "https://github.com/baum-ai/baum/releases/download/v1.2.4-rc.1/baum%20desktop.zip.sig",
    );
    expect(signatureUrl("1.2.4", "/cache/x.AppImage", "http://127.0.0.1:8000/feed/")).toBe(
      "http://127.0.0.1:8000/feed/x.AppImage.sig",
    );
  });

  // A released app never honours the test variables: its version is never a
  // test version (release.yml refuses to release one).
  it("honours a test feed and key only in a test build", () => {
    const key = Buffer.from("untrusted comment: minisign public key: 1\nRWQ=\n").toString("base64");
    const env = { [TEST_FEED]: "http://127.0.0.1:8000/feed", [TEST_PUBLIC_KEY]: key };
    for (const version of ["1.2.3-test.1", "1.2.3-test", "1.2.3-test.2.a+linux"]) {
      expect(isTestVersion(version), version).toBe(true);
      expect(testOverrides(version, env), version).toEqual({
        feed: "http://127.0.0.1:8000/feed/",
        publicKey: "untrusted comment: minisign public key: 1\nRWQ=\n",
      });
    }
    for (const version of ["1.2.3", "1.2.3-rc.1", "1.2.3-rc.1-test.1", "1.2.3-testing", "0.0.0"]) {
      expect(isTestVersion(version), version).toBe(false);
      expect(testOverrides(version, env), version).toBeNull();
    }
    expect(testOverrides("1.2.3-test.1", {})).toBeNull();
    expect(testOverrides("1.2.3-test.1", { [TEST_FEED]: "http://h/f/" })).toEqual({
      feed: "http://h/f/",
      publicKey: undefined,
    });
  });

  it("checks once a day by the wall clock", () => {
    const last = 1_000_000_000;
    expect(due(last, last + 60_000)).toBe(false);
    expect(due(last, last - 60_000)).toBe(false);
    expect(due(last, last + CHECK_INTERVAL)).toBe(true);
  });

  it("links to the release page", () => {
    expect(releasePage("1.2.3")).toBe("https://github.com/baum-ai/baum/releases/tag/v1.2.3");
  });

  // The release workflow signs bundles with the key whose public half its
  // scripts read from updater-key.pub.
  it("trusts only the release key", () => {
    const key = Buffer.from(RELEASE_PUBLIC_KEY, "base64").toString("utf8");
    expect(key.startsWith("untrusted comment: minisign public key: C42ACF774FCC1D5F\n")).toBe(true);
    expect(readFileSync(join(import.meta.dirname, "..", "..", "updater-key.pub"), "utf8")).toBe(
      key,
    );
  });

  it("only copies the updater can replace install", () => {
    const writable = () => true;
    const readOnly = () => false;
    const appImage = {
      platform: "linux" as const,
      execPath: "/tmp/.mount_baumXYZ/usr/bin/baum-desktop",
      env: { APPIMAGE: "/home/alice/Apps/baum.AppImage", APPDIR: "/tmp/.mount_baumXYZ" },
      writable,
    };
    expect(copyInstalls(appImage)).toBe(true);
    // An AppImage in a directory the user cannot write.
    const dirs: string[] = [];
    expect(
      copyInstalls({
        ...appImage,
        writable: (dir) => {
          dirs.push(dir);
          return false;
        },
      }),
    ).toBe(false);
    expect(dirs).toEqual(["/home/alice/Apps"]);
    // A deb, or variables inherited from another AppImage.
    expect(copyInstalls({ ...appImage, env: {} })).toBe(false);
    expect(copyInstalls({ ...appImage, env: { APPIMAGE: appImage.env.APPIMAGE } })).toBe(false);
    expect(copyInstalls({ ...appImage, execPath: "/usr/lib/baum-desktop/baum-desktop" })).toBe(
      false,
    );
    expect(copyInstalls({ ...appImage, execPath: "/tmp/.mount_baumXYZ-other/baum" })).toBe(false);

    const mac = {
      platform: "darwin" as const,
      execPath: "/Applications/baum.app/Contents/MacOS/baum",
      env: {},
      writable,
    };
    expect(copyInstalls(mac)).toBe(true);
    expect(copyInstalls({ ...mac, writable: readOnly })).toBe(false);
    expect(
      copyInstalls({
        ...mac,
        execPath: "/private/var/folders/x/T/AppTranslocation/ABC/d/baum.app/Contents/MacOS/baum",
      }),
    ).toBe(false);

    expect(copyInstalls({ platform: "win32", execPath: "C:\\baum.exe", env: {}, writable })).toBe(
      true,
    );
    expect(copyInstalls({ platform: "freebsd", execPath: "/baum", env: {}, writable })).toBe(false);
  });
});

// What each kind of copy does with an update: which electron-updater
// updater checks, and whether the app downloads and installs it or only
// announces it.
describe("package types", () => {
  const writable = () => true;
  const readOnly = () => false;
  const mount = "/tmp/.mount_baumXYZ";
  const cases: {
    name: string;
    platform: NodeJS.Platform;
    execPath: string;
    env?: Record<string, string>;
    writable?: (dir: string) => boolean;
    packaged?: boolean;
    kind: string | null;
    checks: boolean;
    installs: boolean;
  }[] = [
    {
      name: "AppImage in a writable directory",
      platform: "linux",
      execPath: `${mount}/baum-desktop`,
      env: { APPIMAGE: "/home/alice/Apps/baum-desktop-linux-amd64.AppImage", APPDIR: mount },
      kind: "appimage",
      checks: true,
      installs: true,
    },
    {
      name: "AppImage in a directory the user cannot write",
      platform: "linux",
      execPath: `${mount}/baum-desktop`,
      env: { APPIMAGE: "/opt/baum-desktop-linux-amd64.AppImage", APPDIR: mount },
      writable: readOnly,
      kind: "appimage",
      checks: true,
      installs: false,
    },
    {
      name: "deb",
      platform: "linux",
      execPath: "/opt/baum/baum-desktop",
      kind: "deb",
      checks: true,
      installs: false,
    },
    {
      name: "unpackaged (development)",
      platform: "linux",
      execPath: "/oss/desktop/node_modules/electron/dist/electron",
      packaged: false,
      kind: "deb",
      checks: false,
      installs: false,
    },
    {
      name: "macOS app in Applications",
      platform: "darwin",
      execPath: "/Applications/baum.app/Contents/MacOS/baum-desktop",
      kind: "mac",
      checks: true,
      installs: true,
    },
    {
      name: "translocated macOS app",
      platform: "darwin",
      execPath:
        "/private/var/folders/x/T/AppTranslocation/ABC/d/baum.app/Contents/MacOS/baum-desktop",
      kind: "mac",
      checks: true,
      installs: false,
    },
    {
      name: "macOS app on its read-only disk image",
      platform: "darwin",
      execPath: "/Volumes/baum/baum.app/Contents/MacOS/baum-desktop",
      writable: readOnly,
      kind: "mac",
      checks: true,
      installs: false,
    },
    {
      name: "Windows install",
      platform: "win32",
      execPath: "C:\\Users\\alice\\AppData\\Local\\Programs\\baum\\baum-desktop.exe",
      kind: "nsis",
      checks: true,
      installs: true,
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      const env = c.env ?? {};
      expect(updaterKind(c.platform, env)).toBe(c.kind);
      expect(enabled("1.2.3", c.packaged ?? true, undefined)).toBe(c.checks);
      expect(
        copyInstalls({
          platform: c.platform,
          execPath: c.execPath,
          env,
          writable: c.writable ?? writable,
        }),
      ).toBe(c.installs);
    });
  }

  it("leaves other platforms alone", () => {
    expect(updaterKind("freebsd", {})).toBeNull();
  });
});

describe("remembered updates", () => {
  it("remembers failed installs", () => {
    const path = join(scratch(), "config", FILE);
    expect(remembered(path).failed_installs).toEqual([]);
    remember(path, "failed_installs", "1.2.3");
    remember(path, "failed_installs", "1.2.3");
    remember(path, "failed_installs", "1.2.4");
    remember(path, "rejected_downloads", "1.2.5");
    expect(remembered(path)).toEqual({
      failed_installs: ["1.2.3", "1.2.4"],
      rejected_downloads: ["1.2.5"],
    });
    // A damaged file does not block checks, but must not be overwritten.
    writeFileSync(path, "not json");
    expect(remembered(path).failed_installs).toEqual([]);
    expect(() => remember(path, "failed_installs", "1.2.4")).toThrow();
    expect(readFileSync(path, "utf8")).toBe("not json");
  });
});

describe("state", () => {
  it("is what Settings reads", () => {
    expect(JSON.parse(JSON.stringify(harness().updater.view()))).toStrictEqual({
      version: "1.2.3",
      enabled: true,
      installs: true,
      phase: "idle",
      available: null,
      page: null,
      checked_at: null,
      jobs: 0,
      error: null,
      noticed: null,
    });
  });

  it("asks for a check only when updates are on", () => {
    const off = harness({ version: "0.0.0" }).updater;
    expect(() => off.requestCheck()).toThrow("This build does not check for updates.");
    const { updater } = harness();
    expect(updater.requestCheck().phase).toBe("checking");
  });

  it("installs only a ready update, and only in a copy that can replace itself", () => {
    expect(() => harness().updater.requestInstall()).toThrow("No update is ready to install.");
    const announce = harness({ installs: false });
    expect(() => announce.updater.requestInstall()).toThrow("No update is ready to install.");
  });

  it("remembers a closed notice until the app quits", () => {
    const { updater } = harness();
    expect(updater.dismissNotice("1.2.4").noticed).toBe("1.2.4");
    expect(updater.view().noticed).toBe("1.2.4");
    // A new app starts with nothing noticed.
    expect(harness().updater.view().noticed).toBe(null);
  });
});

describe("checks", () => {
  it("checks 30 seconds after launch, then once a day", async () => {
    const { updater, backend } = harness({ installs: false });
    updater.start();
    await advance(FIRST_CHECK_DELAY - 1000);
    expect(backend.checks).toBe(0);
    await advance(1000);
    expect(backend.checks).toBe(1);
    const view = updater.view();
    expect(view.available).toBe("1.2.4");
    expect(view.page).toBe("https://github.com/baum-ai/baum/releases/tag/v1.2.4");
    expect(view.checked_at).toBe(Math.floor(Date.now() / 1000));
    await advance(CHECK_INTERVAL - CLOCK_TICK);
    expect(backend.checks).toBe(1);
    await advance(CLOCK_TICK);
    expect(backend.checks).toBe(2);
    updater.stop();
  });

  it("checks when a computer that slept through the day wakes", async () => {
    const { updater, backend } = harness({ installs: false });
    updater.start();
    await advance(FIRST_CHECK_DELAY);
    expect(backend.checks).toBe(1);
    // Timers do not run while the computer sleeps; the clock moves on.
    vi.setSystemTime(Date.now() + CHECK_INTERVAL + 60_000);
    await advance(CLOCK_TICK);
    expect(backend.checks).toBe(2);
    updater.stop();
  });

  it("checks at once when asked, and once for repeated clicks", async () => {
    const { updater, backend } = harness({ installs: false });
    updater.start();
    await advance(1000);
    updater.requestCheck();
    updater.requestCheck();
    await settle();
    expect(backend.checks).toBe(1);
    await advance(FIRST_CHECK_DELAY);
    expect(backend.checks).toBe(1);
    updater.requestCheck();
    await settle();
    expect(backend.checks).toBe(2);
    updater.stop();
  });

  it("never checks when updates are off", async () => {
    for (const deps of [{ version: "0.0.0" }, { packaged: false }, { setting: "0" }]) {
      const { updater, backend } = harness(deps);
      updater.start();
      await advance(CHECK_INTERVAL * 2);
      expect(backend.checks).toBe(0);
      expect(updater.view().enabled).toBe(false);
    }
  });

  it("reports a failed check and an up-to-date one", async () => {
    const { updater, backend } = harness();
    backend.checkError = new Error("offline");
    updater.start();
    await advance(FIRST_CHECK_DELAY);
    expect(updater.view()).toMatchObject({
      phase: "idle",
      error: "cannot check for updates: offline",
    });
    backend.checkError = null;
    backend.offered = null;
    updater.requestCheck();
    await settle();
    expect(updater.view()).toMatchObject({ phase: "idle", error: null, available: null });
    expect(updater.view().checked_at).not.toBe(null);
    updater.stop();
  });

  it("only announces an update this copy cannot install", async () => {
    const { updater, backend } = harness({ installs: false });
    updater.start();
    await advance(FIRST_CHECK_DELAY);
    expect(updater.view()).toMatchObject({ phase: "idle", available: "1.2.4", installs: false });
    expect(backend.downloads).toBe(0);
    updater.stop();
  });
});

describe("downloads", () => {
  it("downloads and verifies an update, then waits for jobs and installs it", async () => {
    const { updater, backend, fetched, jobs, calls } = harness();
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    expect(fetched).toEqual([
      "https://github.com/baum-ai/baum/releases/download/v1.2.4/baum-desktop-linux-amd64.AppImage.sig",
    ]);
    expect(updater.view()).toMatchObject({ phase: "ready", available: "1.2.4", installs: true });
    // A second check finds the same version ready.
    updater.requestCheck();
    await settle();
    expect(backend.downloads).toBe(1);
    expect(updater.view().phase).toBe("ready");

    jobs.push(2, 1);
    expect(updater.requestInstall().phase).toBe("waiting");
    await settle();
    expect(updater.view()).toMatchObject({ phase: "waiting", jobs: 2 });
    await advance(JOBS_POLL);
    expect(updater.view()).toMatchObject({ phase: "waiting", jobs: 1 });
    expect(backend.installs).toBe(0);
    await advance(JOBS_POLL, () => backend.installs === 1);
    expect(updater.view()).toMatchObject({ phase: "installing", jobs: 0 });
    expect(backend.installs).toBe(1);
    expect(calls).toEqual(["jobs", "jobs", "jobs", "stop"]);
    updater.stop();
  });

  it("a cancelled wait for jobs keeps the update ready", async () => {
    const { updater, backend, jobs } = harness();
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    jobs.push(3);
    updater.requestInstall();
    await settle();
    expect(updater.requestCancel().phase).toBe("waiting");
    await settle();
    expect(updater.view()).toMatchObject({ phase: "ready", jobs: 0 });
    expect(backend.installs).toBe(0);
    // Installing again works.
    updater.requestInstall();
    await settle(() => backend.installs === 1);
    expect(backend.installs).toBe(1);
    updater.stop();
  });

  it("a cancel accepted while jobs are counted stops the install", async () => {
    let finish = (_jobs: number) => {};
    const { updater, backend } = harness({
      runningJobs: () => new Promise((resolve) => (finish = resolve)),
    });
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    updater.requestInstall();
    await settle();
    // The last job finishes while the click is on its way.
    updater.requestCancel();
    finish(0);
    await settle();
    expect(updater.view().phase).toBe("ready");
    expect(backend.installs).toBe(0);
    updater.stop();
  });

  it("rejects a download signed for another version, and only announces it from then on", async () => {
    const { updater, backend, deps } = harness({}, releaseSignature(KEY, BUNDLE, "1.2.3"));
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    const view = updater.view();
    expect(view).toMatchObject({ phase: "idle", installs: false, available: "1.2.4" });
    expect(view.error).toMatch(/^cannot download baum 1.2.4: signed for .*not version 1.2.4$/);
    expect(remembered(deps.settingsPath).rejected_downloads).toEqual(["1.2.4"]);
    expect(existsSync(join(backend.dir, "baum-desktop-linux-amd64.AppImage"))).toBe(false);
    updater.requestCheck();
    await settle();
    expect(backend.downloads).toBe(1);
    expect(updater.view()).toMatchObject({ phase: "idle", installs: false });
    expect(updater.view().error).toMatch(/not signed by the release key/);
    updater.stop();
  });

  it("rejects a download signed by another key", async () => {
    const other = testKey();
    const { updater } = harness({}, releaseSignature(other, BUNDLE, "1.2.4"));
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    expect(updater.view()).toMatchObject({ phase: "idle", installs: false });
    expect(updater.view().error).toMatch(/not the release key/);
    updater.stop();
  });

  it("rejects a tampered download", async () => {
    const { updater } = harness({}, releaseSignature(KEY, Buffer.from("other"), "1.2.4"));
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    expect(updater.view().error).toMatch(/does not match the file/);
    updater.stop();
  });

  it("a missing signature fails the download without remembering the version", async () => {
    const { updater, deps } = harness({
      fetch: async () => new Response("Not Found", { status: 404 }),
    });
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    expect(updater.view()).toMatchObject({ phase: "idle", installs: true });
    expect(updater.view().error).toMatch(/HTTP 404/);
    expect(remembered(deps.settingsPath).rejected_downloads).toEqual([]);
    updater.stop();
  });

  it("abandons a download that receives nothing for two minutes", async () => {
    const { updater, backend } = harness();
    backend.hang = { onData: () => {} };
    updater.start();
    await advance(FIRST_CHECK_DELAY);
    expect(updater.view().phase).toBe("downloading");
    // Data keeps a slow download going.
    for (let i = 0; i < 5; i++) {
      await advance(STALL_TIMEOUT - 1000);
      backend.hang.onData();
    }
    expect(updater.view().phase).toBe("downloading");
    await advance(STALL_TIMEOUT);
    expect(updater.view()).toMatchObject({
      phase: "idle",
      error: "cannot download baum 1.2.4: no data for two minutes",
    });
    updater.stop();
  });

  it("remembers a failed install and only announces that version from then on", async () => {
    const { updater, backend, deps, calls } = harness();
    backend.installError = new Error("read-only file system");
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    updater.requestInstall();
    await settle(() => updater.view().phase === "idle");
    expect(calls).toEqual(["jobs", "stop", "restart"]);
    expect(updater.view()).toMatchObject({ phase: "idle", installs: false });
    expect(updater.view().error).toBe(
      "This copy of baum could not update itself to 1.2.4 (read-only file system); download it from its release page.",
    );
    expect(remembered(deps.settingsPath).failed_installs).toEqual(["1.2.4"]);
    updater.requestCheck();
    await settle();
    expect(backend.downloads).toBe(1);
    expect(updater.view().error).toBe(
      "An earlier attempt to install baum 1.2.4 failed; download it from its release page.",
    );
    updater.stop();
  });

  it("checks the file again before installing it", async () => {
    const { updater, backend, deps } = harness();
    updater.start();
    await advance(FIRST_CHECK_DELAY, () => !busy(updater));
    writeFileSync(join(backend.dir, "baum-desktop-linux-amd64.AppImage"), "swapped");
    updater.requestInstall();
    await settle(() => updater.view().error !== null);
    expect(backend.installs).toBe(0);
    expect(updater.view().error).toMatch(/does not match the file/);
    expect(remembered(deps.settingsPath).rejected_downloads).toEqual(["1.2.4"]);
    updater.stop();
  });
});

describe("commands", () => {
  const context = { window: null, role: "daemon-pages" as const, origin: "app://baum" };

  it("keep their names and results", async () => {
    const { deps } = harness({ installs: false });
    const updater = registerUpdaterCommands(deps);
    expect(await dispatch("update_state", {}, context)).toMatchObject({ phase: "idle" });
    expect(await dispatch("check_for_updates", {}, context)).toMatchObject({ phase: "checking" });
    await expect(dispatch("install_update", {}, context)).rejects.toThrow(
      "No update is ready to install.",
    );
    expect(await dispatch("cancel_update", {}, context)).toMatchObject({ version: "1.2.3" });
    expect(await dispatch("dismiss_update_notice", { version: "1.2.4" }, context)).toMatchObject({
      noticed: "1.2.4",
    });
    await expect(dispatch("dismiss_update_notice", {}, context)).rejects.toThrow();
    await settle();
    expect(updater.view().available).toBe("1.2.4");
    updater.stop();
  });
});
