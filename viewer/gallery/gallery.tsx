import { useCallback, useEffect, useMemo, useState } from "react";

import reviewSchema from "../../schema/review-1.json";
import example from "../../schema/review-example.json";
import legacySchema from "../../schema/legacy-review-tiles.json";
import { ReviewTiles } from "../src/components/review-tiles/review-tiles";
import { validateTile } from "../src/precompiled/review-tile-validators.js";
import type { ReviewDocument } from "../src/review-document";
import { tileRecord, validReviewTile, withoutRetiredTileFields } from "../src/review-tiles";
import type { GalleryItem, GallerySources, SourceGroup } from "./sources";
import { standInDocument } from "./tiles";

/** The tile gallery: every tile the dev server finds, drawn by the same
 *  `ReviewTiles` the review page uses, with the viewer's own verdict on it.
 *  Dev-only (see `vite.gallery.config.ts`); the standalone app never loads it. */

const GROUPS: { id: SourceGroup; title: string; hint: string }[] = [
  {
    id: "runs",
    title: "Prompt-eval runs",
    hint: "attempts under tools/prompt-eval/runs, read from each attempt's document",
  },
  {
    id: "stored",
    title: "Stored reviews",
    hint: "exported from the dev database into oss/viewer/gallery-data/stored",
  },
  {
    id: "fixtures",
    title: "Fixtures",
    hint: "oss/viewer/gallery-data/fixtures: one tile or an array per file, edit and save",
  },
];

type Loaded = { state: "loading" } | { state: "failed"; error: string } | { state: "ready" };

const POLL_MS = 1500;

/** The sources, asked for again every moment: a finished eval run, a saved
 *  fixture or a new export draws without a reload. */
function useSources(): { sources: GallerySources | null; load: Loaded } {
  const [sources, setSources] = useState<GallerySources | null>(null);
  const [load, setLoad] = useState<Loaded>({ state: "loading" });
  useEffect(() => {
    let stop = false;
    let version = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const response = await fetch(`/api/sources?version=${version}`, { cache: "no-store" });
        if (!response.ok) throw new Error(`The sources request failed (${response.status}).`);
        const body = (await response.json()) as GallerySources | { unchanged: true };
        if (!stop && !("unchanged" in body)) {
          version = body.version;
          setSources(body);
        }
        if (!stop) setLoad({ state: "ready" });
      } catch (error) {
        if (!stop) setLoad({ state: "failed", error: String(error) });
      }
      if (!stop) timer = setTimeout(poll, POLL_MS);
    };
    void poll();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, []);
  return { sources, load };
}

type Filters = { kind: string; group: string; invalid: boolean };

function readFilters(): Filters {
  const params = new URLSearchParams(window.location.search);
  return {
    kind: params.get("kind") ?? "",
    group: params.get("source") ?? "",
    invalid: params.get("invalid") === "1",
  };
}

function useFilters(): [Filters, (next: Partial<Filters>) => void] {
  const [filters, setFilters] = useState(readFilters);
  const update = useCallback((next: Partial<Filters>) => {
    setFilters((current) => {
      const merged = { ...current, ...next };
      const params = new URLSearchParams();
      if (merged.kind) params.set("kind", merged.kind);
      if (merged.group) params.set("source", merged.group);
      if (merged.invalid) params.set("invalid", "1");
      const query = params.toString();
      window.history.replaceState(null, "", query ? `?${query}` : window.location.pathname);
      return merged;
    });
  }, []);
  return [filters, update];
}

type Verdict = { valid: true } | { valid: false; reasons: string[] };

/** The viewer's own verdict: the shared shape first, then the checks that
 *  need the document (a code source's path, version, revision and diff). */
function verdictFor(tile: unknown, document: ReviewDocument): Verdict {
  const value = withoutRetiredTileFields(tile);
  const fields = tileRecord(value);
  if (!fields) return { valid: false, reasons: ["not an object"] };
  if (!validateTile(value)) return { valid: false, reasons: shapeErrors(fields) };
  if (!validReviewTile(value, document)) {
    const { head, merge_base: mergeBase, version } = document.review;
    return {
      valid: false,
      reasons: [
        `the shape is valid but a code source fails the viewer's checks: its path, review_version (document is v${version}), revision (head ${head.slice(0, 9)} or merge base ${mergeBase.slice(0, 9)} at that version) or diff line numbers`,
      ],
    };
  }
  return { valid: true };
}

type ShapeError = {
  instancePath: string;
  schemaPath: string;
  keyword: string;
  message?: string;
  params?: { additionalProperty?: string };
};

type Variants = { oneOf: { properties: { kind: { const: string } } }[] };

