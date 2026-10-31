import { useEffect, useId, useState } from "react";
import { ChevronRightIcon } from "@baum/viewer/components/icons";
import { request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** A comment of a review thread. Its body is the reviewer's text, untrusted,
 *  and shown as plain text. */
export type ThreadComment = { author: string; body: string; url?: string };

/** An unresolved review thread, as `api/prs/review-threads` lists it. The
 *  excerpt is its first comment's headline, in plain text; the messages are
 *  its comments, absent from an older daemon. */
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
  messages?: ThreadComment[];
};

type Read = { threads?: ReviewThread[]; capped?: boolean; error?: string };

/** The pull request's unresolved review threads, read from GitHub when shown,
 *  each linking to GitHub, opening to its whole conversation and, when the
 *  signed-in account may, resolvable with one click. `unresolved` is
 *  discovery's count: the daemon answers from its cache while the count
 *  matches, and the list is asked for again when it changes. */
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
          {open.map((thread) => (
            <Thread
              key={thread.id}
              thread={thread}
              url={url}
              busy={resolving.has(thread.id)}
              error={failed[thread.id]}
              resolve={() => void resolve(thread)}
            />
          ))}
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

/** One thread: its first comment's headline, which opens to every comment
 *  baum read of it. */
function Thread({
  thread,
  url,
  busy,
  error,
  resolve,
}: {
  thread: ReviewThread;
  url: string;
  busy: boolean;
  error?: string;
  resolve: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  const messages = thread.messages ?? [];
  const more = thread.comments - messages.length;
  return (
    <li className="upnext-thread">
      <span className="upnext-block-text">
        {messages.length > 0 ? (
          <button
            type="button"
            className="upnext-thread-toggle"
            aria-expanded={expanded}
            aria-controls={id}
            onClick={() => setExpanded((open) => !open)}
          >
            <ChevronRightIcon size={12} className="upnext-chevron" />
            <span className="upnext-thread-excerpt" title={thread.excerpt}>
              {thread.excerpt || "(No text)"}
            </span>
          </button>
        ) : (
          <span className="upnext-thread-excerpt" title={thread.excerpt}>
            {thread.excerpt || "(No text)"}
          </span>
        )}
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
        {error ? (
          <span className="upnext-thread-error" role="alert">
            {error}
          </span>
        ) : null}
        {expanded ? (
          <ol id={id} className="upnext-thread-messages" aria-label="Conversation">
            {messages.map((message, index) => (
              <li key={index}>
                <span className="upnext-thread-author">{message.author || "Someone"}</span>
                <p className="upnext-thread-body">{message.body || "(No text)"}</p>
              </li>
            ))}
            {more > 0 ? (
              <li className="upnext-block-sub">
                <a href={thread.url || url} target="_blank" rel="noreferrer">
                  {more === 1 ? "1 more comment" : `${more} more comments`} on GitHub
                </a>
              </li>
            ) : null}
          </ol>
        ) : null}
      </span>
      {thread.can_resolve ? (
        <button
          type="button"
          className="upnext-secondary upnext-thread-resolve"
          disabled={busy}
          aria-busy={busy}
          onClick={resolve}
        >
          {busy ? "Resolving…" : "Resolve"}
        </button>
      ) : null}
    </li>
  );
}
