import pluralize from "pluralize";

import { formatBucket, formatCount, formatDate } from "./components/format";

/** The decision log's view models and the pure decisions its components make
 *  about them: how a person, who decided, a feature's status, span and counts
 *  and a pull request's review read, and the log's filters, as the address
 *  carries them and as the API query they send. They are the fields the hosted
 *  API answers with; a host fills them from wherever its log comes from. */

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
  /** A later reading of the session replaced the decision, which still
   *  opens. Only the local log sends it. */
  replaced?: boolean;
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

/** A file or folder the log's decisions cite, with how many cite it, as the
 *  file filter suggests it. */
export type DecisionFile = { path: string; kind: "file" | "folder"; decision_count: number };

export type DecisionPersonCount = DecisionPerson & { decision_count: number };

/** A person of the features, with how many features they are in. */
export type FeaturePersonCount = DecisionPerson & { feature_count: number };

/** An author the person filter offers: by decisions on All decisions, by
 *  features on Features. */
export type AuthorCount = DecisionPersonCount | FeaturePersonCount;

export const WHO_OPTIONS: readonly { value: DecisionWho; label: string }[] = [
  { value: "human", label: "Human" },
  { value: "agent_proposed_human_approved", label: "Approved" },
  { value: "agent", label: "Agent" },
];

/** Everything the address bar carries for the log's filters. Dates are local
 *  calendar days, `YYYY-MM-DD`, both ends included. */
export type DecisionFilters = {
  q: string;
  file: string;
  person: string;
  who: DecisionWho[];
  categories: DecisionCategory[];
  from: string | null;
  to: string | null;
  superseded: boolean;
  hideAgent: boolean;
};

export const NO_FILTERS: DecisionFilters = {
  q: "",
  file: "",
  person: "",
  who: [],
  categories: [],
  from: null,
  to: null,
  superseded: false,
  hideAgent: false,
};

const FILTER_KEYS = [
  "q",
  "file",
  "person",
  "who",
  "category",
  "from",
  "to",
  "superseded",
  "agent",
] as const;

const DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

/** A real calendar day, so `2026-02-30` is dropped rather than sent on for the
 *  API to refuse the whole page over. */
function validDay(value: string | null): string | null {
  const match = value ? DAY.exec(value) : null;
  if (!match) return null;
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const at = new Date(year, month - 1, day);
  return at.getFullYear() === year && at.getMonth() === month - 1 && at.getDate() === day
    ? value
    : null;
}

/** Known values in their canonical order, each once. A stale or hand-edited
 *  value is dropped: the API answers an unknown one with a 400. */
function known<T extends string>(values: string[], allowed: readonly T[]): T[] {
  return allowed.filter((value) => values.includes(value));
}

export function parseDecisionFilters(search: URLSearchParams): DecisionFilters {
  return exclusive({
    q: search.get("q")?.trim() ?? "",
    file: search.get("file")?.trim() ?? "",
    person: search.get("person")?.trim() ?? "",
    who: known(
      search.getAll("who"),
      WHO_OPTIONS.map((option) => option.value),
    ),
    categories: known(search.getAll("category"), CATEGORIES),
    from: validDay(search.get("from")),
    to: validDay(search.get("to")),
    superseded: search.get("superseded") === "1",
    hideAgent: search.get("agent") === "hide",
  });
}

/** Hiding agent-only decisions and asking for them cannot both hold. Hiding
 *  wins, whichever came first in the address, so `who=agent&agent=hide` reads
 *  as hiding them. */
function exclusive(filters: DecisionFilters): DecisionFilters {
  return filters.hideAgent
    ? { ...filters, who: filters.who.filter((value) => value !== "agent") }
    : filters;
}

/** `search` with the filter params rewritten from `filters`, everything else
 *  (the open decision) kept. Defaults are left off, so the plain address stays
 *  plain. */
export function withDecisionFilters(
  search: URLSearchParams,
  changes: Partial<DecisionFilters>,
): URLSearchParams {
  const filters = exclusive({ ...parseDecisionFilters(search), ...changes });
  const next = new URLSearchParams(search);
  for (const key of FILTER_KEYS) next.delete(key);
  if (filters.q.trim()) next.set("q", filters.q.trim());
  if (filters.file.trim()) next.set("file", filters.file.trim());
  if (filters.person) next.set("person", filters.person);
  for (const who of known(
    filters.who,
    WHO_OPTIONS.map((option) => option.value),
  ))
    next.append("who", who);
  for (const category of known(filters.categories, CATEGORIES)) next.append("category", category);
  const from = validDay(filters.from);
  const to = validDay(filters.to);
  if (from) next.set("from", from);
  if (to) next.set("to", to);
  if (filters.superseded) next.set("superseded", "1");
  if (filters.hideAgent) next.set("agent", "hide");
  return next;
}

