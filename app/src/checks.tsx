import { useEffect, useState } from "react";
import { request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** A failing check of the head commit, as `api/prs/failing-checks` lists it.
 *  Name and title come from the check itself. */
export type FailingCheck = {
  name: string;
  conclusion: string;
  title?: string;
  url?: string;
  /** The GitHub Actions job behind the check, which Retry re-runs; absent
   *  for other apps' checks and commit statuses. */
  job_id?: number;
};

const CONCLUSIONS: Record<string, string> = {
  failure: "Failed",
  timed_out: "Timed out",
  startup_failure: "Did not start",
  action_required: "Needs approval",
  error: "Error",
};

/** What became of a check, in a word or two. */
export function conclusionText(conclusion: string): string {
  return CONCLUSIONS[conclusion] ?? conclusion.replaceAll("_", " ");
}

type Read = { checks?: FailingCheck[]; error?: string };

/** The failing checks of the head commit the panel shows, read from GitHub
 *  (the daemon keeps them for a short while), each linking to its details.
 *  When the signed-in account may push (`canRetry`), a GitHub Actions job has
 *  Retry, which re-runs that job at once. */
export function FailingChecks({
  repository,
  number,
  head,
  url,
  canRetry,
}: {
  repository: string;
  number: number;
  head: string;
  url: string;
  canRetry: boolean;
}) {
  const [read, setRead] = useState<Read>({});
  const [retrying, setRetrying] = useState<Set<number>>(new Set());
  const [retried, setRetried] = useState<Set<number>>(new Set());
  const [failed, setFailed] = useState<Record<number, string>>({});
  useEffect(() => {
    const abort = new AbortController();
    request<{ checks: FailingCheck[] }>(
      `api/prs/failing-checks?repository=${encodeURIComponent(repository)}&pr=${number}&head_sha=${encodeURIComponent(head)}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((answer) => setRead({ checks: answer.checks ?? [] }))
      .catch((error: unknown) => {
        if (abort.signal.aborted) return;
        setRead((previous) => ({
          ...previous,
          error: error instanceof Error ? error.message : String(error),
        }));
      });
    return () => abort.abort();
  }, [repository, number, head]);

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
  if (read.checks.length === 0) {
    return (
      <p className="upnext-threads upnext-block-sub">
        GitHub lists no failing check for this commit now.
      </p>
    );
  }
  return (
    <div className="upnext-threads">
      <ul aria-label="Failing checks">
        {read.checks.map((check, index) => {
          const job = check.job_id;
          const busy = job != null && retrying.has(job);
          const done = job != null && retried.has(job);
          return (
            <li key={`${check.name}-${index}`}>
              <span className="upnext-block-text">
                <span className="upnext-thread-excerpt">{check.name}</span>
                <span className="upnext-block-sub">
                  {done ? "Re-running on GitHub" : conclusionText(check.conclusion)}
                  {!done && check.title ? ` · ${check.title}` : ""}
                </span>
                {job != null && failed[job] ? (
                  <span className="upnext-thread-error" role="alert">
                    {failed[job]}
                  </span>
                ) : null}
              </span>
              <span className="upnext-check-actions">
                {canRetry && job != null && !done ? (
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
    </div>
  );
}
