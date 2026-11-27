// Where the app's windows may go. The
// main window shows only the bundled screens and the daemon's loopback
// origin and refuses every other navigation, in every frame. Web links open
// in the default browser only through a request for a new window
// (`target="_blank"`, `window.open`) or the `open_link` command, never
// through a navigation.

/** The bundled screens' scheme and host: `app://bundle`, served by
 *  protocol.ts. */
export const BUNDLED_SCHEME = "app";
export const BUNDLED_HOST = "bundle";
export const BUNDLED_ORIGIN = `${BUNDLED_SCHEME}://${BUNDLED_HOST}`;

/** The tray panel's own origin, `app://tray`, serving only `/tray.html` and
 *  its assets. Chromium shares page zoom between pages of one origin, so the
 *  panel is apart from the bundled screens: the window's zoom leaves it
 *  alone. */
export const TRAY_HOST = "tray";
export const TRAY_ORIGIN = `${BUNDLED_SCHEME}://${TRAY_HOST}`;
export const TRAY_PANEL_URL = `${TRAY_ORIGIN}/tray.html`;

export type Decision = "allow" | "deny";
export type NewWindow = "open-in-browser" | "deny";

function parse(url: string): URL | null {
  try {
    return new URL(url);
  } catch {
    return null;
  }
}

/** A URL's origin as the app compares them. Node's URL gives a non-special
 *  scheme such as `app:` the opaque origin "null", so the bundled origin is
 *  built from its scheme and host. */
export function originOf(url: URL): string {
  if (url.protocol === `${BUNDLED_SCHEME}:`) {
    return url.port ? "null" : `${BUNDLED_SCHEME}://${url.hostname}`;
  }
  return url.origin;
}

function hasUserinfo(url: URL): boolean {
  return url.username !== "" || url.password !== "";
}

/** The bundled origin exactly: no port and no user name. */
export function isBundled(url: URL): boolean {
  return (
    url.protocol === `${BUNDLED_SCHEME}:` &&
    url.hostname === BUNDLED_HOST &&
    url.port === "" &&
    !hasUserinfo(url)
  );
}

/** The bundled pages the main window may show, by their exact path: the
 *  connecting screen, and the setup screen only while the app shows it,
 *  never because a page navigated there. Every other path, other spellings
 *  of a page (`/setup`, `/setup.html/`, `/setup%2Ehtml`) included, is
 *  refused. */
export function bundledPageAllowed(path: string, setupOpen: boolean): boolean {
  switch (path) {
    case "/":
    case "/index.html":
      return true;
    case "/setup.html":
      return setupOpen;
    default:
      return false;
  }
}

/** Decides a navigation in the main window, in any of its frames. `daemon`
 *  is the origin of the daemon the window is attached to, if any.
 *
 *  Electron asks this for a navigation in every frame (`will-frame-navigate`)
 *  and redirect, so no answer here may act outside the window: a navigation
 *  to another site is refused, never handed to the browser, or any frame
 *  could open the browser at an address carrying data it read, as often as
 *  it liked. */
export function decide(raw: string, daemon: string | null, setupOpen: boolean): Decision {
  const url = parse(raw);
  if (!url) return "deny";
  // Nothing the app loads has a user name or password in its URL, and one
  // can make an address read as another (`http://127.0.0.1:4100@host/`).
  if (hasUserinfo(url)) return "deny";
  if (isBundled(url)) return bundledPageAllowed(url.pathname, setupOpen) ? "allow" : "deny";
  // The daemon's pages, and frames of its own documents. A `blob:` URL has
  // its creator's origin, so it is excluded by scheme.
  const http = url.protocol === "http:" || url.protocol === "https:";
  if (http && daemon !== null && url.origin === daemon) return "allow";
  // Everything else: other sites, other local servers (a stale daemon
  // address), `about:`, `data:`, `blob:`, `javascript:`, `file:` and other
  // schemes. Refusing `about:blank` and `about:srcdoc` keeps the empty
  // document a new frame starts with, and loads no `srcdoc`.
  return "deny";
}

