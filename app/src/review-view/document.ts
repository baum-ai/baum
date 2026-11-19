import type {
  Anchor,
  HunkRole,
  IndexedHunk,
  RecommendationDecision,
  ReviewDocument,
  RiskScore,
  RiskScoreLevel,
  RiskTier,
  Section,
  SectionDetailsState,
} from "@baum/viewer/review-document";
import { parseDrafts, type DraftCard } from "../tiles/drafts";
import { isRecord, isString, type Tile } from "../tiles/types";

/** The review page's document, `baum.review/2` (`oss/reviewv2/document.go`;
 *  the JSON field names are the Go types' tags): the overview, the sections
 *  with their steps, risks and tiles, and where the change fits. The header,
 *  index and stats are baum.review/1's own objects. The tabs read only this. */
export const REVIEW_V2 = "baum.review/2";

export type ReviewV2 = {
  schema: typeof REVIEW_V2;
  review: ReviewDocument["review"];
  summary: {
    title: string;
    text: string;
    change_kind: string;
    /** The page shows it only when it has points. */
    verdict: V2Verdict | null;
    risk_why: string;
    risk_score?: RiskScore;
    review_minutes: number | null;
  };
  overview: {
    screenshots: string[];
    diagram: string | null;
    intent: V2Intent[];
  };
  context: V2Context | null;
  sections: V2Section[];
  tiles: ViewTile[];
  stats: ReviewDocument["stats"];
  index: ReviewDocument["index"];
  generation?: unknown;
  /** Page-only, from the tile route's `drafts`: the tiles being made in the
   *  session panel, as pending cards on the sections they will stand on. */
  drafts?: DraftCard[];
};

type V2Verdict = {
  decision: RecommendationDecision;
  text: string;
  points: { text: string; section?: string }[];
};

type V2Intent = { session: string; prompt: number; asked: string; got: string };

export type V2ContextPart = { text: string; files: string[] };
export type V2Context = {
  area: V2ContextPart | null;
  reached_by: V2ContextPart | null;
  contracts: V2ContextPart | null;
  related: V2ContextPart | null;
};

/** One line of a points block: `text` (inline markdown) on the page, `more`
 *  and the cited files behind its click. `id` is set for a point made from an
 *  older document's step, which keeps the step's ID (comments rest on it). */
export type V2Point = { id?: string; text: string; more?: string; cites: Anchor[] };

/** One block of a section's body. A tile block places one of the section's
 *  tiles (by ID); the others are text. `title` is the optional label above. */
export type V2Block =
  | { type: "prose" | "note"; title?: string; text: string }
  | { type: "points"; title?: string; items: V2Point[] }
  | { type: "tile"; tile: string };

/** The in-page ID of a section's point: its step's ID where it came from one,
 *  else its place in the body. */
export const pointId = (section: string, block: number, item: number, point: V2Point): string =>
  point.id ?? `${section}-b${block}-p${item}`;

/** The text of a section's lead prose block, if it has one. */
export const leadProse = (section: Pick<V2Section, "body">): string =>
  section.body.flatMap((block) => (block.type === "prose" ? [block.text] : []))[0] ?? "";

export type V2Risk = {
  id: string;
  tier: RiskTier;
  category: string;
  text: string;
  cites: Anchor[];
  state?: "active" | "mitigated";
  introduced_version?: number;
  mitigated_version?: number;
};

export type V2Section = {
  id: string;
  title: string;
  kind: "logical" | "mechanical";
  hunks: string[];
  /** The list's line: what the reader should come away with. */
  gist: string;
  scope?: string;
  level: RiskScoreLevel | null;
  level_reasons: { text: string; cites: Anchor[] }[];
  /** What the page draws between the gist and the risks, in order: text
   *  blocks the section agent wrote and the section's tiles. Empty while the
   *  details are pending or when they failed, and no tile yet. */
  body: V2Block[];
  risks: V2Risk[];
  /** Absent once written. */
  details?: SectionDetailsState;
  derived: Section["derived"];
};

type ViewTileType = "flow" | "columns" | "sequence" | "table" | "code" | "prose" | "custom";
export type ViewTileState = "ready" | "pending" | "failed" | "unavailable";

