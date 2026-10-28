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
  status?: number;
  constructor(message: string, code?: string, status?: number) {
    super(message);
    this.code = code;
    this.status = status;
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
  head_ref?: string;
  /** Whether the signed-in account may push to the repository, which a fix
   *  needs; null or absent when GitHub did not say. */
  viewer_can_push?: boolean | null;
  /** GitHub says the branch is out of date with its base and not conflicting,
   *  which Update branch fixes; absent in data from an older daemon. */
  behind?: boolean;
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
  /** When the job last changed. */
  updated_at?: string;
  /** "fix" for a fix of the pull request, "feature_build" for a
   * repository's first feature build (its `pr` is 0); absent for a review. */
  kind?: "fix" | "feature_build";
  fix?: FixInputs;
  /** The fix agent's transcript can be read from `api/jobs/{id}/transcript`. */
  has_transcript?: boolean;
  head_sha?: string;
  backend?: string;
  model?: string;
  /** The commit a fix pushed, and where GitHub shows it. */
  commit?: string;
  commit_url?: string;
  /** The fix agent's own account of what it changed, secrets masked. */
  summary?: string;
  /** The review threads a fix addressed, and what became of each once its
   *  push succeeded. */
  fix_threads?: FixThread[];
  thread_outcomes?: ThreadOutcome[];
};
/** What a fix was confirmed for. `threads`, when set, identifies the review
 *  threads it addresses. */
export type FixInputs = {
  conflicts?: boolean;
  checks?: boolean;
  threads?: string;
  head_ref?: string;
  base_ref?: string;
};
/** A review thread a fix addresses. Author and excerpt (the first line of its
 *  first comment) are the reviewer's. */
export type FixThread = {
  ref: string;
  path: string;
  line?: number;
  outdated?: boolean;
  author?: string;
  excerpt?: string;
};
/** What became of a review thread after a fix pushed: baum replied and
 *  resolved it, replied only, left it (the agent did not address it), found it
 *  already resolved, or could not reply. `summary` is the agent's, cleaned. */
export type ThreadOutcome = {
  ref: string;
  outcome: "resolved" | "replied" | "skipped" | "already_resolved" | "failed";
  summary?: string;
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
  pull_requests?: "all" | "both" | "authored" | "requested";
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
  signing: "Git couldn't sign a commit, so the agent didn't run.",
  superseded: "A newer commit replaced it.",
  draft: "The pull request became a draft.",
  fork: "The pull request's branch is in a fork, which baum never pushes to.",
  no_push_access: "Your GitHub account can't push to this repository.",
  nothing_to_fix:
    "GitHub reports no merge conflict, failing check or unresolved review comment to fix on this pull request now.",
  threads_changed:
    "The review comments changed after you confirmed the fix, so it stopped. Check them and start it again.",
  unsupported_backend:
    "Fixes run with Claude Code or Codex only. Choose one of them as the review harness to fix pull requests.",
  head_changed: "New commits arrived on the branch, so the fix stopped. Try again on the new head.",
  no_remote: "The local clone has no remote for this repository to push to.",
  git: "A git command in the temporary checkout failed.",
  no_changes: "The agent changed nothing, so there was nothing to push.",
  conflicts_remain: "The agent left conflict markers in a file, so nothing was pushed.",
  history_rewritten:
    "The result didn't build on the pull request's commits, so baum refused to push it.",
  workflow_changed:
    "The agent changed a GitHub workflow or action, which a fix of review comments never pushes. Nothing was pushed.",
  push_rejected:
    "GitHub refused the push, usually because the branch moved on. Nothing was force-pushed.",
  push_failed: "The push failed. Check that git can push to this repository from your clone.",
};
/** A job's reason in words: a feature build's in its own, others' as
 *  reviews and fixes say them. */
export function jobReasonText(reason: string, kind?: Job["kind"]): string {
  if (kind === "feature_build" && Object.hasOwn(buildReasons, reason)) return buildReasons[reason]!;
  return jobReasons[reason] ?? reason.replaceAll("_", " ");
}
/** Why a feature build stopped or cannot run. None asks to confirm it again:
 *  where a build can be resumed, the page offers that itself. */