/** The filters the tab shows off; the tab, anything opened and the other
 *  tab's filters stay. On Features those that pick features and the status
 *  are cleared; on All decisions every decision filter, but not the status. */
export function clearDecisionFilters(search: URLSearchParams, features = false): URLSearchParams {
  const next = withDecisionFilters(
    search,
    features ? { q: "", file: "", person: "", from: null, to: null } : NO_FILTERS,
  );
  if (features) next.delete("status");
  return next;
}

/** The feature statuses the address asks for (`status`, repeatable), known
 *  ones only, each once. Only the Features tab applies them. */
export function parseFeatureStatuses(search: URLSearchParams): FeatureStatus[] {
  return known(
    search.getAll("status"),
    FEATURE_STATUSES.map((option) => option.value),
  );
}

export function withFeatureStatuses(
  search: URLSearchParams,
  statuses: FeatureStatus[],
): URLSearchParams {
  const next = new URLSearchParams(search);
  next.delete("status");
  for (const status of known(
    statuses,
    FEATURE_STATUSES.map((option) => option.value),
  ))
    next.append("status", status);
  return next;
}

/** The filters that pick decisions alone: the Features tab neither offers
 *  nor sends them, and they stay in the address for All decisions. */
function withoutDecisionOnly(filters: DecisionFilters): DecisionFilters {
  return { ...filters, who: [], categories: [], superseded: false, hideAgent: false };
}

/** The feature list's API query: the filters that pick features (the search
 *  text, through a feature's own text and pull requests, the file and author,
 *  through its pull requests and sessions, and the dates, through its
 *  activity) and its statuses. A feature's own decisions are never
 *  filtered. */
export function featureQuery(
  filters: DecisionFilters,
  statuses: FeatureStatus[],
  cursor?: string | null,
): URLSearchParams {
  const query = decisionQuery(withoutDecisionOnly(filters));
  for (const status of statuses) query.append("status", status);
  if (cursor) query.set("cursor", cursor);
  return query;
}

/** Whether the Features tab's list is filtered by more than its statuses:
 *  a search, a file, an author or dates. */
export function hasFeatureFilters(filters: DecisionFilters): boolean {
  return (
    filters.q !== "" ||
    filters.file !== "" ||
    filters.person !== "" ||
    filters.from !== null ||
    filters.to !== null
  );
}

export function hasDecisionFilters(filters: DecisionFilters): boolean {
  return withDecisionFilters(new URLSearchParams(), filters).toString() !== "";
}

/** The instant a local calendar day begins, as RFC 3339 with the reader's own
 *  offset. Read from the Date rather than assumed to be midnight, because a
 *  day that begins with a daylight-saving jump begins at 01:00. */
export function localDayStart(day: string, addDays = 0): string {
  const [year, month, date] = day.split("-").map(Number) as [number, number, number];
  const at = new Date(year, month - 1, date + addDays);
  const offset = -at.getTimezoneOffset();
  const sign = offset >= 0 ? "+" : "-";
  const minutes = Math.abs(offset);
  return `${pad(at.getFullYear(), 4)}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}:00${sign}${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`;
}

/** The API query for `filters`. `to` is the end of that day, so the API's
 *  exclusive bound is the start of the next one. Hiding agent-only decisions
 *  is a `who` the server applies, so every page it sends is shown whole. */
export function decisionQuery(filters: DecisionFilters, cursor?: string | null): URLSearchParams {
  const query = new URLSearchParams();
  if (filters.q) query.set("q", filters.q);
  if (filters.file) query.set("file", filters.file);
  if (filters.person) query.set("person", filters.person);
  const who = exclusive(filters).who;
  const shown =
    filters.hideAgent && who.length === 0
      ? WHO_OPTIONS.map((option) => option.value).filter((value) => value !== "agent")
      : who;
  for (const value of shown) query.append("who", value);
  for (const category of filters.categories) query.append("category", category);
  if (filters.from) query.set("from", localDayStart(filters.from));
  if (filters.to) query.set("to", localDayStart(filters.to, 1));
  if (filters.superseded) query.set("superseded", "true");
  if (cursor) query.set("cursor", cursor);
  return query;
}

