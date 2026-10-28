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

/** What `api/timeline/build` says of the repository: its building version
 *  and whether it has features at all, whatever the lists' filters. */
export type BuildState = { building: Building | null; hasFeatures: boolean };

/** The repository's build state, read again when the log or the build job
 *  moves on. A read that fails keeps the last state. */
export function useBuildState(repository: string, key: string): BuildState {
  const [state, setState] = useState<BuildState>({ building: null, hasFeatures: false });
  useEffect(() => {
    const abort = new AbortController();
    request<{ building?: Building | null; has_features?: boolean }>(
      `api/timeline/build?repository=${encodeURIComponent(repository)}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((body) =>
        setState({ building: body.building ?? null, hasFeatures: body.has_features === true }),
      )
      .catch(() => {
        // A transient failure, or an older daemon: what was shown stays.
      });
    return () => abort.abort();
  }, [repository, key]);
  return state;
}

/** Why a feature build stopped or cannot run, in its own words; reasons
 *  shared with other jobs read as they do. */
const buildReasons: Record<string, string> = {
  cost_cap:
    "It reached its spending cap before it finished. Confirm it again to resume where it stopped.",
  spend_unrecorded: "baum could not record what one of its calls spent, so it stopped.",
  unpriced_model:
    "The model has no price in baum's price list, so the build's cost cannot be capped.",
  unknown_model_price:
    "The model has no price in baum's price list, so the build's cost cannot be capped.",
  daily_cap: "The decision log's daily spending cap is reached. It can go on tomorrow.",
  disabled: "Decisions are turned off for this repository, so it cannot be built.",
  preferences_unreadable: "baum could not read its preferences, so it did not make a call.",
  backend_unavailable: "The model backend is not available on this computer.",
  backend_unsupported: "The configured backend cannot make the feature log's calls.",
  failed: "It failed before it finished. Confirm it again to resume.",
};

export function buildReasonText(reason: string): string {
  return buildReasons[reason] ?? jobReasonText(reason);
}

export function jobHref(job: Job): string {
  return appURL(`?page=jobs&job=${encodeURIComponent(job.id)}`);
}

/** A build running, stopped or failed, above the features it does not
 *  replace until it finishes. `onBuild` opens the confirmation, as a resume
 *  or a new build; without a clone it is offered disabled. */
export function BuildStatus({
  job,
  building,
  clone,
  onBuild,
}: {
  job: Job | undefined;
  building: Building | null;
  clone: string | null;
  onBuild: (resume: boolean) => void;
}) {
  const action = (label: string, resume: boolean) => (
    <Button
      variant="secondary"
      size="compact"
      disabled={!clone}
      title={clone ? undefined : NO_CLONE}
      onClick={() => onBuild(resume)}
    >
      {label}
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
  const failed = job?.state === "failed" ? (job.reason ?? "failed") : "";
  if (!building) {
    // The latest build failed before it made its version.
    if (!failed) return null;
    return (
      <section aria-label="Feature build" className="local-job-outcome">
        <p role="status" className="min-w-0 flex-1 font-medium">
          The last feature history build failed: {buildReasonText(failed)}
        </p>
        <ViewerLink href={jobHref(job!)}>View in Jobs</ViewerLink>
        {action("Try again", false)}
      </section>
    );
  }
  const reason = building.stopped || failed;
  const progress = (
    <p className="text-xs text-(--text-tertiary)">
      {building.total > 0
        ? `${formatCount(building.placed)} of ${formatCount(building.total)} pull requests placed, `
        : null}
      {cost(building.spent_usd)} spent. The features shown stay until it finishes.
    </p>
  );
  return (
    <section aria-label="Feature build" className="local-job-outcome">
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {reason === "cost_cap"
            ? "The feature history build stopped at its spending cap — confirm again to resume"
            : reason
              ? `The feature history build stopped. ${buildReasonText(reason)}`
              : "A feature history build is in progress or stopped"}
        </p>
        {progress}
      </div>
      {job ? <ViewerLink href={jobHref(job)}>View in Jobs</ViewerLink> : null}
      {reason ? action("Resume build", true) : null}
    </section>
  );
}

export const NO_CLONE = "Needs a local clone of the repository";

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
  // Counts estimates, so "Estimate again" asks for a new one.
  const [attempt, setAttempt] = useState(0);
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
  }, [clone, attempt]);
  // The estimate a start was refused for (expired, or its inputs changed)
  // is not confirmed again: it is estimated afresh.
  const [refused, setRefused] = useState(false);
  const estimateAgain = () => {
    setEstimate(null);
    setError("");
    setRefused(false);
    setAttempt((n) => n + 1);
  };
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
      setRefused(true);
    } finally {
      setPending(false);
    }
  };
  return (
    <dialog
      ref={dialog}
      onCancel={(event) => {
        // Escape waits for the start request.
        if (pending) event.preventDefault();
        else close();
      }}
      aria-labelledby="build-title"
    >
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
        <p className="my-3">{buildReasonText(item.blocked_reason)}</p>
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
        <Button variant="ghost" size="compact" disabled={pending} onClick={close}>
          {job || item?.blocked_reason ? "Close" : "Cancel"}
        </Button>
        {refused ? (
          <Button size="compact" onClick={estimateAgain}>
            Estimate again
          </Button>
        ) : item && !item.blocked_reason && !job ? (
          <Button size="compact" disabled={pending} onClick={() => void confirm()}>
            {pending ? "Starting…" : resume ? "Resume build" : "Build feature history"}
          </Button>
        ) : null}
      </div>
    </dialog>
  );
}
