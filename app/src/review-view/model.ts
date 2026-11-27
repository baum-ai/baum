import type {
  Anchor,
  HunkRole,
  IndexedHunk,
  IndexedHunkLine,
  ReviewDocument,
} from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import {
  firstSentence,
  leadProse,
  type ReviewV2,
  type V2Risk,
  type V2Section,
  type ViewTile,
} from "./document";
import { isRecord, isString } from "../tiles/types";
import { fromV1 } from "./from-v1";

import type { Tone } from "./document";
export type { ReviewV2, V2Block, V2Point, V2Section, V2Risk, ViewTile } from "./document";

export type { Tone } from "./document";

type Captioned = { caption?: string; source: Anchor[] };

export type FlowStep = {
  title: string;
  note?: string;
  /** Behind a click on the step: its detail and the files it cites. */
  more?: string;
  cites?: Anchor[];
  tone?: Tone;
  join?: "arrow" | "both" | "none";
};
export type FlowData = Captioned & {
  /** `from`: the step of the first lane (from 1) a later lane branches off. */
  lanes: { label?: string; from?: number; steps: FlowStep[] }[];
};
export type ColumnItem = { title: string; note?: string };
/** Lists side by side in `items`, or, with `rows`, states of one thing
 *  compared item by item: a row holds one item per column, null where that
 *  column has none, and the columns have no items of their own. */
export type ColumnsData = Captioned & {
  columns: { label: string; tone?: Tone; items?: ColumnItem[] }[];
  rows?: (ColumnItem | null)[][];
};
export type SequenceData = Captioned & {
  actors: string[];
  /** `reply`: an answer to an earlier step, drawn dashed. `change`: what the
   *  pull request does to it; `was`: a changed step's earlier text. */
  steps: {
    from: number;
    to: number;
    text: string;
    tone?: Tone;
    reply?: boolean;
    change?: "added" | "changed";
    was?: string;
  }[];
};
/** A table cell: its text, or what it is now with what it was before the
 *  change (`was`, drawn struck through). */
export type TableCell = string | { text: string; was?: string };
export type TableData = Captioned & { columns: string[]; rows: TableCell[][] };

/** "Where this fits": the review's context, listed before its sections. */
export const CONTEXT_ID = "context";

/** The Code tab's place in the address: the viewer's host-owned `tile`
 *  selection under an ID no tile has (tile IDs are the model's `dg_…` or a
 *  library type's), with the file open as its item. */
export const CODE_PLACE = "code";

/** The quiet note under an added tile's title: where its data is from while
 *  it is filled, or nothing. */
export function addedNote(tile: Pick<ViewTile, "from" | "filling">): string | null {
  if (tile.from)
    return tile.filling
      ? `From version ${tile.from}; filling for this version`
      : `From version ${tile.from}`;
  return tile.filling ? "Filling for this version" : null;
}

/** Where the review the page's chrome reads carries the baum.review/2
 *  document the tabs read: an enumerable symbol, kept by the copies the page
 *  makes and never seen by JSON. */
export const REVIEW: unique symbol = Symbol.for("baum.review/2");

type ReviewedDocument = ReviewDocument & { [REVIEW]?: ReviewV2 };

export function withReview<D extends ReviewDocument>(document: D, review: ReviewV2): D {
  (document as ReviewedDocument)[REVIEW] = review;
  return document;
}

/** The baum.review/2 document a review was served with, if any. */
export function servedReview(document: ReviewDocument | null | undefined): ReviewV2 | null {
  return (document as ReviewedDocument | null | undefined)?.[REVIEW] ?? null;
}

const converted = new WeakMap<ReviewDocument, ReviewV2>();
/** The reviews converted from a baum.review/1 version, whose pages the
 *  viewer draws: they are never a brief. */
const legacy = new WeakSet<ReviewV2>();

/** The baum.review/2 document the tabs read for a review: the one it was
 *  served with, else the review converted on its own (no tiles, no overview
 *  parts beyond the review). A converted one is the rail's and the file
 *  list's model only: the version's Summary and sections are drawn from the
 *  baum.review/1 document itself (`ViewPane`). */
