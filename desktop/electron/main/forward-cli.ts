// baum commands sent to the macOS app's executable. The Tauri app bundled
// baum at `baum.app/Contents/MacOS/baum`, which is this app's executable
// now, so a daemon the Tauri app started still names that path: when the
// update replaced the file, the daemon restarts itself from it
// (`baum daemon run --detached --port N`), and paths it handed out while it
// ran, such as the git hooks' fallback, name it too. The packaged macOS app
// started with a baum command, rather than with its own flags, runs the
// bundled baum with the same arguments instead of starting the app.
import { spawn } from "node:child_process";
import { constants } from "node:os";

/** The baum arguments the app was started with, if it was started as baum:
 *  only the packaged macOS app, and only when the first argument is not a
 *  flag. The app's own launches pass only flags (`--hidden` from start at
 *  login, the Tauri app's restart forwarding its own). */
export function forwardedArgs(
  argv: readonly string[],
  platform: NodeJS.Platform,
  packaged: boolean,
): string[] | null {
  if (platform !== "darwin" || !packaged) return null;
  const args = argv.slice(1);
  const first = args[0];
  if (first === undefined || first === "" || first.startsWith("-")) return null;
  return args;
}

/** `baum daemon run` is the daemon itself, which outlives the process that
 *  starts it; every other command finishes, and its caller waits for it. */
export function detaches(args: readonly string[]): boolean {
  return args[0] === "daemon" && args[1] === "run";
}

/** Runs `cli` with `args` in this process's place: the same input, output
 *  and environment. A daemon is left running and this process exits at
 *  once; any other command is waited for, and its exit status is the app's. */
export function forward(
  cli: string,
  args: readonly string[],
  exit: (code: number) => void,
  log: (message: string) => void,
): void {
  const daemon = detaches(args);
  const child = spawn(cli, args, { stdio: "inherit", detached: daemon });
  child.on("error", (e) => {
    log(`cannot run ${cli}: ${e.message}`);
    exit(127);
  });
  if (daemon) {
    child.on("spawn", () => {
      child.unref();
      exit(0);
    });
  } else {
    // A signal ends it as a shell reports one: 128 plus its number.
    child.on("exit", (code, signal) =>
      exit(code ?? (signal === null ? 1 : 128 + constants.signals[signal])),
    );
  }
}
