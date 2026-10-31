import { useEffect, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { CircleCheckIcon, CircleCrossIcon, CircleDashedIcon } from "@baum/viewer/components/icons";
import { LocalRequestError, joinAnd, request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** A check of the head commit, as `api/prs/checks` lists it. Name and title
 *  come from the check itself. */
export type Check = {
  name: string;
  status: "queued" | "in_progress" | "completed";
  /** What became of it once completed; empty while it runs. */
  conclusion: string;
  title?: string;
  url?: string;
  /** The GitHub Actions job behind a failing check, which Retry re-runs;
   *  absent for other apps' checks and commit statuses. */
  job_id?: number;
};

/** A failing check, as an older daemon's `api/prs/failing-checks` lists it. */
type FailingCheck = Omit<Check, "status">;

const FAILING = new Set(["failure", "timed_out", "startup_failure", "action_required", "error"]);

const CONCLUSIONS: Record<string, string> = {
  success: "Passed",
  failure: "Failed",
  timed_out: "Timed out",
  startup_failure: "Did not start",
  action_required: "Needs approval",
  error: "Error",
  cancelled: "Cancelled",
  skipped: "Skipped",
  neutral: "Neutral",
  stale: "Stale",
};

/** What became of a check, in a word or two. */
export function conclusionText(conclusion: string): string {
  return CONCLUSIONS[conclusion] ?? conclusion.replaceAll("_", " ");
}

type CheckState = "failing" | "running" | "passing" | "other";

export function checkState(check: Check): CheckState {
  if (check.status !== "completed") return "running";
  if (FAILING.has(check.conclusion)) return "failing";
  return check.conclusion === "success" ? "passing" : "other";
}

const ORDER: Record<CheckState, number> = { failing: 0, running: 1, passing: 2, other: 3 };

/** The checks counted by state, in words: "1 failing, 2 running and 9 passed". */
export function checksSummary(checks: Check[]): string {
  const count = (state: CheckState) => checks.filter((check) => checkState(check) === state).length;
  return joinAnd(
    [
      count("failing") ? `${formatCount(count("failing"))} failing` : "",
      count("running") ? `${formatCount(count("running"))} running` : "",
      count("passing") ? `${formatCount(count("passing"))} passed` : "",
      count("other") ? `${formatCount(count("other"))} skipped or neutral` : "",
    ].filter(Boolean),
  );
}

function StateIcon({ state }: { state: CheckState }) {
  switch (state) {
    case "failing":
      return <CircleCrossIcon size={14} className="upnext-check-icon upnext-check-failing" />;
    case "running":
      return <CircleDashedIcon size={14} className="upnext-check-icon upnext-check-running" />;
    case "passing":
      return <CircleCheckIcon size={14} className="upnext-check-icon upnext-check-passing" />;
    default:
      return <CircleDashedIcon size={14} className="upnext-check-icon upnext-check-other" />;
  }
}

type Read = { checks?: Check[]; error?: string };

/** While any check runs, the list is read again this often. */
const RUNNING_REFRESH_MS = 20_000;

/** Every check of the head commit the panel shows, failing first, read from
 *  GitHub (the daemon keeps them for a short while) and read again while any
 *  still runs, each linking to its details. When the signed-in account may
 *  push (`canRetry`), a failing GitHub Actions job has Retry, which re-runs
 *  that job at once. `failingOnly` lists only the failing ones. An older
 *  daemon lists only the failing checks. */
export function PullRequestChecks({
  repository,
  number,
  head,
  url,
  canRetry,
  failingOnly = false,
}: {
  repository: string;
  number: number;
  head: string;
  url: string;
  canRetry: boolean;
  failingOnly?: boolean;
}) {
  const [read, setRead] = useState<Read>({});
  const [reads, setReads] = useState(0);
  const [retrying, setRetrying] = useState<Set<number>>(new Set());
  const [retried, setRetried] = useState<Set<number>>(new Set());
  const [failed, setFailed] = useState<Record<number, string>>({});
  const query = `repository=${encodeURIComponent(repository)}&pr=${number}&head_sha=${encodeURIComponent(head)}`;
  useEffect(() => {
    const abort = new AbortController();
    request<{ checks: Check[] }>(`api/prs/checks?${query}`, undefined, "GET", abort.signal)
      .catch((error: unknown) => {
        if (!(error instanceof LocalRequestError) || error.status !== 404) throw error;
        return request<{ checks: FailingCheck[] }>(
          `api/prs/failing-checks?${query}`,
          undefined,
          "GET",
          abort.signal,
        ).then((answer) => ({
          checks: (answer.checks ?? []).map((check): Check => ({ ...check, status: "completed" })),
        }));
      })
      .then((answer) => setRead({ checks: answer.checks ?? [] }))
      .catch((error: unknown) => {
        if (abort.signal.aborted) return;
        setRead((previous) => ({
          ...previous,
          error: error instanceof Error ? error.message : String(error),
        }));
      });
    return () => abort.abort();
  }, [query, reads]);
  const running = read.checks?.some((check) => checkState(check) === "running") ?? false;
  // Every read, failed or not, sets `read` anew, so the next one is
  // scheduled after each while a check runs.
  useEffect(() => {
    if (!running) return;
    const timer = setTimeout(() => setReads((n) => n + 1), RUNNING_REFRESH_MS);
    return () => clearTimeout(timer);
  }, [running, read]);

  const retry = async (job: number) => {
    setRetrying((ids) => new Set(ids).add(job));
    setFailed((all) =>
      Object.fromEntries(Object.entries(all).filter(([id]) => Number(id) !== job)),
    );
    try {
      await request("api/prs/rerun-job", { repository, pr: number, head_sha: head, job_id: job });
      setRetried((ids) => new Set(ids).add(job));
    } catch (error) {
      setFailed((all) => ({
        ...all,
        [job]: error instanceof Error ? error.message : String(error),
      }));
    } finally {
      setRetrying((ids) => {
        const next = new Set(ids);
        next.delete(job);
        return next;
      });
    }
  };

  if (!read.checks) {
    return read.error ? (
      <p className="upnext-threads upnext-block-sub" role="alert">
        {read.error}{" "}
        <a href={`${url}/checks`} target="_blank" rel="noreferrer">
          Open on GitHub
        </a>
      </p>
    ) : (
      <div className="upnext-threads">
        <LoadingStatus>Reading the checks from GitHub…</LoadingStatus>
      </div>
    );
  }
  const shown = read.checks
    .filter((check) => !failingOnly || checkState(check) === "failing")
    .sort((a, b) => ORDER[checkState(a)] - ORDER[checkState(b)] || a.name.localeCompare(b.name));
  if (shown.length === 0) {
    return (
      <p className="upnext-threads upnext-block-sub">
        {failingOnly
          ? "GitHub lists no failing check for this commit now."
          : "GitHub lists no checks for this commit."}
      </p>
    );
  }
  return (
    <div className="upnext-threads">
      <ul aria-label={failingOnly ? "Failing checks" : "Checks"}>
        {shown.map((check, index) => {
          const state = checkState(check);
          const job = check.job_id;
          const busy = job != null && retrying.has(job);
          const done = job != null && retried.has(job);
          return (
            <li key={`${check.name}-${index}`}>
              <StateIcon state={done ? "running" : state} />
              <span className="upnext-block-text">
                <span className="upnext-thread-excerpt">{check.name}</span>
                <span className="upnext-block-sub">
                  {done
                    ? "Re-running on GitHub"
                    : state === "running"
                      ? check.status === "queued"
                        ? "Queued"
                        : "Running"
                      : conclusionText(check.conclusion)}
                  {!done && check.title ? ` · ${check.title}` : ""}
                </span>
                {job != null && failed[job] ? (
                  <span className="upnext-thread-error" role="alert">
                    {failed[job]}
                  </span>
                ) : null}
              </span>
              <span className="upnext-check-actions">
                {canRetry && job != null && state === "failing" && !done ? (
                  <button
                    type="button"
                    className="upnext-secondary upnext-thread-resolve"
                    disabled={busy}
                    aria-busy={busy}
                    onClick={() => void retry(job)}
                  >
                    {busy ? "Retrying…" : "Retry"}
                  </button>
                ) : null}
                {check.url ? (
                  <a
                    href={check.url}
                    target="_blank"
                    rel="noreferrer"
                    className="upnext-secondary upnext-thread-resolve"
                  >
                    Details
                  </a>
                ) : null}
              </span>
            </li>
          );
        })}
      </ul>
      {failingOnly ? null : <p className="upnext-block-more">{checksSummary(read.checks)}.</p>}
    </div>
  );
}
