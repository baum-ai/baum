// TEMPORARY. Delete this file (and its tests and callers' v1 branches) once
// the daemon answers the tile route and the draft route with a
// baum.review/2 `{document}` for every version (reviewv2.FromV1 on the
// daemon). Until then the daemon answers with a baum.review/1 document and a
// separate `view` (`baum.view/1`), which this converts, as the daemon's
// FromV1 will, so the page reads one shape.

import type {
  Anchor,
  BehaviorStep,
  Block,
  ReviewDocument,
  Section,
} from "@baum/viewer/review-document";
import { parseDrafts } from "../tiles/drafts";
import {
  NATIVE_TYPES,
  REVIEW_V2,
  type ReviewV2,
  type V2Context,
  type V2ContextPart,
  type V2Section,
  type ViewTile,
  type ViewTileState,
} from "./document";

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);
const isString = (value: unknown): value is string => typeof value === "string";
const strings = (value: unknown): string[] => (Array.isArray(value) ? value.filter(isString) : []);
const TILE_STATES = new Set<string>(["ready", "pending", "failed", "unavailable"]);

/** The first sentence of a text: a converted section's gist. */
function firstSentence(text: string): string {
  const plain = text.replace(/\s+/g, " ").trim();
  const end = plain.search(/[.!?](\s|$)/);
  return end >= 0 ? plain.slice(0, end + 1) : plain;
}

function behaviourSteps(section: Section): BehaviorStep[] {
  return section.blocks.flatMap((block) =>
    block.kind === "behavior" ? ((block as Extract<Block, { kind: "behavior" }>).steps ?? []) : [],
  );
}

function contextPart(
  part: { text: string; refs: { path: string }[] } | undefined,
): V2ContextPart | null {
  return part?.text.trim()
    ? { text: part.text, files: part.refs.map((reference) => reference.path) }
    : null;
}

function sectionOf(section: Section): V2Section {
  const details = section.details ?? (section.summary ? undefined : ("failed" as const));
  return {
    id: section.id,
    title: section.title,
    kind: section.kind,
    hunks: section.hunks,
    gist: section.summary?.text ? firstSentence(section.summary.text) : "",
    level: section.assessment?.level ?? null,
    level_reasons: (section.assessment?.reasons ?? []).map((reason) => ({
      text: reason.text,
      cites: reason.anchors ?? [],
    })),
    summary: section.summary?.text ?? "",
    steps: behaviourSteps(section).map((step) => ({
      id: step.id,
      title: step.title ?? "",
      text: step.after?.trim() || step.text,
      cites: step.anchors ?? [],
    })),
    risks: section.risks.map((risk) => ({
      id: risk.id,
      tier: risk.tier,
      category: risk.category,
      text: risk.text,
      cites: risk.anchors ?? [],
      ...(risk.state ? { state: risk.state } : {}),
      ...(risk.introduced_version ? { introduced_version: risk.introduced_version } : {}),
      ...(risk.mitigated_version ? { mitigated_version: risk.mitigated_version } : {}),
    })),
    ...(details ? { details } : {}),
    derived: section.derived,
  };
}

/** A baum.review/1 document and its `baum.view/1` view (null or absent for a
 *  version without one) as one baum.review/2 document. The view's tiles take
 *  their owner from where it placed them, the main diagram "overview", in
 *  the order the page drew them; a tile placed nowhere has no owner. */
