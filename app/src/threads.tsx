import { useEffect, useState } from "react";
import { request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** An unresolved review thread, as `api/prs/review-threads` lists it. The
 *  excerpt is its first comment's headline, in plain text. */
export type ReviewThread = {
  id: string;
  path: string;
  line?: number;
  outdated: boolean;
  author: string;
  excerpt: string;
  comments: number;
  url: string;
  can_resolve: boolean;
};

type Read = { threads?: ReviewThread[]; capped?: boolean; error?: string };

/** The pull request's unresolved review threads, read from GitHub when shown,
 *  each linking to GitHub and, when the signed-in account may, resolvable
 *  with one click. `unresolved` is discovery's count: the daemon answers from
 *  its cache while the count matches, and the list is asked for again when
 *  it changes. */
export function ReviewThreads({
  repository,
  number,
  unresolved,
  url,
}: {
  repository: string;
  number: number;
  unresolved: number;
  url: string;
}) {
  const [read, setRead] = useState<Read>({});
  const [resolving, setResolving] = useState<Set<string>>(new Set());
  const [resolved, setResolved] = useState<Set<string>>(new Set());
  const [failed, setFailed] = useState<Record<string, string>>({});
  useEffect(() => {
    const abort = new AbortController();
    request<{ threads: ReviewThread[]; capped: boolean }>(
      `api/prs/review-threads?repository=${encodeURIComponent(repository)}&pr=${number}&unresolved=${unresolved}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((answer) => setRead({ threads: answer.threads, capped: answer.capped }))
      .catch((error: unknown) => {
        if (abort.signal.aborted) return;
        setRead((previous) => ({
          ...previous,
          error: error instanceof Error ? error.message : String(error),
        }));
      });
    return () => abort.abort();
  }, [repository, number, unresolved]);

  const resolve = async (thread: ReviewThread) => {
    setResolving((ids) => new Set(ids).add(thread.id));
    setFailed((all) => Object.fromEntries(Object.entries(all).filter(([id]) => id !== thread.id)));
    try {
      await request("api/prs/resolve-thread", { repository, pr: number, thread: thread.id });
      setResolved((ids) => new Set(ids).add(thread.id));
    } catch (error) {
      setFailed((all) => ({
        ...all,
        [thread.id]: error instanceof Error ? error.message : String(error),
      }));
    } finally {
      setResolving((ids) => {
        const next = new Set(ids);
        next.delete(thread.id);
        return next;
      });
    }
  };

  if (!read.threads) {
    return read.error ? (
      <p className="upnext-threads upnext-block-sub" role="alert">
        {read.error}{" "}
        <a href={url} target="_blank" rel="noreferrer">
          Open on GitHub
        </a>
      </p>
    ) : (
      <div className="upnext-threads">
        <LoadingStatus>Reading the threads from GitHub…</LoadingStatus>
      </div>
    );
  }
  const open = read.threads.filter((thread) => !resolved.has(thread.id));
  return (
    <div className="upnext-threads">
      {open.length === 0 ? (
        <p className="upnext-block-sub">All resolved.</p>
      ) : (
        <ul aria-label="Unresolved review threads">
          {open.map((thread) => {
            const busy = resolving.has(thread.id);
            return (
              <li key={thread.id}>
                <span className="upnext-block-text">
                  <span className="upnext-thread-excerpt" title={thread.excerpt}>
                    {thread.excerpt || "(No text)"}
                  </span>
                  <span className="upnext-block-sub">
                    {thread.author || "Someone"} ·{" "}
                    <a href={thread.url || url} target="_blank" rel="noreferrer">
                      <code>
                        {thread.path}
                        {thread.line ? `:${thread.line}` : ""}
                      </code>
                    </a>
                    {thread.outdated ? " · Outdated" : ""}
                    {thread.comments > 1 ? ` · ${thread.comments} comments` : ""}
                  </span>
                  {failed[thread.id] ? (
                    <span className="upnext-thread-error" role="alert">
                      {failed[thread.id]}
                    </span>
                  ) : null}
                </span>
                {thread.can_resolve ? (
                  <button
                    type="button"
                    className="upnext-secondary upnext-thread-resolve"
                    disabled={busy}
                    aria-busy={busy}
                    onClick={() => void resolve(thread)}
                  >
                    {busy ? "Resolving…" : "Resolve"}
                  </button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {read.capped ? (
        <p className="upnext-block-more">
          GitHub lists more threads than baum reads;{" "}
          <a href={url} target="_blank" rel="noreferrer">
            see them all on GitHub
          </a>
          .
        </p>
      ) : null}
    </div>
  );
}
