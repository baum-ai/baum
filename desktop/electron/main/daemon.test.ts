// The tests of src/daemon.rs, run against a fake baum written as a shell
// script (the checks run on Linux).
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inspect } from "node:util";
import { afterEach, describe, expect, it } from "vitest";
import {
  bundledCliPath,
  childPath,
  cliPath,
  DISK_IMAGE,
  ensure,
  Failure,
  health,
  Launch,
  launchUrl,
  logTail,
  misplaced,
  misplacedMessage,
  parseStatus,
  readCredential,
  TRANSLOCATED,
  withoutDir,
} from "./daemon.ts";

const TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";
const dirs: string[] = [];

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "baum-desktop-"));
  dirs.push(dir);
  return dir;
}

function writePrivate(path: string, data: string) {
  writeFileSync(path, data);
  chmodSync(path, 0o600);
}

function credential(dir: string, port = 4100) {
  writePrivate(
    join(dir, "credential.json"),
    JSON.stringify({ url: `http://127.0.0.1:${port}/#token=${TOKEN}` }),
  );
}

function fakeCli(dir: string, script: string): string {
  const path = join(dir, "baum");
  writeFileSync(path, `#!/bin/sh\n${script}\n`);
  chmodSync(path, 0o755);
  return path;
}

afterEach(() => {
  for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("where baum runs from", () => {
  it("a writable app runs baum", () => {
    expect(misplacedMessage("/Applications/baum.app/Contents/MacOS/baum", "darwin")).toBeNull();
    expect(misplacedMessage("/Volumes/baum/baum.app/Contents/MacOS/baum", "linux")).toBeNull();
  });

  it("a disk image or a translocated app asks to move the app", () => {
    expect(misplacedMessage("/Volumes/baum/baum.app/Contents/MacOS/baum", "darwin")).toBe(
      DISK_IMAGE,
    );
    expect(
      misplacedMessage(
        "/private/var/folders/x/T/AppTranslocation/0A1B/d/baum.app/Contents/MacOS/baum",
        "darwin",
      ),
    ).toBe(TRANSLOCATED);
  });

  it("BAUM_DESKTOP_BAUM wins, even from a disk image", async () => {
    const cli = {
      override: "/dev/baum",
      exe: "/Volumes/baum/baum.app/Contents/MacOS/baum",
      platform: "darwin" as const,
      bundled: "/bundled",
    };
    expect(misplaced(cli)).toBeNull();
    expect(await cliPath(cli)).toBe("/dev/baum");
    await expect(cliPath({ ...cli, override: undefined })).rejects.toThrow(DISK_IMAGE);
  });

  it("runs the stable copy where the app keeps one, and reports a failed copy", async () => {
    const cli = { override: undefined, exe: "/opt/baum/baum", platform: "linux" as const };
    expect(await cliPath({ ...cli, bundled: "/b" })).toBe("/b");
    expect(await cliPath({ ...cli, bundled: "/b", stable: async () => "/copy" })).toBe("/copy");
    await expect(
      cliPath({
        ...cli,
        bundled: "/b",
        stable: async () => {
          throw new Error("Cannot copy baum out of the app");
        },
      }),
    ).rejects.toBeInstanceOf(Failure);
  });

  it("finds the bundled binary in the resources or the sidecar build", () => {
    const where = { packaged: true, resources: "/r", appPath: "/a" };
    expect(bundledCliPath({ ...where, platform: "linux", arch: "x64" })).toBe("/r/bin/baum");
    expect(bundledCliPath({ ...where, platform: "win32", arch: "x64" })).toBe("/r/bin/baum.exe");
    const dev = { ...where, packaged: false };
    expect(bundledCliPath({ ...dev, platform: "linux", arch: "x64" })).toBe(
      "/a/binaries/baum-x86_64-unknown-linux-gnu",
    );
    expect(bundledCliPath({ ...dev, platform: "darwin", arch: "arm64" })).toBe(
      "/a/binaries/baum-aarch64-apple-darwin",
    );
    expect(bundledCliPath({ ...dev, platform: "win32", arch: "x64" })).toBe(
      "/a/binaries/baum-x86_64-pc-windows-msvc.exe",
    );
  });

  it("drops the AppImage mount from the child's PATH", () => {
    expect(
      withoutDir(
        "/tmp/.mount_baum-dfeJFK/usr/bin:/home/user/.local/bin:/tmp/.mount_baum-dfeJFK:/tmp/.mount_baum-dfeJFKG/usr/bin:/usr/bin",
        "/tmp/.mount_baum-dfeJFK",
        ":",
      ),
    ).toBe("/home/user/.local/bin:/tmp/.mount_baum-dfeJFKG/usr/bin:/usr/bin");
    const env = {
      APPIMAGE: "/x.AppImage",
      APPDIR: "/tmp/.mount_a",
      PATH: "/tmp/.mount_a/usr/bin:/usr/bin",
    };
    expect(childPath(env, "/tmp/.mount_a/usr/bin/baum-desktop")).toBe("/usr/bin");
    // A child of an AppImage inherits APPIMAGE but runs from elsewhere.
    expect(childPath(env, "/usr/bin/code")).toBeUndefined();
    expect(childPath({ PATH: "/usr/bin" }, "/usr/bin/baum-desktop")).toBeUndefined();
  });
});

describe("the credential", () => {
  it("accepts only loopback launch URLs", () => {
    expect(launchUrl(`http://127.0.0.1:4100/#token=${TOKEN}`).token).toBe(TOKEN);
    for (const bad of [
      `https://127.0.0.1:4100/#token=${TOKEN}`,
      `http://localhost:4100/#token=${TOKEN}`,
      `http://127.0.0.1/#token=${TOKEN}`,
      `http://user@127.0.0.1:4100/#token=${TOKEN}`,
      `http://127.0.0.1:4100/s/#token=${TOKEN}`,
      `http://127.0.0.1:4100/?x=1#token=${TOKEN}`,
      `http://127.0.0.1:4100/?#token=${TOKEN}`,
      `http://127.0.0.1:4100/#token=${TOKEN}&other=1`,
      "http://127.0.0.1:4100/#token=short",
      "http://127.0.0.1:4100/",
      "not a url",
    ]) {
      expect(() => launchUrl(bad), bad).toThrow();
    }
  });

  it("reads a private credential", () => {
    const dir = tempDir();
    credential(dir);
    const launch = readCredential(dir);
    expect(launch.origin).toBe("http://127.0.0.1:4100");
    expect(launch.bearer()).toBe(TOKEN);
    expect(launch.url()).toBe(`http://127.0.0.1:4100/#token=${TOKEN}`);
  });

  it("rejects a credential others can read, a missing one and a damaged one", () => {
    const dir = tempDir();
    credential(dir);
    chmodSync(join(dir, "credential.json"), 0o644);
    expect(() => readCredential(dir)).toThrow(/only you can read/);
    expect(() => readCredential(tempDir())).toThrow(/missing/);
    const damaged = tempDir();
    writePrivate(join(damaged, "credential.json"), "{");
    expect(() => readCredential(damaged)).toThrow(/unreadable/);
  });

  it("is never printed", () => {
    const launch = new Launch(`http://127.0.0.1:4100/#token=${TOKEN}`, TOKEN, {
      version: "v1",
      pid: 1,
      app: null,
      state: "/s",
    });
    for (const shown of [inspect(launch), JSON.stringify(launch), String(launch)]) {
      expect(shown).not.toContain(TOKEN);
      expect(shown).not.toContain("token=");
    }
  });
});

describe("the status", () => {
  it("needs an absolute state directory", () => {
    const ok = parseStatus([
      '{"protocol":1,"version":"v1","pid":7,"app":"http://127.0.0.1:1","state":"/s"}',
      "",
    ]);
    expect(ok).toEqual({ version: "v1", pid: 7, app: "http://127.0.0.1:1", state: "/s" });
    expect(() => parseStatus(['{"state":"relative"}'])).toThrow();
    expect(() => parseStatus([])).toThrow();
    expect(() => parseStatus(["not json"])).toThrow();
    expect(() => parseStatus(["[1]"])).toThrow();
  });
});

describe("the daemon's logs", () => {
  it("keeps the newest lines", () => {
    const dir = tempDir();
    writeFileSync(join(dir, "daemon.log"), "one\ntwo\nthree\n");
    writeFileSync(join(dir, "daemon.out"), "out\n");
    expect(logTail(dir, 2)).toEqual(["── daemon.log", "two", "three", "── daemon.out", "out"]);
    expect(logTail(tempDir(), 2)).toEqual([]);
  });
});

describe("baum daemon ensure", () => {
  it("loads the running daemon", async () => {
    const dir = tempDir();
    credential(dir);
    const cli = fakeCli(
      dir,
      `[ "$*" = "daemon ensure --json" ] || exit 9
echo '{"protocol":1,"version":"v1","pid":42,"app":"http://127.0.0.1:4100","state":"${dir}"}'`,
    );
    const launch = await ensure(cli, 5000);
    expect(launch.status.pid).toBe(42);
    expect(launch.url()).toBe(`http://127.0.0.1:4100/#token=${TOKEN}`);
  });

  it("rejects a credential for another daemon", async () => {
    const dir = tempDir();
    credential(dir);
    const cli = fakeCli(dir, `echo '{"app":"http://127.0.0.1:4200","state":"${dir}"}'`);
    await expect(ensure(cli, 5000)).rejects.toThrow(/different daemon/);
  });

  it("reports the command's error", async () => {
    const dir = tempDir();
    const cli = fakeCli(dir, "echo 'cannot lock the state directory' >&2; exit 1");
    const failure = (await ensure(cli, 5000).catch((e) => e)) as Failure;
    expect(failure.message).toContain("failed (exit status: 1)");
    expect(failure.details).toEqual(["cannot lock the state directory"]);
  });

  it("gives up after the timeout", async () => {
    const dir = tempDir();
    const cli = fakeCli(dir, "exec sleep 30");
    const started = Date.now();
    await expect(ensure(cli, 300)).rejects.toThrow(/did not start within/);
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it("is not held up by a detached child holding its output", async () => {
    const dir = tempDir();
    credential(dir);
    const cli = fakeCli(
      dir,
      `sleep 30 &
echo '{"app":"http://127.0.0.1:4100","state":"${dir}"}'`,
    );
    const started = Date.now();
    await ensure(cli, 5000);
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it("reports a missing baum", async () => {
    await expect(ensure("/nonexistent/baum", 1000)).rejects.toThrow(/Cannot run/);
  });
});

describe("the health check", () => {
  async function serve(status: number, body: string) {
    const requests: IncomingMessage[] = [];
    const server = createServer((request, response) => {
      requests.push(request);
      response.writeHead(status).end(body);
    });
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const port = (server.address() as AddressInfo).port;
    const launch = new Launch(`http://127.0.0.1:${port}/#token=${TOKEN}`, TOKEN, {
      version: "",
      pid: 0,
      app: null,
      state: "/s",
    });
    return { launch, requests, close: () => server.close() };
  }

  it("sends the credential", async () => {
    const daemon = await serve(200, '{"status":"ok"}');
    expect(await health(daemon.launch)).toBe("ok");
    expect(daemon.requests[0]?.url).toBe("/api/health");
    expect(daemon.requests[0]?.headers.authorization).toBe(`Bearer ${TOKEN}`);
    daemon.close();
  });

  it("a rejected credential means the daemon was replaced", async () => {
    const daemon = await serve(401, "");
    expect(await health(daemon.launch)).toBe("replaced");
    daemon.close();
  });

  it("fails on errors and silence", async () => {
    const error = await serve(500, "");
    expect(await health(error.launch)).toBe("down");
    error.close();
    const gone = await serve(200, "");
    gone.close();
    expect(await health(gone.launch, 200)).toBe("down");
  });
});
