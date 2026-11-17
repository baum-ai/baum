import type {
  Anchor,
  Block,
  BehaviorStep,
  HunkRole,
  IndexedHunk,
  ReviewDocument,
  RiskTier,
  Section,
} from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import { parseDrafts, type DraftCard } from "../tiles/drafts";
import type { Tile } from "../tiles/types";

/** The review page's view (`baum.view/1`), as the daemon serves it beside the
 *  review on the tile route: what the overview shows beyond the review
 *  itself, the tiles placed on each section, and the tiles. */
export type ReviewView = {
  schema: "baum.view/1";
  overview: {
    risk_summary?: string;
    verdict?: { text: string; points: { text: string; section?: string }[] } | null;
    screenshots: string[];
    diagram?: string | null;
    intent: { asked: string; got: string; session: string; prompt: number }[];
  };
  sections: { id: string; tiles: string[] }[];
  tiles: ViewTile[];
  /** Page-only, from the tile route's `drafts`: the tiles being made in the
   *  session panel, as pending cards on the sections they will stand on. */
  drafts?: DraftCard[];
};

export type ViewTileType = "flow" | "columns" | "sequence" | "table" | "code" | "prose" | "custom";
export type ViewTileState = "ready" | "pending" | "failed" | "unavailable";

export type ViewTile = Tile & {
  type: ViewTileType | string;
  state: ViewTileState;
  /** The person added it (a chat-made tile, or a library type added from the
   *  session panel): only such a tile can be removed. */
  added?: true;
  /** Its data was written for this earlier version. */
  from?: number;
  /** Being filled for this version. */
  filling?: true;
};

/** A native tile's colour of a step, column or message. */
export type Tone = "plain" | "new" | "good" | "bad" | "muted" | "missing";

type Captioned = { caption?: string; source: Anchor[] };

export type FlowStep = {
  title: string;
  note?: string;
  tone?: Tone;
  join?: "arrow" | "both" | "none";
};
export type FlowData = Captioned & { lanes: { label?: string; steps: FlowStep[] }[] };
export type ColumnsData = Captioned & {
  columns: { label: string; tone?: Tone; items: { title: string; note?: string }[] }[];
};
export type SequenceData = Captioned & {
  actors: string[];
  steps: { from: number; to: number; text: string; tone?: Tone }[];
};
export type TableData = Captioned & { columns: string[]; rows: string[][] };

/** "Where this fits": the review's context, listed before its sections. */
export const CONTEXT_ID = "context";

/** The Code tab's place in the address: the viewer's host-owned `tile`
 *  selection under an ID no tile has (tile IDs are the model's `dg_…` or a
 *  library type's), with the file open as its item. */
export const CODE_PLACE = "code";

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const isString = (value: unknown): value is string => typeof value === "string";
const strings = (value: unknown): string[] => (Array.isArray(value) ? value.filter(isString) : []);

const STATES = new Set<string>(["ready", "pending", "failed", "unavailable"]);

/** A light structural check of a served view: what the page reads without
 *  looking twice. A malformed entry is dropped, never the whole view, and a
 *  view that is not one at all is null (the page then builds the empty view
 *  from the review alone). `drafts` are the tile route's pending cards. */