export function reviewOf(document: ReviewDocument): ReviewV2 {
  const served = servedReview(document);
  if (served) return served;
  let review = converted.get(document);
  if (!review) {
    // A baum.review/1 version's context is part of its Summary, which the
    // viewer draws: no "Where this fits" place of its own.
    review = { ...fromV1(document), context: null };
    converted.set(document, review);
    legacy.add(review);
  }
  return review;
}

export function viewTile(review: ReviewV2, id: string | null | undefined): ViewTile | undefined {
  return id ? review.tiles.find((tile) => tile.id === id) : undefined;
}

/** The tiles a section (or "Where this fits") owns, in the document's
 *  order; the overview's diagram is never one of them. */
export function tilesOn(review: ReviewV2, owner: string): ViewTile[] {
  return review.tiles.filter((tile) => tile.owner === owner && tile.id !== review.overview.diagram);
}

/** The section (or "Where this fits") a tile stands on, if any. */
export function sectionOfTile(review: ReviewV2, tile: string): string | undefined {
  const owner = viewTile(review, tile)?.owner;
  return owner && owner !== OVERVIEW_OWNER && tile !== review.overview.diagram ? owner : undefined;
}

/** The owner of the overview's main diagram. */
export const OVERVIEW_OWNER = "overview";

/** A section's details: being written, failed, or written. A section no
 *  longer in the code is never being written. */
export function detailsOf(section: V2Section): "complete" | "pending" | "failed" {
  if (section.details === "pending")
    return section.derived.state === "stale" ? "failed" : "pending";
  return section.details === "failed" ? "failed" : "complete";
}

// ---------- the brief ----------

/** The most lines a change may add and remove and still read as a brief. */
export const BRIEF_LINES = 60;

/** Whether the review is drawn as a brief: one page that holds the summary,
 *  the change's own diff, what its one section says and where it fits, with
 *  no How it works. A change qualifies when it is small enough to read whole
 *  and nothing in the review asks for a closer look: one section, none left
 *  over from earlier commits, at most BRIEF_LINES changed lines, an assessed
 *  risk of 1 or 2, and nothing placed on "Where this fits". A baum.review/1
 *  version never is: the viewer draws its Summary and section pages, so its
 *  section has no place on the Overview and must stay in the rail. */
export function isBrief(review: ReviewV2): boolean {
  if (legacy.has(review)) return false;
  if (review.sections.length !== 1 || review.sections[0]!.derived.state === "stale") return false;
  const score = review.summary.risk_score;
  if (!score || score.status !== "assessed" || score.score === null || score.score > 2)
    return false;
  if (review.index.hunks.length === 0) return false;
  const lines = review.index.hunks.reduce((sum, hunk) => sum + hunk.additions + hunk.deletions, 0);
  if (lines > BRIEF_LINES) return false;
  return (
    tilesOn(review, CONTEXT_ID).length === 0 &&
    !review.drafts?.some((draft) => draft.section === CONTEXT_ID)
  );
}

// ---------- what to check ----------

/** A risk with the section it stands on. */
export type CheckedRisk = { risk: V2Risk; section: string };

const TIER_ORDER = ["critical", "high", "medium", "low"];

/** What the Overview lists under the verdict: the verdict's points, then the
 *  open risks of the sections still in the code, the most severe first (a
 *  tier's risks in the review's order), the low ones apart, since most risks
 *  are low and the list is for what needs a look. */
