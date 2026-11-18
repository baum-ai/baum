import { createHash } from "node:crypto";
import {
  closeSync,
  type Dirent,
  existsSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  statSync,
} from "node:fs";
import { basename, dirname, join, relative, sep } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { pathToFileURL } from "node:url";

import type { DiagramSource, DocumentEntry, Entry } from "./contract.ts";

/** What the diagram gallery's dev server finds on disk: replay runs, the
 *  desktop app's review stores, and loose JSON files. A baum.review/2 document
 *  holds its own diagrams and goes to the page whole, which reads it with the
 *  app's own parser; an older version's composition is read here. Node only;
 *  the page imports contract.ts, never this. Problems name paths and counts,
 *  never review content. */

export type SourceDirectories = {
  /** Replay's output directory (`<run>/cases/<id>/document.json` and
   *  `composition.json`). */
  runs?: string;
  /** Searched for the desktop app's `versions.sqlite` review stores. */
  stores?: string;
  /** Loose `*.json` files: a review, a composition, one diagram or an array. */
  files?: string;
};

export type Scan = {
  version: string;
  entries: Entry[];
  documents: DocumentEntry[];
  problems: string[];
};

const REVIEW_V2 = "baum.review/2";
const maxDepth = 6;
/** Walk bounds; a test may lower them. */
export const limits = { files: 2000, directories: 20000 };
// How long the stores' paths are trusted before their directory is walked again.
const storeWalkMs = 30_000;
// Never worth descending into while looking for replay outputs.
const skipped = new Set(["node_modules", "objects", "attachments", "checkouts.git"]);
const replayFiles = new Set(["composition.json", "document.json"]);

type Found = { path: string; stamp: string };

function stampOf(path: string): string | null {
  try {
    const { mtimeMs, size } = statSync(path);
    return `${mtimeMs}:${size}`;
  } catch {
    return null;
  }
}

/** SQLite's file change counter (header bytes 24–27), which every commit
 *  outside WAL mode bumps: a write can leave a store's size and coarse
 *  modification time as they were. */
