import { useCallback, useEffect, useRef, useState } from "react";
import {
  LocalRequestError,
  readJob,
  readJobPage,
  readPullRequestJobs,
  type Entry,
  type Job,
} from "./api";

const errorText = (e: unknown) => (e instanceof Error ? e.message : "Local request failed.");

/** Finished jobs the Jobs page loads at a time. */
export const JOB_PAGE = 50;

export type JobHistory = {
  /** Jobs read so far, newest first; running ones may be among them. */
  jobs: Job[];
  /** Whether older jobs remain to load. */
  more: boolean;
  /** Starts the list again from the newest page. */
  restart: () => void;
  /** Whether the newest page was read. */
  read: boolean;
  /** Loading the newest page failed, or an older one did. */
  error: string;
  loadingMore: boolean;
  loadMore: () => void;
};

/**
 * The Jobs page's history, a page at a time (`api/jobs?limit=&before=`). It
 * reads the newest page again whenever `revision` changes. Pages already
 * loaded stay when the new page reaches them; otherwise (more than a page of
 * jobs ran in between) the list starts over from the newest page.
 */
export function useJobHistory(revision: number, enabled: boolean): JobHistory {
  const [state, setState] = useState<{ jobs: Job[]; next: string | undefined; read: boolean }>({
    jobs: [],
    next: undefined,
    read: false,
  });
  const [error, setError] = useState("");
  const [loadingMore, setLoadingMore] = useState(false);
  const current = useRef(state);
  current.current = state;
  // Bumped by every read of the newest page, so an older page that was being
  // loaded for the list it replaced is dropped.
  const generation = useRef(0);
  // Set to drop the pages loaded and keep only the newest, and the read that
  // does it.
  const startOver = useRef(false);
  const [restarts, setRestarts] = useState(0);
  const restart = useCallback(() => {
    startOver.current = true;
    setRestarts((n) => n + 1);
  }, []);
  useEffect(() => {
    if (!enabled) return;
    const abort = new AbortController();
    const mine = ++generation.current;
    // A page being loaded for the list this read replaces is dropped.
    setLoadingMore(false);
    readJobPage(undefined, JOB_PAGE, abort.signal)
      .then((page) => {
        if (abort.signal.aborted) return;
        setError("");
        const replace = startOver.current;
        startOver.current = false;
        setState((previous) => {
          const known = new Set(previous.jobs.map((job) => job.id));
          if (replace || !previous.read || !page.jobs.some((job) => known.has(job.id)))
            return { jobs: page.jobs, next: page.next, read: true };
          const ids = new Set(page.jobs.map((job) => job.id));
          return {
            jobs: [...page.jobs, ...previous.jobs.filter((job) => !ids.has(job.id))],
            next: previous.next,
            read: true,
          };
        });
      })
      .catch((e) => {
        if (!abort.signal.aborted && mine === generation.current) setError(errorText(e));
      });
    return () => abort.abort();
  }, [revision, enabled, restarts]);
  const loadMore = useCallback(() => {
    const { next } = current.current;
    if (!next) return;
    const mine = generation.current;
    setLoadingMore(true);
    setError("");
    readJobPage(next, JOB_PAGE)
      .then((page) => {
        if (mine !== generation.current) return;
        setLoadingMore(false);
        setState((previous) => {
          const known = new Set(previous.jobs.map((job) => job.id));
          return {
            jobs: [...previous.jobs, ...page.jobs.filter((job) => !known.has(job.id))],
            next: page.next,
            read: true,
          };
        });
      })
      .catch((e) => {
        if (mine !== generation.current) return;
        setLoadingMore(false);
        // The job the cursor names was archived: only the newest page is left
        // to read from.
        if (e instanceof LocalRequestError && e.status === 400) restart();
        else setError(errorText(e));
      });
  }, [restart]);
  return {
    jobs: state.jobs,
    more: !!state.next,
    read: state.read,
    error,
    loadingMore,
    loadMore,
    restart,
  };
}

/**
 * Every loaded job of the pull request, oldest first, read again whenever
 * `revision` changes (the app's reads of the current jobs). The jobs of the
 * pull request shown before are dropped when another opens; a failed read keeps
 * what was read.
 */
export function usePullRequestJobs(entry: Entry | undefined, revision: number): Job[] {
  const repository = entry?.pull_request.repository;
  const number = entry?.pull_request.number;
  const key = repository === undefined ? "" : `${repository}#${number}`;
  const [read, setRead] = useState<{ key: string; jobs: Job[] }>({ key: "", jobs: [] });
  useEffect(() => {
    if (repository === undefined || number === undefined) return;
    const abort = new AbortController();
    readPullRequestJobs(repository, number, abort.signal)
      .then((jobs) => {
        if (!abort.signal.aborted) setRead({ key, jobs });
      })
      .catch(() => {});
    return () => abort.abort();
  }, [repository, number, key, revision]);
  return read.key === key ? read.jobs : [];
}

/**
 * The job a transcript page opens: the current jobs' copy when they have it,
 * else the daemon's, which finds any loaded job. When it leaves the current
 * jobs the last copy stays while the daemon is asked, so the page is not
 * rebuilt. `undefined` while the first copy is read, null when there is none.
 * A failed read is not taken for a missing job: it is tried again whenever
 * `revision` changes (the app's reads of the current jobs).
 */
export function useOpenJob(
  id: string | null,
  current: Job[],
  revision: number,
): Job | null | undefined {
  const [read, setRead] = useState<{ id: string; job: Job | null } | undefined>();
  const last = useRef<{ id: string; job: Job | null } | undefined>(undefined);
  const inCurrent = id === null ? undefined : current.find((job) => job.id === id);
  useEffect(() => {
    if (id === null) return;
    if (inCurrent) {
      // The current copy is the freshest; an older fetched one is dropped.
      setRead(undefined);
      return;
    }
    const abort = new AbortController();
    readJob(id, abort.signal)
      .then((job) => {
        if (!abort.signal.aborted) setRead({ id, job });
      })
      .catch(() => {
        // readJob answers null for a missing job; any other failure keeps what
        // is shown (a copy, or still loading) until the next revision retries.
      });
    return () => abort.abort();
  }, [id, inCurrent, revision]);
  if (id === null) return null;
  const fetched = read?.id === id ? read.job : undefined;
  const fresh = inCurrent ?? fetched;
  if (fresh !== undefined) last.current = { id, job: fresh };
  return fresh ?? (last.current?.id === id ? last.current.job : undefined);
}
