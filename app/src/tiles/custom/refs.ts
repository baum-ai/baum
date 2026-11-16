import type { Anchor, IndexedHunk } from "@baum/viewer/review-document";
import { isRecord } from "../types";
import type { TileDocument } from "../tile-document";

/** The citations in a custom tile's data and what the page tells its frame
 *  about each (see "Custom tiles" and "Citation keys" in
 *  oss/ARCHITECTURE.md). The page resolves them only from the document it
 *  has loaded: it never fetches a URL, reads a file or calls the code host
 *  for a citation. A frame may later ask to jump only to a key resolved
 *  here, so the frame names places by key and never by address. */

/** As many as the daemon allows a tile; past them, the rest are unresolved. */
export const MAX_CITATIONS = 256;
/** A detail frame's excerpts: how many hunks, how many lines each, and how
 *  much text in all; past them a hunk is sent without its lines. */
export const MAX_EXCERPTS = 64;
export const MAX_EXCERPT_LINES = 200;
export const MAX_EXCERPT_CHARS = 384 * 1024;
const CONTEXT_LINES = 3;
const MAX_LABEL = 300;

/** The fields of a citation's key, in order; "?" marks an optional one. */
const KEY_FIELDS: Record<string, readonly string[]> = {
  hunk: ["id", "lines?"],
  file: ["path"],
  commit: ["sha"],
  pr: ["number"],
  pr_body: ["section?"],
  pr_comment: ["id"],
  review_thread: ["id"],
  prompt: ["session", "index"],
  session_event: ["session", "event"],
  check: ["id", "test?"],
  bot_report: ["id"],
  issue: ["system", "key"],
  spec: ["page", "assertion?"],
  external_url: ["url"],
  attachment: ["id?", "url?"],
  risk: ["id"],
  evidence: ["id"],
  change: ["id"],
  tile: ["id"],
};

/** A citation's key: `JSON.stringify([type, ...fields])`, as the kit and Go
 *  compute it, or null for something that is not one. */
export function keyOf(c: unknown): string | null {
  if (!isRecord(c) || typeof c.type !== "string") return null;
  const fields = Object.hasOwn(KEY_FIELDS, c.type) ? KEY_FIELDS[c.type] : undefined;
  if (!fields) return null;
  const key: unknown[] = [c.type];
  for (const field of fields) {
    const optional = field.endsWith("?");
    let value: unknown = c[optional ? field.slice(0, -1) : field];
    if (field === "lines?") {
      value =
        Array.isArray(value) && value.length === 2 && value.every(Number.isInteger)
          ? [value[0], value[1]]
          : null;
    } else if (
      typeof value !== "string" &&
      !(typeof value === "number" && Number.isFinite(value))
    ) {
      if (!optional || (value !== undefined && value !== null)) return null;
      value = null;
    }
    key.push(value);
  }
  if (c.type === "attachment" && key[1] === null && key[2] === null) return null;
  return JSON.stringify(key);
}

// The closed shapes of $defs/citation in oss/schema/tiles-1.json: a value
// is a citation only when it has exactly one of them.
type Field = (value: unknown) => boolean;
const str: Field = (value) => typeof value === "string";
const pattern =
  (re: RegExp): Field =>
  (value) =>
    typeof value === "string" && re.test(value);
const positive: Field = (value) => Number.isInteger(value) && (value as number) >= 1;
const orNull =
  (field: Field): Field =>
  (value) =>
    value === null || field(value);
