import { useEffect, useRef, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import { ViewerLink } from "@baum/viewer/navigation";
import {
  appURL,
  cost,
  estimateFeatureBuild,
  isActiveJob,
  jobReasonText,
  request,
  startFix,
  type Estimate,
  type Job,
} from "./api";
import { harnessName } from "./desktop/reviews";
import { LoadingStatus } from "./nine-dots";

/** A repository's first feature build, from the Timeline: its history of
 *  pull requests placed in features by a confirmed, capped job of the job
 *  queue (`feature_build`), and its state while it runs or after it stopped. */

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

/** The repository's building version, read again when the log or the build
 *  job moves on. */
export function useBuilding(repository: string, key: string): Building | null {
  const [building, setBuilding] = useState<Building | null>(null);
  useEffect(() => {
    const abort = new AbortController();
    request<{ building?: Building | null }>(
      `api/timeline/build?repository=${encodeURIComponent(repository)}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((body) => setBuilding(body.building ?? null))
      .catch(() => {
        // An older daemon, or a failed read: no build to show.
        if (!abort.signal.aborted) setBuilding(null);
      });
    return () => abort.abort();
  }, [repository, key]);
  return building;
}

export function jobHref(job: Job): string {
  return appURL(`?page=jobs&job=${encodeURIComponent(job.id)}`);
}

/** A build running or stopped, above the features it does not replace
 *  until it finishes. */
export function BuildStatus({
  job,
  building,
  onResume,
}: {
  job: Job | undefined;
  building: Building | null;
  onResume: (() => void) | null;
}) {
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
  if (!building) return null;
  const capped = building.stopped === "cost_cap";
  return (
    <section aria-label="Feature build" className="local-job-outcome">
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {capped
            ? "The feature history build stopped at its spending cap — confirm again to resume"
            : building.stopped
              ? `The feature history build stopped: ${jobReasonText(building.stopped)}`
              : "The feature history build stopped before it finished"}
        </p>
        <p className="text-xs text-(--text-tertiary)">
          {building.total > 0
            ? `${formatCount(building.placed)} of ${formatCount(building.total)} pull requests placed, `
            : null}
          {cost(building.spent_usd)} spent. The features shown stay until it finishes.
        </p>
      </div>
      {job ? <ViewerLink href={jobHref(job)}>View in Jobs</ViewerLink> : null}
      {onResume ? (
        <Button variant="secondary" size="compact" onClick={onResume}>
          Resume build
        </Button>
      ) : null}
    </section>
  );
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** The estimate of a feature build and its confirmation, then a link to its
 *  job. */
export function FeatureBuildDialog({
  repository,
  clone,
  resume,
  close,
  started,
}: {
  repository: string;
  clone: string;
  /** A build that stopped is resumed, else a new one is built. */
  resume: boolean;
  close: () => void;
  started: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [job, setJob] = useState<Job | null>(null);
  useEffect(() => {
    dialog.current?.showModal?.();
  }, []);
  useEffect(() => {
    let active = true;
    estimateFeatureBuild(clone).then(
      (answer) => {
        if (active) setEstimate(answer);
      },
      (e) => {
        if (active) setError(errorText(e));
      },
    );
    return () => {
      active = false;
    };
  }, [clone]);
  const item = estimate?.items[0];
  const title = resume ? "Resume the feature history build" : "Build feature history";
  const confirm = async () => {
    if (!estimate) return;
    setPending(true);
    setError("");
    try {
      const jobs = await startFix(estimate);
      setJob(jobs[0] ?? null);
      started();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="build-title">
      <div className="local-dialog-head">
        <div>
          <h2 id="build-title">{title}</h2>
          <p>{repository}</p>
        </div>
      </div>
      {error ? <Alert>{error}</Alert> : null}
      {job ? (
        <p className="my-3" role="status">
          The build started. It runs as a job; the features shown stay until it finishes.{" "}
          <ViewerLink href={jobHref(job)}>View it in Jobs</ViewerLink>
        </p>
      ) : !item && !error ? (
        <LoadingStatus block>Estimating the build…</LoadingStatus>
      ) : item?.blocked_reason ? (
        <p className="my-3">{jobReasonText(item.blocked_reason)}</p>
      ) : item ? (
        <div className="local-fix-confirmation">
          <p>
            baum places this repository's merged and open pull requests of the last 180 days in
            features, beside the features in use, and writes their text. The new features replace
            the current ones in one step once the build finishes, and the weekly pass consolidates
            them (merging duplicates, splitting oversized ones) right after. It stops before a call
            that would pass the cap; confirming again resumes it.
          </p>
          <dl className="local-fix-facts">
            <dt>Pull requests</dt>
            <dd>{formatCount(item.build_pull_requests ?? 0)} to place</dd>
            <dt>Harness</dt>
            <dd>{harnessName(item.backend ?? "")}</dd>
            <dt>Model</dt>
            <dd>{item.model || "Harness default"}</dd>
            <dt>Estimate</dt>
            <dd>
              {item.estimate_usd == null ? "Cost unknown" : `About ${cost(item.estimate_usd)}`}
            </dd>
            <dt>Spending cap</dt>
            <dd>{cost(item.build_cap_usd ?? 0)} over all its runs</dd>
          </dl>
        </div>
      ) : null}
      <div className="local-dialog-actions">
        <Button variant="ghost" size="compact" onClick={close}>
          {job || item?.blocked_reason ? "Close" : "Cancel"}
        </Button>
        {item && !item.blocked_reason && !job ? (
          <Button size="compact" disabled={pending} onClick={() => void confirm()}>
            {pending ? "Starting…" : resume ? "Resume build" : "Build feature history"}
          </Button>
        ) : null}
      </div>
    </dialog>
  );
}