/** A tile of the document, placed by its owner: `overview` (the main
 *  diagram), `context` ("Where this fits") or a section's ID. */
export type ViewTile = Tile & {
  type: ViewTileType | string;
  state: ViewTileState;
  owner: string;
  /** What the tile is to show, the brief its tile agent filled. */
  shows?: string;
  caption?: string;
  cites: Anchor[];
  /** The person added it (a chat-made tile, or a library type added from the
   *  session panel): only such a tile can be removed. */
  added?: true;
  /** Its data was written for this earlier version. */
  from?: number;
  /** Being filled for this version. */
  filling?: true;
};

// ---------- the parser ----------

/** The first sentence of a text, for a list's quiet line. */
export function firstSentence(text: string): string {
  const plain = text.replace(/\s+/g, " ").trim();
  const end = plain.search(/[.!?](\s|$)/);
  return end >= 0 ? plain.slice(0, end + 1) : plain;
}

const text = (value: unknown): string => (isString(value) ? value : "");
const strings = (value: unknown): string[] => (Array.isArray(value) ? value.filter(isString) : []);
const records = (value: unknown): Record<string, unknown>[] =>
  Array.isArray(value) ? value.filter(isRecord) : [];
const anchors = (value: unknown): Anchor[] =>
  records(value).filter((anchor) => isString(anchor.type)) as unknown as Anchor[];
const count = (value: unknown): number | undefined =>
  typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;

const TILE_STATES = new Set<string>(["ready", "pending", "failed", "unavailable"]);
const TIERS = new Set<string>(["critical", "high", "medium", "low", "mechanical"]);
const DECISIONS = new Set<string>(["approve", "look_first", "request_changes"]);
const TIER_ORDER: RiskTier[] = ["critical", "high", "medium", "low", "mechanical"];

/** The native tile types: their caption and citations are drawn from their
 *  data (`caption`, `source`), which the tile's own fill when absent. */
export const NATIVE_TYPES = new Set(["flow", "columns", "sequence", "table"]);

/** A light structural check of a served baum.review/2 document: what the
 *  page reads without looking twice. A malformed item (a section, a step, a
 *  risk, a tile, an intent row) is dropped, never the page; a document that is
 *  not one at all is null. `drafts` are the tile route's pending cards. */
export function parseReviewV2(value: unknown, drafts?: unknown): ReviewV2 | null {
  if (!isRecord(value) || value.schema !== REVIEW_V2 || !isRecord(value.review)) return null;
  if (typeof value.review.version !== "number") return null;
  const index = parseIndex(value.index);
  const summary = isRecord(value.summary) ? value.summary : {};
  const overview = isRecord(value.overview) ? value.overview : {};
  const tiles = records(value.tiles).flatMap((tile) => {
    const parsed = parseTile(tile, index.hunks);
    return parsed ? [parsed] : [];
  });
  const sections = records(value.sections).flatMap((section) => {
    const parsed = parseSection(section, index.hunks, tiles);
    return parsed ? [parsed] : [];
  });
  const pending = parseDrafts(drafts);
  return {
    schema: REVIEW_V2,
    review: value.review as ReviewDocument["review"],
    summary: {
      title: text(summary.title),
      text: text(summary.text),
      change_kind: text(summary.change_kind),
      verdict: parseVerdict(summary.verdict),
      risk_why: text(summary.risk_why).trim(),
      ...(isRecord(summary.risk_score) && Array.isArray(summary.risk_score.reasons)
        ? { risk_score: summary.risk_score as RiskScore }
        : {}),
      review_minutes: count(summary.review_minutes) ?? null,
    },
    overview: {
      screenshots: strings(overview.screenshots),
      diagram: isString(overview.diagram) && overview.diagram ? overview.diagram : null,
      intent: records(overview.intent).flatMap((row) =>
        isString(row.asked) &&
        row.asked.trim() &&
        isString(row.session) &&
        typeof row.prompt === "number"
          ? [{ session: row.session, prompt: row.prompt, asked: row.asked, got: text(row.got) }]
          : [],
      ),
    },
    context: parseContext(value.context),
    sections,
    tiles,
    stats: parseStats(value.stats, sections.length, index.hunks),
    index,
    ...(value.generation !== undefined && value.generation !== null
      ? { generation: value.generation }
      : {}),
    ...(pending.length > 0 ? { drafts: pending } : {}),
  };
}

