import { formatUSD } from "@baum/viewer/components/format";
import type { ReviewDocument } from "@baum/viewer/review-document";
export type Clone = {
  id: string;
  path: string;
  repository: string;
  available: boolean;
  settings: {
    /** null follows the global automatic generation preference. */
    automatic: boolean | null;
    daily_cap: number | null;
    backend: string | null;
    model: string | null;
  };
};
export type Repository = { repository: string; settings: Clone["settings"]; clones: Clone[] };
export class LocalRequestError extends Error {
  code?: string;
  constructor(message: string, code?: string) {
    super(message);
    this.code = code;
  }
}
export type GitHubState = {
  review: "" | "approved" | "changes_requested" | "commented" | "unreviewed";
  checks: "" | "passing" | "failing" | "running" | "none";
  merge: "" | "ready" | "conflict" | "blocked" | "draft";
  unresolved: number | null;
  unresolved_capped: boolean;
};
export type PR = GitHubState & {
  repository: string;
  number: number;
  head_sha: string;
  title: string;
  author?: string;
  url: string;
  state: string;
  draft: boolean;
  fork: boolean;
  authored_by_me: boolean;
  review_requested_from_me: boolean;
  author_has_write: boolean | null;
  team_membership_known: boolean;
};
export type Entry = GitHubState & {
  review_minutes: number | null;
  pull_request: PR;
  clone_id?: string;
  /** The repository has no available clone: listed from discovery, and a review waits for one. */
  needs_clone?: boolean;
  status: string;
  version_count: number;
  latest: {
    version: number;
    generated_at: string;
    backend: string;
    model: string;
    recorded_cost_usd: number | null;
  } | null;
  risk_score: { score: number | null; status: string } | null;
  historical: boolean;
  stale: boolean;
  failure_reason?: string;
  store_error?: string;
};
export type Usage = {
  Input: number;
  Output: number;
  CacheRead: number;
  CacheCreate: number;
  CacheCreate1h: number | null;
  Speed: string | null;
  MinPrompt: number | null;
  MaxPrompt: number | null;
  Unreported: boolean;
};
export type Accounting = {
  usage: Usage;
  charged_tokens: number;
  attempts: {
    model: string;
    model_source?: string;
    usage: Usage;
    calls: number;
    charged_tokens: number;
    failed: boolean;
  }[];
};
/** A named set of 2 to 8 open pull requests of one repository, reviewed as one. */
export type ReviewGroup = {
  id: string;
  repository: string;
  name: string;
  members: number[];
  created_at: string;
  updated_at: string;
  version_count: number;
  latest: {
    version: number;
    generated_at: string;
    backend: string;
    model: string;
    recorded_cost_usd: number | null;
  } | null;
  risk_score: { score: number | null; status: string } | null;
  /** The members the latest version covered, with the heads it read. */
  reviewed: { number: number; head_sha: string; review_version: number }[];
  outdated: boolean;
};
export const MAX_GROUP_MEMBERS = 8;
/** An older daemon answers 404 or `available: false`: no groups. */
export function parseGroups(body: unknown): ReviewGroup[] {
  const list = (body as { available?: unknown; groups?: unknown } | null)?.groups;
  return (body as { available?: unknown } | null)?.available !== false && Array.isArray(list)
    ? (list as ReviewGroup[])
    : [];
}
export function sameRepository(a: string, b: string) {
  return a.toLowerCase() === b.toLowerCase();
}
export type Provenance = {
  tokens?: number;
  version: number;
  model: string;
  accounting?: Accounting;
  recorded_cost_usd: number | null;
  cost_reason?: string;
};
export type Job = {
  id: string;
  clone: string;
  repository: string;
  pr: number;
  state: string;
  phase: string;
  calls: number;
  tokens: number;
  repairs: number;
  actual_cost_usd: number | null;
  accounting?: Accounting;
  cost_reason?: string;
  reason?: string;
  /** A failure in words, from the generator; absent for other failures. */
  detail?: string;
  /** Queued by the daemon's scheduler, not confirmed by a person. */
  automatic?: boolean;
  trigger?: string;
  /** A review group job names its group; its `pr` is 0. */
  group?: string;
  group_name?: string;
  members?: string;
};
/** One repository's automatic generation, from `api/automatic`. */
export type AutomaticStatus = {
  repository: string;
  enabled: boolean;
  daily_cap: number | null;
  spent_today: number;
  spent_known: boolean;
  paused_until?: string;
  notice?: string;
  pending: number;
};
export type Automatic = {
  enabled: boolean;
  cursor_agent_acknowledged: boolean;
  /** Which pull requests get automatic reviews; absent from older daemons. */
  pull_requests?: "both" | "authored" | "requested";
  repositories: AutomaticStatus[];
};
const jobReasons: Record<string, string> = {
  input:
    "baum couldn't gather what the review needs: the pull request, its commits in your clone, or GitHub.",
  backend: "The agent couldn't run, or its provider was unavailable.",
  budget: "The run reached its time or size limit before it finished.",
  validation: "The agent's review didn't pass baum's checks, even after repairs.",
  interrupted: "The run stopped before it finished.",
  internal: "Something went wrong inside baum.",
  superseded: "A newer commit replaced it.",
  draft: "The pull request became a draft.",
};
/** What a finished job's reason means, in a sentence. */
export function jobReasonText(reason: string): string {
  return jobReasons[reason] ?? reason.replaceAll("_", " ");
}
/** How the jobs list and PR page say a job started without confirmation. */
export function automaticLabel(job: Job): string | null {
  if (!job.automatic) return null;
  const why =
    job.trigger === "review_requested"
      ? "your review was requested"
      : job.trigger === "new_head"
        ? "new commits were pushed"
        : job.trigger === "ready_for_review"
          ? "the pull request was marked ready for review"
          : "";
  if (job.trigger === "retry")
    return "Started automatically again: the daemon stopped during the previous automatic run";
  return why ? `Started automatically because ${why}` : "Started automatically";
}
/** Plain-language text for a repository's automatic generation notice. */
export function automaticNotice(status: AutomaticStatus): string | null {
  switch (status.notice) {
    case undefined:
    case "":
      return null;
    case "daily_cap":
      return `Automatic generation is paused: today's automatic spending reached the daily cap${
        status.daily_cap === null ? "" : ` of ${formatUSD(status.daily_cap)}`
      }. It resumes ${
        status.paused_until ? new Date(status.paused_until).toLocaleString() : "tomorrow"
      }.`;
    case "cursor_agent_acknowledgement_required":
      return "A pull request was not generated automatically: cursor-agent has no cost estimate. Acknowledge this in Repositories & settings to let automatic generation use it.";
    case "isolation_confirmation_required":
      return "A pull request was not generated automatically: it comes from a fork or an author without known write access, which always needs your confirmation.";
    case "unknown_model_price":
      return "A pull request was not generated automatically: the configured model has no known price. Choose a priced model.";
    case "unavailable":
      return "A pull request was not generated automatically: GitHub or the local clone was unavailable after several attempts.";
    default:
      return `A pull request was not generated automatically (${status.notice}).`;
  }
}
export type Estimate = {
  id: string;
  estimate_usd: number | null;
  price_version: string;
  expires_at: string;
  items: {
    clone: string;
    pr: number;
    head_sha: string;
    backend: string;
    model: string;
    estimate_usd: number | null;
    estimate_reason?: string;
    max_tokens: number;
    max_calls: number;
    requires_isolation_confirmation: boolean;
    blocked_reason?: string;
  }[];
};
export type DiagnosticStatus = "ok" | "warning" | "problem" | "info";
export type DiagnosticCheck = {
  id: string;
  label: string;
  status: DiagnosticStatus;
  detail: string;
  path?: string;
  version?: string;
  source?: string;
  fix?: string;
};
/** The same report as `baum internal doctor --json`. */
export type DiagnosticReport = {
  status: "ok" | "warning" | "problem";
  checks: DiagnosticCheck[];
};
export type Diagnostics = {
  local: boolean;
  generation_runner: boolean;
  discovery: boolean;
  index: boolean;
  report: DiagnosticReport | null;
};
/** An agent session in progress, from `api/live-sessions`: identifiers and
 * times only, never a path or any transcript content. */
