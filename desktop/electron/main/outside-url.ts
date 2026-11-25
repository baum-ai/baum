// The rules for links that leave the app, shared by the new-window handler
// and the `open_link` command (navigation.rs in the Rust shell).

/** How often a page may open the browser. A person clicking links never
 *  clicks two within this; a script asking for new windows in a loop opens
 *  the browser once. */
export const OPEN_INTERVAL_MS = 1000;

export type NewWindow = "open-in-browser" | "deny";

/** A URL as debug builds log it: its scheme and host only, since a path or
 *  query can carry a credential or data a page read. */
export function schemeAndHost(url: URL): string {
  const scheme = url.protocol;
  return url.hostname === "" ? scheme : `${scheme}//${url.hostname}`;
}

export function hasUserinfo(url: URL): boolean {
  return url.username !== "" || url.password !== "";
}

/** Whether a hostname (as `URL` normalises it) is `localhost`, an address of
 *  this machine or the unspecified address. Another local server, such as a
 *  stale daemon address, is never opened in the browser with a credential in
 *  hand. */
export function isLoopback(url: URL): boolean {
  const host = url.hostname;
  if (host.startsWith("[")) return ipv6Local(host.slice(1, -1));
  if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) return ipv4Local(host.split(".").map(Number));
  const domain = host.replace(/\.+$/, "").toLowerCase();
  return domain === "localhost" || domain.endsWith(".localhost");
}

function ipv4Local(octets: number[]): boolean {
  return octets[0] === 127 || octets.every((o) => o === 0);
}

/** `address` is the normalised form `URL` gives: compressed, hexadecimal groups. */
function ipv6Local(address: string): boolean {
  const groups = expandIpv6(address);
  if (groups === null) return false;
  const [a, b, c, d, e, f, g, h] = groups;
  // `[::ffff:127.0.0.1]` reaches 127.0.0.1.
  if (a === 0 && b === 0 && c === 0 && d === 0 && e === 0 && f === 0xffff) {
    return ipv4Local([(g ?? 0) >> 8, (g ?? 0) & 0xff, (h ?? 0) >> 8, (h ?? 0) & 0xff]);
  }
  const zeros = groups.slice(0, 7).every((x) => x === 0);
  return zeros && (h === 0 || h === 1);
}

function expandIpv6(address: string): number[] | null {
  const halves = address.split("::");
  if (halves.length > 2) return null;
  const parse = (part: string | undefined) =>
    part === undefined || part === "" ? [] : part.split(":").map((g) => Number.parseInt(g, 16));
  const head = parse(halves[0]);
  const tail = halves.length === 2 ? parse(halves[1]) : [];
  const missing = 8 - head.length - tail.length;
  if (halves.length === 1 ? missing !== 0 : missing < 0) return null;
  const groups = [...head, ...Array<number>(halves.length === 2 ? missing : 0).fill(0), ...tail];
  return groups.length === 8 && groups.every((g) => Number.isInteger(g)) ? groups : null;
}

/** Requests for a new window (`target="_blank"`, `window.open`) never open
 *  one: links to other web sites go to the browser, and anything else is
 *  refused. */
export function decideNewWindow(url: URL): NewWindow {
  const web = url.protocol === "http:" || url.protocol === "https:";
  return web && url.hostname !== "" && !isLoopback(url) && !hasUserinfo(url)
    ? "open-in-browser"
    : "deny";
}

/** Counts the browser opens of new-window requests: one per `OPEN_INTERVAL_MS`,
 *  and the rest are dropped. */
export class OpenLimit {
  private last: number | null = null;

  /** Whether a request at `now` (milliseconds) may open the browser, which
   *  then counts. */
  allow(now: number): boolean {
    if (this.last !== null && Math.max(0, now - this.last) < OPEN_INTERVAL_MS) return false;
    this.last = now;
    return true;
  }
}