function parseVerdict(value: unknown): V2Verdict | null {
  if (!isRecord(value)) return null;
  const points = records(value.points).flatMap((point) =>
    isString(point.text) && point.text.trim()
      ? [
          {
            text: point.text,
            ...(isString(point.section) && point.section ? { section: point.section } : {}),
          },
        ]
      : [],
  );
  const decision = isString(value.decision) && DECISIONS.has(value.decision) ? value.decision : "";
  if (!decision && points.length === 0) return null;
  return {
    decision: (decision || "look_first") as RecommendationDecision,
    text: text(value.text),
    points,
  };
}

function parseContextPart(value: unknown): V2ContextPart | null {
  if (!isRecord(value) || !isString(value.text) || !value.text.trim()) return null;
  return { text: value.text, files: strings(value.files).filter((file) => file.trim()) };
}

function parseContext(value: unknown): V2Context | null {
  if (!isRecord(value)) return null;
  const context = {
    area: parseContextPart(value.area),
    reached_by: parseContextPart(value.reached_by),
    contracts: parseContextPart(value.contracts),
    related: parseContextPart(value.related),
  };
  return Object.values(context).some(Boolean) ? context : null;
}

function parseIndex(value: unknown): ReviewDocument["index"] {
  const index = isRecord(value) ? value : {};
  const list = <T>(key: string) => (Array.isArray(index[key]) ? (index[key] as T[]) : []);
  return {
    ...(index as Partial<ReviewDocument["index"]>),
    hunks: list<IndexedHunk>("hunks").filter(
      (hunk) => isRecord(hunk) && isString(hunk.id) && isString(hunk.path),
    ),
    checks: list("checks"),
    reports: list("reports"),
    comments: list("comments"),
    threads: list("threads"),
    sessions: list<ReviewDocument["index"]["sessions"][number]>("sessions").filter(
      (session) => isRecord(session) && isString(session.id),
    ),
    prs: list("prs"),
  };
}

function parseStats(
  value: unknown,
  sections: number,
  hunks: IndexedHunk[],
): ReviewDocument["stats"] {
  if (isRecord(value) && typeof value.files === "number") return value as ReviewDocument["stats"];
  const roleLoc: Partial<Record<HunkRole, number>> = {};
  for (const hunk of hunks)
    roleLoc[hunk.role] = (roleLoc[hunk.role] ?? 0) + hunk.additions + hunk.deletions;
  return {
    sections,
    files: new Set(hunks.map((hunk) => hunk.path)).size,
    hunks: hunks.length,
    additions: hunks.reduce((total, hunk) => total + hunk.additions, 0),
    deletions: hunks.reduce((total, hunk) => total + hunk.deletions, 0),
    risk_loc: { critical: 0, high: 0, medium: 0, low: 0, mechanical: 0, unrated: 0 },
    role_loc: roleLoc,
    checks: { required_failed: 0, optional_failed: 0, pending: 0, skipped: 0 },
    agent_findings: { open: 0, resolved: 0, dismissed: 0 },
  };
}

function parseRisk(value: Record<string, unknown>): V2Risk | null {
  if (!isString(value.id) || !isString(value.text) || !value.text.trim()) return null;
  return {
    id: value.id,
    tier: (isString(value.tier) && TIERS.has(value.tier) ? value.tier : "low") as RiskTier,
    category: text(value.category),
    text: value.text,
    cites: anchors(value.cites),
    ...(value.state === "mitigated" || value.state === "active" ? { state: value.state } : {}),
    ...(count(value.introduced_version)
      ? { introduced_version: count(value.introduced_version) }
      : {}),
    ...(count(value.mitigated_version)
      ? { mitigated_version: count(value.mitigated_version) }
      : {}),
  };
}

