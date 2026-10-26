import { useEffect, useRef, useState } from "react";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import { WarningIcon } from "@baum/viewer/components/icons";
import {
  cost,
  estimateFix,
  fixName,
  isActiveJob,
  jobReasonText,
  request,
  startFix,
  type Entry,
  type Estimate,
  type Job,
} from "./api";
import { LoadingStatus } from "./nine-dots";

const phases: Record<string, string> = {
  queued: "Queued",
  preparing: "Checking the pull request",
  "reading checks": "Reading the failing checks",
  fetching: "Fetching the branch",
  merging: "Merging the base branch",
  fixing: "The agent is fixing it",
  committing: "Committing",
  pushing: "Pushing",
  finishing: "Finishing",
};

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** Confirms a fix before anything runs: what is broken, the harness and model
 *  that will fix it, the branch baum pushes to, and that the agent gets write
 *  access to a temporary checkout. A fix never starts without it. */
export function FixDialog({
  entry,
  clone,
  close,
  started,
}: {
  entry: Entry;
  clone: string;
  close: () => void;
  started: () => Promise<void>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    let active = true;
    estimateFix(clone, entry.pull_request.number).then(
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
  }, [clone, entry.pull_request.number]);
  const item = estimate?.items[0];
  const fix = item?.fix ?? {};
  const title = item && !item.blocked_reason ? fixName(fix) : "Fix this pull request";
  const branch = fix.head_ref ?? entry.pull_request.head_ref ?? "its branch";
  const confirm = async () => {
    if (!estimate) return;
    setPending(true);
    setError("");
    try {
      await startFix(estimate);
      await started();
      close();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="fix-title">
      <div className="local-dialog-head">
        <div>
          <h2 id="fix-title">{title}</h2>
          <p>
            {entry.pull_request.repository} #{entry.pull_request.number}
          </p>
        </div>
      </div>
      {error ? <Alert>{error}</Alert> : null}
      {!item && !error ? (
        <LoadingStatus block>Checking the pull request on GitHub…</LoadingStatus>
      ) : null}
      {item?.blocked_reason ? <p className="my-3">{jobReasonText(item.blocked_reason)}</p> : null}
      {item && !item.blocked_reason ? (
        <div className="local-fix-confirmation">
          <p>
            {fix.conflicts && fix.checks
              ? `An agent merges ${fix.base_ref ?? "the base branch"} into ${branch}, resolves the conflicts and fixes the failing checks.`
              : fix.conflicts
                ? `An agent merges ${fix.base_ref ?? "the base branch"} into ${branch} and resolves the conflicts.`
                : `An agent fixes the failing checks on ${branch}.`}{" "}
            baum then commits the result and pushes it to {branch} on GitHub with your own git
            credentials. It never force-pushes.
          </p>
          <dl className="local-fix-facts">
            <dt>Harness</dt>
            <dd>{item.backend}</dd>
            <dt>Model</dt>
            <dd>{item.model || "Harness default"}</dd>
            <dt>Pushes to</dt>
            <dd>
              <code>{branch}</code>
            </dd>
            <dt>Estimate</dt>
            <dd>
              {item.estimate_usd == null ? "Cost unknown" : `Up to ${cost(item.estimate_usd)}`}
            </dd>
          </dl>
          <p className="local-fix-warning">
            <WarningIcon size={14} className="flex-none" />
            <span>
              The agent runs on this computer with write access to a temporary checkout of the
              branch, and its changes are pushed without a review. Check the commit before you
              merge.
              {item.backend === "claude"
                ? " Claude Code can edit files but not run commands, so it cannot run your tests."
                : item.backend === "codex"
                  ? " Codex can run commands in its sandbox, without network access."
                  : ""}
            </span>
          </p>
        </div>
      ) : null}
      <div className="local-dialog-actions">
        <Button variant="ghost" size="compact" onClick={close}>
          {item?.blocked_reason ? "Close" : "Cancel"}
        </Button>
        {item && !item.blocked_reason ? (
          <Button size="compact" disabled={pending} onClick={() => void confirm()}>
            {pending ? "Starting…" : title}
          </Button>
        ) : null}
      </div>
    </dialog>
  );
}

/** A fix of the pull request on its page: running, pushed or failed. */
export function FixNotice({ job, changed }: { job: Job; changed: () => Promise<void> }) {
  const [error, setError] = useState("");
  const name = fixName(job.fix ?? {});
  if (isActiveJob(job))
    return (
      <section aria-label="Fix" className="local-job-outcome">
        <LiveDot />
        <div className="min-w-0 flex-1">
          <p role="status" className="font-medium">
            {job.state === "queued"
              ? `${name} queued`
              : `${name}: ${phases[job.phase] ?? job.phase}`}
          </p>
        </div>
        <Button
          variant="ghost"
          size="compact"
          onClick={() =>
            void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
              .then(changed)
              .catch(() => setError("The fix could not be cancelled. Try again."))
          }
        >
          Cancel fix
        </Button>
        {error ? <Alert>{error}</Alert> : null}
      </section>
    );
  return (
    <section aria-label="Fix" className="local-job-outcome">
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {job.state === "completed" && job.commit ? (
            <>
              {name}: pushed{" "}
              <a href={job.commit_url} target="_blank" rel="noreferrer">
                <code>{job.commit.slice(0, 7)}</code>
              </a>
              {job.fix?.head_ref ? (
                <>
                  {" "}
                  to <code>{job.fix.head_ref}</code>
                </>
              ) : null}
            </>
          ) : job.state === "failed" ? (
            `${name} failed`
          ) : (
            `${name} was ${job.state}`
          )}
        </p>
        {job.reason ? <p>{jobReasonText(job.reason)}</p> : null}
        <FixDetails job={job} />
      </div>
    </section>
  );
}

/** What the agent said it did, and a failure's own words. */
export function FixDetails({ job }: { job: Job }) {
  return (
    <>
      {job.summary ? (
        <details className="local-fix-details">
          <summary>What the agent did</summary>
          <p>{job.summary}</p>
        </details>
      ) : null}
      {job.detail ? (
        <details className="local-fix-details">
          <summary>What happened</summary>
          <pre>{job.detail}</pre>
        </details>
      ) : null}
    </>
  );
}