const buildReasons: Record<string, string> = {
  unpriced_model:
    "The model has no price in baum's price list, so the build's cost cannot be estimated.",
  unknown_model_price:
    "The model has no price in baum's price list, so the build's cost cannot be estimated.",
  disabled: "Decisions are turned off for this repository, so it cannot be built.",
  preferences_unreadable: "baum could not read its preferences, so it did not make a call.",
  backend_unavailable: "The model backend is not available on this computer.",
  backend_unsupported: "The configured backend cannot make the feature log's calls.",
  failed: "It failed before it finished.",
  cancelled: "It was cancelled before it finished.",
  interrupted: "baum stopped before the build finished.",
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
    kind?: "fix" | "feature_build";
    fix?: FixInputs;
    /** A feature build's pull requests left to place. */
    build_pull_requests?: number;
    /** The review threads a fix would address; capped when there were more
     *  than baum includes, and how many were left out because an account
     *  without access to the repository started them. */
    fix_threads?: FixThread[];
    fix_threads_capped?: boolean;
    fix_threads_untrusted?: number;
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
/** Why a session waits on its user: it asks to run a tool, it asked a
 *  question, or its turn ended. */
export type WaitingReason = "permission" | "input" | "turn_finished";
/** An agent session in progress, from `api/live-sessions`: identifiers and
 * times, and the one masked line a waiting agent showed; never a path or any
 * transcript content. */
export type LiveSession = {
  agent: string;
  session_id: string;
  state: "running" | "idle" | "ended";
  started_at: string;
  last_activity_at: string;
  /** When an ended session ended; absent otherwise. */
  ended_at?: string;
  /** The normalised kind of the latest hook event. */
  last_event: "start" | "prompt" | "tool" | "wait" | "stop" | "end";
  /** Why it waits on its user; absent while it works, once it ended, and
   *  from a daemon older than this field. */
  waiting_reason?: WaitingReason;
  /** When that wait began. */
  waiting_since?: string;
  /** The line the agent showed, such as "Claude needs your permission to
   *  use Bash", masked by the daemon. */
  waiting_message?: string;
  repository: string;
  branch?: string;
  /** Absent while the branch has no pull request. */
  pr?: number;
  /** The session's name as its agent shows it (Claude Code's title, Codex's
   *  thread name, Cursor's conversation name), else its first prompt; one
   *  line, masked by the daemon. Absent when the agent's files cannot be
   *  read, and from a daemon older than this field. */
  title?: string;
  /** The opening of the session's latest compaction summary (the request
   *  it records), one line, masked; absent until the agent compacted it. */
  summary?: string;
};
const agentNames: Record<string, string> = {
  "claude-code": "Claude Code",
  // Analytics names agents by their session store's vendor slug.
  claude_code: "Claude Code",
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
/** Where a pull request or group page goes back to: Pull requests, with its
 *  view, when the page was opened from there, else the Inbox. `prefix` starts
 *  the query of a link that should go back to the same place. */
export function backTo(search: URLSearchParams): { href: string; label: string; prefix: string } {
  if (search.get("page") !== "pulls") return { href: appURL(), label: "Inbox", prefix: "" };
  const back = new URLSearchParams({ page: "pulls" });
  for (const key of ["filter", "repo"]) {
    const value = search.get(key);
    if (value) back.set(key, value);
  }
  return { href: appURL(`?${back}`), label: "Pull requests", prefix: "page=pulls&" };
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
      response.status,
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
/** The inbox view holds what is the person's to check now: pull requests
 *  asking for their review and their own. Everyone else's are under "other";
 *  "all" is every open pull request. */
export function filtered(entries: Entry[], filter: string, repository: string) {
  const yours = (e: Entry) =>
    e.pull_request.review_requested_from_me || e.pull_request.authored_by_me;
  return entries.filter(
    (e) =>
      (!repository || e.pull_request.repository === repository) &&
      (filter === "history" ? e.historical : !e.historical) &&
      (filter === "inbox"
        ? yours(e)
        : filter === "other"
          ? !yours(e)
          : filter === "requested"
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
 * Active work takes precedence over earlier terminal attempts. A fix is not
 * a review: see fixJob. */
export function reviewJob(entry: Entry, jobs: Job[]): Job | undefined {
  return prJob(entry, jobs, (job) => job.kind !== "fix");
}
/** The PR's latest fix job, an active one first. */
export function fixJob(entry: Entry, jobs: Job[]): Job | undefined {
  return prJob(entry, jobs, (job) => job.kind === "fix");
}
function prJob(entry: Entry, jobs: Job[], kind: (job: Job) => boolean): Job | undefined {
  const matches = (job: Job) =>
    !job.group &&
    kind(job) &&
    job.pr === entry.pull_request.number &&
    job.repository.toLowerCase() === entry.pull_request.repository.toLowerCase();
  return jobs.findLast((job) => matches(job) && isActiveJob(job)) ?? jobs.findLast(matches);
}
/** The group's latest active job, else its latest job. */
export function groupJob(group: Pick<ReviewGroup, "id">, jobs: Job[]): Job | undefined {
  const matches = (job: Job) => job.group === group.id;
  return jobs.findLast((job) => matches(job) && isActiveJob(job)) ?? jobs.findLast(matches);
}
/** "a", "a and b", "a, b and c". */
export function joinAnd(parts: string[]): string {
  return parts.length < 2
    ? (parts[0] ?? "")
    : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}
/** The name of a fix, by what it fixes. */
export function fixName(fix: {
  conflicts?: boolean;
  checks?: boolean;
  threads?: string | boolean;
}): string {
  const parts = [
    fix.conflicts ? "conflicts" : "",
    fix.checks ? "CI" : "",
    fix.threads ? "review comments" : "",
  ].filter(Boolean);
  return `Fix ${joinAnd(parts.length ? parts : ["CI"])}`;
}
/** The fix a pull request offers, or null: only an open pull request whose
 *  branch is in the repository itself (not a fork), in a repository the
 *  signed-in account may push to, with a merge conflict, failing checks or,
 *  on the person's own pull request, unresolved review threads. The daemon
 *  checks all of it again against GitHub before anything runs. */
export function fixLabel(entry: Entry): string | null {
  const pr = entry.pull_request;
  if (entry.historical || entry.needs_clone || pr.fork || pr.viewer_can_push !== true) return null;
  if (pr.state && pr.state.toUpperCase() !== "OPEN") return null;
  const conflicts = entry.merge === "conflict";
  const checks = entry.checks === "failing";
  const threads = pr.authored_by_me && (entry.unresolved ?? 0) > 0;
  return conflicts || checks || threads ? fixName({ conflicts, checks, threads }) : null;
}
const threadOutcomes: Record<ThreadOutcome["outcome"], string> = {
  resolved: "Replied and resolved",
  replied: "Replied, not resolved",
  skipped: "Left open",
  already_resolved: "Already resolved",
  failed: "Reply failed",
};
/** What became of one review thread, in a few words. */
export function threadOutcomeText(outcome: ThreadOutcome["outcome"]): string {
  return threadOutcomes[outcome] ?? outcome.replaceAll("_", " ");
}
/** A fix's replies in one line: "Replied and resolved 3 of 4 threads". */
export function threadOutcomesSummary(outcomes: ThreadOutcome[] | undefined): string | null {
  if (!outcomes?.length) return null;
  const resolved = outcomes.filter((o) => o.outcome === "resolved").length;
  const replied = outcomes.filter((o) => o.outcome === "replied").length;
  const failed = outcomes.filter((o) => o.outcome === "failed").length;
  const total = `${outcomes.length} ${outcomes.length === 1 ? "thread" : "threads"}`;
  let text = `Replied and resolved ${resolved} of ${total}`;
  if (replied) text += `; replied on ${replied} more without resolving`;
  if (failed) text += `; ${failed} ${failed === 1 ? "reply" : "replies"} failed`;
  return text;
}
/** Prepares a fix of the pull request: what it would fix, with which harness
 *  and model, and on which branch, or why it cannot run. */
export async function estimateFix(clone: string, pr: number): Promise<Estimate> {
  return request<Estimate>("api/jobs/estimate", {
    items: [{ clone, pr, kind: "fix" }],
    mode: "single",
  });
}
/** Estimates a feature build of the clone's repository: its first build,
 *  or the rest of one that stopped. */
export async function estimateFeatureBuild(clone: string): Promise<Estimate> {
  return request<Estimate>("api/jobs/estimate", {
    items: [{ clone, pr: 0, kind: "feature_build" }],
    mode: "single",
  });
}
/** Starts the job of a confirmed fix or feature build estimate. */
export async function startFix(estimate: Estimate): Promise<Job[]> {
  const answer = await request<{ jobs: Job[] }>("api/jobs", {
    estimate_id: estimate.id,
    confirm_estimate: true,
  });
  return answer.jobs;
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