function changeCounter(path: string): string {
  let fd: number | undefined;
  try {
    fd = openSync(path, "r");
    const header = Buffer.alloc(4);
    return readSync(fd, header, 0, 4, 24) === 4 ? String(header.readUInt32BE(0)) : "";
  } catch {
    return "";
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

const byName = (a: Dirent, b: Dirent) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);

/** Replay's outputs under `root`, depth- and count-bounded. Runs are named by
 *  when they started, so they are walked newest first and the bound leaves
 *  out the oldest. Symbolic links are not followed, so replay's chain links do
 *  not list a case twice. */
function findReplayFiles(root: string, problems: string[]): Found[] {
  const found: Found[] = [];
  let directories = 0;
  const walk = (directory: string, depth: number) => {
    if (found.length >= limits.files || directories >= limits.directories) return;
    directories++;
    let entries: Dirent[];
    try {
      entries = readdirSync(directory, { withFileTypes: true });
    } catch {
      return;
    }
    entries.sort(byName);
    if (depth === 0) entries.reverse();
    for (const entry of entries) {
      const path = join(directory, entry.name);
      if (entry.isFile() && replayFiles.has(entry.name)) {
        const stamp = stampOf(path);
        if (stamp && found.length < limits.files) found.push({ path, stamp });
      } else if (
        entry.isDirectory() &&
        depth < maxDepth &&
        !skipped.has(entry.name) &&
        !entry.name.startsWith(".")
      ) {
        walk(path, depth + 1);
      }
    }
  };
  walk(root, 0);
  if (found.length >= limits.files || directories >= limits.directories)
    problems.push(`replay: stopped after ${found.length} files in ${directories} directories`);
  return found;
}

const clonePath = join(".git", "baum", "reviews", "versions.sqlite");
const storeWalks = new Map<string, { at: number; paths: string[]; problem?: string }>();

/** The review stores under `root`: a clone's `.git/baum/reviews/versions.sqlite`,
 *  and any other `versions.sqlite`, as in the app's fallback location
 *  (`$XDG_DATA_HOME/baum/reviews/<hash>/`). A clone is not descended into, nor
 *  are dot directories and node_modules. The paths are kept for storeWalkMs;
 *  in between, each poll only stats them. */
function findStores(root: string, problems: string[]): string[] {
  const hit = storeWalks.get(root);
  if (hit && Date.now() - hit.at < storeWalkMs) {
    if (hit.problem) problems.push(hit.problem);
    return hit.paths;
  }
  const paths: string[] = [];
  let directories = 0;
  const walk = (directory: string, depth: number) => {
    if (paths.length >= limits.files || directories >= limits.directories) return;
    directories++;
    if (existsSync(join(directory, ".git"))) {
      if (stampOf(join(directory, clonePath))) paths.push(join(directory, clonePath));
      return;
    }
    let entries: Dirent[];
    try {
      entries = readdirSync(directory, { withFileTypes: true });
    } catch {
      return;
    }
    entries.sort(byName);
    for (const entry of entries) {
      const path = join(directory, entry.name);
      if (entry.isFile() && entry.name === "versions.sqlite") paths.push(path);
      else if (
        entry.isDirectory() &&
        depth < maxDepth &&
        entry.name !== "node_modules" &&
        !entry.name.startsWith(".")
      )
        walk(path, depth + 1);
    }
  };
  walk(root, 0);
  const problem =
    paths.length >= limits.files || directories >= limits.directories
      ? `stores: stopped after ${paths.length} stores in ${directories} directories`
      : undefined;
  if (problem) problems.push(problem);
  storeWalks.set(root, { at: Date.now(), paths, problem });
  return paths;
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function isTile(
  value: unknown,
): value is { id: string; type: string; title?: unknown; data?: unknown } {
  const tile = record(value);
  return typeof tile?.id === "string" && typeof tile.type === "string";
}

type Diagram = { id: string; type: string; title: string; placement?: string; data: unknown };

function toDiagram(tile: { id: string; type: string; title?: unknown; data?: unknown }): Diagram {
  return {
    id: tile.id,
    type: tile.type,
    title: typeof tile.title === "string" ? tile.title : "",
    data: tile.data,
  };
}

/** A composition's diagrams with where each is placed, or why none can be read.
 *  `schema` may be absent only in a loose file. */
function compositionDiagrams(value: unknown, requireSchema: boolean): Diagram[] | string {
  const composition = record(value);
  if (!composition) return "not an object";
  const schema = composition.schema;
  if (schema !== "baum.composition/2" && (requireSchema || schema !== undefined))
    return typeof schema === "string" && schema.startsWith("baum.composition/")
      ? `${schema.slice(0, 40)}, not baum.composition/2`
      : "not a baum.composition/2";
  if (composition.diagrams === undefined)
    return requireSchema ? [] : "expected a composition, a diagram or an array of diagrams";
  if (!Array.isArray(composition.diagrams)) return "diagrams is not an array";
  const overview = record(composition.overview)?.diagram;
  const sections = Array.isArray(composition.sections) ? composition.sections : [];
  const placement = (id: string): string | undefined => {
    if (overview === id) return "overview";
    for (const section of sections) {
      const placed = record(section);
      if (
        typeof placed?.id === "string" &&
        Array.isArray(placed.tiles) &&
        placed.tiles.includes(id)
      )
        return placed.id;
    }
    return undefined;
  };
  return composition.diagrams.filter(isTile).map((tile) => {
    const diagram = toDiagram(tile);
    const placed = placement(diagram.id);
    return placed ? { ...diagram, placement: placed } : diagram;
  });
}

type Read = { diagrams: Diagram[] } | { document: unknown } | { skip: string };

/** What a stored or written JSON value holds. A loose file may also be one
 *  diagram, an array of them, or a composition without its schema. */
function readValue(value: unknown, loose: boolean): Read {
  if (record(value)?.schema === REVIEW_V2) return { document: value };
  if (loose && Array.isArray(value)) return { diagrams: value.filter(isTile).map(toDiagram) };
  if (loose && isTile(value)) return { diagrams: [toDiagram(value)] };
  const diagrams = compositionDiagrams(value, !loose);
  return typeof diagrams === "string" ? { skip: diagrams } : { diagrams };
}

// V8's JSON errors quote the text they stopped at, so they are never passed on.
function parseJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error("not valid JSON");
  }
}

/** A reason safe to show: an error code, SQLite's fixed text for its result
 *  code, or our own message; never an error's message, which may quote what
 *  it read. */
function reason(error: unknown): string {
  const code = record(error)?.code;
  const errstr = record(error)?.errstr;
  if (typeof code === "string") return typeof errstr === "string" ? `${code} (${errstr})` : code;
  return error instanceof Error && error.message === "not valid JSON"
    ? error.message
    : "could not be read";
}

