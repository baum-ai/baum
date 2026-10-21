import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";

import { documentTiles, fixtureParts, type FoundTile } from "./tiles.ts";

/** What the gallery's dev server finds on disk: prompt-eval run outputs,
 *  stored reviews exported from the dev database, and hand-edited fixtures.
 *  Dev-only, like the rest of this directory. */

export type SourceGroup = "runs" | "stored" | "fixtures";

export type GalleryItem = {
  id: string;
  group: SourceGroup;
  /** What the file is: the run and attempt, the pull request, the fixture. */
  label: string;
  /** Where it came from, relative to the directory it was read from. */
  file: string;
  /** For a run's attempt: the command that opens the whole review. */
  command?: string;
  /** The document the tiles are shown against; absent for a loose fixture. */
  document?: unknown;
  tiles: FoundTile[];
};

export type GallerySources = {
  /** Changes whenever a file the gallery reads does. */
  version: string;
  items: GalleryItem[];
  /** Files that could not be read, and why. */
  problems: { file: string; message: string }[];
  /** Attempts of the runs that hold no tiles, so their absence is explained. */
  runAttemptsWithoutTiles: number;
};

export type SourceDirectories = {
  /** prompt-eval's runs/ directory. */
  runs: string;
  /** Holds stored/ (exported reviews) and fixtures/ (hand-edited tiles). */
  data: string;
  /** The runs directory as the host sees it, for the `view` command. */
  hostRuns?: string;
};

type Cached = { stamp: string; parsed: unknown };
const cache = new Map<string, Cached>();

function readJSON(path: string): unknown {
  const { mtimeMs, size } = statSync(path);
  const stamp = `${mtimeMs}:${size}`;
  const hit = cache.get(path);
  if (hit?.stamp === stamp) return hit.parsed;
  const parsed: unknown = JSON.parse(readFileSync(path, "utf8"));
  cache.set(path, { stamp, parsed });
  return parsed;
}

function jsonFiles(directory: string, depth: number): string[] {
  let names: string[];
  try {
    names = readdirSync(directory).toSorted();
  } catch {
    return [];
  }
  const files: string[] = [];
  for (const name of names) {
    const path = join(directory, name);
    let directoryEntry = false;
    try {
      directoryEntry = statSync(path).isDirectory();
    } catch {
      continue;
    }
    if (directoryEntry) {
      if (depth > 0 && name !== "objects" && name !== "captures") {
        files.push(...jsonFiles(path, depth - 1));
      }
    } else if (name.endsWith(".json")) files.push(path);
  }
  return files;
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function isReviewDocument(value: unknown): boolean {
  return record(value)?.schema === "baum.review/1";
}

function documentLabel(document: unknown): string {
  const review = record(record(document)?.review);
  return review ? `${review.repository}#${review.pull_request} v${review.version}` : "review";
}

export function scanSources(directories: SourceDirectories): GallerySources {
  const items: GalleryItem[] = [];
  const problems: GallerySources["problems"] = [];
  const stamps: string[] = [];
  let runAttemptsWithoutTiles = 0;
  const fail = (file: string, error: unknown) =>
    problems.push({ file, message: error instanceof Error ? error.message : String(error) });
  const stamp = (path: string) => {
    const { mtimeMs, size } = statSync(path);
    stamps.push(`${path}:${mtimeMs}:${size}`);
  };

  // prompt-eval writes attempts/*.json under each run's output directory; a
  // stack or job attempt holds another kind of document and is left out.
  for (const path of jsonFiles(directories.runs, 4)) {
    if (!path.includes(`${join("attempts", "")}`)) continue;
    const file = relative(directories.runs, path);
    try {
      stamp(path);
      const attempt = record(readJSON(path));
      if (!attempt || !isReviewDocument(attempt.document)) continue;
      const tiles = documentTiles(attempt.document);
      if (tiles.length === 0) {
        runAttemptsWithoutTiles++;
        continue;
      }
      const model = record(attempt.model)?.name;
      const prompt = record(attempt.prompt)?.version;
      const kinds = Array.isArray(attempt.tile_kinds) ? attempt.tile_kinds.join(", ") : "";
      const host = directories.hostRuns ? join(directories.hostRuns, file) : join("runs", file);
      items.push({
        id: `runs/${file}`,
        group: "runs",
        label: `${documentLabel(attempt.document)} · ${prompt ?? "prompt"} · ${model ?? "model"}${kinds ? ` · tiles ${kinds}` : ""}`,
        file,
        command: `tools/prompt-eval/prompt-eval view ${directories.hostRuns ? host : resolve(host)}`,
        document: attempt.document,
        tiles,
      });
    } catch (error) {
      fail(file, error);
    }
  }

  // Stored reviews exported by `just viewer::gallery-export`: a file holds the
  // document, or `{ source, document }` with a note on where it came from.
  const stored = join(directories.data, "stored");
  for (const path of jsonFiles(stored, 1)) {
    const file = relative(directories.data, path);
    try {
      stamp(path);
      const value = record(readJSON(path));
      const document = isReviewDocument(value) ? value : value?.document;
      if (!isReviewDocument(document)) throw new Error("not a baum.review/1 document");
      const tiles = documentTiles(document);
      if (tiles.length === 0) continue;
      items.push({
        id: file,
        group: "stored",
        label: documentLabel(document),
        file,
        document,
        tiles,
      });
    } catch (error) {
      fail(file, error);
    }
  }

  // Fixtures: edit one and the gallery draws it again within a moment.
  const fixtures = join(directories.data, "fixtures");
  for (const path of jsonFiles(fixtures, 1)) {
    const file = relative(directories.data, path);
    try {
      stamp(path);
      const parts = fixtureParts(readJSON(path));
      if (!parts) throw new Error("expected one tile, an array of tiles or { document, tiles }");
      items.push({
        id: file,
        group: "fixtures",
        label: file.replace(/^fixtures\//, ""),
        file,
        document: parts.document,
        tiles: parts.tiles.map((tile, index) => ({
          tile,
          location: parts.tiles.length > 1 ? `entry ${index + 1}` : "file",
        })),
      });
    } catch (error) {
      fail(file, error);
    }
  }

  const version = createHash("sha256")
    .update(stamps.join("\n"))
    .update(JSON.stringify(problems))
    .digest("hex")
    .slice(0, 16);
  return { version, items, problems, runAttemptsWithoutTiles };
}