export function parseReviewView(value: unknown, drafts?: unknown): ReviewView | null {
  if (!isRecord(value) || value.schema !== "baum.view/1") return null;
  const overview = isRecord(value.overview) ? value.overview : {};
  const verdict = isRecord(overview.verdict) ? overview.verdict : null;
  const points = Array.isArray(verdict?.points)
    ? verdict.points.flatMap((point) =>
        isRecord(point) && isString(point.text)
          ? [{ text: point.text, ...(isString(point.section) ? { section: point.section } : {}) }]
          : [],
      )
    : [];
  const intent = Array.isArray(overview.intent)
    ? overview.intent.flatMap((row) =>
        isRecord(row) &&
        isString(row.asked) &&
        isString(row.session) &&
        typeof row.prompt === "number"
          ? [
              {
                asked: row.asked,
                got: isString(row.got) ? row.got : "",
                session: row.session,
                prompt: row.prompt,
              },
            ]
          : [],
      )
    : [];
  const sections = Array.isArray(value.sections)
    ? value.sections.flatMap((section) =>
        isRecord(section) && isString(section.id)
          ? [{ id: section.id, tiles: strings(section.tiles) }]
          : [],
      )
    : [];
  const tiles = Array.isArray(value.tiles)
    ? value.tiles.flatMap((tile): ViewTile[] =>
        isRecord(tile) && isString(tile.id) && isString(tile.type)
          ? [
              {
                id: tile.id,
                type: tile.type,
                title: isString(tile.title) ? tile.title : "",
                data: tile.data ?? null,
                state:
                  isString(tile.state) && STATES.has(tile.state)
                    ? (tile.state as ViewTileState)
                    : "unavailable",
                ...(Array.isArray(tile.covers) ? { covers: strings(tile.covers) } : {}),
                ...(tile.definition !== undefined ? { definition: tile.definition } : {}),
                ...(Array.isArray(tile.problems) ? { problems: strings(tile.problems) } : {}),
                ...(tile.added === true ? { added: true as const } : {}),
                ...(typeof tile.from === "number" && tile.from > 0 ? { from: tile.from } : {}),
                ...(tile.filling === true ? { filling: true as const } : {}),
              },
            ]
          : [],
      )
    : [];
  const pending = parseDrafts(drafts);
  return {
    schema: "baum.view/1",
    overview: {
      ...(isString(overview.risk_summary) && overview.risk_summary.trim()
        ? { risk_summary: overview.risk_summary }
        : {}),
      verdict:
        verdict && isString(verdict.text) && verdict.text.trim()
          ? { text: verdict.text, points }
          : null,
      screenshots: strings(overview.screenshots),
      diagram: isString(overview.diagram) ? overview.diagram : null,
      intent,
    },
    sections,
    tiles,
    ...(pending.length > 0 ? { drafts: pending } : {}),
  };
}

/** Whether a view has more than the review alone: a version whose
 *  composition was generated, or one with tiles on it. */
export function isArranged(view: ReviewView | null): boolean {
  if (!view) return false;
  const { overview } = view;
  return (
    view.tiles.length > 0 ||
    Boolean(overview.risk_summary || overview.verdict || overview.diagram) ||
    overview.screenshots.length > 0 ||
    overview.intent.length > 0
  );
}

/** The quiet note under an added tile's title: where its data is from while
 *  it is filled, or nothing. */
export function addedNote(tile: Pick<ViewTile, "from" | "filling">): string | null {
  if (tile.from)
    return tile.filling
      ? `From version ${tile.from}; filling for this version`
      : `From version ${tile.from}`;
  return tile.filling ? "Filling for this version" : null;
}

/** The view of a version without a composition: nothing beyond the review,
 *  every section listed with no tiles, `context` first, as the daemon serves
 *  it. */
export function emptyView(document: Pick<ReviewDocument, "sections">): ReviewView {
  return {
    schema: "baum.view/1",
    overview: { verdict: null, screenshots: [], diagram: null, intent: [] },
    sections: [
      { id: CONTEXT_ID, tiles: [] },
      ...document.sections.map((section) => ({ id: section.id, tiles: [] })),
    ],
    tiles: [],
  };
}

/** Where a review document carries the view served with it: an enumerable
 *  symbol, kept by the copies the page makes and never seen by JSON. */
export const VIEW: unique symbol = Symbol.for("baum.view");

export type ViewedDocument = ReviewDocument & { [VIEW]?: ReviewView | null };

export function withReviewView<D extends ReviewDocument>(document: D, view: ReviewView | null): D {
  (document as ViewedDocument)[VIEW] = view;
  return document;
}

