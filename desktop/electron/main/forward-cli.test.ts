import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { detaches, forward, forwardedArgs } from "./forward-cli.ts";

const EXE = "/Applications/baum.app/Contents/MacOS/baum";

describe("baum commands sent to the app's executable", () => {
  it("forwards only a packaged macOS app started with a command", () => {
    const respawn = [EXE, "daemon", "run", "--detached", "--port", "61323"];
    expect(forwardedArgs(respawn, "darwin", true)).toEqual(respawn.slice(1));
    expect(forwardedArgs([EXE, "hook", "post-commit"], "darwin", true)).toEqual([
      "hook",
      "post-commit",
    ]);
    // The app's own launches: none, start at login, the Tauri app's restart.
    expect(forwardedArgs([EXE], "darwin", true)).toBeNull();
    expect(forwardedArgs([EXE, "--hidden"], "darwin", true)).toBeNull();
    expect(forwardedArgs([EXE, "--remote-debugging-port=9333"], "darwin", true)).toBeNull();
    expect(forwardedArgs([EXE, ""], "darwin", true)).toBeNull();
    // Elsewhere, and in development, the arguments are the app's.
    expect(forwardedArgs(respawn, "linux", true)).toBeNull();
    expect(forwardedArgs(respawn, "darwin", false)).toBeNull();
  });

  it("leaves only the daemon running on its own", () => {
    expect(detaches(["daemon", "run", "--detached"])).toBe(true);
    expect(detaches(["daemon", "status"])).toBe(false);
    expect(detaches(["hooks", "status"])).toBe(false);
  });
});

describe.skipIf(process.platform === "win32")("running the bundled baum", () => {
  const dirs: string[] = [];
  afterEach(() => {
    for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
  });

  /** A stand-in baum that records its arguments and runs `script`. */
  function baum(script: string) {
    const dir = mkdtempSync(join(tmpdir(), "baum-forward-"));
    dirs.push(dir);
    const cli = join(dir, "baum");
    const seen = join(dir, "args");
    writeFileSync(cli, `#!/bin/sh\necho "$@" >"${seen}"\n${script}\n`);
    chmodSync(cli, 0o755);
    return { cli, args: () => readFileSync(seen, "utf8").trim() };
  }

  const run = (cli: string, args: string[]) =>
    new Promise<{ code: number; logged: string[] }>((resolve) => {
      const logged: string[] = [];
      forward(
        cli,
        args,
        (code) => resolve({ code, logged }),
        (m) => logged.push(m),
      );
    });

  it("waits for a command and takes its exit status", async () => {
    const b = baum("exit 3");
    expect((await run(b.cli, ["hook", "post-commit"])).code).toBe(3);
    expect(b.args()).toBe("hook post-commit");
  });

  it("reports a signal as a shell does", async () => {
    const b = baum("kill -TERM $$");
    expect((await run(b.cli, ["hooks", "status"])).code).toBe(128 + 15);
  });

  it("does not wait for the daemon", async () => {
    const b = baum("sleep 3");
    const started = Date.now();
    expect((await run(b.cli, ["daemon", "run", "--detached"])).code).toBe(0);
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it("fails when the bundled baum is missing", async () => {
    const result = await run("/nonexistent/baum", ["daemon", "status"]);
    expect(result.code).toBe(127);
    expect(result.logged[0]).toContain("cannot run /nonexistent/baum");
  });
});