/** The two views of a log with features: its features, the default, and every
 *  decision on one timeline (`tab=all`). A log without features has only the
 *  timeline, whatever the address says. */
export type LogTab = "features" | "all";

export function parseLogTab(search: URLSearchParams): LogTab {
  return search.get("tab") === "all" ? "all" : "features";
}

/** Another tab, with the same filters. What is opened belongs to the tab it
 *  was opened in, so it is closed. */
export function withLogTab(search: URLSearchParams, tab: LogTab): URLSearchParams {
  const next = new URLSearchParams(search);
  if (tab === "all") next.set("tab", "all");
  else next.delete("tab");
  next.delete("feature");
  next.delete("open");
  return next;
}

/** A cursor-paged list in the order the server sent it: ranked by a search
 *  (`relevance`) or newest first (`newest`, or unsaid by an older API). The
 *  decision log's two lists page the same way. */
export type ListOrder = "relevance" | "newest" | undefined;

/** Set when a later page came in another order, as when the search index
 *  stopped answering mid-way through a ranked list and the server sent the
 *  first page newest first: `restarted` when that page replaced the list,
 *  `stopped` when it was empty, so the rows already loaded stay and no more
 *  can be loaded. */
export type Reordered = false | "restarted" | "stopped";

/** What the end of a list that changed order says, the same for both lists;
 *  null while it has not. */
export function reorderedNote(reordered: Reordered): string | null {
  if (reordered === "stopped")
    return "Search ranking is unavailable right now, so no more matches can be loaded.";
  if (reordered === "restarted")
    return "Search ranking is unavailable right now; showing the newest matches.";
  return null;
}

export type DecisionDay = { day: string; decisions: DecisionSummary[] };

/** Rows under a header per local day, in the order they came: the API sends
 *  them newest first, so the days come out newest first too. */
export function groupByDay(decisions: DecisionSummary[]): DecisionDay[] {
  const days: DecisionDay[] = [];
  for (const decision of decisions) {
    const day = localDayKey(decision.decided_at);
    const last = days.at(-1);
    if (last?.day === day) last.decisions.push(decision);
    else days.push({ day, decisions: [decision] });
  }
  return days;
}

export type DecisionSection = {
  key: string;
  heading: string;
  /** Rows say their day themselves when the section is not one day. */
  dated: boolean;
  decisions: DecisionSummary[];
};

/** How the list reads: a ranked search as one list in the server's order,
 *  best match first, since regrouping it by day would undo the ranking;
 *  anything else under a header per day. */
export function decisionSections(
  decisions: DecisionSummary[],
  order: ListOrder,
): DecisionSection[] {
  if (order === "relevance")
    return decisions.length > 0
      ? [{ key: "relevance", heading: "Best matches first", dated: true, decisions }]
      : [];
  return groupByDay(decisions).map((day) => ({
    key: day.day,
    heading: formatDate(day.decisions[0]!.decided_at),
    dated: false,
    decisions: day.decisions,
  }));
}

/** The local month and day a feature was last active, as ISO times at their
 *  start, for the timeline's headings; null for a feature with no activity. */
export function featureMonth(feature: FeatureSummary | undefined): string | null {
  const at = lastActive(feature);
  return at ? new Date(at.getFullYear(), at.getMonth(), 1).toISOString() : null;
}

export function featureDay(feature: FeatureSummary | undefined): string | null {
  const at = lastActive(feature);
  return at ? new Date(at.getFullYear(), at.getMonth(), at.getDate()).toISOString() : null;
}

/** The month and day headings above each feature, in the list's order: a
 *  heading starts where the feature's month or day differs from the last
 *  feature that had activity. A feature with none takes no heading. */
export function featureHeadings(
  features: FeatureSummary[],
): { month: string | null; day: string | null }[] {
  let previous: FeatureSummary | undefined;
  return features.map((feature) => {
    const month = featureMonth(feature);
    const day = featureDay(feature);
    const heading = {
      month: month !== null && month !== featureMonth(previous) ? month : null,
      day: day !== null && day !== featureDay(previous) ? day : null,
    };
    if (day !== null) previous = feature;
    return heading;
  });
}

function lastActive(feature: FeatureSummary | undefined): Date | null {
  const at = feature?.last_activity_at ?? feature?.first_activity_at;
  return at ? new Date(at) : null;
}
