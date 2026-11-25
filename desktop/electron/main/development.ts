// The bridge to the container's Vite server under development (src/
// development.rs in the Tauri shell): an unpackaged app run with
// `BAUM_DESKTOP_DEV_DIR` loads the daemon's pages through Vite's proxy on
// port 1420 and the setup screen from Vite, so edits reload in place. No
// credential leaves the normal fragment and cookie exchange; the shared file
// holds only the daemon's origin.
import { renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** Vite's port, kept off the platform UI's 5173 so both can run at once. */
export const PORT = 1420;

/** `localhost`, not `127.0.0.1`: the daemon's pages use Vite on
 *  `127.0.0.1`, so the setup screen's origin differs from theirs. */
export const SETUP_PAGE = `http://localhost:${PORT}/setup.html`;

/** The shared directory, when the app runs under development. A packaged
 *  app never does. */
export function developmentDir(env: NodeJS.ProcessEnv, packaged: boolean): string | null {
  if (packaged) return null;
  return env.BAUM_DESKTOP_DEV_DIR || null;
}

/** The URL the window loads for the daemon's launch URL: under development,
 *  Vite's, after telling Vite's proxy the daemon's origin. */
export function frontend(launch: string, dir: string | null): string {
  if (dir === null) return launch;
  const url = new URL(launch);
  const pending = join(dir, "daemon-origin.tmp");
  try {
    writeFileSync(pending, url.origin);
    renameSync(pending, join(dir, "daemon-origin"));
  } catch (e) {
    throw new Error(`Cannot configure the development proxy: ${(e as Error).message}`);
  }
  url.port = String(PORT);
  return url.toString();
}

/** Vite's copy of the setup screen, under development only. */
export function setupPage(dir: string | null): string | null {
  return dir === null ? null : SETUP_PAGE;
}

/** Whether `raw` is Vite's setup screen, ignoring only the fragment. */
export function isSetupPage(raw: string, dir: string | null): boolean {
  if (dir === null) return false;
  try {
    const url = new URL(raw);
    const page = new URL(SETUP_PAGE);
    return url.origin === page.origin && url.pathname === page.pathname && url.search === "";
  } catch {
    return false;
  }
}
