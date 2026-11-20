import type { ReviewDocument } from "@baum/viewer/review-document";
import { useCallback, useEffect, useMemo, useState } from "react";
import { chromeOf } from "../src/review-view/chrome";
import { parseReviewV2 } from "../src/review-view/document";
import { review1303 } from "../src/review-view/fixture-1303";
import {
  CONTEXT_ID,
  listedSections,
  OVERVIEW_OWNER,
  type ReviewV2,
  reviewOf,
  type ViewTile,
  withReview,
} from "../src/review-view/model";
import { NATIVE } from "../src/review-view/native-tiles";
import { ViewTileCard } from "../src/review-view/view-tile";
import type { TileContext } from "../src/tiles/types";
import type { DocumentEntry, Entry, Response } from "./contract";
import { SAMPLES } from "./samples";

/** The native diagram gallery: every flow, columns, sequence and table it
 *  knows, drawn by the review page's own `ViewTileCard` at the page's widths,
 *  in light and dark side by side. Dev-only (`vite.gallery.config.ts`). */

type Source = "fixture" | "sample" | Entry["source"];

type Shown = {
  key: string;
  source: Source;
  origin: string;
  tile: ViewTile;
  /** Where the page draws it: "overview", "context" or a section's ID; none
   *  when the page doesn't draw it at all. */
  placement?: string;
  /** The review its citations resolve against. */
  review: ReviewDocument;
};

const SOURCES: { id: Source; title: string; hint: string }[] = [
  {
    id: "fixture",
    title: "Fixture acme/demo#7",
    hint: "the review page's prototype fixture, src/review-view/fixture-1303.ts",
  },
  { id: "sample", title: "Samples", hint: "edge cases in gallery/samples.ts" },
  {
    id: "replay",
    title: "Replay runs",
    hint: "each case's document.json and composition.json under replay's output directory",
  },
  {
    id: "store",
    title: "Review stores",
    hint: "review documents and compositions in the desktop app's review stores",
  },
  { id: "file", title: "Files", hint: "loose JSON in oss/app/gallery-data" },
];

const TYPES = ["flow", "columns", "sequence", "table"];

/** The width of a section's column on the review page (its pane's content
 *  box, the `@container` a tile lays itself out in) at the window widths the
 *  page's checks use. From the CSS: 1440: the app's pinned sidebar (244px),
 *  the frame's margin (8) and page padding (2 × 24), then the wide review
 *  grid's rail (264), its folded side pane (3rem) and borders, less the
 *  pane's padding (2 × 20). 900: the sidebar folded (56), the review stacked
 *  in one column. 390: edge to edge, less the pane's padding (2 × 16). The
 *  overview tab caps its column at 64rem (`max-w-5xl`), wider than each of
 *  these, so the overview's diagram has the same width. */
const WIDTHS = {
  desktop: { label: "1440 window", px: 786, pad: 20 },
  medium: { label: "900 window", px: 746, pad: 20 },
  phone: { label: "390 phone", px: 358, pad: 16 },
} as const;
type Width = keyof typeof WIDTHS;

const THEMES = { both: ["light", "dark"], light: ["light"], dark: ["dark"] } as const;
type Theme = keyof typeof THEMES;

// ---------- the diagrams ----------

const CHROME = review1303();
const ctx = { review: CHROME } as unknown as TileContext;

/** A diagram from a composition or a sample as a tile, its citations left
 *  out: its hunks are another review's, which the page can't resolve here. */
const tileOf = (entry: {
  id: string;
  type: string;
  title: string;
  placement?: string;
  data: unknown;
}): ViewTile => ({
  id: entry.id,
  type: entry.type,
  title: entry.title,
  state: "ready",
  owner: entry.placement ?? "",
  cites: [],
  data:
    typeof entry.data === "object" && entry.data !== null && !Array.isArray(entry.data)
      ? { ...entry.data, source: [] }
      : entry.data,
});

/** Where the page draws a tile of `review`: the overview's main diagram on
 *  the overview whatever its owner (and not in its section), any other tile
 *  in the listed section or "Where this fits" it belongs to. The rest, the
 *  overview's other tiles and those of a section the page doesn't list, it
 *  never draws. */