/** The view a document was served with, or the empty view built from the
 *  review alone (an older daemon, or a version without a composition). */
export function reviewViewOf(document: ReviewDocument): ReviewView {
  return (document as ViewedDocument)[VIEW] ?? emptyView(document);
}

export function viewTile(view: ReviewView, id: string | null | undefined): ViewTile | undefined {
  return id ? view.tiles.find((tile) => tile.id === id) : undefined;
}

/** The tiles placed on a section (or on "Where this fits"), in their order;
 *  the overview's diagram is never one of them. */
export function tilesOn(view: ReviewView, section: string): ViewTile[] {
  const ids = view.sections.find((entry) => entry.id === section)?.tiles ?? [];
  return ids.flatMap((id) => {
    const tile = id === view.overview.diagram ? undefined : viewTile(view, id);
    return tile ? [tile] : [];
  });
}

/** The section a tile is placed on, if any. */
export function sectionOfTile(view: ReviewView, tile: string): string | undefined {
  return view.sections.find((entry) => entry.tiles.includes(tile))?.id;
}

// ---------- the sections list ----------

export type ListedSection = {
  id: string;
  title: string;
  /** The quiet line under the title in the list. */
  line?: string;
  /** No longer in the pull request's code. */
  obsolete?: boolean;
};

/** The first sentence of a text, for a list's quiet line. */
export function firstSentence(text: string): string {
  const plain = text.replace(/\s+/g, " ").trim();
  const end = plain.search(/[.!?](\s|$)/);
  return end >= 0 ? plain.slice(0, end + 1) : plain;
}

/** How it works lists "Where this fits" first (when the review has a context,
 *  or tiles or pending cards stand on it), then the review's sections in its
 *  order, the ones no longer in the code last. */
export function listedSections(document: ReviewDocument, view: ReviewView): ListedSection[] {
  const context: ListedSection[] =
    document.summary.context ||
    tilesOn(view, CONTEXT_ID).length > 0 ||
    view.drafts?.some((draft) => draft.section === CONTEXT_ID)
      ? [
          {
            id: CONTEXT_ID,
            title: "Where this fits",
            line: document.summary.context
              ? firstSentence(document.summary.context.area.text)
              : undefined,
          },
        ]
      : [];
  const sections = document.sections.map(
    (section): ListedSection => ({
      id: section.id,
      title: section.title,
      line: section.summary?.text ? firstSentence(section.summary.text) : undefined,
      ...(section.derived.state === "stale" ? { obsolete: true } : {}),
    }),
  );
  return [
    ...context,
    ...sections.filter((section) => !section.obsolete),
    ...sections.filter((section) => section.obsolete),
  ];
}

/** The section How it works opens on: the first real section, never
 *  "Where this fits" while there is another. */
export function defaultSection(list: ListedSection[]): string | null {
  return (list.find((section) => section.id !== CONTEXT_ID) ?? list[0])?.id ?? null;
}

// ---------- places ----------

export type ViewTab = "overview" | "sections" | "code";

/** Where the reader is on the page, read off the viewer's selection. */
export type ViewPlace =
  | { tab: "overview" }
  | { tab: "sections"; section: string; item?: string; view?: SectionView }
  | { tab: "code"; file: string | null };

/** The page's place for a selection: the Summary is the Overview, a section
 *  or risk is How it works there (the Changes at its first section), the
 *  Code place is the Code tab, and a tile is the section it is placed on. */