function parseSection(
  value: Record<string, unknown>,
  hunks: IndexedHunk[],
  tiles: ViewTile[],
): V2Section | null {
  if (!isString(value.id) || !value.id || !isString(value.title)) return null;
  const own = strings(value.hunks);
  const risks = records(value.risks).flatMap((risk) => {
    const parsed = parseRisk(risk);
    return parsed ? [parsed] : [];
  });
  const level =
    typeof value.level === "number" && value.level >= 1 && value.level <= 5
      ? (Math.round(value.level) as RiskScoreLevel)
      : null;
  const details =
    value.details === "pending" || value.details === "failed" ? value.details : undefined;
  return {
    id: value.id,
    title: value.title,
    kind: value.kind === "mechanical" ? "mechanical" : "logical",
    hunks: own,
    gist: text(value.gist).trim(),
    ...(isString(value.scope) && value.scope.trim() ? { scope: value.scope } : {}),
    level,
    level_reasons: records(value.level_reasons).flatMap((reason) =>
      isString(reason.text) ? [{ text: reason.text, cites: anchors(reason.cites) }] : [],
    ),
    body: sectionBody(
      value,
      tiles.filter((tile) => tile.owner === value.id),
    ),
    risks,
    ...(details ? { details } : {}),
    derived: derivedOf(value.derived, own, risks, hunks),
  };
}

function parsePoint(value: Record<string, unknown>): V2Point | null {
  if (!isString(value.text) || !value.text.trim()) return null;
  return {
    text: value.text,
    ...(isString(value.more) && value.more.trim() ? { more: value.more } : {}),
    cites: anchors(value.cites),
  };
}

/** A section's body: its blocks as served, a malformed one dropped, a tile
 *  block kept only for a tile the section owns (and placed once), then any
 *  owned tile the body does not place (a tile the person added, or one made
 *  after the body) at the end. A section with no usable body reads as one
 *  made from its `summary` (a prose block), its `steps` (a points block) and
 *  its tiles, as the daemon's `BodyOf` does for documents written before. */
export function sectionBody(value: Record<string, unknown>, owned: ViewTile[]): V2Block[] {
  const placed = new Set<string>();
  const body = records(value.body).flatMap((block): V2Block[] => {
    const title = isString(block.title) && block.title.trim() ? { title: block.title.trim() } : {};
    if ((block.type === "prose" || block.type === "note") && isString(block.text))
      return block.text.trim() ? [{ type: block.type, ...title, text: block.text }] : [];
    if (block.type === "points") {
      const items = records(block.items).flatMap((item) => {
        const point = parsePoint(item);
        return point ? [point] : [];
      });
      return items.length > 0 ? [{ type: "points", ...title, items }] : [];
    }
    if (block.type === "tile" && isString(block.tile)) {
      const id = block.tile;
      if (placed.has(id) || !owned.some((tile) => tile.id === id)) return [];
      placed.add(id);
      return [{ type: "tile", tile: id }];
    }
    return [];
  });
  if (body.length === 0) {
    const summary = text(value.summary).trim();
    const items = records(value.steps).flatMap((step, at): V2Point[] => {
      if (!isString(step.text) || !step.text.trim()) return [];
      const title = text(step.title).trim();
      return [
        {
          id: isString(step.id) && step.id ? step.id : `step_${at + 1}`,
          text: title
            ? `**${/[.!?]$/.test(title) ? title : `${title}.`}** ${step.text}`
            : step.text,
          cites: anchors(step.cites),
        },
      ];
    });
    if (summary) body.push({ type: "prose", text: summary });
    if (items.length > 0) body.push({ type: "points", items });
  }
  for (const tile of owned) if (!placed.has(tile.id)) body.push({ type: "tile", tile: tile.id });
  return body;
}

/** The host's facts of a section, as served, over what the page can work out
 *  itself where a draft has none yet. */
function derivedOf(
  value: unknown,
  own: string[],
  risks: V2Risk[],
  hunks: IndexedHunk[],
): Section["derived"] {
  const ours = hunks.filter((hunk) => own.includes(hunk.id));
  const top = TIER_ORDER.find((tier) => risks.some((risk) => risk.tier === tier));
  return {
    risk_tier: top ?? "unrated",
    additions: ours.reduce((total, hunk) => total + hunk.additions, 0),
    deletions: ours.reduce((total, hunk) => total + hunk.deletions, 0),
    role_loc: {},
    reviewers: [],
    state: "new",
    evidence_summary: {},
    open_findings: 0,
    ...(isRecord(value) ? (value as Partial<Section["derived"]>) : {}),
  };
}