const uri: Field = (value) => {
  if (typeof value !== "string") return false;
  try {
    return Boolean(new URL(value).protocol);
  } catch {
    return false;
  }
};
const SHAPES: Record<string, { required: Record<string, Field>; optional: Record<string, Field> }> =
  {
    hunk: {
      required: { id: pattern(/^h_[0-9a-f]{12}(-[0-9]+)?$/) },
      optional: {
        lines: (value) => Array.isArray(value) && value.length === 2 && value.every(positive),
      },
    },
    file: { required: { path: str }, optional: {} },
    commit: { required: { sha: pattern(/^[0-9a-f]{7,64}$/) }, optional: {} },
    pr: { required: { number: positive }, optional: {} },
    pr_body: { required: {}, optional: { section: orNull(str) } },
    pr_comment: {
      required: { id: (value) => typeof value === "string" || Number.isInteger(value) },
      optional: {},
    },
    review_thread: { required: { id: str }, optional: {} },
    prompt: { required: { session: str, index: positive }, optional: { input: str } },
    session_event: { required: { session: str, event: str }, optional: {} },
    check: { required: { id: str }, optional: { test: str } },
    bot_report: { required: { id: str }, optional: {} },
    issue: { required: { system: str, key: str }, optional: {} },
    spec: {
      required: { page: pattern(/^SPEC-\d+$/) },
      optional: { assertion: orNull(pattern(/^SPEC-\d+-A\d+$/)) },
    },
    external_url: { required: { url: uri }, optional: { label: str } },
    attachment: {
      required: {},
      optional: {
        id: (value) => pattern(/^att_[a-f0-9]+$/)(value) && (value as string).length <= 128,
        url: uri,
      },
    },
    risk: {
      required: {
        id: (value) => pattern(/^rsk_[a-z0-9_]+$/)(value) && (value as string).length <= 128,
      },
      optional: {},
    },
    evidence: {
      required: {
        id: (value) => pattern(/^ev_[a-z0-9_]+$/)(value) && (value as string).length <= 128,
      },
      optional: {},
    },
    change: { required: { id: pattern(/^sec_[a-z0-9_]+$/) }, optional: {} },
    tile: { required: { id: pattern(/^[a-z][a-z0-9_]{0,127}$/) }, optional: {} },
  };

/** Whether a value has the shape of a citation. */
export function isCitation(value: unknown): value is Record<string, unknown> & { type: string } {
  if (!isRecord(value) || typeof value.type !== "string" || !Object.hasOwn(SHAPES, value.type))
    return false;
  const shape = SHAPES[value.type]!;
  for (const [name, field] of Object.entries(shape.required))
    if (!Object.hasOwn(value, name) || !field(value[name])) return false;
  for (const [name, member] of Object.entries(value)) {
    if (name === "type" || Object.hasOwn(shape.required, name)) continue;
    const field = Object.hasOwn(shape.optional, name) ? shape.optional[name] : undefined;
    if (!field || !field(member)) return false;
  }
  if (value.type === "attachment" && !("id" in value) && !("url" in value)) return false;
  return true;
}

/** The citations in data, in the order the daemon walks it (object members
 *  by name, array items in order; a citation's own members are not
 *  searched), at most `MAX_CITATIONS + 1`. */
export function findCitations(data: unknown): Record<string, unknown>[] {
  const found: Record<string, unknown>[] = [];
  const walk = (value: unknown, depth: number) => {
    if (found.length > MAX_CITATIONS || depth > 64) return;
    if (Array.isArray(value)) {
      for (const item of value) walk(item, depth + 1);
    } else if (isRecord(value)) {
      if (isCitation(value)) {
        found.push(value);
        return;
      }
      for (const name of Object.keys(value).toSorted()) walk(value[name], depth + 1);
    }
  };
  walk(data, 0);
  return found;
}

/** Where the page goes when a frame asks to jump to a citation. */
export type CitationTarget =
  | { kind: "hunk"; id: string; path: string; line?: number }
  | { kind: "file"; path: string }
  | { kind: "tile"; id: string }
  | { kind: "change"; id: string }
  | { kind: "item"; change: string; id: string };

export type ResolvedRefs = {
  /** The payloads sent to the frame, by key. */
  refs: Record<string, Record<string, unknown>>;
  /** The places a jump may go, by key: only navigable citations. */
  targets: ReadonlyMap<string, CitationTarget>;
};

const baseName = (path: string) => path.slice(path.lastIndexOf("/") + 1);
const clip = (text: string, most: number) =>
  text.length > most ? `${text.slice(0, most - 1)}…` : text;

type Excerpt = {
  header: string;
  lines: { kind: "add" | "remove" | "context"; old?: number; new?: number; text: string }[];
  omitted_before: number;
  omitted_after: number;
};

/** The cited lines of a hunk with a few around them, or the whole hunk, from
 *  the excerpt the document stores; null when it stores none. */
