import { useEffect, useId, useMemo, useState } from "react";
import { ChevronRightIcon } from "@baum/viewer/components/icons";
import { request } from "./api";
import { githubToast } from "./github-toast";
import { useToast } from "./toast";
import { LoadingStatus } from "./nine-dots";
import { SMALL } from "./action-classes";
import { cn } from "@baum/viewer/components/classnames";
import { Markdown } from "@baum/viewer/components/markdown";
import type { LiveThreads } from "@baum/viewer/review-document";

/** A comment of a review thread: the reviewer's text, untrusted. `markdown`
 *  is rendered as GitHub renders it, raw HTML sanitised (`Markdown`); without
 *  it (a comment too long to send whole, or an older daemon) `body`, its
 *  plain text, shows as text. */
export type ThreadComment = { author: string; body: string; markdown?: string; url?: string };

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
  const toast = useToast();
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
      toast(
        githubToast(
          { repository, number, title: thread.excerpt, url: thread.url },
          "Thread resolved on GitHub",
        ),
      );
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
                {message.markdown ? (
                  <Markdown className="upnext-thread-markdown upnext-prose">
                    {message.markdown}
                  </Markdown>
                ) : (
                  <p className="upnext-thread-body">{message.body || "(No text)"}</p>
                )}
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
          className={cn("upnext-secondary upnext-thread-resolve", SMALL)}
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

/** A pull request whose review threads a page reads live: its number and
 *  discovery's count of its unresolved threads, null when unknown. */
export type LiveThreadsPull = { number: number; unresolved: number | null; capped: boolean };

/** The pull request's review threads as GitHub has them now, for a review
 *  page to read over its review's snapshot (`withLiveThreads`). See
 *  `useLiveThreadsOf`. */
export function useLiveThreads(
  repository: string,
  number: number,
  unresolved: number | null,
  capped: boolean,
  // The page's refresh revision: a refresh asks for the list again even when
  // the count did not change.
  revision = 0,
): LiveThreads | null {
  return useLiveThreadsOf(repository, [{ number, unresolved, capped }], revision);
}

/** The review threads of several pull requests of one repository as GitHub
 *  has them now, as one list: a group review's threads are its members'.
 *  For each pull request, discovery's count decides: with none unresolved
 *  every thread is resolved and nothing is asked; otherwise the daemon lists
 *  the unresolved ones, from its cache while the count matches, and the list
 *  is asked for again when the count changes or the page is refreshed. A
 *  read stands until the next one answers, so a changed count does not fall
 *  back to the snapshot meanwhile. Null until every list asked for is read,
 *  and while any read failed, since a missing list would read its threads as
 *  resolved: the snapshot stands. */
export function useLiveThreadsOf(
  repository: string,
  pulls: LiveThreadsPull[],
  revision = 0,
): LiveThreads | null {
  // What to ask for, as a string so a new array of the same pulls asks
  // nothing again.
  const key = pulls
    .filter((pull) => !(pull.unresolved === 0 && !pull.capped))
    .map((pull) => `${pull.number}:${pull.unresolved ?? -1}`)
    .join(",");
  const [reads, setReads] = useState<{ repository: string; byPull: Map<number, LiveThreads> }>(
    () => ({ repository, byPull: new Map() }),
  );
  useEffect(() => {
    if (!key) return;
    const abort = new AbortController();
    const settle = (number: number, live: LiveThreads | null) =>
      setReads((previous) => {
        const byPull = new Map(previous.repository === repository ? previous.byPull : []);
        if (live) byPull.set(number, live);
        else byPull.delete(number);
        return { repository, byPull };
      });
    for (const [number, unresolved] of askedPulls(key)) {
      request<{ threads: ReviewThread[]; capped: boolean }>(
        `api/prs/review-threads?repository=${encodeURIComponent(repository)}&pr=${number}&unresolved=${unresolved}`,
        undefined,
        "GET",
        abort.signal,
      )
        .then((answer) =>
          settle(number, {
            unresolved: new Set((answer.threads ?? []).map((thread) => thread.id)),
            complete: !answer.capped,
          }),
        )
        .catch(() => {
          // The snapshot stands; the next count asks again.
          if (!abort.signal.aborted) settle(number, null);
        });
    }
    return () => abort.abort();
  }, [repository, key, revision]);
  return useMemo(() => {
    const read: LiveThreads[] = [];
    for (const [number] of askedPulls(key)) {
      const live = reads.repository === repository ? reads.byPull.get(number) : undefined;
      if (!live) return null;
      read.push(live);
    }
    return {
      unresolved: new Set(read.flatMap((live) => [...live.unresolved])),
      complete: read.every((live) => live.complete),
    };
  }, [reads, repository, key]);
}

/** The pull request numbers and unresolved counts a `useLiveThreadsOf` key
 *  names. */
const askedPulls = (key: string): [number, number][] =>
  key ? key.split(",").map((part) => part.split(":").map(Number) as [number, number]) : [];
