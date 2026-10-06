import pluralize from "pluralize";

import { sha256Hex } from "./sha256.ts";

/** The generated review of one pull request, as it is stored.
 *
 *  Mirrors `schema/review-1.json` by hand (schema `baum.review/1`); that file
 *  is canonical. Only the fields the viewer renders are typed strictly; closed
 *  vocabularies are unions so a renderer can be exhaustive where it matters
 *  and fall back where the schema may grow (new block kinds and anchor types
 *  are additive and must render as unsupported content rather than fail the
 *  page). */

export type RiskTier = "critical" | "high" | "medium" | "low" | "mechanical";
export type SectionTier = RiskTier | "unrated";
export type RiskSource = "model" | "tool" | "agent_review" | "human_review";
export type EvidenceStatus =
  | "verified"
  | "observed"
  | "reported"
  | "asserted"
  | "pending"
  | "skipped"
  | "missing"
  | "failed"
  | "unavailable";
export type EvidenceKind =
  | "test"
  | "ci"
  | "run"
  | "report"
  | "coverage"
  | "benchmark"
  | "media"
  | "review";
export type SectionState = "new" | "unchanged" | "changed" | "stale";
export type GenerationState = "pending" | "current" | "outdated" | "failed";
export type PullRequestState = "draft" | "open" | "merged" | "closed";
export type Verdict =
  | "approve"
  | "comment"
  | "request_changes"
  | "needs_human"
  | "blocked_on"
  | "none";
export type FindingSeverity = "must_fix" | "should_fix" | "consider";
export type FindingState = "open" | "resolved" | "dismissed";
export type CheckConclusion =
  | "success"
  | "failure"
  | "pending"
  | "skipped"
  | "cancelled"
  | "neutral";
export type HunkRole = "code" | "test" | "generated" | "migration" | "docs" | "config" | "vendored";
export type HunkChange = "added" | "modified" | "removed" | "renamed";

export type Anchor =
  | { type: "hunk"; id: string; lines?: [number, number] }
  | { type: "file"; path: string }
  | { type: "commit"; sha: string }
  | { type: "pr"; number: number }
  | { type: "pr_body"; section: string | null }
  | { type: "pr_comment"; id: number }
  | { type: "review_thread"; id: string }
  | { type: "prompt"; session: string; index: number; input?: string }
  | { type: "session_event"; session: string; event: string }
  | { type: "check"; id: string; test?: string }
  | { type: "bot_report"; id: string }
  | { type: "issue"; system: string; key: string }
  | { type: "spec"; page: string; assertion?: string }
  | { type: "external_url"; url: string; label: string }
  | { type: "attachment"; url: string; id?: string };

export type Risk = {
  tiles?: unknown[];
  id: string;
  tier: RiskTier;
  category: string;
  source: RiskSource;
  text: string;
  anchors: Anchor[];
  /** Added with review history. Optional so published legacy documents remain readable. */
  state?: "active" | "mitigated";
  introduced_version?: number;
  mitigated_version?: number | null;
};

export type Evidence = {
  id: string;
  kind: EvidenceKind;
  claim: string;
  status: EvidenceStatus;
  granularity?: "job" | "test";
  source: string;
  anchors: Anchor[];
  detail?: string;
  expects?: string;
};

export type Entity = {
  kind: string;
  name: string;
  path?: string;
  change: HunkChange;
  hunks: string[];
};

export type BehaviorStep = { id: string; text: string; anchors: Anchor[]; evidence: string[] };

export type Decision = {
  id: string;
  by: "human" | "agent";
  text: string;
  rejected?: string;
  anchors: Anchor[];
};

export type Group = {
  id: string;
  rule: string;
  derived_from?: string;
  hunks: string[];
  count: number;
};

export type Dependency = {
  name: string;
  ecosystem: string;
  from?: string | null;
  to?: string | null;
  hunks: string[];
  changelog?: Anchor;
};

export type Block =
  | { kind: "intent"; text: string; anchors: Anchor[] }
  | { kind: "behavior"; steps: BehaviorStep[] }
  | { kind: "entities"; items: Entity[] }
  | { kind: "groups"; items: Group[] }
  | { kind: "decisions"; items: Decision[] }
  | { kind: "dependency"; items: Dependency[] }
  | { kind: "included_prs"; items: { pr: number; note?: string }[] }
  // Presentational blocks attribute their content through `source`, an anchor
  // list, and carry the text itself in `content`.
  | { kind: "diagram"; format: string; content: string; source: Anchor[] }
  | { kind: "code"; language: string; content: string; caption?: string; source: Anchor[] }
  | { kind: "media"; url: string; caption?: string; source: Anchor[] }
  | { kind: "images"; items: { attachment: string; caption?: string }[]; source: Anchor[] }
  | { kind: "repro"; steps: string[]; source: Anchor[] }
  | { kind: "table"; columns: string[]; rows: string[][]; source: Anchor[] }
  | { kind: string; anchors?: Anchor[] };

export type Attention = { text: string; refs: string[] } | null;

/** The rubric's 1–5 scale of inherent risk, independent of any finding. */
export type RiskScoreLevel = 1 | 2 | 3 | 4 | 5;

/** The model's rating of one logical section. Mechanical sections and
 *  sections written before assessments existed carry none. */
export type SectionAssessment = {
  level: RiskScoreLevel;
  factors: string[];
  reasons: { text: string; anchors: Anchor[] }[];
};

/** The generator's score for the whole pull request, derived at generation.
 *  `lower_bound` means incomplete input may hide a higher score. */
export type RiskScore = {
  rubric: string;
  status: "assessed" | "lower_bound" | "not_assessed";
  score: RiskScoreLevel | null;
  reasons: { section: string; risk: string | null; text: string }[];
  limitations: { kind: "unassessed_sections" | "omitted_source" | string; count: number }[];
};

export type Section = {
  tiles?: unknown[];
  id: string;
  title: string;
  kind: "logical" | "mechanical";
  hunks: string[];
  summary: { text: string; anchors: Anchor[] };
  attention: Attention;
  risks: Risk[];
  evidence: Evidence[];
  blocks: Block[];
  assessment?: SectionAssessment;
  derived: {
    risk_tier: SectionTier;
    additions: number;
    deletions: number;
    role_loc: Partial<Record<HunkRole, number>>;
    reviewers: { login: string; source: string }[];
    state: SectionState;
    evidence_summary: Partial<Record<EvidenceStatus, number>>;
    open_findings: number;
    /** How much the section asks of its reviewer, on the risk score's rules;
     *  absent on obsolete and unrated sections and on documents generated
     *  before section scores existed. */
    review_score?: RiskScoreLevel;
    review_reason?: { risk: string | null; text: string };
  };
};