// Parsed replay and loose files by path, kept while the file's stamp is
// unchanged and dropped once a scan no longer finds the file.
const cache = new Map<string, { stamp: string; value: unknown }>();
function cached<T>(seen: Set<string>, path: string, stamp: string, read: () => T): T {
  seen.add(path);
  const hit = cache.get(path);
  if (hit?.stamp === stamp) return hit.value as T;
  const value = read();
  cache.set(path, { stamp, value });
  return value;
}

/** Replay writes `<run>/cases/<id>/<file>`; label it "run · case". */
function replayOrigin(path: string): string {
  const parts = path.split(sep);
  const index = parts.lastIndexOf("cases");
  if (index > 0 && index === parts.length - 3)
    return `${parts.slice(0, index).join("/")} · ${parts[index + 1]}`;
  return dirname(path) === "." ? path : dirname(path).split(sep).join("/");
}

/** A store's label: its clone, or the directory that holds it. */
function storeLabel(path: string): string {
  const parts = (path.endsWith(clonePath) ? path.slice(0, -clonePath.length) : dirname(path))
    .split(sep)
    .filter(Boolean);
  return parts.length ? parts.join("/") : ".";
}

/** Opened read-only, as the app opens a store it cannot write: a WAL store
 *  needs its -shm file, which SQLite cannot create in a read-only directory
 *  (SQLITE_READONLY_DIRECTORY). With no log and no rollback journal the last
 *  writer checkpointed and closed, so the file is whole and is read as it is
 *  (immutable); otherwise the error stands. */
function openStore(path: string): DatabaseSync {
  let database: DatabaseSync | undefined;
  try {
    database = new DatabaseSync(path, { readOnly: true });
    database.prepare("PRAGMA schema_version").get();
    return database;
  } catch (error) {
    database?.close();
    const code = record(error)?.errcode;
    const unopenable = code === 1544 || (typeof code === "number" && (code & 0xff) === 14);
    if (!unopenable || existsSync(`${path}-wal`) || existsSync(`${path}-journal`)) throw error;
    return new DatabaseSync(`${pathToFileURL(path).href}?immutable=1`, { readOnly: true });
  }
}

type StoreItem = Read | { problem: string };
type StoreState = { stamp: string; latest: Map<string, StoreItem>; missing: string[] };
// Each store's latest version per pull request, by "<repository>#<pr> v<n>".
const stores = new Map<string, StoreState>();

/** What a version's row holds: its baum.review/2 document, else its composition. */
function storeItem(row: Record<string, unknown> | undefined): StoreItem {
  const text = (value: unknown) =>
    typeof value === "string"
      ? value
      : value instanceof Uint8Array
        ? new TextDecoder().decode(value)
        : null;
  const document = text(row?.document);
  if (document?.includes(`"${REVIEW_V2}"`)) {
    try {
      const value = JSON.parse(document);
      if (record(value)?.schema === REVIEW_V2) return { document: value };
    } catch {
      // Not a document this page can show; its composition may still be.
    }
  }
  const composition = text(row?.composition);
  if (composition === null) return { diagrams: [] };
  try {
    return readValue(parseJSON(composition), false);
  } catch (error) {
    return { problem: reason(error) };
  }
}

/** The latest version of each pull request in a store. Versions are only ever
 *  added, so a version read before is not read again; only the keys are. */
function readStore(path: string, stamp: string, previous?: StoreState): StoreState {
  const database = openStore(path);
  try {
    const columns = new Set(
      (database.prepare("PRAGMA table_info(versions)").all() as { name: string }[]).map(
        (column) => column.name,
      ),
    );
    const bodies = ["document", "composition"].filter((name) => columns.has(name));
    const missing = ["composition", "document"].filter((name) => !columns.has(name));
    const keys = database
      .prepare(
        "SELECT repository, pr, MAX(version) AS version FROM versions GROUP BY repository, pr ORDER BY repository, pr",
      )
      .all() as { repository: string; pr: number; version: number }[];
    const body = bodies.length
      ? database.prepare(
          `SELECT ${bodies.join(", ")} FROM versions WHERE repository = ? AND pr = ? AND version = ?`,
        )
      : null;
    const latest = new Map<string, StoreItem>();
    for (const { repository, pr, version } of keys) {
      const name = `${repository}#${pr} v${version}`;
      const known = previous?.latest.get(name);
      if (known) latest.set(name, known);
      else if (body)
        latest.set(
          name,
          storeItem(body.get(repository, pr, version) as Record<string, unknown> | undefined),
        );
    }
    return { stamp, latest, missing };
  } finally {
    database.close();
  }
}