export function placeOf(
  selection: Selection,
  document: ReviewDocument,
  view: ReviewView,
): ViewPlace {
  const list = listedSections(document, view);
  const known = (id: string) => list.some((section) => section.id === id);
  const fallback = defaultSection(list);
  switch (selection.kind) {
    case "changes":
      return fallback ? { tab: "sections", section: fallback } : { tab: "overview" };
    case "section":
      return known(selection.id)
        ? {
            tab: "sections",
            section: selection.id,
            ...(isSectionView(selection.view) && selection.view !== "overview"
              ? { view: selection.view }
              : {}),
          }
        : fallback
          ? { tab: "sections", section: fallback }
          : { tab: "overview" };
    case "risk":
      return known(selection.section)
        ? { tab: "sections", section: selection.section, item: selection.id }
        : { tab: "overview" };
    case "tile": {
      if (selection.id === CODE_PLACE) return { tab: "code", file: selection.item ?? null };
      const section = sectionOfTile(view, selection.id);
      return section && known(section)
        ? { tab: "sections", section, item: `tile-${selection.id}` }
        : { tab: "overview" };
    }
    default:
      return { tab: "overview" };
  }
}

/** The selection that shows a place. */
export function selectionOf(place: ViewPlace): Selection {
  switch (place.tab) {
    case "overview":
      return { kind: "summary" };
    case "sections":
      return {
        kind: "section",
        id: place.section,
        ...(place.view && place.view !== "overview" ? { view: place.view } : {}),
      };
    case "code":
      return { kind: "tile", id: CODE_PLACE, ...(place.file ? { item: place.file } : {}) };
  }
}

/** What a comment started on the page is about: the section read, the file
 *  open in Code, or the pull request on the Overview. */
export function commentSelection(selection: Selection): Selection {
  if (selection.kind === "tile" && selection.id === CODE_PLACE)
    return selection.item ? { kind: "file", id: selection.item } : { kind: "summary" };
  return selection;
}

// ---------- the Code tab ----------

export type FileGroup = "Code" | "Tests" | "Docs" | "Config" | "Generated";
export const FILE_GROUPS: FileGroup[] = ["Code", "Tests", "Docs", "Config", "Generated"];

const GROUP_OF_ROLE: Record<HunkRole, FileGroup> = {
  code: "Code",
  migration: "Code",
  test: "Tests",
  docs: "Docs",
  config: "Config",
  generated: "Generated",
  vendored: "Generated",
};

export type ViewFile = {
  path: string;
  group: FileGroup;
  hunks: IndexedHunk[];
  /** The sections whose hunks are in the file, in the review's order. */
  sections: string[];
};

/** Every changed file, grouped by what its hunks are (code, tests, docs,
 *  config, generated), each group's files in the order the review indexed
 *  them. */
export function viewFiles(document: ReviewDocument): ViewFile[] {
  const files = new Map<string, ViewFile>();
  for (const hunk of document.index.hunks) {
    const file = files.get(hunk.path) ?? {
      path: hunk.path,
      group: GROUP_OF_ROLE[hunk.role] ?? "Code",
      hunks: [],
      sections: [],
    };
    file.hunks.push(hunk);
    files.set(hunk.path, file);
  }
  for (const section of document.sections) {
    if (section.derived.state === "stale") continue;
    for (const id of section.hunks) {
      const path = document.index.hunks.find((hunk) => hunk.id === id)?.path;
      const file = path ? files.get(path) : undefined;
      if (file && !file.sections.includes(section.id)) file.sections.push(section.id);
    }
  }
  const all = [...files.values()];
  return FILE_GROUPS.flatMap((group) => all.filter((file) => file.group === group));
}

/** The file the Code tab opens on: the one asked for when the review changes
 *  it, else the first that is not generated. */
export function shownFile(files: ViewFile[], asked: string | null): ViewFile | undefined {
  return (
    (asked ? files.find((file) => file.path === asked) : undefined) ??
    files.find((file) => file.group !== "Generated") ??
    files[0]
  );
}

/** A behaviour step or risk pinned to a hunk it cites, with the section that
 *  explains it. */
export type HunkNote = {
  kind: "step" | "risk";
  id: string;
  section: string;
  sectionTitle: string;
  text: string;
  tier?: RiskTier;
};

