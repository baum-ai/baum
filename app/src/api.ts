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
export type PR = {
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
export type Entry = {
  pull_request: PR;
  clone_id?: string;
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
  /** Queued by the daemon's scheduler, not confirmed by a person. */
  automatic?: boolean;
  trigger?: string;
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
  repositories: AutomaticStatus[];
};
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
  if (!response.ok)
    throw Error(
      `Local request failed (${response.status}): ${(await response.text()).slice(0, 240)}`,
    );
  if (response.status === 204) return undefined as T;
  return response.json();
}
export function selector(clone: string, pr: number, version?: number) {
  return new URLSearchParams({
    clone,
    pr: String(pr),
    ...(version ? { version: String(version) } : {}),
  }).toString();
}
export function documentRequest(clone: string, pr: number, version?: number, signal?: AbortSignal) {
  return request<ReviewDocument>(
    `api/document?${selector(clone, pr, version)}`,
    undefined,
    "GET",
    signal,
  ).then((doc) => {
    if (
      doc.schema !== "baum.review/1" ||
      !doc.review ||
      !Array.isArray(doc.sections) ||
      !doc.summary
    )
      throw Error("Invalid review document.");
    return doc;
  });
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
    job.pr === entry.pull_request.number &&
    job.repository.toLowerCase() === entry.pull_request.repository.toLowerCase();
  return jobs.findLast((job) => matches(job) && isActiveJob(job)) ?? jobs.findLast(matches);
}

// estimateReasons explains, in words, why an item has no estimate on purpose.
const estimateReasons: Record<string, string> = {
  billed_by_cursor_subscription:
    "Cost is billed by your Cursor subscription; baum can't estimate it.",
};
// blockedReasons explains why an item is left out of the generation.
const blockedReasons: Record<string, string> = {
  unknown_model_price:
    "No price is known for this model. Choose a priced model in repository settings.",
  isolation_confirmation_required:
    "A fork or an author without known write access needs its own confirmation: generate this pull request on its own.",
};
export function estimateReasonText(reason: string): string {
  return estimateReasons[reason] ?? "baum can't estimate the cost of this run.";
}
export function blockedReasonText(reason: string): string {
  return blockedReasons[reason] ?? "It cannot be generated with the current settings.";
}