export type Finding = {
  id: string;
  severity: FindingSeverity;
  text: string;
  section: string | null;
  anchors: Anchor[];
  state: FindingState;
  addressed_by: string | null;
};

export type AgentReview = {
  id: string;
  tool: string;
  assessed_head: string;
  stale: boolean;
  verdict: Verdict;
  blocked_on: string | null;
  counts: Record<FindingSeverity, number>;
  anchors: Anchor[];
  findings: Finding[];
};

export type IndexedHunkLine =
  | { kind: "add"; new_line: number; text: string }
  | { kind: "remove"; old_line: number; text: string }
  | { kind: "context"; old_line: number; new_line: number; text: string };

export type IndexedHunk = {
  id: string;
  path: string;
  new_range: [number, number];
  additions: number;
  deletions: number;
  change: HunkChange;
  role: HunkRole;
  github_url?: string;
  diff?: {
    header: string;
    lines: IndexedHunkLine[];
  };
};

export type IndexedCheck = {
  id: string;
  kind: "check_run" | "status";
  name: string;
  conclusion: CheckConclusion;
  required: boolean;
  head: string;
  started_at?: string | null;
  completed_at?: string | null;
  /** Optional in the schema; a check can report without a run link. */
  url?: string;
};

export type IndexedReport = {
  id: string;
  tool: string;
  marker: string;
  comment: number;
  head: string | null;
  summary: string;
  url: string;
};

export type IndexedComment = {
  id: number;
  kind: "issue" | "review" | "review_comment";
  author: string;
  bot: boolean;
  created_at: string;
  url: string;
};

export type IndexedThread = {
  id: string;
  path: string | null;
  line: number | null;
  resolved: boolean;
  outdated?: boolean;
  url?: string;
};

export type SessionEvent = { id: string; at: string; verb: string; summary: string };
export type SessionPrompt = { index: number; id: string; at: string; text: string };

export type IndexedSession = {
  id: string;
  vendor: string;
  models: string[];
  title: string | null;
  git_branch?: string | null;
  started_at?: string | null;
  ended_at?: string | null;
  duration_minutes?: number | null;
  outcome?: string | null;
  prompt_count?: number | null;
  tool_calls?: number | null;
  restricted: boolean;
  events?: SessionEvent[];
  prompts?: SessionPrompt[];
};

export type IndexedPR = {
  number: number;
  title: string;
  state: "open" | "merged" | "closed";
  url: string;
};

export type Implements =
  | { type: "issue"; system: string; key: string }
  | { type: "spec"; page: string; assertion?: string }
  /** A type the schema grew after this renderer, which must show as itself
   *  rather than fail the page or be filed under a type we do know. */
  | { type: string };

/** A stored description image. `source_url` is where it was copied from and is
 *  never loaded: the page serves the stored copy. */
export type IndexedAttachment = {
  id: string;
  position: number;
  source_url: string;
  content_type: string;
  width: number;
  height: number;
  alt?: string;
  heading?: string;
};

export type ReviewDocument = {
  schema: string;
  review: {
    repository: string;
    pull_request: number;
    pr_state: PullRequestState;
    head: string;
    base: string;
    merge_base: string;
    merged_commit: string | null;
    version: number;
    previous_version: number | null;
    generation_state: GenerationState;
    generated_at: string;
    evidence_refreshed_at: string;
    generator: {
      model: string;
      review_md: string | null;
      /** The pull request changes REVIEW.md; the base version was applied. */
      review_md_changed?: boolean;
      schema_revision: string;
      truncated: string[];
    };
    sources: Record<string, boolean | number>;
    relations: { type: string; number?: number; key?: string }[];
  };
  summary: {
    title: string;
    text: string;
    change_kind: string;
    implements: Implements[];
    release_note: string | null;
    breaking_change: string | null;
    attention: Attention;
    /** The generator's account of how the review went. It was a plain string
     *  before it was an object, and documents generated then are still in the
     *  store. */
    review_exchange?:
      | string
      | {
          summary?: string;
          reviews?: number;
          back_and_forths?: number | null;
          critical_issues_found?: number | null;
          changes_after_open?: {
            commits?: number;
            lines_added?: number;
            lines_removed?: number;
          } | null;
        };
    author: { login: string; association: string; bot: boolean };
    authorship: {
      declared: string;
      declared_tools: string[];
      anchors: Anchor[];
      observed_sessions: number;
      observed_vendors: string[];
      observed_models: string[];
    };
    /** Absent on documents generated before risk scores existed. */
    risk_score?: RiskScore;
  };
  stats: {
    sections: number;
    files: number;
    hunks: number;
    additions: number;
    deletions: number;
    risk_loc: Record<RiskTier | "unrated", number>;
    role_loc: Partial<Record<HunkRole, number>>;
    checks: { required_failed: number; optional_failed: number; pending: number; skipped: number };
    agent_findings: Record<FindingState, number>;
  };
  sections: Section[];
  agent_reviews: AgentReview[];
  unassigned_hunks: string[];
  index: {
    hunks: IndexedHunk[];
    checks: IndexedCheck[];
    reports: IndexedReport[];
    comments: IndexedComment[];
    threads: IndexedThread[];
    sessions: IndexedSession[];
    prs: IndexedPR[];
    /** Absent when the review has no description images. */
    attachments?: IndexedAttachment[];
  };
};

/** What kind of risk this is, in one word, for the places that lead with it.
 *
 *  Every risk carries a category, but the schema neither constrains it nor is
 *  the document validated on the way in, and this string is now an identifier
 *  rather than a footnote: it titles the risk pane and starts the rail row. A
 *  missing one must cost that row its word, not the page. */
export function riskCategory(risk: { category?: string | null }, fallback = "Risk"): string {
  return (risk.category ? humanize(risk.category) : "") || fallback;
}

/** A risk in one line of a 264px rail. The category leads because it is what
 *  separates two risks in the same section, and the section title follows it
 *  into the truncation. */
export function riskLabel(risk: TriageRisk): string {
  return `${riskCategory(risk)} · ${risk.sectionTitle}`;
}

/** The state of a live pull request, in the vocabulary this schema uses.
 *
 *  The browse API and this schema do not share one: the API carries four states
 *  as three plus a `draft` flag, the schema carries four. Comparing them
 *  unconverted reads every draft pull request as "open", and as one whose
 *  review was generated in a state it has since left. */
export function livePullRequestState(
  pull: { state: "open" | "merged" | "closed"; draft: boolean } | null | undefined,
): PullRequestState | undefined {
  if (!pull) return undefined;
  // A pull request closed while it was a draft keeps the flag, and what matters
  // about it then is that it is closed. Draft is only the state of one that is
  // otherwise open.
  return pull.draft && pull.state === "open" ? "draft" : pull.state;
}

/** The reserved status scale of `Badge`, which risk, evidence and verdicts all
 *  map onto. Categorical values (vendors, kinds) stay neutral. */