function parseTile(value: Record<string, unknown>, hunks: IndexedHunk[]): ViewTile | null {
  if (!isString(value.id) || !value.id || !isString(value.type) || !value.type) return null;
  const cites = anchors(value.cites);
  const caption = isString(value.caption) && value.caption.trim() ? value.caption : undefined;
  return {
    id: value.id,
    type: value.type,
    title: text(value.title),
    owner: text(value.owner),
    ...(isString(value.shows) && value.shows ? { shows: value.shows } : {}),
    ...(caption ? { caption } : {}),
    cites,
    data: tileData(value.type, value.data ?? null, caption, cites, hunks),
    state:
      isString(value.state) && TILE_STATES.has(value.state)
        ? (value.state as ViewTileState)
        : "unavailable",
    ...(Array.isArray(value.covers) ? { covers: strings(value.covers) } : {}),
    ...(value.definition !== undefined && value.definition !== null
      ? { definition: value.definition }
      : {}),
    ...(Array.isArray(value.problems) ? { problems: strings(value.problems) } : {}),
    ...(value.added === true ? { added: true as const } : {}),
    ...(count(value.from) ? { from: count(value.from) } : {}),
    ...(value.filling === true ? { filling: true as const } : {}),
  };
}

/** A tile's data as the page's faces read it. A native tile's caption and
 *  citations stand on the tile in baum.review/2 and in its data in the faces;
 *  a code tile (`{hunk, lines}`, its text filled by the host) becomes the
 *  excerpt the code face draws. */
function tileData(
  type: string,
  data: unknown,
  caption: string | undefined,
  cites: Anchor[],
  hunks: IndexedHunk[],
): unknown {
  if (NATIVE_TYPES.has(type) && isRecord(data))
    return {
      ...data,
      ...(data.caption === undefined && caption ? { caption } : {}),
      ...(Array.isArray(data.source) ? {} : { source: cites }),
    };
  if (type === "code" && isRecord(data) && !isString(data.content) && isString(data.hunk))
    return codeExcerpt(data, caption, cites, hunks) ?? data;
  return data;
}

const LANGUAGES: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  mjs: "javascript",
  py: "python",
  rs: "rust",
  sql: "sql",
  md: "markdown",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  sh: "bash",
  css: "css",
  html: "html",
};

/** A code tile's excerpt: the text the host filled (`text`, or `content` or
 *  `code`), else the hunk's lines in the range read from the index, as the
 *  new side of the change has them. Null when neither has the code. */
function codeExcerpt(
  data: Record<string, unknown>,
  caption: string | undefined,
  cites: Anchor[],
  hunks: IndexedHunk[],
): { language: string; content: string; caption?: string; source: Anchor[] } | null {
  const id = data.hunk as string;
  const hunk = hunks.find((each) => each.id === id);
  const lines =
    Array.isArray(data.lines) &&
    data.lines.length === 2 &&
    data.lines.every((line) => typeof line === "number")
      ? (data.lines as [number, number])
      : undefined;
  const filled = [data.text, data.content, data.code].find(isString);
  const content =
    filled ??
    (hunk?.diff
      ? hunk.diff.lines
          .filter(
            (line) =>
              line.kind !== "remove" &&
              (!lines || (line.new_line >= lines[0] && line.new_line <= lines[1])),
          )
          .map((line) => line.text)
          .join("\n")
      : "");
  if (!content) return null;
  const extension = hunk?.path.slice(hunk.path.lastIndexOf(".") + 1) ?? "";
  const source: Anchor[] = [
    lines ? { type: "hunk", id, lines } : { type: "hunk", id },
    ...cites.filter((anchor) => !(anchor.type === "hunk" && anchor.id === id)),
  ];
  return {
    language: isString(data.language) ? data.language : (LANGUAGES[extension] ?? "text"),
    content,
    ...(caption ? { caption } : {}),
    source,
  };
}