/** Where the schema lists a kind's variant: its index in the tile variants. */
function variantIndex(kind: unknown): number | null {
  for (const schema of [
    reviewSchema.$defs.tile as Variants,
    legacySchema.$defs.legacyTile as Variants,
  ]) {
    const index = schema.oneOf.findIndex((variant) => variant.properties.kind.const === kind);
    if (index >= 0) return index;
  }
  return null;
}

/** What the validator said about the tile, narrowed to the variant of its own
 *  kind: every other variant fails too, which only buries the real problem. */
function shapeErrors(tile: Record<string, unknown>): string[] {
  const errors = (validateTile as unknown as { errors?: ShapeError[] | null }).errors ?? [];
  const own = variantIndex(tile.kind);
  // Ajv's path names the variant first (`.../oneOf/<index>/...`), then what failed in it.
  const relevant = errors.filter((error) =>
    own === null
      ? error.keyword !== "anyOf" && error.keyword !== "oneOf"
      : Number(/\/oneOf\/(\d+)\//.exec(error.schemaPath)?.[1]) === own &&
        !(error.keyword === "const" && error.instancePath === "/kind"),
  );
  const reasons = (relevant.length ? relevant : errors).map((error) => {
    const extra = error.params?.additionalProperty ? ` "${error.params.additionalProperty}"` : "";
    return `${error.instancePath || "(tile)"} ${error.message ?? "is invalid"}${extra}`;
  });
  return reasons.length ? [...new Set(reasons)].slice(0, 8) : ["does not match the tile schema"];
}

function kindOf(tile: unknown): string {
  const kind = tileRecord(tile)?.kind;
  return typeof kind === "string" ? kind : "(no kind)";
}

function idOf(tile: unknown): string {
  const id = tileRecord(tile)?.id;
  return typeof id === "string" ? id : "(no id)";
}

const documents = new WeakMap<GalleryItem, ReviewDocument>();

/** The document a file's tiles are shown against: its own, or the example
 *  adjusted to the tiles' code sources (see `standInDocument`). */
function documentFor(item: GalleryItem): ReviewDocument {
  let document = documents.get(item);
  if (!document) {
    document =
      (item.document as ReviewDocument | undefined) ??
      standInDocument(
        example as unknown as ReviewDocument,
        item.tiles.map((found) => found.tile),
      );
    documents.set(item, document);
  }
  return document;
}

type Entry = { item: GalleryItem; tile: unknown; location: string; position: number };

function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex min-w-0 items-center gap-2">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded-(--radius-sm) bg-(--surface-sunken) px-2 py-1 text-xs">
        {command}
      </code>
      <button
        type="button"
        className="shrink-0 rounded-(--radius-sm) border border-(--border-default) px-2 py-1 text-xs"
        onClick={() => {
          void navigator.clipboard?.writeText(command).then(() => setCopied(true));
        }}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </div>
  );
}