export type Tone = "neutral" | "info" | "positive" | "caution" | "critical";

/** Follows the tier weighting of the review mock: only the two tiers a reviewer
 *  must read carry colour, so a section full of medium risks does not shout. */
export function tierTone(tier: RiskTier | "unrated"): Tone {
  return tier === "critical" ? "critical" : tier === "high" ? "caution" : "neutral";
}

export function evidenceTone(status: EvidenceStatus): Tone {
  switch (status) {
    case "verified":
      return "positive";
    case "observed":
      return "info";
    case "failed":
      return "critical";
    case "missing":
      return "caution";
    default:
      return "neutral";
  }
}

export function checkTone(conclusion: CheckConclusion): Tone {
  switch (conclusion) {
    case "success":
      return "positive";
    case "failure":
      return "critical";
    case "cancelled":
      return "caution";
    default:
      return "neutral";
  }
}

export function verdictTone(verdict: Verdict): Tone {
  switch (verdict) {
    case "approve":
      return "positive";
    case "request_changes":
      return "critical";
    case "needs_human":
    case "blocked_on":
      return "caution";
    default:
      return "neutral";
  }
}

export function severityTone(severity: FindingSeverity): Tone {
  return severity === "must_fix" ? "critical" : severity === "should_fix" ? "caution" : "neutral";
}

export function findingStateTone(state: FindingState): Tone {
  return state === "open" ? "caution" : state === "resolved" ? "positive" : "neutral";
}

/** The tone of a pull request's state wherever it is shown. A closed,
 *  unmerged pull request is a product signal, not an error, so it does not
 *  get the critical tone. */
export function pullRequestStateTone(state: string): Tone {
  return state === "merged" ? "positive" : state === "open" ? "info" : "neutral";
}

export function generationTone(state: GenerationState): Tone {
  switch (state) {
    case "current":
      return "positive";
    case "outdated":
      return "caution";
    case "failed":
      return "critical";
    default:
      return "neutral";
  }
}

export function sectionStateTone(state: SectionState): Tone {
  return state === "new" ? "info" : state === "changed" ? "caution" : "neutral";
}

/** The state a section's badge shows, where "unchanged" means none. On the
 *  first review every section is new, so there "New" would say nothing and is
 *  left off; it is kept from the second version on, where it sets a section
 *  apart from the ones read last time. */
export function sectionBadgeState(
  state: SectionState,
  review: Pick<ReviewDocument["review"], "previous_version">,
): SectionState {
  return state === "new" && review.previous_version == null ? "unchanged" : state;
}

export function sectionStateLabel(state: SectionState): string {
  return state === "stale" ? "Obsolete" : humanize(state);
}

/** A risk's age across versions. On the first review every risk is new, so
 *  it has no label there, the same way sections carry no "New" badge.
 *
 *  A risk in an obsolete section is "Obsolete" whatever its recorded state:
 *  its section losing its hunks says nothing about whether it was addressed. */
export function riskLifecycleLabel(
  risk: Risk,
  review: Pick<ReviewDocument["review"], "version" | "previous_version">,
  sectionState: SectionState,
): string | null {
  if (sectionState === "stale") return sectionStateLabel(sectionState);
  if (!risk.state || !risk.introduced_version) return null;
  if (risk.state === "mitigated") {
    return risk.mitigated_version ? `Mitigated in v${risk.mitigated_version}` : "Mitigated";
  }
  if (risk.introduced_version !== review.version) return `From v${risk.introduced_version}`;
  return review.previous_version == null ? null : "New";
}

export function riskLifecycleTone(risk: Risk, sectionState: SectionState): Tone {
  if (sectionState === "stale") return sectionStateTone(sectionState);
  return risk.state === "mitigated" ? "positive" : "info";
}

/** Words the document spells in lower case that a reader spells in capitals.
 *  Sentence case would print "Ui" and "Api compat", which read as misspellings
 *  of names rather than as the names themselves. */
const ACRONYMS = new Set(["ui", "api"]);

/** The review whose diff still holds a risk's code evidence, when the one
 *  being read no longer does: the last version before the risk was mitigated,
 *  or before the one being read if its section is obsolete, in which the
 *  section still had hunks. `activeVersions` lists the versions in which the
 *  section had hunks; until the history is read, the version just before is
 *  the best guess. */
export function riskEvidenceReviewVersion(
  risk: Risk,
  sectionState: SectionState,
  reviewVersion: number,
  activeVersions?: readonly number[],
): number | undefined {
  let before = sectionState === "stale" ? reviewVersion : undefined;
  if (risk.state === "mitigated" && risk.mitigated_version)
    before = Math.min(before ?? risk.mitigated_version, risk.mitigated_version);
  if (!before || before <= 1) return undefined;
  if (!activeVersions) return before - 1;
  const earlier = activeVersions.filter((version) => version < before);
  return earlier.length > 0 ? Math.max(...earlier) : undefined;
}

/** `public_endpoint` reads as "Public endpoint"; a closed-list value is never
 *  shown raw. */
export function humanize(value: string): string {
  const words = value.replace(/[_-]+/g, " ").trim().split(" ");
  return words
    .map((word, index) => {
      if (ACRONYMS.has(word.toLowerCase())) return word.toUpperCase();
      return index === 0 && word ? word[0]!.toUpperCase() + word.slice(1) : word;
    })
    .join(" ");
}

/** Additions and deletions in the diff-stat idiom, thousands separated. */
export function formatLines(additions: number, deletions: number): string {
  return `+${additions.toLocaleString("en-US")} −${deletions.toLocaleString("en-US")}`;
}

export function shortSha(sha: string): string {
  return sha.slice(0, 7);
}

/** One swatch per tier, from the ink scale rather than the chart palette: risk
 *  is a status, and the two tiers a reviewer must read are the only ones that
 *  leave the greys. */
export const TIER_COLOR: Record<RiskTier | "unrated", string> = {
  critical: "var(--red-600)",
  high: "var(--amber-600)",
  medium: "var(--ink-500)",
  low: "var(--ink-300)",
  unrated: "var(--ink-200)",
  mechanical: "var(--ink-100)",
};

/** Every tier, most severe first. The one order the page ranks tiers by. */
export const TIER_ORDER: (RiskTier | "unrated")[] = [
  "critical",
  "high",
  "medium",
  "low",
  "unrated",
  "mechanical",
];

/** What each point of the risk rubric means, in the reader's words. */
export const RISK_SCORE_MEANING: Record<RiskScoreLevel, string> = {
  1: "Negligible impact",
  2: "Localized change",
  3: "Shared behavior or compatibility",
  4: "Sensitive or broad impact",
  5: "Critical impact",
};

