// Paths as the setup code compares them: two paths are equal when their
// components are (so `/a//b/` is `/a/b`), `starts_with` matches whole
// components, and `..` is never resolved. Unix paths split on `/`; Windows ones on `/` and `\`.
import { realpathSync } from "node:fs";

export type Platform = NodeJS.Platform;

const windows = (platform: Platform) => platform === "win32";

interface Parsed {
  /** `/` on Unix; on Windows the prefix (`C:`, `\\server\share`) and root. */
  root: string;
  parts: string[];
}

function parse(path: string, platform: Platform): Parsed {
  if (!windows(platform)) {
    const absolute = path.startsWith("/");
    const pieces = path.split("/");
    const parts: string[] = [];
    pieces.forEach((piece, i) => {
      if (piece === "") return;
      // A leading `.` of a relative path is a component; any other is not.
      if (piece === "." && !(i === 0 && !absolute)) return;
      parts.push(piece);
    });
    return { root: absolute ? "/" : "", parts };
  }
  const match = /^(\\\\[^\\/]+[\\/][^\\/]+|[A-Za-z]:)?([\\/])?/.exec(path);
  const prefix = match?.[1] ?? "";
  const rootSlash = match?.[2] ? "\\" : "";
  const rest = path.slice(match?.[0].length ?? 0);
  const parts = rest.split(/[\\/]/).filter((p, i) => p !== "" && (p !== "." || i === 0));
  return { root: prefix + rootSlash, parts };
}

function render({ root, parts }: Parsed, platform: Platform): string {
  const separator = windows(platform) ? "\\" : "/";
  return root + parts.join(separator);
}

export function isAbsolute(path: string, platform: Platform): boolean {
  if (!windows(platform)) return path.startsWith("/");
  return /^([A-Za-z]:[\\/]|\\\\[^\\/]+[\\/][^\\/]+)/.test(path);
}

export function samePath(a: string, b: string, platform: Platform): boolean {
  const x = parse(a, platform);
  const y = parse(b, platform);
  return (
    x.root === y.root &&
    x.parts.length === y.parts.length &&
    x.parts.every((p, i) => p === y.parts[i])
  );
}

/** Whether `base`'s components begin `path`'s. */
export function startsWith(path: string, base: string, platform: Platform): boolean {
  const x = parse(path, platform);
  const y = parse(base, platform);
  return x.root === y.root && y.parts.every((p, i) => x.parts[i] === p);
}

/** The last component unless it is `..` (or there is none). */
export function fileName(path: string, platform: Platform): string | null {
  const last = parse(path, platform).parts.at(-1);
  return last === undefined || last === ".." || last === "." ? null : last;
}

/** The path without its last component; null for a root or an empty path. */
export function parent(path: string, platform: Platform): string | null {
  const parsed = parse(path, platform);
  if (parsed.parts.length === 0) return null;
  return render({ root: parsed.root, parts: parsed.parts.slice(0, -1) }, platform);
}

/** `path` and each of its parents, ending at the root. */
export function ancestors(path: string, platform: Platform): string[] {
  const all: string[] = [];
  for (let p: string | null = path; p !== null; p = parent(p, platform)) all.push(p);
  return all;
}

export function hasParentComponent(path: string, platform: Platform): boolean {
  return parse(path, platform).parts.includes("..");
}

/** `base` joined with `rest`, which replaces it when absolute. */
export function join(base: string, rest: string, platform: Platform): string {
  if (isAbsolute(rest, platform) || base === "") return rest;
  const separator = windows(platform) ? "\\" : "/";
  const ends = windows(platform) ? /[\\/]$/.test(base) : base.endsWith("/");
  return ends ? base + rest : base + separator + rest;
}

export function pathDelimiter(platform: Platform): string {
  return windows(platform) ? ";" : ":";
}

/** A PATH value's entries, empty ones included, as std::env::split_paths. */
export function splitPaths(value: string, platform: Platform): string[] {
  return value.split(pathDelimiter(platform));
}

export function joinPaths(entries: readonly string[], platform: Platform): string {
  const delimiter = pathDelimiter(platform);
  const bad = entries.find((e) => e.includes(delimiter));
  if (bad !== undefined) throw new Error(`path segment contains separator \`${delimiter}\``);
  return entries.join(delimiter);
}

/** Whether two paths name the same file once links are resolved. */
export function sameFile(a: string, b: string, platform: Platform): boolean {
  let x: string;
  let y: string;
  try {
    x = realpathSync.native(a);
    y = realpathSync.native(b);
  } catch {
    return false;
  }
  const ascii = (s: string) => s.replace(/[A-Z]/g, (c) => c.toLowerCase());
  return platform === "win32" ? ascii(x) === ascii(y) : x === y;
}
