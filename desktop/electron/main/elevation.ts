// Running the fixed install scripts as root, through the operating system's
// password prompt.
import { spawn } from "node:child_process";
import type { Platform } from "./path-rules.ts";

/** Runs a shell script with administrator rights: the password prompt of
 *  the operating system, then `/bin/sh -c script sh args...`. Paths travel
 *  only as arguments, never inside the script. A rejection's message is the
 *  message for the user. */
export interface Elevate {
  run(script: string, args: readonly string[]): Promise<void>;
}

export interface Captured {
  /** The exit status; null when a signal ended the process. */
  code: number | null;
  stdout: string;
  stderr: string;
}

/** Runs a program to completion with its output captured (no shell, no
 *  stdin). Rejects only when it cannot be started. */
export function capture(
  file: string,
  args: readonly string[],
  env?: NodeJS.ProcessEnv,
): Promise<Captured> {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, { stdio: ["ignore", "pipe", "pipe"], env });
    const out: Buffer[] = [];
    const err: Buffer[] = [];
    child.stdout.on("data", (b: Buffer) => out.push(b));
    child.stderr.on("data", (b: Buffer) => err.push(b));
    child.on("error", reject);
    child.on("close", (code) =>
      resolve({
        code,
        stdout: Buffer.concat(out).toString("utf8"),
        stderr: Buffer.concat(err).toString("utf8"),
      }),
    );
  });
}

/** The user-facing message for a privileged script that exited with `code`. */
export function scriptFailure(code: number | null, stderr: string): string {
  switch (code) {
    case 3:
      return "The baum command changed since the app installed it, so it was left alone.";
    case 4:
      return "The paths do not look like a baum command the app installs, so nothing was changed.";
    default:
      return `The change needs an administrator and did not succeed: ${stderr.trim()}`;
  }
}

// AppleScript quotes every argument; nothing is interpolated.
const APPLE_SCRIPT = `on run argv
set shellCommand to "/bin/sh -c " & quoted form of (item 1 of argv) & " sh"
repeat with i from 2 to count of argv
set shellCommand to shellCommand & " " & quoted form of (item i of argv)
end repeat
do shell script shellCommand with prompt "baum needs your password to change the baum command." with administrator privileges
end run`;

const macOS: Elevate = {
  async run(script, args) {
    let output: Captured;
    try {
      output = await capture("/usr/bin/osascript", ["-e", APPLE_SCRIPT, script, ...args]);
    } catch (e) {
      throw new Error(`Cannot ask for an administrator's password: ${String(e)}`);
    }
    if (output.code === 0) return;
    // "execution error: … (N)": N is the script's exit status, or -128
    // when the user cancelled the prompt.
    const trimmed = output.stderr.trim();
    let code: number | null = null;
    if (trimmed.endsWith(")")) {
      const n = trimmed.slice(0, -1).split("(").at(-1) ?? "";
      if (/^[+-]?\d+$/.test(n)) code = Number(n);
    }
    if (code === -128) throw new Error("Cancelled.");
    throw new Error(scriptFailure(code, output.stderr));
  },
};

const linux: Elevate = {
  async run(script, args) {
    let output: Captured;
    try {
      output = await capture("/usr/bin/pkexec", ["/bin/sh", "-c", script, "sh", ...args]);
    } catch (e) {
      throw new Error(
        `Cannot ask for an administrator's password (pkexec: ${String(e)}). Install the command as root instead, or put ~/.local/bin on your PATH and try again.`,
      );
    }
    switch (output.code) {
      case 0:
        return;
      // pkexec: the user dismissed the prompt or was not authorized.
      case 126:
        throw new Error("Cancelled.");
      // pkexec: no authentication agent could ask.
      case 127:
        throw new Error(
          "No authorization agent is running to ask for your password, so nothing was changed.",
        );
      default:
        throw new Error(scriptFailure(output.code, output.stderr));
    }
  },
};

const windows: Elevate = {
  async run() {
    throw new Error("Not used on Windows.");
  },
};

/** The administrator prompt of the operating system. */
export function systemPrompt(platform: Platform): Elevate {
  if (platform === "darwin") return macOS;
  if (platform === "win32") return windows;
  return linux;
}