/** The order a reviewer works down the sections: highest review score first,
 *  the review's own order within a score, then the mechanical section,
 *  sections without a score and obsolete ones. The score is read off the
 *  document, never recalculated, so a document generated before section
 *  scores existed keeps the review's own order. */
export function sectionsInReviewOrder<T extends Pick<Section, "kind" | "derived">>(
  sections: T[],
): T[] {
  if (!sectionsAreScored(sections)) return sections;
  return sections.toSorted((a, b) => reviewRank(a) - reviewRank(b));
}

/** 0 for a score of 5 down to 4 for a score of 1, then the tail groups. */
function reviewRank(section: Pick<Section, "kind" | "derived">): number {
  if (section.derived.state === "stale") return 8;
  if (section.kind === "mechanical") return 6;
  if (section.derived.review_score === undefined) return 7;
  return 5 - section.derived.review_score;
}

export function sectionsAreScored(sections: Pick<Section, "derived">[]): boolean {
  return sections.some((section) => section.derived.review_score !== undefined);
}

export function reviewScoreTone(score: RiskScoreLevel): Tone {
  return score === 5 ? "critical" : score === 4 ? "caution" : "neutral";
}

/** The pull request's own score, drawn larger than a section's: every level
 *  gets a colour so the number is never left looking like plain text. 1–2 read
 *  as calm, 3 as neutral, and 4–5 share the section scale's alarm tones. */
export function riskScoreTone(score: RiskScoreLevel): Tone {
  return score <= 2 ? "positive" : reviewScoreTone(score);
}

export type RiskScoreView =
  | { assessed: false; legacy: boolean; limitations: string[] }
  | {
      assessed: true;
      score: RiskScoreLevel;
      lowerBound: boolean;
      meaning: string;
      reasons: { section: string; sectionTitle: string; text: string }[];
      limitations: string[];
    };

/** The pull request's risk score as the summary shows it. The score is read
 *  off the document, never recalculated here: a document generated before
 *  scores existed has none and reads as not assessed. */
export function riskScoreView(
  document: Pick<ReviewDocument, "summary" | "sections">,
): RiskScoreView {
  const stored = document.summary.risk_score;
  if (!stored) return { assessed: false, legacy: true, limitations: [] };
  const limitations = stored.limitations.map(({ kind, count }) =>
    kind === "unassessed_sections"
      ? `${pluralize("section", count, true)} not assessed`
      : kind === "omitted_source"
        ? `${pluralize("changed file", count, true)} unreadable`
        : `${humanize(kind)} (${count})`,
  );
  if (stored.status === "not_assessed" || stored.score === null) {
    return { assessed: false, legacy: false, limitations };
  }
  const titles = new Map(document.sections.map((section) => [section.id, section.title]));
  return {
    assessed: true,
    score: stored.score,
    lowerBound: stored.status === "lower_bound",
    meaning: RISK_SCORE_MEANING[stored.score],
    reasons: stored.reasons.map((reason) => ({
      section: reason.section,
      sectionTitle: titles.get(reason.section) ?? reason.section,
      text: reason.text,
    })),
    limitations,
  };
}

/** The review's risk score when it assessed one, for the page's head and its
 *  compact bar. Not assessed is null: the Summary says why, and a "not
 *  assessed" mark there would read as a finding. */
export function assessedRisk(
  document: ReviewDocument | null,
): Extract<RiskScoreView, { assessed: true }> | null {
  const view = document ? riskScoreView(document) : null;
  return view?.assessed ? view : null;
}

export type RiskSegment = { tier: RiskTier | "unrated"; lines: number; share: number };

/** Lines by risk tier as proportional segments, in reading order, zero tiers
 *  omitted. `share` is a fraction of all rated lines. */
export function riskSegments(loc: Record<RiskTier | "unrated", number>): RiskSegment[] {
  const total = TIER_ORDER.reduce((sum, tier) => sum + (loc[tier] ?? 0), 0);
  if (total === 0) return [];
  return TIER_ORDER.flatMap((tier) => {
    const lines = loc[tier] ?? 0;
    return lines > 0 ? [{ tier, lines, share: lines / total }] : [];
  });
}

export type SectionFinding = Finding & { tool: string; stale: boolean; assessed_head: string };

/** Findings every agent review raised against one section, oldest review
 *  first, so a reader sees what was said and whether the thread closed. */
export function sectionFindings(document: ReviewDocument, sectionId: string): SectionFinding[] {
  return document.agent_reviews.flatMap((review) =>
    review.findings
      .filter((finding) => finding.section === sectionId)
      .map((finding) => ({
        ...finding,
        tool: review.tool,
        stale: review.stale,
        assessed_head: review.assessed_head,
      })),
  );
}

export type CheckStatus = {
  state: "passing" | "failing" | "pending" | "inconclusive" | "none";
  /** What a reader has to look at, required checks first. */
  failed: IndexedCheck[];
  passed: number;
  pending: number;
  /** Settled without running: skipped and neutral conclusions, which are not
   *  failures and are not passes either. */
  skipped: number;
  total: number;
  /** True when at least one failure is a required check, which is the
   *  difference between "the branch cannot merge" and "something is noisy". */
  blocking: boolean;
};

/** Whether the head under review is green.
 *
 *  Only checks on that head count. A refresh may leave runs from an earlier
 *  revision in the index, and one of those must not paint this one red — nor,
 *  worse, paint it green.
 *
 *  A cancelled check counts as failed rather than pending: it will not finish
 *  on its own, so treating it as still running shows a green light that is
 *  never coming. A head whose checks all skipped is inconclusive rather than
 *  passing, because nothing on it actually succeeded. */
export function checkStatus(document: ReviewDocument): CheckStatus {
  const checks = document.index.checks.filter((check) => check.head === document.review.head);
  // A failing check's link is kept only when it points into this pull
  // request's repository, so the summary never opens someone else's run.
  const failed = checks
    .filter((check) => check.conclusion === "failure" || check.conclusion === "cancelled")
    .toSorted((left, right) => Number(right.required) - Number(left.required))
    .map((check) => ({ ...check, url: repositoryLink(document, check.url) }));
  const pending = checks.filter((check) => check.conclusion === "pending").length;
  const passed = checks.filter((check) => check.conclusion === "success").length;
  const skipped = checks.filter(
    (check) => check.conclusion === "skipped" || check.conclusion === "neutral",
  ).length;
  return {
    state:
      checks.length === 0
        ? "none"
        : failed.length > 0
          ? "failing"
          : pending > 0
            ? "pending"
            : passed > 0
              ? "passing"
              : "inconclusive",
    failed,
    passed,
    pending,
    skipped,
    total: checks.length,
    blocking: failed.some((check) => check.required),
  };
}

export type TriageRisk = Risk & {
  section: string;
  sectionTitle: string;
  sectionState: SectionState;
};

