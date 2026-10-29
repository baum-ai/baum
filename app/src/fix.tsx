import { useState } from "react";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import {
  appURL,
  fixName,
  isActiveJob,
  jobReasonText,
  request,
  threadOutcomesSummary,
  threadOutcomeText,
  type FixThread,
  type Job,
} from "./api";
import { ViewerLink } from "@baum/viewer/navigation";

export const phases: Record<string, string> = {
  queued: "Queued",
  preparing: "Checking the pull request",
  "reading checks": "Reading the failing checks",
  "reading review comments": "Reading the review comments",
  fetching: "Fetching the branch",
  merging: "Merging the base branch",
  fixing: "The agent is fixing it",
  committing: "Committing",
  pushing: "Pushing",
  replying: "Replying on the review threads",
  finishing: "Finishing",
};

/** Where a fix the person asked for stands before its job exists: on its way
 *  to the daemon, or refused with the reason in words. */
export type FixState = { starting: boolean; error: string };

/** What a running fix is doing, in words. */
export function phaseText(job: Job) {
  return job.state === "queued" ? "Queued" : (phases[job.phase] ?? job.phase);
}

/** The link to a fix's transcript: Watch while it runs. Nothing without one. */
export function TranscriptLink({ job }: { job: Job }) {
  if (!job.has_transcript) return null;
  return (
    <ViewerLink href={appURL(`?page=jobs&job=${encodeURIComponent(job.id)}`)}>
      {isActiveJob(job) ? "Watch" : "Transcript"}
    </ViewerLink>
  );
}

/** Where a thread is: its file and line. */
function threadPlace(thread: FixThread) {
  return thread.line ? `${thread.path}:${thread.line}` : thread.path;
}

/** A fix of the pull request on its page: running, pushed or failed. */
export function FixNotice({ job, changed }: { job: Job; changed: () => Promise<void> }) {
  const [error, setError] = useState("");
  const [cancelling, setCancelling] = useState(false);
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
        <TranscriptLink job={job} />
        <Button
          variant="secondary"
          size="compact"
          disabled={cancelling}
          onClick={() => {
            setCancelling(true);
            setError("");
            void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
              .then(changed)
              .catch(() => setError("The fix could not be cancelled. Try again."))
              .finally(() => setCancelling(false));
          }}
        >
          {cancelling ? "Cancelling…" : "Cancel fix"}
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
      <TranscriptLink job={job} />
    </section>
  );
}

/** What the agent said it did, what became of each review thread, and a
 *  failure's own words. */
export function FixDetails({ job }: { job: Job }) {
  const outcomes = job.thread_outcomes ?? [];
  const threads = new Map((job.fix_threads ?? []).map((thread) => [thread.ref, thread]));
  return (
    <>
      {outcomes.length ? (
        <details className="local-fix-details">
          <summary>{threadOutcomesSummary(outcomes)}</summary>
          <ul className="local-fix-outcomes">
            {outcomes.map((outcome) => {
              const thread = threads.get(outcome.ref);
              return (
                <li key={outcome.ref}>
                  <code>{thread ? threadPlace(thread) : outcome.ref}</code>{" "}
                  <span className={`local-fix-outcome local-fix-outcome-${outcome.outcome}`}>
                    {threadOutcomeText(outcome.outcome)}
                  </span>
                  {outcome.summary ? <span>: {outcome.summary}</span> : null}
                </li>
              );
            })}
          </ul>
        </details>
      ) : null}
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