export function placementOf(review: ReviewV2, tile: ViewTile): string | undefined {
  if (tile.id === review.overview.diagram) return OVERVIEW_OWNER;
  if (tile.owner === OVERVIEW_OWNER) return undefined;
  const listed = listedSections(review).some((section) => section.id === tile.owner);
  return listed || (tile.owner === CONTEXT_ID && review.context) ? tile.owner : undefined;
}

/** A baum.review/2 document's native tiles, parsed and drawn as the page
 *  does (caption and citations in their data, cited against the document),
 *  or the problem that it isn't one. */
export function documentTiles(entry: DocumentEntry): Shown[] | string {
  const review = parseReviewV2(entry.document);
  if (!review) return `${entry.origin}: not a baum.review/2 document`;
  const chrome = withReview(chromeOf(review), review);
  return review.tiles
    .filter((tile) => NATIVE[tile.type])
    .map((tile) => ({
      key: `${entry.key}:${tile.id}`,
      source: entry.source,
      origin: `${entry.origin} · ${tile.id}`,
      tile,
      placement: placementOf(review, tile),
      review: chrome,
    }));
}

/** The fixture's native tiles as the page parses them (caption and
 *  citations moved into their data). */
const FIXTURE_REVIEW = reviewOf(CHROME);
const FIXTURE: Shown[] = FIXTURE_REVIEW.tiles
  .filter((tile) => NATIVE[tile.type])
  .map((tile) => ({
    key: `fixture:${tile.id}`,
    source: "fixture",
    origin: `fixture-1303.ts · ${tile.id}`,
    tile,
    placement: placementOf(FIXTURE_REVIEW, tile),
    review: CHROME,
  }));

const BUILT_IN: Shown[] = [
  ...FIXTURE,
  ...SAMPLES.map((sample) => ({
    key: `sample:${sample.id}`,
    source: "sample" as const,
    origin: `samples.ts · ${sample.id} · ${sample.expect} · citations not drawn`,
    tile: tileOf(sample),
    placement: sample.placement,
    review: CHROME,
  })),
];

/** Whether the page draws it, and why not in one line. */
export function verdict(type: string, data: unknown): { valid: boolean; reason?: string } {
  const native = NATIVE[type];
  if (!native)
    return { valid: false, reason: `"${type}" isn't a native type (${TYPES.join(", ")})` };
  if (native.valid(data)) return { valid: true };
  const shape: Record<string, string> = {
    flow: "lanes: at least one, each with steps of { title, more? } strings",
    columns: "columns: at least one, each a label and items of { title }",
    sequence:
      "actors: two or more strings; steps of { from, to, text } with from and to an actor's index",
    table: "columns: strings; rows: arrays of a string or { text, was? }",
  };
  return { valid: false, reason: `Fails the page's check of a ${type}: ${shape[type]}` };
}

// ---------- the server's diagrams ----------

type Load = { state: "loading" } | { state: "failed"; error: string } | { state: "ready" };

const POLL_MS = 2000;

/** The server's diagrams, asked for again every two seconds: a new replay
 *  run or review version draws without a reload. */
function useServer(): {
  entries: Entry[];
  documents: DocumentEntry[];
  problems: string[];
  load: Load;
} {
  const [found, setFound] = useState<{
    entries: Entry[];
    documents: DocumentEntry[];
    problems: string[];
  }>({ entries: [], documents: [], problems: [] });
  const [load, setLoad] = useState<Load>({ state: "loading" });
  useEffect(() => {
    let stop = false;
    let version = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const response = await fetch(`/api/diagrams?version=${encodeURIComponent(version)}`, {
          cache: "no-store",
        });
        if (!response.ok) throw new Error(`the server answered ${response.status}`);
        const body = (await response.json()) as Response;
        if (!stop && !("unchanged" in body)) {
          version = body.version;
          setFound({
            entries: body.entries,
            documents: body.documents ?? [],
            problems: body.problems,
          });
        }
        if (!stop) setLoad({ state: "ready" });
      } catch (error) {
        if (!stop)
          setLoad({
            state: "failed",
            error: error instanceof Error ? error.message : String(error),
          });
      }
      if (!stop) timer = setTimeout(poll, POLL_MS);
    };
    void poll();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  }, []);
  return { ...found, load };
}

// ---------- the controls, kept in the address ----------

type View = { type: string; source: string; width: Width; theme: Theme };

