import { useCallback, useEffect, useRef, useState } from "react";
import { readJob, readJobPage, readPullRequestJobs, type Entry, type Job } from "./api";

const errorText = (e: unknown) => (e instanceof Error ? e.message : "Local request failed.");

/** Finished jobs the Jobs page loads at a time. */
export const JOB_PAGE = 50;

export type JobHistory = {
  /** Jobs read so far, newest first; running ones may be among them. */
  jobs: Job[];
  /** Whether older jobs remain to load. */
  more: boolean;
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
  useEffect(() => {
    if (!enabled) return;
    const abort = new AbortController();
    const mine = ++generation.current;
    readJobPage(undefined, JOB_PAGE, abort.signal)
      .then((page) => {
        if (abort.signal.aborted) return;
        setError("");
        setLoadingMore(false);
        setState((previous) => {
          const known = new Set(previous.jobs.map((job) => job.id));
          if (!previous.read || !page.jobs.some((job) => known.has(job.id)))
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
  }, [revision, enabled]);
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
        setError(errorText(e));
      });
  }, []);
  return {
    jobs: state.jobs,
    more: !!state.next,
    read: state.read,
    error,
    loadingMore,
    loadMore,
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
 * else the daemon's, which finds any loaded job. `undefined` while it is read,
 * null when there is none.
 */
export function useOpenJob(id: string | null, current: Job[]): Job | null | undefined {
  const [read, setRead] = useState<{ id: string; job: Job | null } | undefined>();
  const inCurrent = id === null ? undefined : current.find((job) => job.id === id);
  useEffect(() => {
    if (id === null || inCurrent) return;
    const abort = new AbortController();
    readJob(id, abort.signal)
      .then((job) => {
        if (!abort.signal.aborted) setRead({ id, job });
      })
      .catch(() => {
        if (!abort.signal.aborted) setRead({ id, job: null });
      });
    return () => abort.abort();
  }, [id, inCurrent]);
  if (id === null) return null;
  if (inCurrent) return inCurrent;
  return read?.id === id ? read.job : undefined;
}
