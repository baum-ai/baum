// Windows has no links on the PATH to place: the app puts its `baum.exe`
// alone in a directory of its data directory (`target`, refreshed at every
// launch), adds that directory to the front of the user's PATH (new
// terminals pick it up) and removes it again on uninstall.
import { capture } from "./elevation.ts";
import type { Found, InstallRecord, LoginPath, Plan, Status, Target } from "./install.ts";
import { parent, samePath, sameFile } from "./path-rules.ts";

const P = "win32" as const;

/** Reads and writes the user's PATH in the registry, keeping unexpanded
 *  variables, and announces the change so new terminals see it. Entries
 *  are compared with their variables expanded, so `%LOCALAPPDATA%\…`
 *  counts as the same directory. The directory travels in an environment
 *  variable, never in the script. */
export const SCRIPT = `$ErrorActionPreference = 'Stop'
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$path = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
$expand = { param($p) [Environment]::ExpandEnvironmentVariables($p).TrimEnd('\\') }
if ($env:BAUM_PATH_ACTION -eq 'read') {
  Write-Output ((@($path -split ';' | Where-Object { $_ }) | ForEach-Object { & $expand $_ }) -join ';')
  exit 0
}
$dir = $env:BAUM_PATH_DIR.TrimEnd('\\')
$rest = @($path -split ';' | Where-Object { $_ -and ((& $expand $_) -ne $dir) })
if ($env:BAUM_PATH_ACTION -eq 'add') { $rest = @($dir) + $rest }
$new = $rest -join ';'
if ($new.Length -gt 32000) { throw 'Your PATH is too long to add another directory; remove entries you no longer need.' }
$key.SetValue('Path', $new, 'ExpandString')
[Environment]::SetEnvironmentVariable('BAUM_PATH_REFRESH', '1', 'User')
[Environment]::SetEnvironmentVariable('BAUM_PATH_REFRESH', $null, 'User')`;

async function powershell(action: string, dir: string, env: NodeJS.ProcessEnv): Promise<string> {
  let output: Awaited<ReturnType<typeof capture>>;
  try {
    output = await capture(
      "powershell.exe",
      ["-NoProfile", "-NonInteractive", "-Command", SCRIPT],
      {
        ...env,
        BAUM_PATH_ACTION: action,
        BAUM_PATH_DIR: dir,
      },
    );
  } catch (e) {
    throw new Error(`Cannot run PowerShell: ${String(e)}`);
  }
  if (output.code !== 0) throw new Error(`Cannot change your PATH: ${output.stderr.trim()}`);
  return output.stdout.trim();
}

/** The user's PATH entries, variables expanded. */
export async function userPath(env: NodeJS.ProcessEnv): Promise<string[]> {
  return (await powershell("read", "", env)).split(";").filter((e) => e !== "");
}

export async function changeUserPath(dir: string, add: boolean, env: NodeJS.ProcessEnv) {
  await powershell(add ? "add" : "remove", dir, env);
}

export function sameDir(a: string, b: string): boolean {
  const trim = (p: string) => p.replace(/\\+$/, "").toLowerCase();
  return trim(a) === trim(b);
}

export async function status(
  target: Target,
  login: LoginPath,
  records: InstallRecord[],
  env: NodeJS.ProcessEnv,
): Promise<Status> {
  const status: Status = {
    target: null,
    unavailable: null,
    found: login.baum,
    installed: false,
    shadowed: null,
    record: null,
    plan: null,
    path_error: login.error ?? login.warning,
  };
  if ("error" in target) {
    status.unavailable = target.error;
    return status;
  }
  const dir = parent(target.path, P) ?? "";
  let listed = false;
  try {
    listed = (await userPath(env)).some((e) => sameDir(e, dir));
  } catch {}
  const runsTarget = (found: Found) => sameFile(found.resolved, target.path, P);
  status.installed = listed || (status.found !== null && runsTarget(status.found));
  if (status.installed && status.found !== null && !runsTarget(status.found)) {
    status.shadowed = status.found;
  }
  status.record = records.find((r) => listed && samePath(r.link, dir, P)) ?? null;
  if (!status.installed) {
    const plan: Plan = {
      link: dir,
      replaces: null,
      backup: null,
      admin: false,
      on_path: true,
      blocked: null,
      kept_backup: null,
    };
    status.plan = plan;
  }
  status.target = target.path;
  return status;
}

export async function install(
  plan: Plan,
  target: string,
  env: NodeJS.ProcessEnv,
): Promise<InstallRecord> {
  await changeUserPath(plan.link, true, env);
  return { link: plan.link, target, backup: null, admin: false };
}

export async function uninstall(record: InstallRecord, env: NodeJS.ProcessEnv): Promise<string> {
  await changeUserPath(record.link, false, env);
  return `Removed ${record.link} from your PATH. Open a new terminal for it to take effect.`;
}
