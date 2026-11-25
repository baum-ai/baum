// A baum.review/1 document as the baum.review/2 model the rail and the file
// list read (`reviewOf`). The version's Summary and sections are drawn from the
// baum.review/1 document itself, so only what the rail needs is built here.

import type {
  Anchor,
  BehaviorStep,
  Block,
  ReviewDocument,
  Section,
} from "@baum/viewer/review-document";
import {
  firstSentence,
  REVIEW_V2,
  type ReviewV2,
  type V2Block,
  type V2Context,
  type V2ContextPart,
  type V2Point,
  type V2Section,
} from "./document";

function behaviourSteps(section: Section): BehaviorStep[] {
  return section.blocks.flatMap((block) =>
    block.kind === "behavior" ? ((block as Extract<Block, { kind: "behavior" }>).steps ?? []) : [],
  );
}

/** A section's body from baum.review/1's text: its summary as a prose block
 *  and its steps as a points block, each point "**Title.** text". */
export function bodyOfSteps(
  summary: string,
  steps: { id: string; title: string; text: string; cites: Anchor[] }[],
): V2Block[] {
  const body: V2Block[] = [];
  if (summary.trim()) body.push({ type: "prose", text: summary.trim() });
  const items = steps.flatMap((step): V2Point[] => {
    if (!step.text.trim()) return [];
    const title = step.title.trim();
    return [
      {
        id: step.id,
        text: title ? `**${/[.!?]$/.test(title) ? title : `${title}.`}** ${step.text}` : step.text,
        cites: step.cites,
      },
    ];
  });
  if (items.length > 0) body.push({ type: "points", items });
  return body;
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
    // The section's summary and behaviour steps read as its body.
    body: bodyOfSteps(
      section.summary?.text ?? "",
      behaviourSteps(section).map((step) => ({
        id: step.id,
        title: step.title ?? "",
        text: step.after?.trim() || step.text,
        cites: step.anchors ?? [],
      })),
    ),
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

/** A baum.review/1 document as a baum.review/2 one: no tiles, no overview
 *  parts and no verdict beyond the review's recommendation. */
export function fromV1(document: ReviewDocument): ReviewV2 {
  const recommendation = document.summary.recommendation ?? null;
  const context = document.summary.context;
  const v2Context: V2Context | null = context
    ? {
        area: contextPart(context.area),
        reached_by: contextPart(context.reached_by),
        contracts: contextPart(context.contracts),
        related: contextPart(context.related),
      }
    : null;
  return {
    schema: REVIEW_V2,
    review: document.review,
    summary: {
      title: document.summary.title,
      text: document.summary.text,
      change_kind: document.summary.change_kind,
      verdict: recommendation
        ? { decision: recommendation.decision, text: recommendation.text ?? "", points: [] }
        : null,
      risk_why: "",
      ...(document.summary.risk_score ? { risk_score: document.summary.risk_score } : {}),
      review_minutes: document.summary.review_minutes ?? null,
    },
    overview: { screenshots: [], videos: [], diagram: null, intent: [] },
    context: v2Context && Object.values(v2Context).some(Boolean) ? v2Context : null,
    sections: document.sections.map(sectionOf),
    tiles: [],
    stats: document.stats,
    index: document.index,
  };
}
