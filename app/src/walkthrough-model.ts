import { uiState } from "./ui-state";
import {
  RISK_SCORE_MEANING,
  hunkLine,
  reviewRecommendation,
  reviewRefs,
  sectionsInReviewOrder,
  type Anchor,
  type Block,
  type Evidence,
  type IndexedHunk,
  type ReviewDocument,
  type RiskScoreLevel,
  type RiskTier,
  type Section,
} from "@baum/viewer/review-document";

/** Where a comment lands on GitHub: a line of the reviewed diff, or a range
 *  ending on it. */
export type CommentPlace = {
  path: string;
  line: number;
  side: "RIGHT" | "LEFT";
  start_line?: number;
};

/** One behaviour the tour stops at. */
export type Stop = {
  /** The behaviour step's id, or the section's for a section without steps. */
  id: string;
  chapter: number;
  title: string;
  /** The question to judge it by, offered as the comment box's prompt. */
  question: string | null;
  /** The review's own statement of the behaviour. */
  text: string;
  /** The hunks that implement it. */
  anchors: Anchor[];
  /** What backs the statement: tests, CI, runs, or what was only asserted. */
  evidence: Evidence[];
  place: CommentPlace | null;
  /** The place in a few characters, such as `service.go:241`. */
  where: string;
};

export type Chapter = {
  id: string;
  title: string;
  level: RiskScoreLevel | null;
  /** What the level means, or the section's summary without one. */
  meaning: string | null;
  /** Why the section carries its level. */
  why: string;
  /** What the section changes, in a sentence or three. */
  summary: string;
  /** The lines its hunks add and remove. */
  lines: { additions: number; deletions: number };
  /** What the change set out to do, as the pull request and its sessions
   *  say, when the review found it. */
  intent: string | null;
  /** The section's open risks, worst first. */
  risks: { text: string; tier: RiskTier }[];
  /** Indexes into `Tour.stops`. */
  stops: number[];
};

export type Tour = {
  intro: string;
  releaseNote: string | null;
  /** The pull request's own reasons for its score, each linked to its
   *  chapter's first stop when it names one. */
  risks: { text: string; stop: number | null }[];
  level: RiskScoreLevel | null;
  chapters: Chapter[];
  stops: Stop[];
  /** The review's reading estimate, in whole minutes. */
  minutes: number | null;
};

const TIERS: RiskTier[] = ["critical", "high", "medium", "low", "mechanical"];

const text = (value: unknown) => (typeof value === "string" ? value.trim() : "");

/** A headline for a step written before steps had titles: its first
 *  sentence, cut at its first clause when the sentence strings several
 *  together with semicolons. The whole statement is still shown beneath. */
export function firstSentence(value: string): string {
  const match = /^(.+?[.!?])(\s|$)/.exec(value);
  const sentence = (match ? match[1]! : value).replace(/[.!?]$/, "");
  const clause = sentence.indexOf("; ");
  return clause > 0 ? sentence.slice(0, clause) : sentence;
}

const basename = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** Where a comment on code cited by `anchors` lands: the first hunk it can
 *  place, on its cited lines or else its first changed line. */
export function placeOf(anchors: Anchor[], hunks: Map<string, IndexedHunk>): CommentPlace | null {
  for (const anchor of anchors) {
    if (anchor.type !== "hunk") continue;
    const hunk = hunks.get(anchor.id);
    if (!hunk) continue;
    const [start, end] = Array.isArray(anchor.lines) ? anchor.lines : [];
    if (typeof start === "number" && typeof end === "number" && start > 0 && end >= start)
      return start < end
        ? { path: hunk.path, line: end, side: "RIGHT", start_line: start }
        : { path: hunk.path, line: end, side: "RIGHT" };
    const added = hunk.diff?.lines.find((line) => line.kind === "add");
    if (added && added.kind === "add")
      return { path: hunk.path, line: added.new_line, side: "RIGHT" };
    const line = hunkLine(hunk);
    if (line) return { path: hunk.path, line, side: "RIGHT" };
    const removed = hunk.diff?.lines.find((line) => line.kind === "remove");
    if (removed && removed.kind === "remove")
      return { path: hunk.path, line: removed.old_line, side: "LEFT" };
  }
  return null;
}