export type RiskTriage = {
  /** The tiers a reviewer must read before anything else. */
  lead: TriageRisk[];
  /** Everything below them, in tier order, for a reader who wants the rest. */
  rest: TriageRisk[];
};

const MUST_READ: RiskTier[] = ["critical", "high"];

/** Every risk in the document, gathered out of the sections and split at the
 *  line a reviewer has to read.
 *
 *  Each risk keeps the state of the section it came from, so a reader can tell
 *  one in new or touched code from one that has stood since the last review.
 *  Nothing is dropped: the lower tiers are held back, not discarded, because a
 *  triage list that silently hides half the risks is not a triage list.
 *  A repeated risk id is {@link normalizeReviewDocument}'s business, at fetch. */
export function triageRisks(document: ReviewDocument): RiskTriage {
  const all = document.sections.flatMap((section) =>
    section.risks
      .filter((risk) => section.derived.state !== "stale" && risk.state !== "mitigated")
      .map((risk) => ({
        ...risk,
        section: section.id,
        sectionTitle: section.title,
        sectionState: sectionBadgeState(section.derived.state, document.review),
      })),
  );
  const byTier = (risks: TriageRisk[]) =>
    risks.toSorted((left, right) => TIER_ORDER.indexOf(left.tier) - TIER_ORDER.indexOf(right.tier));
  return {
    lead: byTier(all.filter((risk) => MUST_READ.includes(risk.tier))),
    rest: byTier(all.filter((risk) => !MUST_READ.includes(risk.tier))),
  };
}

/** Entity kinds that name something outside this change's own code.
 *
 *  The schema carries no audience, so this says which surface moved rather
 *  than who is affected by it — "Endpoint" is a fact the document states,
 *  "API clients" would be a guess dressed as one. */
const SURFACE_KIND: Record<string, string> = {
  endpoint: "Endpoint",
  page: "Page",
  table: "Stored data",
  migration: "Migration",
  event: "Event",
  queue: "Queue",
  config: "Configuration",
  cli: "Command line",
};

export type BehaviourSection = {
  section: string;
  title: string;
  state: SectionState;
  /** The surfaces this section changes, in the order the document names them. */
  surfaces: string[];
  additions: number;
  deletions: number;
  steps: { id: string; text: string }[];
  /** Steps past the per-section cap. */
  more: number;
};

/** What this revision changed about how the system behaves, one block per
 *  section that changed.
 *
 *  A section unchanged since the previous version is deliberately left out —
 *  its behaviour was read last time, and triage is for what has not been. Each
 *  block is capped rather than the list as a whole, so a section with twenty
 *  steps cannot crowd out the one beneath it. */
export function behaviourChanges(document: ReviewDocument, limit = 4): BehaviourSection[] {
  return document.sections
    .filter((section) => section.derived.state === "new" || section.derived.state === "changed")
    .flatMap((section) => {
      const steps = section.blocks.flatMap((block) => {
        if (block.kind !== "behavior") return [];
        const behavior = block as Extract<Block, { kind: "behavior" }>;
        return (behavior.steps ?? []).map((step) => ({ id: step.id, text: step.text }));
      });
      if (steps.length === 0) return [];
      const surfaces: string[] = [];
      for (const block of section.blocks) {
        if (block.kind !== "entities") continue;
        for (const entity of (block as Extract<Block, { kind: "entities" }>).items ?? []) {
          const surface = SURFACE_KIND[entity.kind];
          if (surface && !surfaces.includes(surface)) surfaces.push(surface);
        }
      }
      return [
        {
          section: section.id,
          title: section.title,
          state: sectionBadgeState(section.derived.state, document.review),
          surfaces,
          additions: section.derived.additions,
          deletions: section.derived.deletions,
          steps: steps.slice(0, limit),
          more: Math.max(0, steps.length - limit),
        },
      ];
    });
}

export type ExchangeActor = "review" | "implementation" | "human";

export type ExchangeEvent = {
  id: string;
  actor: ExchangeActor;
  /** Null on an addressed-in entry: the document records which commit answered
   *  a finding, not when that commit landed. */
  at: string | null;
  /** The tool, the session vendor or the login that acted. */
  by: string;
  url?: string;
  verdict?: Verdict;
  /** The head an agent assessed, or the commit that answered a finding. */
  commit?: string;
  blockedOn?: string | null;
  findings: Finding[];
};

/** The back and forth between the tools that reviewed this pull request and
 *  whoever answered them, in one ordered run.
 *
 *  An agent review is dated by the pull request comment it was posted as. The
 *  commit that answered a finding carries no timestamp in the schema, so it
 *  sits directly under the review that raised it rather than claiming a place
 *  on the clock. Human replies on the diff are kept: leaving them out would
 *  show an exchange between agents that in fact had a person in it. */
export function reviewExchange(document: ReviewDocument): ExchangeEvent[] {
  const comment = (id: number) =>
    document.index.comments.find((candidate) => candidate.id === id) ?? null;
  // A review names the comment it was posted as either directly or through the
  // bot report that carries it. Following only the direct anchor leaves such a
  // review undated, which sorts it after every dated reply and shows the
  // reader an order that did not happen.
  const postedAs = (review: AgentReview) => {
    for (const anchor of review.anchors) {
      if (anchor.type === "pr_comment") {
        const found = comment(anchor.id);
        if (found) return found;
      }
      if (anchor.type === "bot_report") {
        const report = document.index.reports.find((candidate) => candidate.id === anchor.id);
        const found = report ? comment(report.comment) : null;
        if (found) return found;
      }
    }
    return null;
  };
  // Undated events sort after every dated one rather than before them.
  const LAST = "\uffff";
  const entries: { at: string; after: number; event: ExchangeEvent }[] = [];
  // One commit can answer findings raised by more than one review, so they are
  // collected per commit across all of them. Keyed per review, the commit is
  // emitted once and every finding but the first review's is dropped, leaving
  // those findings resolved with nothing shown to have resolved them.
  const answering = new Map<string, { at: string; findings: Finding[] }>();

  for (const review of document.agent_reviews) {
    const on = postedAs(review);
    const at = on?.created_at ?? LAST;
    entries.push({
      at,
      after: 0,
      event: {
        id: review.id,
        actor: "review",
        at: on?.created_at ?? null,
        by: review.tool,
        url: on?.url,
        verdict: review.verdict,
        commit: review.assessed_head,
        blockedOn: review.blocked_on,
        findings: review.findings,
      },
    });
    for (const finding of review.findings) {
      if (!finding.addressed_by) continue;
      const already = answering.get(finding.addressed_by);
      if (already) {
        already.findings.push(finding);
        // The commit sits under the *last* review it answers, not the first.
        // Dating it by the first puts it above a review it also answers, and
        // claims it resolved a finding that did not exist yet.
        if (at > already.at) already.at = at;
      } else {
        answering.set(finding.addressed_by, { at, findings: [finding] });
      }
    }
  }

  for (const [commit, answered] of answering) {
    entries.push({
      at: answered.at,
      after: 1,
      event: {
        id: `answered-${commit}`,
        actor: "implementation",
        at: null,
        // Not the linked session's vendor. The document records no
        // commit-to-session relationship, and a pull request can hold one
        // observed session while a person or another agent authors the commit
        // that answers a finding.
        by: "Implementation",
        commit,
        findings: answered.findings,
      },
    });
  }

  // A bot's own post on the pull request is a review round too. The document
  // records agent reviews separately when it knows their findings; a bot
  // comment nothing else claims is still a tool speaking, and a timeline that
  // hid it would show a person answering nobody. Bot replies on the diff are
  // the findings of such a round, not rounds of their own, so they stay out.
  const posted = new Set(
    document.agent_reviews.flatMap((review) =>
      review.anchors.flatMap((anchor) => {
        if (anchor.type === "pr_comment") return [anchor.id];
        if (anchor.type === "bot_report") {
          const report = document.index.reports.find((candidate) => candidate.id === anchor.id);
          return report ? [report.comment] : [];
        }
        return [];
      }),
    ),
  );
  for (const item of document.index.comments) {
    if (item.bot && item.kind !== "review_comment" && !posted.has(item.id)) {
      entries.push({
        at: item.created_at,
        after: 0,
        event: {
          id: `bot-${item.id}`,
          actor: "review",
          at: item.created_at,
          by: item.author,
          url: item.url,
          findings: [],
        },
      });
      continue;
    }
    if (item.bot || item.kind === "issue") continue;
    entries.push({
      at: item.created_at,
      after: 0,
      event: {
        id: `comment-${item.id}`,
        actor: "human",
        at: item.created_at,
        by: item.author,
        url: item.url,
        findings: [],
      },
    });
  }

  return entries
    .map((entry, position) => ({ ...entry, position }))
    .toSorted(
      (left, right) =>
        left.at.localeCompare(right.at) ||
        left.after - right.after ||
        left.position - right.position,
    )
    .map((entry) => entry.event);
}

