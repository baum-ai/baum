// The `app://` scheme that serves the bundled screens from dist/ (the Vite
// build's copy for the desktop). `app://bundle` serves exactly the connecting
// screen (`/`, `/index.html`), the setup screen (`/setup.html`) only while
// the app shows it, and the asset files those pages reference, directly or
// through each other. `app://tray` serves only the tray panel (`/tray.html`)
// and its assets: its own origin keeps the window's zoom off the panel. Every
// other path, host, spelling of a page (`/setup`, `/setup.html/`) and any
// percent-encoding included, is not found. HTML responses carry a content
// security policy with no `connect-src`: commands go over IPC.
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import type { Protocol } from "electron";
import { BUNDLED_HOST, BUNDLED_SCHEME, TRAY_HOST } from "./navigation.ts";

export const CONTENT_SECURITY_POLICY = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self'",
  "connect-src 'none'",
  "frame-ancestors 'none'",
].join("; ");

const BUNDLED_PAGES = ["/index.html", "/setup.html"];
const TRAY_PAGES = ["/tray.html"];

/** The assets each origin serves. */
export interface AssetSets {
  bundle: ReadonlySet<string>;
  tray: ReadonlySet<string>;
}

const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
  ".json": "application/json",
  ".wasm": "application/wasm",
};

const ASSET_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** The files of `dist/assets` the pages (`/index.html`-style paths)
 *  reference, directly or through each other: a file counts when a page or a
 *  counted script or style sheet names it. The daemon app's own chunks,
 *  which dist/ also holds, stay out unless a page shares them. */
export function assetClosure(dist: string, pages: readonly string[]): Set<string> {
  let names: string[];
  try {
    names = readdirSync(join(dist, "assets")).filter((n) => ASSET_NAME.test(n));
  } catch {
    return new Set();
  }
  const included = new Set<string>();
  const queue: string[] = pages.map((page) => join(dist, page));
  while (queue.length > 0) {
    let text: string;
    try {
      text = readFileSync(queue.shift() as string, "utf8");
    } catch {
      continue;
    }
    for (const name of names) {
      if (included.has(name) || !text.includes(name)) continue;
      included.add(name);
      if ([".js", ".css", ".html"].includes(extname(name))) queue.push(join(dist, "assets", name));
    }
  }
  return included;
}

/** The assets of the bundled screens and of the tray panel, each closed
 *  over what its pages reference. */
export function assetSets(dist: string): AssetSets {
  return { bundle: assetClosure(dist, BUNDLED_PAGES), tray: assetClosure(dist, TRAY_PAGES) };
}

/** The file under dist/ a request for `raw` gets, or null when it gets
 *  nothing. */
export function resolveRequest(raw: string, setupOpen: boolean, assets: AssetSets): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (
    url.protocol !== `${BUNDLED_SCHEME}:` ||
    url.port !== "" ||
    url.username !== "" ||
    url.password !== ""
  ) {
    return null;
  }
  let pages: readonly string[];
  let allowed: ReadonlySet<string>;
  if (url.hostname === BUNDLED_HOST) {
    pages = BUNDLED_PAGES;
    allowed = assets.bundle;
  } else if (url.hostname === TRAY_HOST) {
    pages = TRAY_PAGES;
    allowed = assets.tray;
  } else {
    return null;
  }
  const path = url.pathname === "/" && pages === BUNDLED_PAGES ? "/index.html" : url.pathname;
  if (path === "/setup.html" && !setupOpen) return null;
  if (pages.includes(path)) return path.slice(1);
  const asset = /^\/assets\/([^/]+)$/.exec(path)?.[1];
  if (asset && ASSET_NAME.test(asset) && allowed.has(asset)) return `assets/${asset}`;
  return null;
}

export function contentType(file: string): string {
  return TYPES[extname(file)] ?? "application/octet-stream";
}

/** Before the app is ready: `app://` is a standard, secure scheme, so its
 *  pages have an origin, and fetch can load from it (module scripts). */
export function registerAppScheme(protocol: Protocol): void {
  protocol.registerSchemesAsPrivileged([
    {
      scheme: BUNDLED_SCHEME,
      privileges: { standard: true, secure: true, supportFetchAPI: true },
    },
  ]);
}

/** After the app is ready: serves the bundled files from `dist`. */
export function serveAppScheme(protocol: Protocol, dist: string, setupOpen: () => boolean): void {
  const assets = assetSets(dist);
  protocol.handle(BUNDLED_SCHEME, (request) => {
    const file = resolveRequest(request.url, setupOpen(), assets);
    if (!file || request.method !== "GET") return new Response("Not found", { status: 404 });
    let body: Buffer;
    try {
      body = readFileSync(join(dist, file));
    } catch {
      return new Response("Not found", { status: 404 });
    }
    const headers: Record<string, string> = {
      "Content-Type": contentType(file),
      "X-Content-Type-Options": "nosniff",
      "Cache-Control": "no-store",
    };
    if (file.endsWith(".html")) headers["Content-Security-Policy"] = CONTENT_SECURITY_POLICY;
    return new Response(new Uint8Array(body), { status: 200, headers });
  });
}