function sectionWhy(section: Section): string {
  return (
    text(section.assessment?.reasons?.[0]?.text) ||
    text(section.derived.review_reason?.text) ||
    text(section.summary?.text)
  );
}

function sectionLevel(section: Section): RiskScoreLevel | null {
  const level = section.derived.review_score ?? section.assessment?.level;
  return level && level >= 1 && level <= 5 ? level : null;
}

/** The tour through a review: its logical sections, riskiest first, each a
 *  chapter of the behaviours it changes. A section written without behaviour
 *  steps is one stop of its summary. The mechanical section, obsolete
 *  sections and sections without hunks are left out. */
/** What the review asks to look at first: the recommendation's sentence when
 *  it says to look first or to request changes, else the attention an older
 *  document carries. `section` is the section the first ref leads to. */
export function lookAt(document: ReviewDocument): { text: string; section: string | null } | null {
  const recommendation = reviewRecommendation(document);
  const text =
    recommendation && recommendation.decision !== "approve" ? recommendation.text.trim() : "";
  if (text && recommendation) {
    const refs = reviewRefs(document);
    for (const ref of recommendation.refs) {
      const found = refs.get(ref);
      if (found?.kind === "item") return { text, section: found.section };
    }
    return { text, section: null };
  }
  const attention = document.summary.attention?.text?.trim();
  return attention
    ? { text: attention, section: document.summary.attention?.refs?.[0] ?? null }
    : null;
}

export function buildTour(document: ReviewDocument): Tour {
  const hunks = new Map(document.index.hunks.map((hunk) => [hunk.id, hunk]));
  const chapters: Chapter[] = [];
  const stops: Stop[] = [];
  const sections = sectionsInReviewOrder(document.sections).filter(
    (section) =>
      section.kind === "logical" && section.hunks.length > 0 && section.derived.state !== "stale",
  );
  for (const section of sections) {
    const chapter = chapters.length;
    const evidence = new Map((section.evidence ?? []).map((item) => [item.id, item]));
    const steps = (section.blocks ?? []).flatMap((block: Block) =>
      block.kind === "behavior"
        ? ((block as Extract<Block, { kind: "behavior" }>).steps ?? [])
        : [],
    );
    const own: number[] = [];
    const add = (stop: Omit<Stop, "chapter" | "place" | "where">) => {
      const place = placeOf(stop.anchors, hunks);
      own.push(stops.length);
      stops.push({
        ...stop,
        chapter,
        place,
        where: place ? `${basename(place.path)}:${place.line}` : "",
      });
    };
    for (const step of steps) {
      const statement = text(step.text);
      if (!statement) continue;
      add({
        id: step.id,
        title: text(step.title) || firstSentence(statement),
        question: text(step.question) || null,
        text: statement,
        anchors: (step.anchors ?? []).filter((anchor) => anchor.type === "hunk"),
        evidence: (step.evidence ?? []).flatMap((id) => {
          const item = evidence.get(id);
          return item ? [item] : [];
        }),
      });
    }
    if (own.length === 0) {
      const summary = text(section.summary?.text);
      const anchors = (section.summary?.anchors ?? []).filter((anchor) => anchor.type === "hunk");
      add({
        id: section.id,
        title: section.title,
        question: null,
        text: summary,
        anchors: anchors.length
          ? anchors
          : section.hunks.slice(0, 1).map((id) => ({ type: "hunk" as const, id })),
        evidence: [],
      });
    }
    const level = sectionLevel(section);
    chapters.push({
      id: section.id,
      title: section.title,
      level,
      meaning: level ? RISK_SCORE_MEANING[level] : null,
      why: sectionWhy(section),
      summary: text(section.summary?.text),
      lines: { additions: section.derived.additions, deletions: section.derived.deletions },
      risks: (section.risks ?? [])
        .filter((risk) => risk.state !== "mitigated" && text(risk.text))
        .map((risk) => ({ text: text(risk.text), tier: risk.tier }))
        .sort((a, b) => TIERS.indexOf(a.tier) - TIERS.indexOf(b.tier)),
      intent:
        (section.blocks ?? [])
          .flatMap((block: Block) =>
            block.kind === "intent"
              ? [text((block as Extract<Block, { kind: "intent" }>).text)]
              : [],
          )
          .filter(Boolean)
          .join("\n\n") || null,
      stops: own,
    });
  }
  const score = document.summary.risk_score;
  const firstStop = (sectionId: string) =>
    chapters.find((chapter) => chapter.id === sectionId)?.stops[0] ?? null;
  const risks = (score?.reasons ?? [])
    .filter((reason) => text(reason.text))
    .map((reason) => ({ text: text(reason.text), stop: firstStop(reason.section) }));
  const look = lookAt(document);
  const minutes = document.summary.review_minutes;
  return {
    intro: text(document.summary.text),
    releaseNote: text(document.summary.release_note) || null,
    risks: risks.length
      ? risks
      : look
        ? [{ text: look.text, stop: firstStop(look.section ?? "") }]
        : [],
    level: score?.score ?? null,
    chapters,
    stops,
    minutes:
      typeof minutes === "number" && Number.isInteger(minutes) && minutes >= 1 && minutes <= 240
        ? minutes
        : null,
  };
}