export type IntentLine = {
  /** The section that argued this intent, so the line can jump to it. */
  section: string;
  title: string;
  text: string;
  anchors: Anchor[];
};

/** The document's "why", read off the intent block each section carries. The
 *  schema states intent per section rather than per document, so the summary
 *  collects them in section order and every line stays attributable to the
 *  section that argued it. */
export function summaryIntent(document: ReviewDocument): IntentLine[] {
  return document.sections.flatMap((section) =>
    section.blocks.flatMap((block) => {
      if (block.kind !== "intent") return [];
      const intent = block as Extract<Block, { kind: "intent" }>;
      if (!intent.text) return [];
      return [
        {
          section: section.id,
          title: section.title,
          text: intent.text,
          anchors: intent.anchors ?? [],
        },
      ];
    }),
  );
}

export type CitedPrompt = {
  id: string;
  session: string;
  vendor: string;
  index: number;
  /** Null when the index does not carry the prompt: a restricted session, or a
   *  document generated with prompts omitted. The number still stands. */
  text: string | null;
};

/** The prompts the intent lines cite, de-duplicated, in session then prompt
 *  order — what a person actually asked for, underneath the model's account of
 *  why. A cited prompt the index does not carry keeps its number and loses its
 *  text, the way an unresolved anchor stays visible rather than vanishing. */
export function intentPrompts(document: ReviewDocument, lines: IntentLine[]): CitedPrompt[] {
  const order = new Map(document.index.sessions.map((session, position) => [session.id, position]));
  const cited = new Map<string, CitedPrompt>();
  for (const line of lines) {
    for (const anchor of line.anchors) {
      if (anchor.type !== "prompt") continue;
      const id = `${anchor.session}:${anchor.index}`;
      if (cited.has(id)) continue;
      const session = document.index.sessions.find((item) => item.id === anchor.session);
      cited.set(id, {
        id,
        session: anchor.session,
        vendor: session?.vendor ?? "Session",
        index: anchor.index,
        text: session?.prompts?.find((prompt) => prompt.index === anchor.index)?.text ?? null,
      });
    }
  }
  return [...cited.values()].toSorted((a, b) => {
    const left = order.get(a.session) ?? Number.MAX_SAFE_INTEGER;
    const right = order.get(b.session) ?? Number.MAX_SAFE_INTEGER;
    return left === right ? a.index - b.index : left - right;
  });
}

export type ReviewExchangeFacts = {
  /** Figures the account cannot state as precisely as a number can. A null is
   *  a figure the document did not carry, which is not the same as a zero. */
  reviews: number | null;
  backAndForths: number | null;
  criticalIssues: number | null;
  changesAfterOpen: { commits: number; additions: number; deletions: number } | null;
  summary: string | null;
  /** Whether the account states figures at all. An older plain-string account
   *  states none, so its nulls are not figures reported as unknown. */
  counted: boolean;
};

/** The generator's own account of the review.
 *
 *  An older document states it as a plain string, which is read as the summary
 *  it was rather than as a missing field: the shape changed, the meaning did
 *  not. */
export function reviewExchangeFacts(document: ReviewDocument): ReviewExchangeFacts | null {
  const stated = document.summary.review_exchange;
  if (!stated) return null;
  if (typeof stated === "string") {
    return {
      reviews: null,
      backAndForths: null,
      criticalIssues: null,
      changesAfterOpen: null,
      summary: stated,
      counted: false,
    };
  }
  const changes = stated.changes_after_open;
  return {
    reviews: stated.reviews ?? null,
    backAndForths: stated.back_and_forths ?? null,
    criticalIssues: stated.critical_issues_found ?? null,
    changesAfterOpen: changes
      ? {
          commits: changes.commits ?? 0,
          additions: changes.lines_added ?? 0,
          deletions: changes.lines_removed ?? 0,
        }
      : null,
    summary: stated.summary ?? null,
    counted: true,
  };
}

export type SectionDecision = Decision & {
  section: string;
  sectionTitle: string;
};

/** Every decision the review records, in section order.
 *
 *  Deliberately not scoped to a session. The schema states decisions per
 *  section with no session field, and their anchors only look like one: a
 *  decision a person prompted cites that prompt, while a decision the agent
 *  took while building cites the code it produced. Filtering on session
 *  anchors therefore keeps the review-driven decisions and drops the
 *  implementation ones — the opposite of a complete list. A change can also
 *  carry commits from an agent whose session was never linked, so even a
 *  complete anchor would not make the attribution sound.
 *
 *  `by` is the distinction that does hold, and it is the one worth drawing. */