export function decisionChecks(review: ReviewV2): {
  points: { text: string; section?: string }[];
  risks: CheckedRisk[];
  low: CheckedRisk[];
} {
  const open = review.sections
    .filter((section) => section.derived.state !== "stale")
    .flatMap((section) =>
      section.risks
        .filter((risk) => risk.state !== "mitigated")
        .map((risk): CheckedRisk => ({ risk, section: section.id })),
    );
  const rank = (each: CheckedRisk) => {
    const at = TIER_ORDER.indexOf(each.risk.tier);
    return at < 0 ? TIER_ORDER.length : at;
  };
  const sorted = open.toSorted((a, b) => rank(a) - rank(b));
  return {
    points: review.summary.verdict?.points ?? [],
    risks: sorted.filter((each) => each.risk.tier !== "low"),
    low: sorted.filter((each) => each.risk.tier === "low"),
  };
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

/** How it works lists "Where this fits" first (when the review has a context,
 *  or tiles or pending cards stand on it), then the review's sections in its
 *  order, the ones no longer in the code last. A section's line is its gist
 *  (else its summary's first sentence). A brief lists none: its Overview
 *  holds what they would say. */
export function listedSections(review: ReviewV2): ListedSection[] {
  if (isBrief(review)) return [];
  const context = review.context;
  const contextLine = context
    ? (context.area ?? context.reached_by ?? context.contracts ?? context.related)?.text
    : undefined;
  const listed: ListedSection[] =
    context ||
    tilesOn(review, CONTEXT_ID).length > 0 ||
    review.drafts?.some((draft) => draft.section === CONTEXT_ID)
      ? [
          {
            id: CONTEXT_ID,
            title: "Where this fits",
            line: contextLine ? firstSentence(contextLine) : undefined,
          },
        ]
      : [];
  const sections = review.sections.map(
    (section): ListedSection => ({
      id: section.id,
      title: section.title,
      line:
        section.gist || (leadProse(section).trim() ? firstSentence(leadProse(section)) : undefined),
      ...(section.derived.state === "stale" ? { obsolete: true } : {}),
    }),
  );
  return [
    ...listed,
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

/** Where the reader is on the page, read off the viewer's selection. */
export type ViewPlace =
  | { tab: "overview" }
  | { tab: "sections"; section: string; item?: string; view?: SectionView }
  | { tab: "code"; file: string | null };

/** The page's place for a selection: the Summary is the Overview, a section
 *  or risk is How it works there (the Changes at its first section), the
 *  Code place is the Code tab, and a tile is the section it is placed on. */
export function placeOf(selection: Selection, review: ReviewV2): ViewPlace {
  const list = listedSections(review);
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
      const section = sectionOfTile(review, selection.id);
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

type FileGroup = "Code" | "Tests" | "Docs" | "Config" | "Generated";
const FILE_GROUPS: FileGroup[] = ["Code", "Tests", "Docs", "Config", "Generated"];

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
export function viewFiles(document: ReviewV2): ViewFile[] {
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

/** How the Files changed list groups the files: by what they are, by the
 *  directory they are in, or by the section that explains them. */
export type FileGrouping = "type" | "folder" | "feature";

export const FILE_GROUPINGS: { value: FileGrouping; label: string }[] = [
  { value: "type", label: "Type" },
  { value: "folder", label: "Folder" },
  { value: "feature", label: "Feature" },
];

const NO_SECTION = "Not in a section";

/** One heading of the file list and the files under it. A file explained by
 *  several sections is under each of them. */
export type FileGroupRow = {
  key: string;
  title: string;
  /** Indentation, for a folder inside a folder. */
  depth: number;
  files: ViewFile[];
  /** The title is a section's, which may carry inline markdown. */
  markdown?: boolean;
  /** The generated files, which stay folded until opened. */
  generated?: boolean;
};

type Folder = { name: string; files: ViewFile[]; folders: Map<string, Folder> };

const baseName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** The directories of the changed files as groups, depth first. A directory
 *  holding no file of its own and one directory is joined to that one
 *  (`oss/internal/jobs`), so the list never has a heading with a single
 *  heading under it. Files of the repository root come first. */
function folderGroups(files: ViewFile[]): FileGroupRow[] {
  const root: Folder = { name: "", files: [], folders: new Map() };
  for (const file of files) {
    let at = root;
    const parts = file.path.split("/").slice(0, -1);
    for (const part of parts) {
      let next = at.folders.get(part);
      if (!next) {
        next = { name: part, files: [], folders: new Map() };
        at.folders.set(part, next);
      }
      at = next;
    }
    at.files.push(file);
  }
  const rows: FileGroupRow[] = [];
  const walk = (start: Folder, depth: number, prefix: string) => {
    let folder = start;
    let name = start.name;
    while (folder.files.length === 0 && folder.folders.size === 1) {
      folder = [...folder.folders.values()][0]!;
      name = `${name}/${folder.name}`;
    }
    const path = prefix ? `${prefix}/${name}` : name;
    const sorted = folder.files.toSorted((a, b) =>
      baseName(a.path).localeCompare(baseName(b.path)),
    );
    if (folder.files.length > 0 || folder.folders.size > 1)
      rows.push({ key: `folder:${path}`, title: name, depth, files: sorted });
    const inner = folder.files.length > 0 || folder.folders.size > 1 ? depth + 1 : depth;
    for (const child of [...folder.folders.values()].toSorted((a, b) =>
      a.name.localeCompare(b.name),
    ))
      walk(child, inner, path);
  };
  if (root.files.length > 0)
    rows.push({
      key: "folder:",
      title: "Repository root",
      depth: 0,
      files: root.files.toSorted((a, b) => a.path.localeCompare(b.path)),
    });
  for (const child of [...root.folders.values()].toSorted((a, b) => a.name.localeCompare(b.name)))
    walk(child, 0, "");
  return rows;
}

/** The changed files in the groups of a grouping: the type groups in their
 *  fixed order, the directories, or the sections in the list's order with the
 *  files of no section last. Groups without files are left out. */
export function groupFiles(
  files: ViewFile[],
  grouping: FileGrouping,
  sections: ListedSection[],
): FileGroupRow[] {
  if (grouping === "folder") return folderGroups(files);
  if (grouping === "feature") {
    const rows = sections.flatMap((section): FileGroupRow[] => {
      const members = files.filter((file) => file.sections.includes(section.id));
      return members.length > 0
        ? [
            {
              key: `feature:${section.id}`,
              title: section.title,
              depth: 0,
              files: members,
              markdown: true,
            },
          ]
        : [];
    });
    const loose = files.filter((file) => file.sections.length === 0);
    return loose.length > 0
      ? [...rows, { key: "feature:", title: NO_SECTION, depth: 0, files: loose }]
      : rows;
  }
  return FILE_GROUPS.flatMap((group): FileGroupRow[] => {
    const members = files.filter((file) => file.group === group);
    return members.length > 0
      ? [
          {
            key: `type:${group}`,
            title: group,
            depth: 0,
            files: members,
            ...(group === "Generated" ? { generated: true } : {}),
          },
        ]
      : [];
  });
}

// ---------- the overview ----------

/** Why the risk score is what it is: the review's `risk_why`, else the
 *  score's strongest reason. */
export function riskSummary(review: ReviewV2): string | null {
  return (
    review.summary.risk_why.trim() || review.summary.risk_score?.reasons[0]?.text?.trim() || null
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

/** What the person asked in the linked sessions: the overview's intent, else
 *  each linked session's first prompt the index carries. */
export function intentRows(review: ReviewV2): IntentRow[] {
  const sessions = review.index.sessions;
  const at = (session: string, index: number) =>
    sessions.find((item) => item.id === session)?.prompts?.find((p) => p.index === index)?.at;
  if (review.overview.intent.length > 0)
    return review.overview.intent.map((row) => {
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

function isSectionView(value: unknown): value is SectionView {
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
function sectionFilesOf(document: ReviewV2, section: V2Section, roles: HunkRole[]): ViewFile[] {
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
export function sectionViews(document: ReviewV2, section: V2Section): SectionViews {
  const code = {
    files: sectionFilesOf(document, section, VIEW_ROLES.code),
    generated: sectionFilesOf(document, section, GENERATED_ROLES),
  };
  const tests = sectionFilesOf(document, section, VIEW_ROLES.tests);
  const docs = sectionFilesOf(document, section, VIEW_ROLES.docs);
  const session = sessionQuotes(document, section, tilesOn(document, section.id));
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

/** The view beside the Overview when the section is wide enough for two
 *  columns: the one asked for while it has something, else the first that
 *  has; null when the section has nothing but its Overview. */
export function sideSectionView(
  asked: SectionView | undefined,
  counts: SectionViews["counts"],
): Exclude<SectionView, "overview"> | null {
  if (asked && asked !== "overview" && counts[asked] > 0) return asked;
  for (const { value } of SECTION_VIEWS)
    if (value !== "overview" && counts[value] > 0) return value;
  return null;
}

/** The view of a section that draws a file: Code (its generated files
 *  included), Tests or Docs; null for a file the section doesn't touch. */
export function viewOfPath(
  views: SectionViews,
  path: string,
): Exclude<SectionView, "overview" | "session"> | null {
  const has = (files: ViewFile[]) => files.some((file) => file.path === path);
  if (has(views.code.files) || has(views.code.generated)) return "code";
  if (has(views.tests.files)) return "tests";
  if (has(views.docs.files)) return "docs";
  return null;
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

/** What a line opens: a test, a helper (a function or a Go block, by name),
 *  or a group of tests (`describe`), whose own lines are setup. */
type Declaration = {
  kind: "test" | "helper" | "group";
  name: string | null;
  indent: number;
  closed?: boolean;
  /** The names of the groups the hunk shows it inside, outermost first. */
  scope?: string;
};

const GO_FUNC = /^func\s+(?:\(\s*(?:\w+\s+)?\*?(\w+)(?:\[[^\]]*\])?\s*\)\s*)?([A-Za-z_]\w*)\s*[[(]/;
const GO_BLOCK = /^(type|var|const|import)\b\s*(\(|[A-Za-z_]\w*)?/;
const JS_TEST =
  /^(\s*)(?:it|test)(\.(?:only|skip|concurrent|todo|fails|each|for)\b[^(]*)?\(\s*(?:(["'`])((?:\\.|(?!\3).)*)\3)?/;
const JS_GROUP =
  /^\s*(?:describe|suite|test\.describe)(?:\.\w+)*\s*\(\s*(?:(["'`])((?:\\.|(?!\1).)*)\1)?/;
const JS_FUNC =
  /^(?:export\s+)?(?:(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)|(?:const|let)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>)/;

/** A test or another declaration a line opens, if any. */
function declaration(language: Language, text: string): Declaration | null {
  const indent = text.length - text.trimStart().length;
  if (language === "go") {
    const func = GO_FUNC.exec(text);
    if (func) {
      const [, receiver, name] = func;
      if (!receiver && /^(Test|Benchmark|Fuzz|Example)/.test(name!))
        return { kind: "test", name: name!, indent: 0 };
      return { kind: "helper", name: receiver ? `${receiver}.${name}` : name!, indent: 0 };
    }
    const block = GO_BLOCK.exec(text);
    if (!block) return null;
    const [, word, what] = block;
    const name =
      word === "import" ? "imports" : what && what !== "(" ? `${word} ${what}` : `${word} block`;
    // `type T int` or `var x = 1` ends on its line.
    return { kind: "helper", name, indent: 0, closed: !/[{(]\s*$/.test(text) };
  }
  const group = JS_GROUP.exec(text);
  if (group) {
    const closed = /\)\s*;?\s*$/.test(text) && !/[{(]\s*$/.test(text);
    return { kind: "group", name: group[2] ?? null, indent, closed };
  }
  const func = indent === 0 ? JS_FUNC.exec(text) : null;
  if (func)
    return { kind: "helper", name: func[1] ?? func[2]!, indent, closed: !/[{(]\s*$/.test(text) };
  const call = JS_TEST.exec(text);
  if (!call) return null;
  const [, , modifier = "", quote, name = ""] = call;
  // A name built at run time (`each`, a template) can't be read.
  if (/^\.(each|for)\b/.test(modifier)) return { kind: "test", name: null, indent };
  // `test.skip(condition, reason)` and the like name no test.
  if (!quote) return modifier ? null : { kind: "test", name: null, indent };
  const readable = !(quote === "`" && name.includes("${"));
  // A test written on one line ends on it.
  const closed = /\)\s*;?\s*$/.test(text) && !/[{(]\s*$/.test(text);
  return { kind: "test", name: readable ? name : null, indent, closed };
}

export type TestChange = "added" | "changed" | "renamed" | "removed";

/** One test, helper or run of other lines that a file's hunks change. */
export type TestOutlineItem = {
  /** A test; a helper (a function, type, variable or the imports), by name;
   *  or the lines that are neither (a `describe`'s setup, top-level code). */
  kind: "test" | "helper" | "other";
  name: string;
  /** A renamed test's or helper's old name. */
  was?: string;
  change: TestChange;
  additions: number;
  deletions: number;
  /** The line of the diff to open it at: its declaration, else its first
   *  changed line, on the side that has it. */
  at?: { side: "new" | "old"; line: number };
};

export type TestOutlineFile = {
  path: string;
  change: IndexedHunk["change"];
  additions: number;
  deletions: number;
  /** The file's tests in the diff's order, then its helpers and other lines;
   *  null when they can't be read with confidence: another language, a
   *  test's name built at run time, a change inside a block whose start is
   *  above the hunk, or code that isn't in the review. */
  items: TestOutlineItem[] | null;
};

type Draft = TestOutlineItem & { order: number; declared?: IndexedHunkLine["kind"] };

export const OTHER_LINES = "Other lines";

const COMMENT = /^\s*(\/\/|\/\*|\*)/;

const atOf = (line: IndexedHunkLine): TestOutlineItem["at"] =>
  line.kind === "remove"
    ? { side: "old", line: line.old_line }
    : { side: "new", line: line.new_line };

/** The words of a name, for telling a rename from a different test. */
function words(name: string): Set<string> {
  return new Set(
    name
      .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
      .replace(/([A-Z])([A-Z][a-z])/g, "$1 $2")
      .toLowerCase()
      .split(/[^a-z0-9]+/)
      .filter(Boolean),
  );
}

/** How alike two names are: the words they share over all their words. */
function likeness(a: string, b: string): number {
  const left = words(a);
  const right = words(b);
  let shared = 0;
  for (const word of left) if (right.has(word)) shared += 1;
  return shared / (left.size + right.size - shared || 1);
}

/** A file's items, read from its hunks' lines, or null (see `items`). */
function outlineItems(path: string, hunks: IndexedHunk[]): TestOutlineItem[] | null {
  const language = languageOf(path);
  if (language === null) return [];
  if (language === "other") return null;
  // Every item in the diff's order, and the latest one under each key: a
  // test's key has the groups it is in, so like-named tests in two suites
  // stay apart.
  const drafts: Draft[] = [];
  const latest = new Map<string, Draft>();
  const keyOf = (kind: TestOutlineItem["kind"], name: string, scope = "") =>
    `${kind}:${scope}\u0000${name}`;
  const fresh = (kind: TestOutlineItem["kind"], name: string, scope?: string): Draft => {
    const draft: Draft = {
      kind,
      name,
      change: "changed",
      additions: 0,
      deletions: 0,
      order: drafts.length,
    };
    drafts.push(draft);
    latest.set(keyOf(kind, name, scope), draft);
    return draft;
  };
  const itemOf = (kind: TestOutlineItem["kind"], name: string, scope?: string): Draft =>
    latest.get(keyOf(kind, name, scope)) ?? fresh(kind, name, scope);
  const ownerOf = (open: Declaration): Draft =>
    open.kind === "group" || open.name === null
      ? itemOf("other", OTHER_LINES)
      : itemOf(open.kind === "test" ? "test" : "helper", open.name, open.scope);
  const count = (draft: Draft, line: IndexedHunkLine) => {
    if (line.kind === "add") draft.additions += 1;
    else if (line.kind === "remove") draft.deletions += 1;
    else return;
    draft.at ??= atOf(line);
  };
  // A test removed with another added straight after it, as Git lines up a
  // test rewritten under a new name: it is renamed, and a later hunk whose
  // header names the old test holds the new one's lines.
  const replaced = new Map<string, Declaration>();
  for (const hunk of hunks) {
    if (!hunk.diff) return null;
    // Where the hunk starts: inside the declaration its header names, when
    // the header names a test (or, in Go, any declaration), else unknown.
    const context = hunk.diff.header.replace(/^@@[^@]*@@\s?/, "");
    let named = context ? declaration(language, context) : null;
    if (named?.name && replaced.has(named.name)) named = replaced.get(named.name)!;
    let removed: string | null = null;
    let current: Declaration | "top" | "unknown" =
      named && (named.kind === "test" || language === "go") && named.indent === 0
        ? named
        : "unknown";
    // The groups (`describe`) open at this point of the hunk, innermost
    // last: a test that closes gives the lines back to the group around it.
    const groups: Declaration[] = [];
    const around = (closing: Declaration): Declaration | "top" => {
      const group = groups.at(-1);
      if (group) return group;
      // An indented test whose group starts above the hunk: the lines after
      // it are still that group's setup.
      return closing.indent > 0 ? { kind: "group", name: null, indent: closing.indent - 1 } : "top";
    };
    // Changed comments at the margin belong to the declaration under them.
    let pending: IndexedHunkLine[] = [];
    const settle = (draft: Draft) => {
      for (const line of pending) count(draft, line);
      pending = [];
    };
    for (const line of hunk.diff.lines) {
      const text = line.text;
      const changed = line.kind !== "context";
      if (!text.trim()) {
        if (changed && typeof current === "object") count(ownerOf(current), line);
        continue;
      }
      // A Go file's package clause says nothing about its tests.
      if (language === "go" && /^package\s/.test(text)) continue;
      const opened = declaration(language, text);
      if (opened) {
        if (opened.kind === "test" && opened.name === null) return null;
        if (opened.kind !== "helper" && groups.length > 0)
          opened.scope = groups.map((group) => group.name ?? "").join("\u0000");
        if (opened.kind === "test" && line.kind === "add" && removed) replaced.set(removed, opened);
        removed = opened.kind === "test" && line.kind === "remove" ? opened.name : null;
        if (opened.kind !== "group" || changed) {
          let draft = ownerOf(opened);
          // Each test or helper is declared once a side: another declaration
          // under the same key, but for a line changed in place, is another
          // test (a like-named one in a group the hunk doesn't show).
          if (
            opened.kind !== "group" &&
            draft.declared &&
            (draft.declared === line.kind || draft.declared === "context" || !changed)
          )
            draft = fresh(draft.kind, draft.name, opened.scope);
          settle(draft);
          if (opened.kind !== "group") {
            draft.declared = draft.declared && draft.declared !== line.kind ? "context" : line.kind;
            draft.at = atOf(line);
          }
          count(draft, line);
        }
        if (opened.kind === "group" && !opened.closed) groups.push(opened);
        current = opened.closed ? around(opened) : opened;
        continue;
      }
      const indent = text.length - text.trimStart().length;
      const closes = /^\s*[})]/.test(text);
      if (!COMMENT.test(text)) removed = null;
      if (typeof current === "object") {
        // Go's bodies are indented: a line at the margin that closes nothing
        // (a comment above the next function) is outside the one before.
        if (language === "go" && indent === 0 && !closes) {
          current = "top";
          if (changed) pending.push(line);
          continue;
        }
        if (changed) count(ownerOf(current), line);
        if (closes && indent <= current.indent) {
          const closing: Declaration = current;
          while ((groups.at(-1)?.indent ?? -1) >= indent) groups.pop();
          current = closing.kind === "group" ? (groups.at(-1) ?? "top") : around(closing);
        }
        continue;
      }
      // Outside any declaration the hunk shows: a line at the margin is top
      // level; an indented change may be inside a test opened above.
      if (indent === 0) {
        current = "top";
        if (!changed) continue;
        if (COMMENT.test(text)) pending.push(line);
        else {
          const other = itemOf("other", OTHER_LINES);
          settle(other);
          count(other, line);
        }
      } else if (changed && (current === "unknown" || language === "go")) return null;
    }
    if (pending.length > 0) settle(itemOf("other", OTHER_LINES));
  }
  const items = drafts.filter((draft) => draft.additions + draft.deletions > 0);
  for (const item of items)
    item.change =
      item.declared === "add" ? "added" : item.declared === "remove" ? "removed" : "changed";
  // A test or helper removed is renamed when Git lined another up in its
  // place, or one was added under a like name. One of the same name is a
  // like-named test in another group, not a rename.
  const renamed = new Set<Draft>();
  for (const gone of items) {
    if (gone.change !== "removed" || gone.kind === "other") continue;
    const successor = replaced.get(gone.name);
    let best =
      items.find(
        (added) =>
          added.change === "added" &&
          added.kind === "test" &&
          added.name === successor?.name &&
          added.name !== gone.name &&
          !renamed.has(added),
      ) ?? null;
    let score = 0.6;
    for (const added of best ? [] : items) {
      if (
        added.change !== "added" ||
        added.kind !== gone.kind ||
        added.name === gone.name ||
        renamed.has(added)
      )
        continue;
      const like = likeness(gone.name, added.name);
      if (like >= score) [best, score] = [added, like];
    }
    if (!best) continue;
    renamed.add(gone);
    renamed.add(best);
    Object.assign(best, {
      change: "renamed",
      was: gone.name,
      additions: best.additions + gone.additions,
      deletions: best.deletions + gone.deletions,
      order: Math.min(best.order, gone.order),
    });
  }
  const rank = { test: 0, helper: 1, other: 2 };
  return items
    .filter((item) => !(renamed.has(item) && item.change === "removed"))
    .toSorted((a, b) => rank[a.kind] - rank[b.kind] || a.order - b.order)
    .map(({ order: _order, declared: _declared, ...item }) => item);
}

/** The section's test files as an outline of their diff: each file with the
 *  tests, helpers and other lines it changes, read from the hunks' lines
 *  (Go's `func TestX(` and its benchmarks, fuzz tests and examples,
 *  JavaScript's and TypeScript's `it("…"` and `test("…"`). */
export function testOutline(files: { path: string; hunks: IndexedHunk[] }[]): TestOutlineFile[] {
  return files.map((file) => {
    const changes = new Set(file.hunks.map((hunk) => hunk.change));
    return {
      path: file.path,
      change: changes.size === 1 ? file.hunks[0]!.change : "modified",
      additions: file.hunks.reduce((total, hunk) => total + hunk.additions, 0),
      deletions: file.hunks.reduce((total, hunk) => total + hunk.deletions, 0),
      items: outlineItems(file.path, file.hunks),
    };
  });
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

/** What a tile cites: its own citations, and its data's `source` (the native
 *  tiles and exhibits) or `anchors` (prose). */
function tileAnchors(tile: ViewTile): Anchor[] {
  const data = isRecord(tile.data) ? [tile.data.source, tile.data.anchors] : [];
  return [tile.cites, ...data].flatMap((list) =>
    Array.isArray(list) ? list.filter(isAnchor) : [],
  );
}

/** The prompts and session events a section's summary, steps, risks and
 *  tiles cite, each once, in the order they happened. A citation the index
 *  can't resolve to its words is left out. */
export function sessionQuotes(
  document: ReviewV2,
  section: V2Section,
  tiles: ViewTile[] = [],
): SessionQuote[] {
  const anchors = [
    ...section.body.flatMap((block) =>
      block.type === "points" ? block.items.flatMap((point) => point.cites) : [],
    ),
    ...section.risks.flatMap((risk) => risk.cites),
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
