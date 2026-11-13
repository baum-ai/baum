import { isActiveJob, type Job } from "./api";

/** What the app reads from the daemon, by resource. A kind of event reads the
 *  parts it can change, so a job's progress does not read the index. */
export type ReloadPart =
  | "clones" // api/repos, api/repositories, api/clones/found
  | "index" // api/index?history=1&detail=summary, api/inbox
  | "jobs" // api/jobs
  | "discovery" // api/prs, api/background
  | "automatic" // api/automatic
  | "settings" // api/settings, api/github-auth
  | "groups" // api/groups
  | "inboxes"; // saved inboxes

export const allReloadParts: readonly ReloadPart[] = [
  "clones",
  "index",
  "jobs",
  "discovery",
  "automatic",
  "settings",
  "groups",
  "inboxes",
];

/** `index`: the review index, or the pull requests discovery read, changed.
 *  Clones and groups are derived from them and are not announced otherwise.
 *  The jobs are read too: a job's state is not announced with the index, so
 *  whether an index change follows one is not known. These events are rare;
 *  `progress`, which is not, leaves the index alone. */
export const indexParts: readonly ReloadPart[] = [
  "index",
  "jobs",
  "discovery",
  "groups",
  "clones",
  "inboxes",
];

/** `progress`: a job changed in anything but its usage. A group's review is
 *  stored by its job, so the groups are read with it. */
export const progressParts: readonly ReloadPart[] = ["jobs", "groups"];

/** What a job that ended stored: a review, its details, a group. */
export const endedParts: readonly ReloadPart[] = ["index", "discovery", "groups"];

/** `automatic`: a notice, or every repository's status after a settings change. */
export const automaticParts: readonly ReloadPart[] = [
  "automatic",
  "settings",
  "clones",
  "discovery",
];

/** Whether `next` holds a finished job that `previous` did not hold finished. */
export function jobsEnded(previous: readonly Job[], next: readonly Job[]): boolean {
  const before = new Map(previous.map((job) => [job.id, job]));
  return next.some((job) => {
    if (isActiveJob(job)) return false;
    const was = before.get(job.id);
    return !was || isActiveJob(was);
  });
}

export type EventGate = {
  /** Runs `fn` now while the page is visible. While hidden it runs once, when
   *  the page shows again; a later call with the same key replaces `fn`. */
  run(key: string, fn: () => void): void;
  dispose(): void;
};

/** Holds what daemon events ask for while the window is hidden (a closed
 *  window or tray panel), so a hidden app makes no requests and repaints
 *  nothing. */
export function createEventGate(): EventGate {
  const held = new Map<string, () => void>();
  const shown = () => {
    if (document.hidden) return;
    document.removeEventListener("visibilitychange", shown);
    const due = [...held.values()];
    held.clear();
    for (const fn of due) fn();
  };
  return {
    run(key, fn) {
      if (!document.hidden) return fn();
      // The listener lives only while something is held, so a gate has no
      // side effects until then and disposing it is safe to repeat.
      if (held.size === 0) document.addEventListener("visibilitychange", shown);
      held.set(key, fn);
    },
    dispose() {
      document.removeEventListener("visibilitychange", shown);
      held.clear();
    },
  };
}