export type LiveSession = {
  agent: string;
  session_id: string;
  state: "running" | "idle" | "ended";
  started_at: string;
  last_activity_at: string;
  /** The normalised kind of the latest hook event. */
  last_event: "start" | "prompt" | "tool" | "stop" | "end";
  repository: string;
  branch?: string;
  /** Absent while the branch has no pull request. */
  pr?: number;
};
const agentNames: Record<string, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  cursor: "Cursor",
};
export function agentName(agent: string): string {
  return agentNames[agent] ?? agent;
}
/** The sessions an older daemon, or one without the capability, does not
 * serve are none. */
export function parseLive(body: unknown): LiveSession[] {
  const list = (body as { sessions?: unknown } | null)?.sessions;
  return Array.isArray(list)
    ? list.filter(
        (s): s is LiveSession =>
          !!s && typeof s.session_id === "string" && typeof s.repository === "string",
      )
    : [];
}
/** Sessions that are not over and belong to the pull request, matched by
 * repository across clones as jobs are. */
export function liveFor(sessions: LiveSession[], pr: Pick<PR, "repository" | "number">) {
  return sessions.filter(
    (s) =>
      s.state !== "ended" &&
      s.pr === pr.number &&
      s.repository.toLowerCase() === pr.repository.toLowerCase(),
  );
}
export type Snapshot = { entries: Entry[]; problems: { clone_id: string; reason: string }[] };
/** The server serves the app only under its session namespace,
 * `/s/<namespace>/`: the first two segments of the page's path. The session
 * cookie is scoped to that path and nothing outside it accepts the cookie, so
 * every API, event stream and in-app URL is built from this base. */
