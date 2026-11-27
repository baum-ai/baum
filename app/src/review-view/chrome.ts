import type {
  Anchor,
  Block,
  ReviewContextPart,
  ReviewDocument,
  Section,
} from "@baum/viewer/review-document";
import { leadProse, pointId, type ReviewV2, type V2ContextPart, type V2Section } from "./document";

/** The review the page's chrome reads, made from a baum.review/2 document:
 *  the review viewer around the tabs (its head, version menu, history, side
 *  pane, comments, selection and keys) and the viewer's parts the tabs draw
 *  (citation chips, images, diffs) read a baum.review/1 document. The header,
 *  index and stats are the same objects; the sections carry their summary,
 *  steps (as a behaviour block), risks and level; the verdict becomes the
 *  recommendation and the context's files its refs. What baum.review/2 has
 *  no field for (the author, the authorship, agent reviews, evidence) is
 *  empty. The tabs themselves read the baum.review/2 document it carries. */
export function chromeOf(review: ReviewV2): ReviewDocument {
  const listed = new Set(review.sections.flatMap((section) => section.hunks));
  const sessions = review.index.sessions;
  return {
    schema: review.schema,
    review: review.review,
    summary: {
      title: review.summary.title,
      text: review.summary.text,
      ...(review.context
        ? {
            context: {
              area: contextPart(review.context.area) ?? { text: "", refs: [] },
              ...(review.context.reached_by
                ? { reached_by: contextPart(review.context.reached_by)! }
                : {}),
              ...(review.context.contracts
                ? { contracts: contextPart(review.context.contracts)! }
                : {}),
              ...(review.context.related ? { related: contextPart(review.context.related)! } : {}),
            },
          }
        : {}),
      change_kind: review.summary.change_kind,
      implements: [],
      release_note: null,
      review_minutes: review.summary.review_minutes,
      breaking_change: null,
      attention: null,
      recommendation: review.summary.verdict
        ? {
            decision: review.summary.verdict.decision,
            text: review.summary.verdict.text,
            refs: review.summary.verdict.points.flatMap((point) =>
              point.section ? [point.section] : [],
            ),
          }
        : null,
      author: { login: "", association: "", bot: false },
      authorship: {
        declared: "",
        declared_tools: [],
        anchors: [],
        observed_sessions: sessions.length,
        observed_vendors: [...new Set(sessions.map((session) => session.vendor))],
      },
      ...(review.summary.risk_score ? { risk_score: review.summary.risk_score } : {}),
    },
    stats: review.stats,
    sections: review.sections.map(
      (section): Section => ({
        id: section.id,
        title: section.title,
        kind: section.kind,
        hunks: section.hunks,
        ...(section.details ? { details: section.details } : {}),
        summary: section.details ? null : { text: leadProse(section) || section.gist, anchors: [] },
        attention: null,
        risks: section.risks.map((risk) => ({
          id: risk.id,
          tier: risk.tier,
          category: risk.category,
          source: "model",
          text: risk.text,
          anchors: risk.cites,
          ...(risk.state ? { state: risk.state } : {}),
          ...(risk.introduced_version ? { introduced_version: risk.introduced_version } : {}),
          ...(risk.mitigated_version ? { mitigated_version: risk.mitigated_version } : {}),
        })),
        evidence: [],
        blocks: behaviourBlocks(section),
        ...(section.level
          ? {
              assessment: {
                level: section.level,
                factors: [],
                reasons: section.level_reasons.map((reason) => ({
                  text: reason.text,
                  anchors: reason.cites as Anchor[],
                })),
              },
            }
          : {}),
        derived: section.derived,
      }),
    ),
    agent_reviews: [],
    unassigned_hunks: review.index.hunks.flatMap((hunk) => (listed.has(hunk.id) ? [] : [hunk.id])),
    index: review.index,
  };
}

/** The points of a section's body as the behaviour steps the side pane,
 *  selection and comments read. */
function behaviourBlocks(section: V2Section): Block[] {
  const steps = section.body.flatMap((block, at) =>
    block.type === "points"
      ? block.items.map((point, position) => ({
          id: pointId(section.id, at, position, point),
          text: point.text,
          anchors: point.cites,
          evidence: [],
        }))
      : [],
  );
  return steps.length > 0 ? [{ kind: "behavior", steps }] : [];
}

function contextPart(part: V2ContextPart | null): ReviewContextPart | undefined {
  return part
    ? { text: part.text, refs: part.files.map((path) => ({ path, symbol: null })) }
    : undefined;
}