const citedHunks = (anchors: Anchor[] | undefined) =>
  new Set((anchors ?? []).flatMap((anchor) => (anchor.type === "hunk" ? [anchor.id] : [])));

export function behaviourSteps(section: Section): BehaviorStep[] {
  return section.blocks.flatMap((block) =>
    block.kind === "behavior" ? ((block as Extract<Block, { kind: "behavior" }>).steps ?? []) : [],
  );
}

/** The notes of each hunk: every behaviour step and every open risk of a
 *  current section whose anchors cite it, steps first, in the review's order. */
export function notesByHunk(document: ReviewDocument): Map<string, HunkNote[]> {
  const notes = new Map<string, HunkNote[]>();
  const pin = (hunks: Set<string>, note: HunkNote) => {
    for (const hunk of hunks) notes.set(hunk, [...(notes.get(hunk) ?? []), note]);
  };
  const sections = document.sections.filter((section) => section.derived.state !== "stale");
  for (const section of sections)
    for (const step of behaviourSteps(section))
      pin(citedHunks(step.anchors), {
        kind: "step",
        id: step.id,
        section: section.id,
        sectionTitle: section.title,
        text: step.title?.trim() || firstSentence(step.text),
      });
  for (const section of sections)
    for (const risk of section.risks) {
      if (risk.state === "mitigated") continue;
      pin(citedHunks(risk.anchors), {
        kind: "risk",
        id: risk.id,
        section: section.id,
        sectionTitle: section.title,
        text: firstSentence(risk.text),
        tier: risk.tier,
      });
    }
  return notes;
}

// ---------- the overview ----------

/** Why the risk score is what it is: the view's two sentences, else the
 *  score's strongest reason. */
export function riskSummary(document: ReviewDocument, view: ReviewView): string | null {
  return (
    view.overview.risk_summary?.trim() ||
    document.summary.risk_score?.reasons[0]?.text?.trim() ||
    null
  );
}

export type IntentRow = {
  asked: string;
  got?: string;
  session: string;
  prompt: number;
  /** When the prompt was typed, where the index has it. */
  at?: string;
};

/** What the person asked in the linked sessions: the view's rows, else each
 *  linked session's first prompt the index carries. */
export function intentRows(document: ReviewDocument, view: ReviewView): IntentRow[] {
  const sessions = document.index.sessions;
  const at = (session: string, index: number) =>
    sessions.find((item) => item.id === session)?.prompts?.find((p) => p.index === index)?.at;
  if (view.overview.intent.length > 0)
    return view.overview.intent.map((row) => {
      const when = at(row.session, row.prompt);
      return {
        asked: row.asked,
        ...(row.got ? { got: row.got } : {}),
        session: row.session,
        prompt: row.prompt,
        ...(when ? { at: when } : {}),
      };
    });
  return sessions.flatMap((session) => {
    const first = (session.prompts ?? [])
      .filter((prompt) => prompt.text.trim())
      .toSorted((a, b) => a.index - b.index)[0];
    return first
      ? [
          {
            asked: clip(first.text.replace(/\s+/g, " ").trim(), 240),
            session: session.id,
            prompt: first.index,
            at: first.at,
          },
        ]
      : [];
  });
}

function clip(text: string, length: number) {
  return text.length <= length ? text : `${text.slice(0, length - 1).trimEnd()}…`;
}

// ---------- a section's views ----------

/** The ways of reading a section of How it works: what it does (its
 *  summary, diagram, steps, risks and tiles), its code, its tests, the
 *  session turns it cites and its docs. */
export type SectionView = "overview" | "code" | "tests" | "session" | "docs";

export const SECTION_VIEWS: { value: SectionView; label: string }[] = [
  { value: "overview", label: "Overview" },
  { value: "code", label: "Code" },
  { value: "tests", label: "Tests" },
  { value: "session", label: "Session" },
  { value: "docs", label: "Docs" },
];