function readView(): View {
  const params = new URLSearchParams(window.location.search);
  const width = params.get("width") ?? "";
  const theme = params.get("theme") ?? "";
  const source = params.get("source") ?? "";
  return {
    type: params.get("type") ?? "",
    source: SOURCES.some((each) => each.id === source) ? source : "",
    width: width in WIDTHS ? (width as Width) : "desktop",
    theme: theme in THEMES ? (theme as Theme) : "both",
  };
}

function useView(): [View, (next: Partial<View>) => void] {
  const [view, setView] = useState(readView);
  const update = useCallback((next: Partial<View>) => {
    setView((current) => {
      const merged = { ...current, ...next };
      const params = new URLSearchParams();
      if (merged.type) params.set("type", merged.type);
      if (merged.source) params.set("source", merged.source);
      if (merged.width !== "desktop") params.set("width", merged.width);
      if (merged.theme !== "both") params.set("theme", merged.theme);
      const query = params.toString();
      window.history.replaceState(null, "", query ? `?${query}` : window.location.pathname);
      return merged;
    });
  }, []);
  return [view, update];
}

// ---------- the page ----------

const SELECT = "rounded-sm border border-default bg-surface-card px-2 py-1 text-xs text-primary";

function Select({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: [string, string][];
  onChange: (value: string) => void;
}) {
  return (
    <label className="flex items-center gap-1.5 text-xs text-secondary">
      {label}
      <select className={SELECT} value={value} onChange={(event) => onChange(event.target.value)}>
        {options.map(([id, title]) => (
          <option key={id} value={id}>
            {title}
          </option>
        ))}
      </select>
    </label>
  );
}

function DiagramEntry({ shown, view }: { shown: Shown; view: View }) {
  const { tile } = shown;
  // A tile the composition says failed or is pending draws as such.
  const verdictNow =
    tile.state === "ready"
      ? verdict(tile.type, tile.data)
      : { valid: false, reason: `The tile is ${tile.state}; the page doesn't read its data` };
  const width = WIDTHS[view.width];
  // The overview's diagram and "Where this fits" draw it in a card; a
  // section's body, and here a tile the page leaves out, bare under a small
  // label.
  const variant =
    shown.placement === OVERVIEW_OWNER || shown.placement === CONTEXT_ID ? "card" : "bare";
  return (
    <article
      className="min-w-0 space-y-2"
      data-testid="gallery-entry"
      data-type={tile.type}
      data-valid={verdictNow.valid ? "true" : "false"}
    >
      <header className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs">
        <strong className="text-base font-semibold text-primary">{tile.title}</strong>
        <code className="text-secondary">{tile.type}</code>
        <span className="text-tertiary">{shown.origin}</span>
        {shown.placement ? (
          <span className="text-tertiary">on {shown.placement}</span>
        ) : (
          <span className="text-tertiary italic" data-testid="gallery-off-page">
            not on the page
          </span>
        )}
        <span
          className={
            verdictNow.valid
              ? "rounded-full bg-status-positive-bg px-2 py-0.5 text-status-positive"
              : "rounded-full bg-status-critical-bg px-2 py-0.5 text-status-critical"
          }
        >
          {verdictNow.valid ? "valid" : "invalid"}
        </span>
      </header>
      {verdictNow.reason ? (
        <p className="text-xs text-status-critical" data-testid="gallery-reason">
          {verdictNow.reason}
        </p>
      ) : null}
      {/* Each panel is a theme scope, as a theme's thumbnail in Settings is: the
          palette's recipes apply to it with its own base, so light and dark show
          side by side. themes/custom.css sets the root only (the tile kit's
          tokens read it that way) and is empty by default, so its build-time
          overrides show on the page's own theme, not on both panels. */}
      <div className="flex flex-wrap items-start gap-3">
        {THEMES[view.theme].map((theme) => (
          <div
            key={theme}
            data-gallery-theme={theme}
            data-theme-scope=""
            data-theme={theme}
            className="flex-none rounded-md border border-default bg-surface-card text-primary"
            style={{ width: width.px + 2 * width.pad, padding: width.pad }}
          >
            <ViewTileCard
              tile={tile}
              review={shown.review}
              ctx={ctx}
              idPrefix={`${theme}-${shown.key}-`}
              variant={variant}
            />
          </div>
        ))}
      </div>
    </article>
  );
}