/** The tray panel's window loads its own page and nothing else: the tray
 *  origin exactly (no port, no user name) at `/tray.html`. */
export function isTrayPanel(raw: string): boolean {
  const url = parse(raw);
  return (
    url !== null &&
    url.protocol === `${BUNDLED_SCHEME}:` &&
    url.hostname === TRAY_HOST &&
    url.port === "" &&
    !hasUserinfo(url) &&
    url.pathname === "/tray.html"
  );
}

/** A URL as the log shows it: its scheme and host only, since a path or
 *  query can carry a credential or data a page read. */
export function schemeAndHost(raw: string): string {
  const url = parse(raw);
  if (!url) return "unparsable URL";
  return url.hostname ? `${url.protocol}//${url.hostname}` : url.protocol;
}

/** Requests for a new window never open one: links to other web sites go to
 *  the browser, and anything else is refused. A sandboxed frame can ask only
 *  with `allow-popups`, and `OpenLimit` bounds how often a script can open
 *  the browser. */
export function decideNewWindow(raw: string): NewWindow {
  const url = parse(raw);
  if (!url) return "deny";
  const web = url.protocol === "http:" || url.protocol === "https:";
  if (web && url.hostname !== "" && !isLoopback(url) && !hasUserinfo(url)) {
    return "open-in-browser";
  }
  // Loopback servers, the daemon's included, and other schemes.
  return "deny";
}

function ipv4LoopbackOrUnspecified(first: number, all: number): boolean {
  return first === 127 || all === 0;
}

/** Another local server, such as a stale daemon address, is never opened:
 *  neither in the window nor, with a credential in hand, in the browser.
 *  Node's URL has already normalized IPv4 spellings (`127.1`, `0x7f000001`)
 *  and compressed IPv6 ones. */
function isLoopback(url: URL): boolean {
  const host = url.hostname;
  if (host.startsWith("[") && host.endsWith("]")) {
    const ip = host.slice(1, -1).toLowerCase();
    // `[::ffff:127.0.0.1]` reaches 127.0.0.1; Node writes it `[::ffff:7f00:1]`.
    const mapped = /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(ip);
    if (mapped) {
      const high = Number.parseInt(mapped[1] as string, 16);
      const low = Number.parseInt(mapped[2] as string, 16);
      return ipv4LoopbackOrUnspecified(high >> 8, high * 0x10000 + low);
    }
    return ip === "::1" || ip === "::";
  }
  const v4 = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(host);
  if (v4) {
    const octets = v4.slice(1).map(Number);
    return ipv4LoopbackOrUnspecified(
      octets[0] as number,
      octets.reduce((a, b) => a + b, 0),
    );
  }
  const domain = host.replace(/\.+$/, "").toLowerCase();
  return domain === "localhost" || domain.endsWith(".localhost");
}

/** How often a page may open the browser, in milliseconds. A person clicking
 *  links never clicks two within this; a script asking for new windows in a
 *  loop opens the browser once. */
export const OPEN_INTERVAL = 1000;

/** Counts the browser opens of new-window requests and `open_link`: one per
 *  `OPEN_INTERVAL`, and the rest are dropped. */
export class OpenLimit {
  #last: number | null = null;

  /** Whether a request at `now` (milliseconds, monotonic) may open the
   *  browser, which then counts. */
  allow(now: number): boolean {
    if (this.#last !== null && now - this.#last < OPEN_INTERVAL) {
      return false;
    }
    this.#last = now;
    return true;
  }
}

/** The bundled page at `path`, such as `/index.html`. */
export function bundledUrl(path: string): string {
  return `${BUNDLED_ORIGIN}${path}`;
}

/** The URL of the bundled connecting screen. */
export const BUNDLED_SCREEN = bundledUrl("/index.html");