export function appBase(pathname = location.pathname): string {
  return /^\/s\/[^/]+\//.exec(pathname)?.[0] ?? "/";
}
/** A URL under the namespace; `path` is relative to it, as `api/repos` or
 * `?page=jobs`. The app routes with query strings on the base itself. */
export function appURL(path = ""): string {
  return appBase() + path;
}
/** Requests `path`, relative to the namespace, as `api/repos`. */
export async function request<T>(
  path: string,
  body?: unknown,
  method = body === undefined ? "GET" : "POST",
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(appURL(path), {
    method,
    signal,
    cache: "no-store",
    ...(body === undefined
      ? {}
      : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  });
  if (!response.ok) {
    const text = (await response.text()).slice(0, 240);
    let body: { code?: string; message?: string; needs_clone?: boolean } = {};
    try {
      body = JSON.parse(text);
    } catch {
      /* The daemon also returns plain text. */
    }
    throw new LocalRequestError(
      body.needs_clone
        ? "Needs a local clone"
        : (body.message ?? `Local request failed (${response.status}): ${text}`),
      body.needs_clone ? "needs_clone" : body.code,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json();
}
/** Queues reviews of these pull requests at once. A fork or an author
 *  without known write access needs the user's confirmation first: then
 *  nothing is queued and the answer is "isolation". */
export async function generateReviews(
  items: { clone: string; pr: number }[],
  mode: "single" | "batch",
  confirmIsolation = false,
): Promise<"queued" | "isolation" | "needs_clone"> {
  const response = await fetch(appURL("api/jobs"), {
    method: "POST",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ items, mode, confirm_isolation: confirmIsolation }),
  });
  if (response.status === 409) {
    const body = await response.json().catch(() => null);
    if (body?.isolation_required) return "isolation";
    if (body?.needs_clone) return "needs_clone";
  }
  if (!response.ok)
    throw Error(
      `Local request failed (${response.status}): ${(await response.text()).slice(0, 240)}`,
    );
  return "queued";
}
/** Queues the review of a group. Besides the answers of `generateReviews`,
 *  a group whose members lack a saved review answers with the numbers. */
export async function generateGroup(
  clone: string,
  group: string,
  confirmIsolation = false,
): Promise<"queued" | "isolation" | "needs_clone" | { missing: number[] }> {
  const response = await fetch(appURL("api/jobs"), {
    method: "POST",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      items: [{ clone, group }],
      mode: "single",
      confirm_isolation: confirmIsolation,
    }),
  });
  if (response.status === 409) {
    const body = await response.json().catch(() => null);
    if (body?.isolation_required) return "isolation";
    if (body?.needs_clone) return "needs_clone";
    if (Array.isArray(body?.members_need_reviews)) return { missing: body.members_need_reviews };
  }
  if (!response.ok)
    throw Error(
      `Local request failed (${response.status}): ${(await response.text()).slice(0, 240)}`,
    );
  return "queued";
}
export function selector(clone: string, pr: number, version?: number) {
  return new URLSearchParams({
    clone,
    pr: String(pr),
    ...(version ? { version: String(version) } : {}),
  }).toString();
}
function validDocument(doc: ReviewDocument) {
  if (doc.schema !== "baum.review/1" || !doc.review || !Array.isArray(doc.sections) || !doc.summary)
    throw Error("Invalid review document.");
  return doc;
}
export function documentRequest(clone: string, pr: number, version?: number, signal?: AbortSignal) {
  return request<ReviewDocument>(
    `api/document?${selector(clone, pr, version)}`,
    undefined,
    "GET",
    signal,
  ).then(validDocument);
}
export function groupDocumentRequest(id: string, version?: number, signal?: AbortSignal) {
  return request<ReviewDocument>(
    `api/groups/${encodeURIComponent(id)}/document${version ? `?version=${version}` : ""}`,
    undefined,
    "GET",
    signal,
  ).then(validDocument);
}
export function rowKey(row: Entry) {
  return `${row.clone_id ?? ""}:${row.pull_request.repository}:${row.pull_request.number}`;
}
export function filtered(entries: Entry[], filter: string, repository: string) {
  return entries.filter(
    (e) =>
      (!repository || e.pull_request.repository === repository) &&
      (filter === "history" ? e.historical : !e.historical) &&
      (filter === "requested"
        ? e.pull_request.review_requested_from_me
        : filter === "mine"
          ? e.pull_request.authored_by_me
          : filter === "outdated" || filter === "no_review"
            ? e.status === filter
            : true),
  );
}
export function cost(value: number | null | undefined) {
  return formatUSD(value);
}

