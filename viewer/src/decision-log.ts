import pluralize from "pluralize";

import { formatBucket, formatCount, formatDate } from "./components/format";

/** The decision log's view models and the pure decisions its components make
 *  about them: how a person, who decided, a feature's status, span and counts
 *  and a pull request's review read. They are the fields the hosted API
 *  answers with; a host fills them from wherever its log comes from. */

export type DecisionWho = "human" | "agent" | "agent_proposed_human_approved";

export type DecisionCategory =
  | "architecture"
  | "data_model"
  | "interface"
  | "behavior"
  | "implementation"
  | "dependency"
  | "testing"
  | "scope"
  | "process";

/** Who a session belongs to. `name`, `login` and `avatar_url` are null when the
 *  email matches nobody the platform knows. */
export type DecisionPerson = {
  email: string;
  name: string | null;
  login: string | null;
  avatar_url: string | null;
};

/** The repository a row is listed under. Missing from an older API. */
export type DecisionRepository = { id: string; full_name: string };

export type DecisionSummary = {
  /** Of a log over several repositories, the one the session joined first
   *  among them. */
  repository?: DecisionRepository;
  /** The decision's stable identity. */
  id: string;
  session_id: string;
  /** The first cited turn. */
  turn: number;
  decided_at: string;
  who: DecisionWho;
  person: DecisionPerson | null;
  vendor: string;
  category: DecisionCategory;
  summary: string;
  superseded: boolean;
  file_count: number;
};

export type DecisionSession = {
  id: string;
  person: DecisionPerson | null;
  vendor: string;
  branch: string;
  started_at: string | null;
  ended_at: string | null;
};

export type DecisionPullRequest = {
  number: number;
  title: string;
  state: "open" | "closed";
  draft: boolean;
  merged: boolean;
  review_state: "none" | "pending" | "current" | "outdated" | "failed";
};

export type DecisionDetail = {
  /** The repository the session is listed under, whose pull requests these
   *  are. */
  repository?: DecisionRepository;
  session: DecisionSession;
  decisions: {
    id: string;
    turn: number;
    turns: number[];
    decided_at: string;
    who: DecisionWho;
    category: DecisionCategory;
    summary: string;
    rejected_alternative: string | null;
    superseded: boolean;
    files: string[];
  }[];
  /** The cited prompts. Empty, with `prompts_withheld`, when the reader
   *  cannot read every repository the session belongs to. */
  turns: { ordinal: number; triggered_at: string | null; text: string }[];
  prompts_withheld: boolean;
  pull_requests: DecisionPullRequest[];
};

export type FeatureStatus = "in_progress" | "shipped" | "abandoned";

/** A decision a feature's text names, with what its chips need. */
export type KeyDecision = {
  id: string;
  session_id: string;
  turn: number;
  who: DecisionWho;
  category: DecisionCategory;
  summary: string;
};

/** A feature of the list. `title` and `description` are null while its
 *  summary is pending (`summary_pending`) or not generated yet. */
export type FeatureSummary = {
  /** The repository the feature belongs to. */
  repository?: DecisionRepository;
  id: string;
  title: string | null;
  description: string | null;
  status: FeatureStatus;
  first_activity_at: string | null;
  last_activity_at: string | null;
  summary_pending: boolean;
  people: DecisionPerson[];
  pull_request_count: number;
  session_count: number;
  decision_count: number;
  key_decisions: KeyDecision[];
};

export type FeaturePullRequest = DecisionPullRequest & {
  /** The current review's summary, null without one. */
  summary: string | null;
  decisions: DecisionSummary[];
};

export type FeatureSession = DecisionSession & { decisions: DecisionSummary[] };

export type FeatureDetail = {
  feature: FeatureSummary & { digest: string | null };
  /** Newest first, each with the decisions attributed to it. */
  pull_requests: FeaturePullRequest[];
  /** The feature's sessions that have no pull request. */
  sessions: FeatureSession[];
};
export const CATEGORIES: readonly DecisionCategory[] = [
  "architecture",
  "data_model",
  "interface",
  "behavior",
  "implementation",
  "dependency",
  "testing",
  "scope",
  "process",
];

export const FEATURE_STATUSES: readonly { value: FeatureStatus; label: string }[] = [
  { value: "in_progress", label: "In progress" },
  { value: "shipped", label: "Shipped" },
  { value: "abandoned", label: "Abandoned" },
];

