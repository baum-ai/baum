import { appURL, automaticLabel, fixName, type Job } from "./api";
import { useNow } from "./live";

/** Where a job's pull request or review group opens. A feature build has
 *  neither. */
export function jobTarget(job: Job) {
  if (job.kind === "feature_build") return null;
  return appURL(
    job.group
      ? `?group=${encodeURIComponent(job.group)}`
      : `?${job.clone ? `clone=${encodeURIComponent(job.clone)}` : `repository=${encodeURIComponent(job.repository)}`}&pr=${job.pr}`,
  );
}
/** What kind of run a job is, as its tag says. */
export function jobType(job: Job) {
  if (job.kind === "feature_build") return "Feature build";
  if (job.kind === "fix") return fixName(job.fix ?? {});
  if (job.kind === "chat") return "Question";
  if (job.kind === "details") return "Details";
  return job.group ? "Review group" : "Review";
}
/** Who or what started a job. A fix is always asked for, so who started it
 *  goes without saying. */
export function jobOrigin(job: Job) {
  return automaticLabel(job) ?? (job.automatic || job.kind === "fix" ? null : "Started by you");
}

const clock = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
const dayClock = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
});
/** An instant as a time of day, with its date when it is not today. */
export function jobClock(iso: string, now: number) {
  const at = new Date(iso);
  return (new Date(now).toDateString() === at.toDateString() ? clock : dayClock).format(at);
}
/** How long a job ran: to the second under an hour, then to the minute. */
export function jobDuration(ms: number) {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** When a job was queued and started, and how long it ran or has run so far,
 *  ticking each second while it runs. A job saved before starts were recorded
 *  counts from when it was queued; one cancelled in the queue never ran, so it
 *  has no start or run time. */
export function useJobTimes(job: Job) {
  const running = job.state === "running";
  const now = useNow(running ? 1000 : 60_000);
  const queued = job.state === "queued" || (!job.started_at && job.state === "cancelled");
  const start = queued ? undefined : (job.started_at ?? job.created_at);
  const end = running ? now : new Date(job.updated_at ?? start ?? 0).getTime();
  return {
    queued: job.created_at ? jobClock(job.created_at, now) : null,
    started: start ? jobClock(start, now) : null,
    finished: !queued && !running && job.updated_at ? jobClock(job.updated_at, now) : null,
    took: start ? jobDuration(end - new Date(start).getTime()) : null,
    running,
  };
}

/** A Jobs row's figure: when the job started and how long it took. */
export function JobTiming({ job }: { job: Job }) {
  const times = useJobTimes(job);
  if (!times.started && !times.queued) return <span className="local-job-figure" />;
  return (
    <span className="local-job-figure local-job-timing">
      <span>{times.started ? `Started ${times.started}` : `Queued ${times.queued}`}</span>
      {times.took ? (
        <span>{times.running ? `${times.took} so far` : `Took ${times.took}`}</span>
      ) : null}
    </span>
  );
}