function excerptOf(hunk: IndexedHunk, lines: [number, number] | null): Excerpt | null {
  const diff = hunk.diff;
  if (!diff || !Array.isArray(diff.lines)) return null;
  const all = diff.lines.map((line) => ({
    kind: line.kind,
    ...("old_line" in line ? { old: line.old_line } : {}),
    ...("new_line" in line ? { new: line.new_line } : {}),
    text: line.text,
  }));
  let from = 0;
  let to = all.length;
  if (lines) {
    const inside = all.flatMap((line, at) =>
      line.new !== undefined && line.new >= lines[0] && line.new <= lines[1] ? [at] : [],
    );
    if (inside.length > 0) {
      from = Math.max(0, inside[0]! - CONTEXT_LINES);
      to = Math.min(all.length, inside.at(-1)! + 1 + CONTEXT_LINES);
    }
  }
  to = Math.min(to, from + MAX_EXCERPT_LINES);
  return {
    header: diff.header,
    lines: all.slice(from, to),
    omitted_before: from,
    omitted_after: all.length - to,
  };
}

const excerptSize = (excerpt: Excerpt) =>
  excerpt.lines.reduce((total, line) => total + line.text.length, excerpt.header.length);

/** Resolves every citation in a tile's data against the document on screen.
 *  A glance gets labels only; a detail gets code excerpts too. */