export function isSectionView(value: unknown): value is SectionView {
  return SECTION_VIEWS.some((view) => view.value === value);
}

/** The roles each code view draws; generated and vendored hunks are folded
 *  at the end of Code. */
const VIEW_ROLES: Record<"code" | "tests" | "docs", HunkRole[]> = {
  code: ["code", "migration", "config"],
  tests: ["test"],
  docs: ["docs"],
};
const GENERATED_ROLES: HunkRole[] = ["generated", "vendored"];

/** A section's hunks of some roles as files, each with only the section's
 *  hunks, in the Code tab's group order and the review's order within it. */
function sectionFilesOf(document: ReviewDocument, section: Section, roles: HunkRole[]): ViewFile[] {
  const ids = new Set(section.hunks);
  const files = new Map<string, ViewFile>();
  for (const hunk of document.index.hunks) {
    if (!ids.has(hunk.id) || !roles.includes(hunk.role)) continue;
    const file = files.get(hunk.path) ?? {
      path: hunk.path,
      group: GROUP_OF_ROLE[hunk.role] ?? "Code",
      hunks: [],
      sections: [section.id],
    };
    file.hunks.push(hunk);
    files.set(hunk.path, file);
  }
  const all = [...files.values()];
  return FILE_GROUPS.flatMap((group) => all.filter((file) => file.group === group));
}

export type SectionViews = {
  code: { files: ViewFile[]; generated: ViewFile[] };
  tests: { files: ViewFile[] };
  docs: { files: ViewFile[] };
  session: SessionQuote[];
  /** What each view has, as its tab counts it (files, or quotes); 0 leaves
   *  the view disabled. The Overview has no count. */
  counts: Record<Exclude<SectionView, "overview">, number>;
};

/** Everything a section's views draw, from the review and the tiles placed
 *  on the section. */
export function sectionViews(
  document: ReviewDocument,
  view: ReviewView,
  section: Section,
): SectionViews {
  const code = {
    files: sectionFilesOf(document, section, VIEW_ROLES.code),
    generated: sectionFilesOf(document, section, GENERATED_ROLES),
  };
  const tests = sectionFilesOf(document, section, VIEW_ROLES.tests);
  const docs = sectionFilesOf(document, section, VIEW_ROLES.docs);
  const session = sessionQuotes(document, section, tilesOn(view, section.id));
  return {
    code,
    tests: { files: tests },
    docs: { files: docs },
    session,
    counts: {
      code: code.files.length + code.generated.length,
      tests: tests.length,
      session: session.length,
      docs: docs.length,
    },
  };
}

/** The view a section shows: the one asked for while it has something,
 *  else the Overview. */
export function shownSectionView(
  asked: SectionView | undefined,
  counts: SectionViews["counts"],
): SectionView {
  return asked && asked !== "overview" && counts[asked] > 0 ? asked : "overview";
}

/** The notes of a section's own steps and risks, by the hunk they cite. */
export function sectionNotes(
  notes: Map<string, HunkNote[]>,
  section: string,
): Map<string, HunkNote[]> {
  const own = new Map<string, HunkNote[]>();
  for (const [hunk, pinned] of notes) {
    const mine = pinned.filter((note) => note.section === section);
    if (mine.length > 0) own.set(hunk, mine);
  }
  return own;
}

// ---------- the tests a section's hunks add or change ----------

type Language = "go" | "js";

/** The languages whose tests can be named from a diff; a test file in
 *  another programming language leaves the names unread. Data files (golden
 *  output, fixtures) declare no tests. */
function languageOf(path: string): Language | "other" | null {
  if (path.endsWith(".go")) return "go";
  if (/\.[cm]?[jt]sx?$/.test(path)) return "js";
  if (/\.(py|rs|java|kts?|rb|swift|cs|php|c|cc|cpp|h|hpp|scala|exs?|zig|dart|lua|sh)$/.test(path))
    return "other";
  return null;
}

