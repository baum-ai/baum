import type { IndexedHunk, ReviewDocument } from "./review-document.ts";

/** Lookups over one review document's hunks, built once per document object
 *  so chips and evidence blocks do not scan the hunk list on every render.
 *  Duplicate ids resolve to the first hunk, as a linear `find` would. */
export type HunkIndex = {
  byId: ReadonlyMap<string, IndexedHunk>;
  /** Paths of hunks as new path. */
  paths: ReadonlySet<string>;
  /** Every directory prefix (without the trailing "/") of a hunk path, old
   *  path or context reference path. */
  citedDirectories: ReadonlySet<string>;
  urls: Map<IndexedHunk, string>;
};

const indexes = new WeakMap<ReviewDocument, HunkIndex>();

export function hunkIndex(document: ReviewDocument): HunkIndex {
  const cached = indexes.get(document);
  if (cached) return cached;
  const byId = new Map<string, IndexedHunk>();
  const paths = new Set<string>();
  const cited = new Set<string>();
  for (const hunk of document.index.hunks) {
    if (!byId.has(hunk.id)) byId.set(hunk.id, hunk);
    paths.add(hunk.path);
    cited.add(hunk.path);
    if (hunk.old_path) cited.add(hunk.old_path);
  }
  for (const part of Object.values(document.summary.context ?? {})) {
    for (const ref of part?.refs ?? []) cited.add(ref.path);
  }
  const citedDirectories = new Set<string>();
  for (const path of cited) {
    for (let i = path.indexOf("/"); i !== -1; i = path.indexOf("/", i + 1)) {
      citedDirectories.add(path.slice(0, i));
    }
  }
  const built: HunkIndex = { byId, paths, citedDirectories, urls: new Map() };
  indexes.set(document, built);
  return built;
}

/** A lookup returning the first item whose path or old path matches, as
 *  `find` over the list would. */
export function firstByPath<T extends { path: string; old_path?: string | null }>(
  items: readonly T[],
): (path: string) => T | undefined {
  const map = new Map<string, T>();
  for (const item of items) {
    if (!map.has(item.path)) map.set(item.path, item);
    if (item.old_path && !map.has(item.old_path)) map.set(item.old_path, item);
  }
  return (path) => map.get(path);
}
