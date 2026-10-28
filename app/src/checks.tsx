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
  /** A GitHub Actions job, which Re-run checks re-runs. */
  actions: boolean;
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
 *  (the daemon keeps them for a short while), each linking to its details. */
export function FailingChecks({
  repository,
  number,
  head,
  url,
}: {
  repository: string;
  number: number;
  head: string;
  url: string;
}) {
  const [read, setRead] = useState<Read>({});
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
        {read.checks.map((check, index) => (
          <li key={`${check.name}-${index}`}>
            <span className="upnext-block-text">
              <span className="upnext-thread-excerpt">{check.name}</span>
              <span className="upnext-block-sub">
                {conclusionText(check.conclusion)}
                {check.title ? ` · ${check.title}` : ""}
              </span>
            </span>
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
          </li>
        ))}
      </ul>
    </div>
  );
}