export function Gallery() {
  const server = useServer();
  const [view, setView] = useView();

  // The page's own chrome follows the one theme shown, light for both.
  useEffect(() => {
    if (view.theme === "dark") document.documentElement.dataset.theme = "dark";
    else delete document.documentElement.dataset.theme;
  }, [view.theme]);

  const fromDocuments = useMemo(() => server.documents.map(documentTiles), [server.documents]);
  const all = useMemo<Shown[]>(
    () => [
      ...BUILT_IN,
      ...fromDocuments.flatMap((each) => (typeof each === "string" ? [] : each)),
      ...server.entries.map((entry) => ({
        key: entry.key,
        source: entry.source,
        origin: `${entry.origin} · citations not drawn`,
        tile: tileOf(entry),
        placement: entry.placement,
        review: CHROME,
      })),
    ],
    [fromDocuments, server.entries],
  );
  const problems = [
    ...server.problems,
    ...fromDocuments.filter((each): each is string => typeof each === "string"),
  ];
  const types = useMemo(
    () => [...new Set([...TYPES, ...all.map((shown) => shown.tile.type)])],
    [all],
  );
  // A type nothing has (yet) shows them all, as the select says.
  const type = types.includes(view.type) ? view.type : "";
  const shown = all.filter(
    (each) => (!type || each.tile.type === type) && (!view.source || each.source === view.source),
  );

  return (
    <div className="space-y-6 px-4 pb-16 text-primary" data-testid="gallery">
      <header className="sticky top-0 z-10 -mx-4 space-y-2 border-b border-default bg-surface-page px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h1 className="text-lg font-semibold">Native diagram gallery</h1>
          <p className="text-xs text-tertiary" data-testid="gallery-count">
            {shown.length} of {all.length} diagrams · edit native-tiles.tsx, flow.css or samples.ts
            and this page updates
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Select
            label="Type"
            value={type}
            options={[["", "All types"], ...types.map((type): [string, string] => [type, type])]}
            onChange={(type) => setView({ type })}
          />
          <Select
            label="Source"
            value={view.source}
            options={[
              ["", "All sources"],
              ...SOURCES.map((source): [string, string] => [
                source.id,
                `${source.title} (${all.filter((each) => each.source === source.id).length})`,
              ]),
            ]}
            onChange={(source) => setView({ source })}
          />
          <Select
            label="Width"
            value={view.width}
            options={Object.entries(WIDTHS).map(([id, width]): [string, string] => [
              id,
              `${width.label} · ${width.px}px`,
            ])}
            onChange={(width) => setView({ width: width as Width })}
          />
          <Select
            label="Theme"
            value={view.theme}
            options={[
              ["both", "Light and dark"],
              ["light", "Light"],
              ["dark", "Dark"],
            ]}
            onChange={(theme) => setView({ theme: theme as Theme })}
          />
        </div>
      </header>

      {server.load.state === "failed" ? (
        <p role="alert" className="text-xs text-status-critical">
          The server's diagrams couldn't be read ({server.load.error}); the built-in ones are shown.
        </p>
      ) : null}
      {problems.map((problem) => (
        <p key={problem} className="text-xs text-status-caution">
          {problem}
        </p>
      ))}

      {SOURCES.filter((source) => !view.source || view.source === source.id).map((source) => {
        const entries = shown.filter((each) => each.source === source.id);
        const fromServer = source.id !== "fixture" && source.id !== "sample";
        if (entries.length === 0 && !fromServer) return null;
        return (
          <section key={source.id} className="space-y-6" aria-label={source.title}>
            <div className="border-b border-subtle pb-1">
              <h2 className="text-base font-semibold">{source.title}</h2>
              <p className="text-xs text-tertiary">{source.hint}</p>
            </div>
            {entries.length === 0 ? (
              <p className="text-xs text-secondary">
                {server.load.state === "ready"
                  ? all.some((each) => each.source === source.id)
                    ? "Nothing here matches the filters."
                    : "None found."
                  : server.load.state === "loading"
                    ? "Loading…"
                    : "The server isn't answering."}
              </p>
            ) : (
              entries.map((each) => <DiagramEntry key={each.key} shown={each} view={view} />)
            )}
          </section>
        );
      })}
    </div>
  );
}