function TileCard({ entry }: { entry: Entry }) {
  const { item, tile, location } = entry;
  const document = documentFor(item);
  const verdict = verdictFor(tile, document);
  return (
    <article
      className="min-w-0 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card)"
      data-testid="gallery-tile"
      data-kind={kindOf(tile)}
      data-valid={verdict.valid ? "true" : "false"}
    >
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-(--border-subtle) px-4 py-2 text-xs">
        <strong className="text-base">{kindOf(tile)}</strong>
        <code className="text-(--text-secondary)">{idOf(tile)}</code>
        <span className="text-(--text-tertiary)">{location}</span>
        <span
          className={
            verdict.valid
              ? "ml-auto rounded-full bg-(--status-positive-bg) px-2 py-0.5 text-(--status-positive)"
              : "ml-auto rounded-full bg-(--status-critical-bg) px-2 py-0.5 text-(--status-critical)"
          }
        >
          {verdict.valid ? "valid" : "invalid"}
        </span>
      </header>
      {!verdict.valid && (
        <ul
          className="space-y-1 border-b border-(--border-subtle) bg-(--status-critical-bg) px-4 py-2 text-xs text-(--status-critical)"
          aria-label="Validation errors"
        >
          {verdict.reasons.map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}
      <div className="p-4">
        <ReviewTiles document={document} tiles={[tile]} />
      </div>
    </article>
  );
}

function ItemBlock({ item, entries }: { item: GalleryItem; entries: Entry[] }) {
  return (
    <section className="min-w-0 space-y-3" aria-label={item.label}>
      <div className="space-y-1">
        <h3 className="text-lg">{item.label}</h3>
        <p className="text-xs text-(--text-tertiary)">
          from <code>{item.file}</code>
          {item.document === undefined ? " · shown against the example document" : ""}
        </p>
        {item.command && <CopyCommand command={item.command} />}
      </div>
      <div className="space-y-4">
        {entries.map((entry) => (
          <TileCard key={`${item.id}:${entry.position}`} entry={entry} />
        ))}
      </div>
    </section>
  );
}

export function Gallery() {
  const { sources, load } = useSources();
  const [filters, setFilters] = useFilters();

  const entries = useMemo<Entry[]>(
    () =>
      (sources?.items ?? []).flatMap((item) =>
        item.tiles.map((found, position) => ({
          item,
          tile: found.tile,
          location: found.location,
          position,
        })),
      ),
    [sources],
  );
  const kinds = useMemo(
    () => [...new Set(entries.map((entry) => kindOf(entry.tile)))].toSorted(),
    [entries],
  );
  const counts = useMemo(() => {
    const result = new Map<string, number>();
    for (const entry of entries)
      result.set(entry.item.group, (result.get(entry.item.group) ?? 0) + 1);
    return result;
  }, [entries]);

  const shown = entries.filter(
    (entry) =>
      (!filters.kind || kindOf(entry.tile) === filters.kind) &&
      (!filters.group || entry.item.group === filters.group),
  );

  return (
    <div className="mx-auto max-w-5xl space-y-8 px-4 pb-16" data-testid="gallery">
      <header className="sticky top-0 z-10 -mx-4 space-y-2 border-b border-(--border-default) bg-(--surface-page) px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h1 className="text-2xl">Tile gallery</h1>
          <p className="text-xs text-(--text-tertiary)" data-testid="gallery-count">
            {shown.length} of {entries.length} tiles · edit a tile component or a fixture and this
            page updates
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-xs">
          <label className="flex items-center gap-2">
            Kind
            <select
              className="rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-2 py-1"
              value={filters.kind}
              onChange={(event) => setFilters({ kind: event.target.value })}
            >
              <option value="">All kinds</option>
              {kinds.map((kind) => (
                <option key={kind} value={kind}>
                  {kind}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-2">
            Source
            <select
              className="rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-2 py-1"
              value={filters.group}
              onChange={(event) => setFilters({ group: event.target.value })}
            >
              <option value="">All sources</option>
              {GROUPS.map((group) => (
                <option key={group.id} value={group.id}>
                  {group.title} ({counts.get(group.id) ?? 0})
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={filters.invalid}
              onChange={(event) => setFilters({ invalid: event.target.checked })}
            />
            Invalid only
          </label>
        </div>
      </header>

      {load.state === "failed" && (
        <p role="alert" className="text-base text-(--status-critical)">
          {load.error}
        </p>
      )}
      {load.state === "loading" && <p className="text-base text-(--text-secondary)">Loading…</p>}

      {sources?.problems.map((problem) => (
        <p key={problem.file} role="alert" className="text-xs text-(--status-caution)">
          Could not read <code>{problem.file}</code>: {problem.message}
        </p>
      ))}

      {GROUPS.filter((group) => !filters.group || filters.group === group.id).map((group) => {
        const items = (sources?.items ?? []).filter((item) => item.group === group.id);
        const blocks = items
          .map((item) => ({
            item,
            entries: shown.filter(
              (entry) =>
                entry.item === item &&
                (!filters.invalid || !verdictFor(entry.tile, documentFor(entry.item)).valid),
            ),
          }))
          .filter((block) => block.entries.length > 0);
        return (
          <section key={group.id} className="space-y-6" aria-label={group.title}>
            <div>
              <h2 className="text-lg">{group.title}</h2>
              <p className="text-xs text-(--text-tertiary)">{group.hint}</p>
            </div>
            {blocks.length === 0 ? (
              <p className="text-xs text-(--text-secondary)">
                {emptyMessage(group.id, sources, items.length > 0)}
              </p>
            ) : (
              blocks.map((block) => (
                <ItemBlock key={block.item.id} item={block.item} entries={block.entries} />
              ))
            )}
          </section>
        );
      })}
    </div>
  );
}

function emptyMessage(
  group: SourceGroup,
  sources: GallerySources | null,
  hasItems: boolean,
): string {
  if (hasItems) return "Nothing here matches the filters.";
  if (group === "runs") {
    const without = sources?.runAttemptsWithoutTiles ?? 0;
    return without > 0
      ? `${without} attempt${without === 1 ? "" : "s"} found, none with tiles. Run the eval with tile_kinds in the manifest.`
      : "No attempts found. Run prompt-eval with tile_kinds in the manifest (see tools/prompt-eval/README.md).";
  }
  if (group === "stored")
    return "None exported. Run just viewer::gallery-export (the dev database has no tiles unless a review was generated with an allow-list).";
  return "No fixtures. Put a tile, or an array of tiles, in a .json file there.";
}
