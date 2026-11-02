import { useEffect, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { CloseIcon } from "@baum/viewer/components/icons";
import { Button, LiveDot } from "@baum/viewer/components/ui";
import { ViewerLink } from "@baum/viewer/navigation";
import {
  appURL,
  cost,
  isActiveJob,
  jobReasonText,
  LocalRequestError,
  request,
  startFeatureBuild,
  type Job,
} from "./api";

/** A repository's first feature build, from the Timeline: its history of
 *  pull requests placed in features by a job of the job queue
 *  (`feature_build`) that starts at once, and its state while it runs or
 *  after it stopped. */

/** The building version a first build writes, from `api/timeline/build`. */
export type Building = { placed: number; total: number; stopped: string; spent_usd: number };

/** The latest feature build job of the repository, if any (the queue lists
 *  jobs oldest first). */
export function buildJobOf(jobs: Job[], repository: string): Job | undefined {
  return jobs.findLast(
    (job) =>
      job.kind === "feature_build" && job.repository.toLowerCase() === repository.toLowerCase(),
  );
}

/** What `api/timeline/build` says of the repository: its building version
 *  and whether it has features at all, whatever the lists' filters. */
export type BuildState = {
  building: Building | null;
  hasFeatures: boolean;
  /** When the current version became current, null without one. */
  currentSince: string | null;
};

/** The repository's build state, read again when the log or the build job
 *  moves on; none is read without a repository. A read that fails keeps the
 *  last state. */
export function useBuildState(repository: string | null, key: string): BuildState {
  const [state, setState] = useState<BuildState>({
    building: null,
    hasFeatures: false,
    currentSince: null,
  });
  useEffect(() => {
    if (repository === null) return;
    const abort = new AbortController();
    request<{ building?: Building | null; has_features?: boolean; current_since?: string | null }>(
      `api/timeline/build?repository=${encodeURIComponent(repository)}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((body) =>
        setState({
          building: body.building ?? null,
          hasFeatures: body.has_features === true,
          currentSince: body.current_since ?? null,
        }),
      )
      .catch(() => {
        // A transient failure, or an older daemon: what was shown stays.
      });
    return () => abort.abort();
  }, [repository, key]);
  return state;
}

/** Why a feature build stopped or cannot run, in its own words; see
 *  `jobReasonText` with the `feature_build` kind. */
export function buildReasonText(reason: string): string {
  return jobReasonText(reason, "feature_build");
}

export function jobHref(job: Job): string {
  return appURL(`?page=jobs&job=${encodeURIComponent(job.id)}`);
}

/** A build running, stopped or failed, above the features it does not
 *  replace until it finishes. `onBuild` starts it at once, resuming a
 *  stopped one; without a clone it is offered disabled. Every state but a
 *  running build offers an action. */
export function BuildStatus({
  job,
  state,
  clone,
  onBuild,
  starting,
}: {
  job: Job | undefined;
  state: BuildState;
  clone: string | null;
  onBuild: () => void;
  /** The start request is on its way. */
  starting: boolean;
}) {
  const { building } = state;
  const [closed, setClosed] = useState(closedBuilds);
  const action = (label: string) => (
    <Button
      variant="secondary"
      size="compact"
      disabled={!clone || starting}
      title={clone ? undefined : NO_CLONE}
      onClick={onBuild}
    >
      {starting ? "Starting…" : label}
    </Button>
  );
  if (isActiveJob(job)) {
    const counted = building && building.total > 0;
    return (
      <section aria-label="Feature build" className="local-job-outcome">
        <LiveDot />
        <p role="status" className="min-w-0 flex-1 font-medium">
          {job!.state === "queued"
            ? "Feature history build queued"
            : counted
              ? `Building feature history… ${formatCount(building.placed)} of ${formatCount(building.total)} pull requests placed`
              : "Building feature history…"}
        </p>
        <ViewerLink href={jobHref(job!)}>View in Jobs</ViewerLink>
      </section>
    );
  }
  // A latest job that finished without completing stopped the build:
  // failed, cancelled or interrupted.
  const ended = job && job.state !== "completed" ? job.reason || job.state : "";
  if (!building) {
    // It stopped before it made its version: shown until a version newer
    // than the job is current or the person closes it. One they cancelled
    // (state cancelled; a daemon stop leaves it interrupted) needs no word.
    const stale =
      state.currentSince !== null &&
      (!job?.updated_at || Date.parse(job.updated_at) < Date.parse(state.currentSince));
    if (!ended || job!.state === "cancelled" || stale || closed.includes(job!.id)) return null;
    return (
      <section aria-label="Feature build" className="local-job-outcome">
        <p role="status" className="min-w-0 flex-1 font-medium">
          The last feature history build stopped before it made a version. {buildReasonText(ended)}
        </p>
        <ViewerLink href={jobHref(job!)}>View in Jobs</ViewerLink>
        {action("Try again")}
        <Button
          variant="ghost"
          size="compact"
          aria-label="Dismiss"
          onClick={() => setClosed(closeBuild(job!.id))}
        >
          <CloseIcon size={14} />
        </Button>
      </section>
    );
  }
  const reason = building.stopped || ended;
  return (
    <section aria-label="Feature build" className="local-job-outcome">
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {reason
            ? `The feature history build stopped. ${buildReasonText(reason)}`
            : "A feature history build is in progress or stopped"}
        </p>
        <p className="text-xs text-(--text-tertiary)">
          {building.total > 0
            ? `${formatCount(building.placed)} of ${formatCount(building.total)} pull requests placed, `
            : null}
          {cost(building.spent_usd)} at API prices so far. The features shown stay until it
          finishes.
          {reason ? null : " Resuming goes on with the same build."}
        </p>
      </div>
      {job ? <ViewerLink href={jobHref(job)}>View in Jobs</ViewerLink> : null}
      {action("Resume build")}
    </section>
  );
}

// The stopped builds the person closed, the latest few; a later job of the
// repository shows again.
const CLOSED_KEY = "baum.timeline.closed-builds.v1";
function closedBuilds(): string[] {
  try {
    const ids = JSON.parse(localStorage.getItem(CLOSED_KEY) ?? "[]");
    return Array.isArray(ids) ? ids : [];
  } catch {
    return [];
  }
}
function closeBuild(id: string): string[] {
  const ids = [...closedBuilds(), id].slice(-20);
  try {
    localStorage.setItem(CLOSED_KEY, JSON.stringify(ids));
  } catch {
    // Closed until the page is opened again.
  }
  return ids;
}

export const NO_CLONE = "Needs a local clone of the repository";

/** A repository whose feature build can run: it has a local clone. */
export type BuildTarget = { repository: string; clone: string };

/** Starts the repositories' feature builds at once, one job each, as every
 *  run the person asks for starts: no dialog, no estimate. One the daemon
 *  will not run says why in the build's own words, naming its repository
 *  when there are several; the others still start. */
export function useFeatureBuildStart(targets: BuildTarget[], started: () => void) {
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");
  const start = async () => {
    if (targets.length === 0 || starting) return;
    setStarting(true);
    setError("");
    const results = await Promise.allSettled(
      targets.map((target) => startFeatureBuild(target.clone)),
    );
    if (results.some((result) => result.status === "fulfilled")) started();
    const errors = results.flatMap((result, i) => {
      if (result.status === "fulfilled") return [];
      const e = result.reason;
      const text =
        e instanceof LocalRequestError && e.code && e.status === 409
          ? buildReasonText(e.code)
          : e instanceof Error
            ? e.message
            : "Local request failed.";
      return [targets.length > 1 ? `${targets[i]!.repository}: ${text}` : text];
    });
    setError(errors.join(" "));
    setStarting(false);
  };
  return { start, starting, error };
}