export function featureStatusLabel(status: FeatureStatus): string {
  return FEATURE_STATUSES.find((option) => option.value === status)?.label ?? status;
}

export function featureStatusTone(status: FeatureStatus): "info" | "positive" | "neutral" {
  return status === "in_progress" ? "info" : status === "shipped" ? "positive" : "neutral";
}

/** "Oct 1 – Oct 6, 2026", or one day when it began and ended on it. */
export function activitySpan(first: string | null, last: string | null): string | null {
  if (!first && !last) return null;
  if (!first || !last || localDayKey(first) === localDayKey(last)) {
    return formatDate(last ?? first);
  }
  const sameYear = new Date(first).getFullYear() === new Date(last).getFullYear();
  return `${sameYear ? formatBucket(first, "day") : formatDate(first)} – ${formatDate(last)}`;
}

/** "2 PRs · 3 sessions · 8 decisions", zero counts said too: a feature with
 *  no decisions yet is worth knowing about. */
export function featureCounts(
  feature: Pick<FeatureSummary, "pull_request_count" | "session_count" | "decision_count">,
): string {
  return [
    // pluralize reads "PR" as an all-caps word and writes "PRS".
    `${formatCount(feature.pull_request_count)} ${feature.pull_request_count === 1 ? "PR" : "PRs"}`,
    `${formatCount(feature.session_count)} ${pluralize("session", feature.session_count)}`,
    `${formatCount(feature.decision_count)} ${pluralize("decision", feature.decision_count)}`,
  ].join(" · ");
}

/** Where in a feature's drill-down a decision sits: under a pull request (by
 *  number) or a session of its own (by id). Null when the feature lists it in
 *  neither. */
export function locateDecision(
  detail: Pick<FeatureDetail, "pull_requests" | "sessions">,
  id: string,
): { group: string; decision: DecisionSummary } | null {
  const groups = [
    ...detail.pull_requests.map((pull) => ({
      group: pullGroup(pull.number),
      decisions: pull.decisions,
    })),
    ...detail.sessions.map((session) => ({
      group: sessionGroup(session.id),
      decisions: session.decisions,
    })),
  ];
  for (const { group, decisions } of groups) {
    const decision = decisions.find((each) => each.id === id);
    if (decision) return { group, decision };
  }
  return null;
}

export function pullGroup(number: number): string {
  return `pull:${number}`;
}

export function sessionGroup(id: string): string {
  return `session:${id}`;
}

function pad(value: number, width = 2): string {
  return String(value).padStart(width, "0");
}

/** The local calendar day an instant falls on, `YYYY-MM-DD`. */
export function localDayKey(iso: string): string {
  const at = new Date(iso);
  return `${pad(at.getFullYear(), 4)}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}`;
}

/** A person as the log names them: their name, else their login, else the
 *  address their session was recorded under. */
export function personLabel(person: DecisionPerson): string {
  return person.name || person.login || person.email;
}

export type WhoPresentation = {
  /** The review viewer's marker for who made it, as `DecisionByBadge` takes
   *  it, so a decision reads the same here as in a review. */
  marker: { by: "human" | "agent"; approved_by_human?: true };
  /** Human decisions stand out, agent-only ones recede. */
  weight: "strong" | "normal" | "quiet";
  /** Said in words for an approved proposal, never by colour alone. */
  approvedBy: string | null;
};

export function whoPresentation(who: DecisionWho, person: DecisionPerson | null): WhoPresentation {
  if (who === "human") return { marker: { by: "human" }, weight: "strong", approvedBy: null };
  if (who === "agent_proposed_human_approved")
    return {
      marker: { by: "agent", approved_by_human: true },
      weight: "normal",
      approvedBy: person ? personLabel(person) : "a person",
    };
  return { marker: { by: "agent" }, weight: "quiet", approvedBy: null };
}

const REVIEW_STATES: Record<DecisionDetail["pull_requests"][number]["review_state"], string> = {
  none: "No review yet",
  pending: "Review pending",
  current: "Review current",
  outdated: "Review outdated",
  failed: "Review failed",
};

export function reviewStateLabel(state: string): string {
  return REVIEW_STATES[state as keyof typeof REVIEW_STATES] ?? state;
}
