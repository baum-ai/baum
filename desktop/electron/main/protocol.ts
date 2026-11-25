// The `app://bundle` scheme that serves the bundled screens from dist/ (the
// Vite build's copy for the desktop): exactly the connecting screen (`/`,
// `/index.html`), the setup screen (`/setup.html`) only while the app shows
// it, the tray panel (`/tray.html`), and the asset files those pages
// reference, directly or through each other. Every other path, other
// spellings of a page (`/setup`, `/setup.html/`) and any percent-encoding
// included, is not found. HTML responses carry the content security policy
// the Tauri shell set, with no `connect-src`: commands go over IPC.
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import type { Protocol } from "electron";
import { BUNDLED_HOST, BUNDLED_SCHEME } from "./navigation.ts";

export const CONTENT_SECURITY_POLICY = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self'",
  "connect-src 'none'",
  "frame-ancestors 'none'",
].join("; ");

const PAGES = new Set(["/index.html", "/setup.html", "/tray.html"]);

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

/** The files of `dist/assets` the bundled pages reference, directly or
 *  through each other: a file counts when a page or a counted script or
 *  style sheet names it. The daemon app's own chunks, which dist/ also
 *  holds, stay out unless a bundled page shares them. */
export function assetClosure(dist: string): Set<string> {
  let names: string[];
  try {
    names = readdirSync(join(dist, "assets")).filter((n) => ASSET_NAME.test(n));
  } catch {
    return new Set();
  }
  const included = new Set<string>();
  const queue: string[] = [...PAGES].map((page) => join(dist, page));
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

/** The file under dist/ a request for `raw` gets, or null when it gets
 *  nothing. */
export function resolveRequest(
  raw: string,
  setupOpen: boolean,
  assets: ReadonlySet<string>,
): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (
    url.protocol !== `${BUNDLED_SCHEME}:` ||
    url.hostname !== BUNDLED_HOST ||
    url.port !== "" ||
    url.username !== "" ||
    url.password !== ""
  ) {
    return null;
  }
  const path = url.pathname === "/" ? "/index.html" : url.pathname;
  if (path === "/setup.html" && !setupOpen) return null;
  if (PAGES.has(path)) return path.slice(1);
  const asset = /^\/assets\/([^/]+)$/.exec(path)?.[1];
  if (asset && ASSET_NAME.test(asset) && assets.has(asset)) return `assets/${asset}`;
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
  const assets = assetClosure(dist);
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