export function scanSources(directories: SourceDirectories): Scan {
  const entries: Entry[] = [];
  const documents: DocumentEntry[] = [];
  const problems: string[] = [];
  const stamps: string[] = [];
  const keys = new Set<string>();
  const seenFiles = new Set<string>();
  const seenStores = new Set<string>();
  const add = (source: DiagramSource, where: string, origin: string, read: Read) => {
    if ("document" in read) {
      documents.push({ key: `${source}:${where}`, source, origin, document: read.document });
      return;
    }
    if (!("diagrams" in read)) return;
    for (const diagram of read.diagrams) {
      let key = `${source}:${where}:${diagram.id}`;
      for (let n = 2; keys.has(key); n++) key = `${source}:${where}:${diagram.id}#${n}`;
      keys.add(key);
      entries.push({ key, source, origin, ...diagram });
    }
  };
  const countSkips = (label: string, skips: Map<string, number>) => {
    for (const [why, count] of skips) problems.push(`${label}: ${count} skipped, ${why}`);
  };
  const skip = (skips: Map<string, number>, why: string) =>
    skips.set(why, (skips.get(why) ?? 0) + 1);

  if (directories.runs) {
    const root = directories.runs;
    const skips = new Map<string, number>();
    for (const { path, stamp } of findReplayFiles(root, problems)) {
      const file = relative(root, path);
      stamps.push(`replay:${file}:${stamp}`);
      try {
        const read = cached(seenFiles, path, stamp, () =>
          readValue(parseJSON(readFileSync(path, "utf8")), false),
        );
        // A document.json other than baum.review/2 is a v1 review, whose
        // diagrams are in the composition.json beside it.
        if ("skip" in read) {
          if (basename(path) === "composition.json") skip(skips, read.skip);
          continue;
        }
        add("replay", file.split(sep).join("/"), replayOrigin(file), read);
      } catch (error) {
        problems.push(`replay ${file}: ${reason(error)}`);
      }
    }
    countSkips("replay", skips);
  }

  if (directories.stores) {
    const root = directories.stores;
    for (const path of findStores(root, problems)) {
      const label = storeLabel(relative(root, path));
      const stamp = stampOf(path);
      if (!stamp) continue;
      const fullStamp = [
        stamp,
        changeCounter(path),
        stampOf(`${path}-wal`),
        stampOf(`${path}-journal`),
      ].join(":");
      stamps.push(`store:${label}:${fullStamp}`);
      seenStores.add(path);
      let state = stores.get(path);
      try {
        if (state?.stamp !== fullStamp) {
          state = readStore(path, fullStamp, state);
          stores.set(path, state);
        }
      } catch (error) {
        problems.push(`store ${label}: ${reason(error)}`);
        continue;
      }
      for (const column of state.missing)
        problems.push(`store ${label}: versions has no ${column} column`);
      const skips = new Map<string, number>();
      for (const [version, item] of state.latest) {
        if ("problem" in item) problems.push(`store ${label} ${version}: ${item.problem}`);
        else if ("skip" in item) skip(skips, item.skip);
        else add("store", `${label}:${version}`, `${label} · ${version}`, item);
      }
      countSkips(`store ${label}`, skips);
    }
  }

  if (directories.files) {
    let names: string[] = [];
    try {
      names = readdirSync(directories.files)
        .filter((name) => name.endsWith(".json"))
        .sort();
    } catch {
      // A missing directory holds nothing.
    }
    for (const name of names) {
      const path = join(directories.files, name);
      const stamp = stampOf(path);
      if (!stamp) continue;
      stamps.push(`file:${name}:${stamp}`);
      try {
        const read = readValue(
          cached(seenFiles, path, stamp, () => parseJSON(readFileSync(path, "utf8"))),
          true,
        );
        if ("skip" in read) problems.push(`file ${name}: ${read.skip}`);
        else add("file", name, basename(name, ".json"), read);
      } catch (error) {
        problems.push(`file ${name}: ${reason(error)}`);
      }
    }
  }

  for (const path of cache.keys()) if (!seenFiles.has(path)) cache.delete(path);
  for (const path of stores.keys()) if (!seenStores.has(path)) stores.delete(path);

  const version = createHash("sha256")
    .update(stamps.join("\n"))
    .update(JSON.stringify(problems))
    .digest("hex")
    .slice(0, 16);
  return { version, entries, documents, problems };
}

/** How many parsed files and stores are kept, for tests. */
export function cacheSizes(): { files: number; stores: number } {
  return { files: cache.size, stores: stores.size };
}