/** What the reader has done on one review version: the stops they looked at
 *  and what they said looks off. */
export type Progress = {
  seen: string[];
  notes: Record<string, string>;
  /** The review's overall comment. */
  note: string;
};

export const EMPTY_PROGRESS: Progress = { seen: [], notes: {}, note: "" };

export function progressKey(document: ReviewDocument): string {
  return `${document.review.repository.toLowerCase()}#${document.review.pull_request}@v${document.review.version}`;
}

export function progressState(key: string) {
  return uiState<Progress>(`api/ui-state/walkthrough?key=${encodeURIComponent(key)}`, () => ({
    seen: [],
    notes: {},
    note: "",
  }));
}
export function loadProgress(key: string): Progress {
  return progressState(key).get().value;
}

export type ReviewEvent = "APPROVE" | "COMMENT" | "REQUEST_CHANGES";

export type ReviewSubmission = {
  repository: string;
  pr: number;
  head_sha: string;
  event: ReviewEvent;
  body: string;
  comments: (CommentPlace & { body: string })[];
};

/** The comments the reader wrote, in tour order. */
export function commentedStops(tour: Tour, progress: Progress): Stop[] {
  return tour.stops.filter((stop) => text(progress.notes[stop.id]));
}

/** The GitHub review: each comment on the line its behaviour cites, and the
 *  ones without a line (or all of them, `inline` false) in the body under the
 *  reader's overall comment. */
export function reviewSubmission(
  document: ReviewDocument,
  tour: Tour,
  progress: Progress,
  event: ReviewEvent,
  inline = true,
): ReviewSubmission {
  const comments: ReviewSubmission["comments"] = [];
  const unplaced: string[] = [];
  for (const stop of commentedStops(tour, progress)) {
    const note = text(progress.notes[stop.id]);
    if (inline && stop.place) comments.push({ ...stop.place, body: note });
    else unplaced.push(`**${stop.title}**${stop.where ? ` (\`${stop.where}\`)` : ""}\n${note}`);
  }
  return {
    repository: document.review.repository,
    pr: document.review.pull_request,
    head_sha: document.review.head,
    event,
    body: [text(progress.note), ...unplaced].filter(Boolean).join("\n\n"),
    comments,
  };
}