export function isActiveJob(job: Job | undefined): boolean {
  return job?.state === "queued" || job?.state === "running";
}
/** A queue entry belongs to the PR, including its GitHub host, across clones.
 * Active work takes precedence over earlier terminal attempts. */
export function reviewJob(entry: Entry, jobs: Job[]): Job | undefined {
  const matches = (job: Job) =>
    !job.group &&
    job.pr === entry.pull_request.number &&
    job.repository.toLowerCase() === entry.pull_request.repository.toLowerCase();
  return jobs.findLast((job) => matches(job) && isActiveJob(job)) ?? jobs.findLast(matches);
}
/** The group's latest active job, else its latest job. */
export function groupJob(group: Pick<ReviewGroup, "id">, jobs: Job[]): Job | undefined {
  const matches = (job: Job) => job.group === group.id;
  return jobs.findLast((job) => matches(job) && isActiveJob(job)) ?? jobs.findLast(matches);
}

/** Safe daemon-owned GitHub connection state; credentials never reach the app. */
export type GitHubConnection = {
  state: string;
  host: string;
  login?: string;
  source?: string;
  override: boolean;
  saved: boolean;
  pending?: {
    id: string;
    user_code: string;
    verification_uri: string;
    expires_at: string;
    interval: number;
  };
  error?: string;
  message?: string;
};