export function reviewDecisions(document: ReviewDocument): SectionDecision[] {
  return document.sections.flatMap((section) =>
    section.blocks.flatMap((block) => {
      if (block.kind !== "decisions") return [];
      const decisions = block as Extract<Block, { kind: "decisions" }>;
      return (decisions.items ?? []).map((item) => ({
        ...item,
        section: section.id,
        sectionTitle: section.title,
      }));
    }),
  );
}

export type ImplementsGroup = {
  /** The `type` the document stated. */
  kind: string;
  /** What the entries hang off, ready to show: the tracker an issue lives in,
   *  the page a set of assertions is on, or the type's own name. */
  label: string;
  /** The identifiers under that heading. Empty when the document named the
   *  container without naming anything in it. */
  items: string[];
};

function addImplements(target: ImplementsGroup, value: string | undefined) {
  if (value && !target.items.includes(value)) target.items.push(value);
}

/** `implements` grouped by what it points at, in the order the document states
 *  it — one row per issue tracker, one per specification page — so four
 *  assertions on one page read as one page rather than four unrelated chips.
 *
 *  Nothing here knows what a tracker or a page is called: every heading is the
 *  document's own `system` or `page` value. A type this renderer does not know
 *  is grouped under its own name and shows whatever identifiers it carries,
 *  rather than being read as one of the types it is not. */
export function implementsGroups(items: Implements[]): ImplementsGroup[] {
  const groups: ImplementsGroup[] = [];
  const group = (kind: string, label: string) => {
    const found = groups.find((item) => item.kind === kind && item.label === label);
    if (found) return found;
    const created: ImplementsGroup = { kind, label, items: [] };
    groups.push(created);
    return created;
  };

  for (const item of items) {
    if (item.type === "issue") {
      const issue = item as Extract<Implements, { type: "issue" }>;
      addImplements(group("issue", humanize(issue.system)), issue.key);
    } else if (item.type === "spec") {
      const spec = item as Extract<Implements, { type: "spec" }>;
      addImplements(group("spec", spec.page), spec.assertion);
    } else {
      const target = group(item.type, humanize(item.type));
      for (const [field, value] of Object.entries(item as Record<string, unknown>)) {
        if (field !== "type" && typeof value === "string") addImplements(target, value);
      }
    }
  }
  return groups;
}

export type HunkGroup = {
  path: string;
  hunks: IndexedHunk[];
  additions: number;
  deletions: number;
};

/** A section's hunks resolved and grouped by file in index order. Hunk IDs the
 *  index does not know are dropped: a missing target is the index's fault, not
 *  a change to hide behind a broken row. */
export function sectionHunks(document: ReviewDocument, hunkIds: string[]): HunkGroup[] {
  const wanted = new Set(hunkIds);
  const groups = new Map<string, HunkGroup>();
  for (const hunk of document.index.hunks) {
    if (!wanted.has(hunk.id)) continue;
    const group = groups.get(hunk.path) ?? {
      path: hunk.path,
      hunks: [],
      additions: 0,
      deletions: 0,
    };
    group.hunks.push(hunk);
    group.additions += hunk.additions;
    group.deletions += hunk.deletions;
    groups.set(hunk.path, group);
  }
  return [...groups.values()];
}

export type ResolvedAnchor = {
  label: string;
  href?: string;
  /** Longer form for a tooltip, when the label had to be short. */
  title?: string;
  /** Where the anchor points, in words, for readers who cannot see the chip. */
  kind: string;
};

/** The GitHub page for a pull request, which is where every anchor without a
 *  more specific URL lands. */
export function pullRequestUrl(document: ReviewDocument): string {
  return `https://github.com/${document.review.repository}/pull/${document.review.pull_request}`;
}

/** GitHub's global upload namespace, `github.com/user-attachments/assets/<id>`
 *  and `…/files/<id>/<name>`: the images and files pasted into this pull
 *  request's description and comments. It names no repository, so it is kept. */
const GITHUB_UPLOADS = new Set(["assets", "files"]);

/** A link the review supplies — for this pull request's comments, threads,
 *  checks and code, or an external link, attachment or media block. A GitHub
 *  link is kept only when it points into this pull request's repository: one
 *  into another repository (sample data carries `acme/widgets` placeholders)
 *  would open the wrong page, so it is dropped and the item reads as text
 *  instead. GitHub uploads (`GITHUB_UPLOADS`) belong to no repository and are
 *  kept. Other http(s) links are left alone; any other scheme is dropped. */
export function repositoryLink(
  document: ReviewDocument,
  url: string | undefined,
): string | undefined {
  if (!url) return undefined;
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return undefined;
  if (parsed.hostname !== "github.com") return url;
  const [owner, repository, ...rest] = parsed.pathname.split("/").filter(Boolean);
  if (owner === "user-attachments" && GITHUB_UPLOADS.has(repository ?? "") && rest.length > 0) {
    return url;
  }
  return `${owner}/${repository}`.toLowerCase() === document.review.repository.toLowerCase()
    ? url
    : undefined;
}

const GITHUB_DIFF_ANCHOR = /^#diff-[0-9a-f]{64}(?:[LR]\d+(?:-[LR]\d+)?)?$/;

/** Where a hunk opens on GitHub. A supplied link is used when it is a real
 *  "Files changed" anchor on this pull request; otherwise — another
 *  repository, or an anchor GitHub does not know, such as `#h_1a2b…` — it is
 *  rebuilt the way GitHub names it, `#diff-<sha256(path)>R<first line>`, so
 *  the link lands on the change rather than at the top of the page. */
export function hunkUrl(document: ReviewDocument, hunk: IndexedHunk): string {
  const files = `${pullRequestUrl(document)}/files`;
  const supplied = repositoryLink(document, hunk.github_url);
  if (supplied) {
    const parsed = new URL(supplied);
    if (
      parsed.hostname !== "github.com" ||
      (parsed.pathname.replace(/\/$/, "") === new URL(files).pathname &&
        GITHUB_DIFF_ANCHOR.test(parsed.hash))
    ) {
      return supplied;
    }
  }
  const line = hunk.change !== "removed" && hunk.new_range[0] > 0 ? `R${hunk.new_range[0]}` : "";
  return `${files}#diff-${sha256Hex(hunk.path)}${line}`;
}

/** The line a hunk opens at in the file view: its first new line, or none for
 *  a removed hunk, which has no new side to land on. */
export function hunkLine(hunk: IndexedHunk): number | undefined {
  return hunk.change !== "removed" && hunk.new_range[0] > 0 ? hunk.new_range[0] : undefined;
}

/** The file of the change an anchor points into, and the line when it names
 *  one, so the link can open the review's file view. Null for anything else. */
export function anchorFile(
  document: ReviewDocument,
  anchor: Anchor,
): { path: string; line?: number } | null {
  if (anchor.type === "file") return { path: anchor.path };
  if (anchor.type !== "hunk") return null;
  const hunk = document.index.hunks.find((candidate) => candidate.id === anchor.id);
  if (!hunk) return null;
  return { path: hunk.path, line: anchor.lines?.[0] ?? hunkLine(hunk) };
}

