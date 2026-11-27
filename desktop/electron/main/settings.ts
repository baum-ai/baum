// desktop.json, the native settings that stay available before the daemon
// starts. Every read and write is synchronous, so a change reads the latest
// document and replaces one section within one turn of the event loop: no
// other change can interleave.
import {
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  writeSync,
} from "node:fs";
import { basename, dirname, join } from "node:path";

export const FILE = "desktop.json";

export type Section = Record<string, unknown>;

let nextFile = 0;

function isObject(value: unknown): value is Section {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function document(path: string): Section {
  let data: string;
  try {
    data = readFileSync(path, "utf8");
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return {};
    throw e;
  }
  const parsed: unknown = JSON.parse(data);
  if (!isObject(parsed)) throw new Error("desktop settings must be a JSON object");
  return parsed;
}

function section(object: Section, name: string): Section {
  const value = object[name];
  if (value === undefined) return {};
  if (!isObject(value)) throw new Error("desktop settings section must be a JSON object");
  return value;
}

/** A strict read lets a native action refuse a damaged file before changing
 *  anything whose record would have to be saved there. */
export function readSection(path: string, name: string): Section {
  return section(document(path), name);
}

/** A bad setting must not leave the window unreadable or stop startup; writes
 *  still refuse the damaged file, preserving all its other settings. */
export function loadSection(path: string, name: string): Section {
  try {
    return readSection(path, name);
  } catch (e) {
    console.error(`baum desktop: ${name} settings unreadable, using defaults: ${String(e)}`);
    return {};
  }
}

/** Reads the latest section and replaces just that section. Unrelated
 *  sections, including ones this version does not know, survive. The change
 *  edits the section in place; its return value is returned. */
export function updateSection<R>(path: string, name: string, change: (section: Section) => R): R {
  const object = document(path);
  const saved = structuredClone(section(object, name));
  const result = change(saved);
  object[name] = saved;
  write(path, `${JSON.stringify(object, null, 2)}\n`);
  return result;
}

/** Writes a private temporary file, flushes it and renames it into place. */
export function write(path: string, data: string): void {
  const dir = dirname(path);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const temporary = join(
    dir,
    `.${basename(path)}.${process.pid}.${process.hrtime.bigint()}.${nextFile++}`,
  );
  const fd = openSync(temporary, "wx", 0o600);
  try {
    try {
      writeSync(fd, data);
      fsyncSync(fd);
    } finally {
      closeSync(fd);
    }
    renameSync(temporary, path);
  } catch (e) {
    rmSync(temporary, { force: true });
    throw e;
  }
}