type Declaration = { test: boolean; name: string | null; indent: number; closed?: boolean };

const GO_FUNC = /^func\s+(\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[[(]/;
const GO_BLOCK = /^(type|var|const|import)\b/;
const JS_TEST =
  /^(\s*)(?:it|test)(\.(?:only|skip|concurrent|todo|fails|each|for)\b[^(]*)?\(\s*(?:(["'`])((?:\\.|(?!\3).)*)\3)?/;
const JS_GROUP = /^\s*(?:describe|suite|test\.describe)(?:\.\w+)*\s*\(/;

/** A test or another declaration a line opens, if any. */
function declaration(language: Language, text: string): Declaration | null {
  const indent = text.length - text.trimStart().length;
  if (language === "go") {
    const func = GO_FUNC.exec(text);
    if (func) {
      const name = func[2]!;
      const test = !func[1] && /^(Test|Benchmark|Fuzz|Example)/.test(name);
      return { test, name, indent: 0 };
    }
    return GO_BLOCK.test(text) ? { test: false, name: null, indent: 0 } : null;
  }
  if (JS_GROUP.test(text)) return { test: false, name: null, indent };
  const call = JS_TEST.exec(text);
  if (!call) return null;
  const [, , modifier = "", quote, name = ""] = call;
  // A name built at run time (`each`, a template) can't be read.
  if (/^\.(each|for)\b/.test(modifier)) return { test: true, name: null, indent };
  // `test.skip(condition, reason)` and the like name no test.
  if (!quote) return modifier ? null : { test: true, name: null, indent };
  const readable = !(quote === "`" && name.includes("${"));
  // A test written on one line ends on it.
  const closed = /\)\s*;?\s*$/.test(text) && !/[{(]\s*$/.test(text);
  return { test: true, name: readable ? name : null, indent, closed };
}

/** The tests the hunks add or change, in their order, read from their
 *  lines: Go's `func TestX(` (and benchmarks, fuzz tests and examples),
 *  JavaScript's and TypeScript's `it("…"` and `test("…"`. Null when a hunk
 *  can't be read with confidence: its code is not in the review, it is in
 *  another language, a test's name is built at run time, or a change sits
 *  inside a block whose start is above the hunk. An empty list means the
 *  hunks change no test. */
export function testNames(hunks: IndexedHunk[]): string[] | null {
  const names: string[] = [];
  const add = (name: string) => {
    if (!names.includes(name)) names.push(name);
  };
  for (const hunk of hunks) {
    const language = languageOf(hunk.path);
    if (language === null) continue;
    if (language === "other" || !hunk.diff) return null;
    // Where the hunk starts: inside the declaration its header names, when
    // the header names a test (or, in Go, any declaration), else unknown.
    const context = hunk.diff.header.replace(/^@@[^@]*@@\s?/, "");
    const named = context ? declaration(language, context) : null;
    let current: Declaration | "top" | "unknown" =
      named && (named.test || language === "go") && named.indent === 0 ? named : "unknown";
    for (const line of hunk.diff.lines) {
      const text = line.text;
      const changed = line.kind !== "context";
      if (!text.trim()) continue;
      const opened = declaration(language, text);
      if (opened) {
        if (opened.test) {
          if (opened.name === null) return null;
          if (line.kind === "add") add(opened.name);
        }
        current = opened.closed ? "top" : opened;
        continue;
      }
      const indent = text.length - text.trimStart().length;
      const closes = /^\s*[})]/.test(text);
      if (typeof current === "object") {
        // Go's bodies are indented: a line at the margin that closes nothing
        // (a comment above the next function) is outside the one before.
        if (language === "go" && indent === 0 && !closes) {
          current = "top";
          continue;
        }
        if (changed && current.test && current.name) add(current.name);
        if (closes && indent <= current.indent) current = "top";
        continue;
      }
      // Outside any declaration the hunk shows: a line at the margin is top
      // level; an indented change may be inside a test opened above.
      if (indent === 0) current = "top";
      else if (changed && (current === "unknown" || language === "go")) return null;
    }
  }
  return names;
}

// ---------- the session turns a section cites ----------

export type SessionQuote = {
  key: string;
  kind: "prompt" | "event";
  /** A person typed it (a prompt), or the agent did it (an event). */
  who: "person" | "agent";
  session: string;
  vendor?: string;
  at?: string;
  text: string;
  /** The transcript's turn to open at: the prompt, or the last prompt
   *  before the event. */
  turn?: number;
};

const isAnchor = (value: unknown): value is Anchor => isRecord(value) && isString(value.type);

/** What a tile's data cites: its `source` (the native tiles and exhibits)
 *  or its `anchors` (prose). */
function tileAnchors(tile: ViewTile): Anchor[] {
  if (!isRecord(tile.data)) return [];
  return [tile.data.source, tile.data.anchors].flatMap((list) =>
    Array.isArray(list) ? list.filter(isAnchor) : [],
  );
}

/** The prompts and session events a section's summary, steps, risks and
 *  tiles cite, each once, in the order they happened. A citation the index
 *  can't resolve to its words is left out. */
export function sessionQuotes(
  document: ReviewDocument,
  section: Section,
  tiles: ViewTile[] = [],
): SessionQuote[] {
  const anchors = [
    ...(section.summary?.anchors ?? []),
    ...behaviourSteps(section).flatMap((step) => step.anchors ?? []),
    ...section.risks.flatMap((risk) => risk.anchors ?? []),
    ...tiles.flatMap(tileAnchors),
  ];
  const quotes = new Map<string, SessionQuote>();
  for (const anchor of anchors) {
    if (anchor.type !== "prompt" && anchor.type !== "session_event") continue;
    const session = document.index.sessions.find((each) => each.id === anchor.session);
    const vendor = session ? { vendor: session.vendor } : {};
    if (anchor.type === "prompt") {
      const key = `prompt:${anchor.session}:${anchor.index}`;
      if (quotes.has(key)) continue;
      const prompt = session?.prompts?.find((each) => each.index === anchor.index);
      const text = (prompt?.text || anchor.input || "").trim();
      if (!text) continue;
      quotes.set(key, {
        key,
        kind: "prompt",
        who: "person",
        session: anchor.session,
        ...vendor,
        ...(prompt?.at ? { at: prompt.at } : {}),
        text: clip(text, 600),
        turn: anchor.index,
      });
    } else {
      const key = `event:${anchor.session}:${anchor.event}`;
      if (quotes.has(key)) continue;
      const event = session?.events?.find((each) => each.id === anchor.event);
      if (!event?.summary.trim()) continue;
      const turn = turnAt(session?.prompts ?? [], event.at);
      quotes.set(key, {
        key,
        kind: "event",
        who: "agent",
        session: anchor.session,
        ...vendor,
        at: event.at,
        text: clip(event.summary.trim(), 600),
        ...(turn !== undefined ? { turn } : {}),
      });
    }
  }
  const time = (quote: SessionQuote) => {
    const at = quote.at ? Date.parse(quote.at) : Number.NaN;
    return Number.isNaN(at) ? Number.POSITIVE_INFINITY : at;
  };
  return [...quotes.values()].toSorted((a, b) => time(a) - time(b));
}

/** The prompt an event happened after: the last one typed before it. */
function turnAt(prompts: { index: number; at: string }[], at: string): number | undefined {
  const when = Date.parse(at);
  if (Number.isNaN(when)) return undefined;
  let turn: { index: number; at: number } | undefined;
  for (const prompt of prompts) {
    const typed = Date.parse(prompt.at);
    if (Number.isNaN(typed) || typed > when) continue;
    if (!turn || typed >= turn.at) turn = { index: prompt.index, at: typed };
  }
  return turn?.index;
}