export function resolveRefs(
  data: unknown,
  doc: TileDocument,
  face: "glance" | "detail",
  labels: ReadonlyMap<string, string>,
): ResolvedRefs {
  const refs: Record<string, Record<string, unknown>> = {};
  const targets = new Map<string, CitationTarget>();
  const hunks = new Map(doc.index.hunks.map((hunk) => [hunk.id, hunk]));
  const files = new Map<string, IndexedHunk[]>();
  for (const hunk of doc.index.hunks)
    for (const path of new Set([hunk.path, hunk.old_path].filter(Boolean) as string[]))
      files.set(path, [...(files.get(path) ?? []), hunk]);
  let excerpts = 0;
  let excerptChars = 0;
  const citations = findCitations(data);
  for (const [position, citation] of citations.entries()) {
    const key = keyOf(citation);
    if (key === null || Object.hasOwn(refs, key)) continue;
    const kind = citation.type as string;
    if (position >= MAX_CITATIONS) {
      refs[key] = { status: "unavailable", label: "Citation", why: "too_many" };
      continue;
    }
    const missing = (label: string) => {
      refs[key] = { status: "unavailable", label: clip(label, MAX_LABEL), why: "not_in_review" };
    };
    const ok = (label: string, fields: Record<string, unknown>, target?: CitationTarget) => {
      refs[key] = {
        status: "ok",
        label: clip(label, MAX_LABEL),
        navigable: Boolean(target),
        ...fields,
      };
      if (target) targets.set(key, target);
    };
    switch (kind) {
      case "hunk": {
        const hunk = hunks.get(citation.id as string);
        if (!hunk) {
          missing("Code");
          break;
        }
        const lines = Array.isArray(citation.lines)
          ? ([citation.lines[0], citation.lines[1]] as [number, number])
          : null;
        const range = lines ?? hunk.new_range;
        const label =
          range && range[0] > 0
            ? `${baseName(hunk.path)}:${range[0] === range[1] ? range[0] : `${range[0]}–${range[1]}`}`
            : baseName(hunk.path);
        const fields: Record<string, unknown> = {
          path: hunk.path,
          ...(hunk.old_path ? { old_path: hunk.old_path } : {}),
          change: hunk.change,
          role: hunk.role,
          range: hunk.new_range,
          additions: hunk.additions,
          deletions: hunk.deletions,
        };
        if (face === "detail") {
          const excerpt = excerptOf(hunk, lines);
          if (excerpt && excerpts < MAX_EXCERPTS) {
            const size = excerptSize(excerpt);
            if (excerptChars + size <= MAX_EXCERPT_CHARS) {
              excerpts++;
              excerptChars += size;
              fields.excerpt = excerpt;
            }
          }
        }
        ok(label, fields, {
          kind: "hunk",
          id: hunk.id,
          path: hunk.path,
          ...(range && range[0] > 0 ? { line: range[0] } : {}),
        });
        break;
      }
      case "file": {
        const path = citation.path as string;
        const own = files.get(path);
        if (!own) {
          missing(baseName(path) || "File");
          break;
        }
        ok(
          path,
          {
            path,
            change: own[0]!.change,
            additions: own.reduce((sum, hunk) => sum + hunk.additions, 0),
            deletions: own.reduce((sum, hunk) => sum + hunk.deletions, 0),
            hunks: own.length,
          },
          { kind: "file", path },
        );
        break;
      }
      case "risk": {
        const risk = doc.risks.find((each) => each.id === citation.id);
        if (!risk) {
          missing("Risk");
          break;
        }
        ok(
          labels.get(risk.id) ?? risk.category,
          {
            tier: risk.tier,
            category: risk.category,
            state: risk.state ?? "active",
            text: clip(risk.text, 500),
            change: risk.change,
          },
          { kind: "item", change: risk.change, id: risk.id },
        );
        break;
      }
      case "evidence": {
        const evidence = doc.evidence.find((each) => each.id === citation.id);
        if (!evidence) {
          missing("Evidence");
          break;
        }
        ok(
          labels.get(evidence.id) ?? evidence.kind,
          {
            evidence_kind: evidence.kind,
            evidence_status: evidence.status,
            claim: clip(evidence.claim, 500),
            change: evidence.change,
          },
          { kind: "item", change: evidence.change, id: evidence.id },
        );
        break;
      }
      case "change": {
        const change = doc.changes.find((each) => each.id === citation.id);
        if (!change) {
          missing("Change");
          break;
        }
        ok(
          change.title,
          { title: change.title, change_kind: change.kind },
          {
            kind: "change",
            id: change.id,
          },
        );
        break;
      }
      case "tile": {
        const tile = doc.tiles.find((each) => each.id === citation.id);
        if (!tile) {
          missing("Tile");
          break;
        }
        ok(tile.title, { title: tile.title }, { kind: "tile", id: tile.id });
        break;
      }
      case "check": {
        const check = doc.index.checks.find((each) => each.id === citation.id);
        if (!check) {
          missing("Check");
          break;
        }
        ok(check.name, {
          name: check.name,
          conclusion: check.conclusion,
          required: check.required,
        });
        break;
      }
      case "review_thread": {
        const thread = doc.index.threads.find((each) => each.id === citation.id);
        if (!thread) {
          missing("Review thread");
          break;
        }
        ok(
          thread.path
            ? `${baseName(thread.path)}${thread.line ? `:${thread.line}` : ""}`
            : "Review thread",
          {
            ...(thread.path ? { path: thread.path } : {}),
            ...(thread.line ? { line: thread.line } : {}),
            resolved: thread.resolved,
            outdated: thread.outdated === true,
          },
        );
        break;
      }
      case "commit": {
        const sha = citation.sha as string;
        ok(sha.slice(0, 7), { sha });
        break;
      }
      case "prompt":
      case "session_event": {
        const session = doc.index.sessions.find((each) => each.id === citation.session);
        if (!session) missing("Session");
        else ok(session.title || "Session", {});
        break;
      }
      case "pr":
        ok(`#${citation.number}`, {});
        break;
      case "pr_body":
        ok("Pull request description", {});
        break;
      case "pr_comment":
        ok("Comment", {});
        break;
      case "bot_report":
        ok("Bot report", {});
        break;
      case "issue":
        ok(`${citation.key}`, {});
        break;
      case "spec":
        ok(String(citation.assertion ?? citation.page), {});
        break;
      case "external_url":
        ok(
          typeof citation.label === "string" && citation.label
            ? citation.label
            : String(citation.url),
          {},
        );
        break;
      case "attachment":
        ok("Attachment", {});
        break;
    }
  }
  return { refs, targets };
}

/** The code a comment on a custom tile is placed on: the hunks its data
 *  cites, in data order. */
export function citedHunks(data: unknown): Anchor[] {
  const seen = new Set<string>();
  const anchors: Anchor[] = [];
  for (const citation of findCitations(data)) {
    if (citation.type !== "hunk") continue;
    const key = keyOf(citation);
    if (key === null || seen.has(key)) continue;
    seen.add(key);
    anchors.push(
      Array.isArray(citation.lines)
        ? {
            type: "hunk",
            id: citation.id as string,
            lines: [citation.lines[0] as number, citation.lines[1] as number],
          }
        : { type: "hunk", id: citation.id as string },
    );
  }
  return anchors;
}