/** Resolves an anchor against the document index into something a reader can
 *  click or at least read. Unresolved targets keep their identifier rather than
 *  vanishing, matching the lifecycle rule that an unresolved anchor flags the
 *  item instead of removing it. */
export function resolveAnchor(document: ReviewDocument, anchor: Anchor): ResolvedAnchor {
  const { index } = document;
  const repository = `https://github.com/${document.review.repository}`;
  switch (anchor.type) {
    case "hunk": {
      const hunk = index.hunks.find((candidate) => candidate.id === anchor.id);
      if (!hunk) return { label: `Hunk ${anchor.id}`, kind: "Unresolved hunk" };
      const lines = anchor.lines ? ` · lines ${anchor.lines[0]}–${anchor.lines[1]}` : "";
      return {
        label: `${hunk.path}${lines}`,
        href: hunkUrl(document, hunk),
        title: `${humanize(hunk.change)} ${hunk.role} hunk, ${formatLines(hunk.additions, hunk.deletions)}`,
        kind: "Diff hunk",
      };
    }
    case "file":
      return { label: anchor.path, kind: "File" };
    case "commit":
      return {
        label: shortSha(anchor.sha),
        href: `${repository}/commit/${anchor.sha}`,
        kind: "Commit",
      };
    case "pr": {
      const pr = index.prs.find((candidate) => candidate.number === anchor.number);
      return {
        label: `#${anchor.number}`,
        href: repositoryLink(document, pr?.url) ?? `${repository}/pull/${anchor.number}`,
        title: pr?.title,
        kind: "Pull request",
      };
    }
    case "pr_body":
      return {
        label: anchor.section ? `Description § ${anchor.section}` : "Description",
        href: pullRequestUrl(document),
        kind: "Pull request description",
      };
    case "pr_comment": {
      const comment = index.comments.find((candidate) => candidate.id === anchor.id);
      if (!comment) return { label: `Comment ${anchor.id}`, kind: "Unresolved comment" };
      const kind = comment.kind === "review" ? "review" : "comment";
      return {
        label: `${comment.author} ${kind}`,
        href: repositoryLink(document, comment.url),
        title: comment.bot ? "Bot comment" : undefined,
        kind: "Pull request comment",
      };
    }
    case "review_thread": {
      const thread = index.threads.find((candidate) => candidate.id === anchor.id);
      if (!thread) return { label: `Thread ${anchor.id}`, kind: "Unresolved review thread" };
      const state = thread.resolved ? " · resolved" : thread.outdated ? " · outdated" : "";
      const line = thread.line ? `:${thread.line}` : "";
      return {
        label: thread.path ? `Thread on ${thread.path}${line}${state}` : `Review thread${state}`,
        href: repositoryLink(document, thread.url),
        kind: "Review thread",
      };
    }
    case "prompt": {
      const session = index.sessions.find((candidate) => candidate.id === anchor.session);
      const prompt = session?.prompts?.find((candidate) => candidate.index === anchor.index);
      return {
        label: `Prompt ${anchor.index}`,
        title: prompt?.text,
        kind: session ? `${session.vendor} session prompt` : "Session prompt",
      };
    }
    case "session_event": {
      const session = index.sessions.find((candidate) => candidate.id === anchor.session);
      const event = session?.events?.find((candidate) => candidate.id === anchor.event);
      return {
        label: event ? event.summary : `Session event ${anchor.event}`,
        title: event ? `${humanize(event.verb)} at ${event.at}` : undefined,
        kind: session ? `${session.vendor} session event` : "Session event",
      };
    }
    case "check": {
      const check = index.checks.find((candidate) => candidate.id === anchor.id);
      if (!check) return { label: `Check ${anchor.id}`, kind: "Unresolved check" };
      const test = anchor.test ? ` › ${anchor.test}` : "";
      return {
        label: `${check.name}${test}`,
        href: repositoryLink(document, check.url),
        title: `${humanize(check.conclusion)}${check.required ? ", required" : ""}`,
        kind: "Check",
      };
    }
    case "bot_report": {
      const report = index.reports.find((candidate) => candidate.id === anchor.id);
      if (!report) return { label: `Report ${anchor.id}`, kind: "Unresolved bot report" };
      return {
        label: `${report.tool} report`,
        href: repositoryLink(document, report.url),
        title: report.summary,
        kind: "Bot report",
      };
    }
    case "issue":
      return { label: anchor.key, title: `${humanize(anchor.system)} issue`, kind: "Issue" };
    case "spec":
      return {
        label: anchor.assertion ?? anchor.page,
        title: anchor.assertion ? `Assertion on ${anchor.page}` : "Specification page",
        kind: "Specification",
      };
    case "external_url":
      return {
        label: anchor.label,
        href: repositoryLink(document, anchor.url),
        kind: "External link",
      };
    case "attachment":
      return {
        label: "Attachment",
        href: repositoryLink(document, anchor.url),
        kind: "Attachment",
      };
    default: {
      const unknown = anchor as { type: string };
      return { label: humanize(unknown.type), kind: "Unsupported anchor" };
    }
  }
}

/** What loading one version of a document came to. The host's loader
 *  answers `missing` for a version it has no copy of and `failed` when it
 *  could not tell. */
export type ReviewVersionState =
  | { status: "loading" }
  | { status: "ready"; document: ReviewDocument }
  | { status: "missing" }
  | { status: "failed" };

/** The host's loader for one earlier version of the document being read. */
export type LoadReviewVersion = (version: number) => Promise<ReviewVersionState>;

/** One entry of a document's version list, newest first. */
export type ReviewVersionSummary = {
  version: number;
  head: string;
  base: string;
  generated_at: string;
  current: boolean;
};

/** Drops a risk repeated by id within its own section. A risk id is only a
 *  promise within its own section, not a promise that a generated document
 *  lists it once — done here, at the one place every document is parsed, so
 *  a repeated entry cannot inflate one reader's count while another reader of
 *  the same `section.risks` renders it twice.
 *
 *  A section stored with a blank title is labelled by its 1-based position,
 *  "Section N", as a host labels it for discussion and approval. */
export function normalizeReviewDocument(document: ReviewDocument): ReviewDocument {
  return {
    ...document,
    sections: document.sections.map((section, index) => {
      // Scoped per section, so the id alone is the key: the same id in two
      // sections is two risks, and that pairing is the row key's business.
      const seen = new Set<string>();
      const risks = section.risks.filter((risk) => {
        if (seen.has(risk.id)) return false;
        seen.add(risk.id);
        return true;
      });
      const title = section.title.trim() === "" ? `Section ${index + 1}` : section.title;
      return { ...section, title, risks };
    }),
  };
}