export function fromV1(document: ReviewDocument, view?: unknown, drafts?: unknown): ReviewV2 {
  const shown = isRecord(view) && view.schema === "baum.view/1" ? view : null;
  const overview = shown && isRecord(shown.overview) ? shown.overview : {};
  const recommendation = document.summary.recommendation ?? null;
  const viewVerdict = isRecord(overview.verdict) ? overview.verdict : null;
  const points = viewVerdict
    ? (Array.isArray(viewVerdict.points) ? viewVerdict.points : []).flatMap((point) =>
        isRecord(point) && isString(point.text)
          ? [{ text: point.text, ...(isString(point.section) ? { section: point.section } : {}) }]
          : [],
      )
    : [];
  const verdictText = viewVerdict && isString(viewVerdict.text) ? viewVerdict.text : "";
  const verdict =
    recommendation || verdictText || points.length > 0
      ? {
          decision: recommendation?.decision ?? "look_first",
          text: verdictText || recommendation?.text || "",
          points,
        }
      : null;
  const diagram = isString(overview.diagram) && overview.diagram ? overview.diagram : null;
  const context = document.summary.context;
  const v2Context: V2Context | null = context
    ? {
        area: contextPart(context.area),
        reached_by: contextPart(context.reached_by),
        contracts: contextPart(context.contracts),
        related: contextPart(context.related),
      }
    : null;
  const pending = parseDrafts(drafts);
  return {
    schema: REVIEW_V2,
    review: document.review,
    summary: {
      title: document.summary.title,
      text: document.summary.text,
      change_kind: document.summary.change_kind,
      verdict,
      risk_why:
        isString(overview.risk_summary) && overview.risk_summary.trim()
          ? overview.risk_summary.trim()
          : "",
      ...(document.summary.risk_score ? { risk_score: document.summary.risk_score } : {}),
      review_minutes: document.summary.review_minutes ?? null,
    },
    overview: {
      screenshots: strings(overview.screenshots),
      diagram,
      intent: (Array.isArray(overview.intent) ? overview.intent : []).flatMap((row) =>
        isRecord(row) &&
        isString(row.asked) &&
        isString(row.session) &&
        typeof row.prompt === "number"
          ? [
              {
                session: row.session,
                prompt: row.prompt,
                asked: row.asked,
                got: isString(row.got) ? row.got : "",
              },
            ]
          : [],
      ),
    },
    context: v2Context && Object.values(v2Context).some(Boolean) ? v2Context : null,
    sections: document.sections.map(sectionOf),
    tiles: shown ? viewTiles(shown, diagram) : [],
    stats: document.stats,
    index: document.index,
    ...(pending.length > 0 ? { drafts: pending } : {}),
  };
}

function viewTiles(view: Record<string, unknown>, diagram: string | null): ViewTile[] {
  const placed: [string, string][] = [];
  if (diagram) placed.push([diagram, "overview"]);
  for (const entry of Array.isArray(view.sections) ? view.sections : [])
    if (isRecord(entry) && isString(entry.id))
      for (const tile of strings(entry.tiles))
        if (!placed.some(([id]) => id === tile)) placed.push([tile, entry.id]);
  const tiles = (Array.isArray(view.tiles) ? view.tiles : []).flatMap((tile): ViewTile[] => {
    if (!isRecord(tile) || !isString(tile.id) || !isString(tile.type)) return [];
    const data = tile.data ?? null;
    const caption =
      isRecord(data) && isString(data.caption) && data.caption.trim() ? data.caption : undefined;
    const cites =
      isRecord(data) && Array.isArray(data.source)
        ? (data.source as Anchor[])
        : isRecord(data) && Array.isArray(data.anchors)
          ? (data.anchors as Anchor[])
          : [];
    return [
      {
        id: tile.id,
        type: tile.type,
        title: isString(tile.title) ? tile.title : "",
        owner: placed.find(([id]) => id === tile.id)?.[1] ?? "",
        ...(caption && NATIVE_TYPES.has(tile.type) ? { caption } : {}),
        cites,
        data,
        state:
          isString(tile.state) && TILE_STATES.has(tile.state)
            ? (tile.state as ViewTileState)
            : "unavailable",
        ...(Array.isArray(tile.covers) ? { covers: strings(tile.covers) } : {}),
        ...(tile.definition !== undefined ? { definition: tile.definition } : {}),
        ...(Array.isArray(tile.problems) ? { problems: strings(tile.problems) } : {}),
        ...(tile.added === true ? { added: true as const } : {}),
        ...(typeof tile.from === "number" && tile.from > 0 ? { from: tile.from } : {}),
        ...(tile.filling === true ? { filling: true as const } : {}),
      },
    ];
  });
  // In the order the page placed them: the diagram, then each section's.
  const at = (id: string) => {
    const position = placed.findIndex(([each]) => each === id);
    return position < 0 ? placed.length : position;
  };
  return tiles.toSorted((a, b) => at(a.id) - at(b.id));
}
