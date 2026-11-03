import { useEffect, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { CircleCheckIcon, CircleCrossIcon, CircleDashedIcon } from "@baum/viewer/components/icons";
import { LocalRequestError, joinAnd, request } from "./api";
import { LoadingStatus } from "./nine-dots";
import { SMALL } from "./action-classes";
import { cn } from "@baum/viewer/components/classnames";
import { Part } from "@baum/viewer/components/review-parts";
import type { ReviewDetailsChecks } from "@baum/viewer/components/review-viewer";
import { Badge } from "@baum/viewer/components/ui";
import { checkStatus, type ReviewDocument } from "@baum/viewer/review-document";

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

/** The checks counted in a word, out of them all, and how it reads: "2/9
 *  failing" while any fails, else "1/9 running" while any runs, else "9/9
 *  passing". */
export function checksCount(checks: Check[]): {
  text: string;
  tone: "critical" | "caution" | "positive";
} {
  const count = (state: CheckState) => checks.filter((check) => checkState(check) === state).length;
  const of = (n: number, word: string) => `${formatCount(n)}/${formatCount(checks.length)} ${word}`;
  if (count("failing")) return { text: of(count("failing"), "failing"), tone: "critical" };
  if (count("running")) return { text: of(count("running"), "running"), tone: "caution" };
  return { text: of(count("passing"), "passing"), tone: "positive" };
}

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

/** The checks as last read, or why the read failed; neither while the first
 *  read is under way. */
export type ChecksRead = { checks?: Check[]; error?: string };

/** While any check runs, the list is read again this often. */
const RUNNING_REFRESH_MS = 20_000;

/** The head commit's checks, read from GitHub (the daemon keeps them for a
 *  short while) when `enabled`, and read again while any still runs. An older
 *  daemon lists only the failing checks. */
export function useChecks(
  repository: string,
  number: number,
  head: string,
  enabled = true,
): ChecksRead {
  const [read, setRead] = useState<ChecksRead>({});
  const [reads, setReads] = useState(0);
  const query = `repository=${encodeURIComponent(repository)}&pr=${number}&head_sha=${encodeURIComponent(head)}`;
  useEffect(() => {
    if (!enabled) return;
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
  }, [query, reads, enabled]);
  const running = read.checks?.some((check) => checkState(check) === "running") ?? false;
  // Every read, failed or not, sets `read` anew, so the next one is
  // scheduled after each while a check runs.
  useEffect(() => {
    if (!enabled || !running) return;
    const timer = setTimeout(() => setReads((n) => n + 1), RUNNING_REFRESH_MS);
    return () => clearTimeout(timer);
  }, [enabled, running, read]);
  return read;
}

/** Every check of the head commit the panel shows, failing first, each
 *  linking to its details: `checks` when the caller reads them (`useChecks`),
 *  else read here. When the signed-in account may push (`canRetry`), a failing
 *  GitHub Actions job has Retry, which re-runs that job at once. While any
 *  check fails only the failing ones show, the others behind Show other
 *  checks. `failingOnly` lists only the failing ones. */
export function PullRequestChecks({
  repository,
  number,
  head,
  url,
  canRetry,
  failingOnly = false,
  checks,
}: {
  repository: string;
  number: number;
  head: string;
  url: string;
  canRetry: boolean;
  failingOnly?: boolean;
  checks?: ChecksRead;
}) {
  const own = useChecks(repository, number, head, checks === undefined);
  const read = checks ?? own;
  const [retrying, setRetrying] = useState<Set<number>>(new Set());
  const [retried, setRetried] = useState<Set<number>>(new Set());
  const [failed, setFailed] = useState<Record<number, string>>({});
  // The checks that are not failing stay folded away until asked for.
  const [showOthers, setShowOthers] = useState(false);
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
  // A failing check, or one retried here, which stays in sight as it reruns.
  const failingNow = (check: Check) =>
    checkState(check) === "failing" || (check.job_id != null && retried.has(check.job_id));
  const sorted = read.checks
    .filter((check) => !failingOnly || checkState(check) === "failing")
    .sort((a, b) => ORDER[checkState(a)] - ORDER[checkState(b)] || a.name.localeCompare(b.name));
  // With nothing failing every check shows; otherwise the failing ones, and
  // the others behind Show other checks.
  const folds = !failingOnly && sorted.some(failingNow);
  const others = folds ? sorted.filter((check) => !failingNow(check)).length : 0;
  const shown = folds && !showOthers ? sorted.filter(failingNow) : sorted;
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
                    className={cn("upnext-secondary upnext-thread-resolve", SMALL)}
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
                    className={cn("upnext-secondary upnext-thread-resolve", SMALL)}
                  >
                    Details
                  </a>
                ) : null}
              </span>
            </li>
          );
        })}
      </ul>
      {others > 0 ? (
        <button
          type="button"
          className="upnext-checks-more"
          aria-expanded={showOthers}
          onClick={() => setShowOthers((show) => !show)}
        >
          {showOthers
            ? "Hide other checks"
            : `Show ${others === 1 ? "1 other check" : `${formatCount(others)} other checks`}`}
        </button>
      ) : null}
    </div>
  );
}

/** The review page's Details checks, as GitHub lists them now.
 *
 *  A review's own check index is a snapshot taken when it was generated, and
 *  an automatic review starts as soon as a new head is pushed, so that
 *  snapshot usually shows CI still running. For a version of the head the
 *  pull request is on, this reads the checks live (`useChecks`) instead.
 *  An earlier head's version, a read not back yet and a read that failed keep
 *  the snapshot, which dates itself once it is old. */
export function liveDetailsChecks(
  document: ReviewDocument,
  pr: { repository: string; number: number; head_sha: string; url: string; canRetry: boolean },
  read: ChecksRead,
): ReviewDetailsChecks {
  if (document.review.head !== pr.head_sha || !read.checks) {
    return { content: undefined, failing: checkStatus(document).state === "failing" };
  }
  const count = read.checks.length ? checksCount(read.checks) : null;
  return {
    content: (
      <Part label="Checks">
        <div className="space-y-2">
          {count ? (
            <Badge tone={count.tone} role="status">
              {count.text}
            </Badge>
          ) : null}
          <PullRequestChecks
            repository={pr.repository}
            number={pr.number}
            head={pr.head_sha}
            url={pr.url}
            canRetry={pr.canRetry}
            checks={read}
          />
        </div>
      </Part>
    ),
    failing: count?.tone === "critical",
  };
}
