import { useState } from "react";
import { formatCount, formatRelative } from "@baum/viewer/components/format";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import {
  appURL,
  fixName,
  isActiveJob,
  jobHref,
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
  "reading the conversation": "Reading the conversation",
  fetching: "Fetching the branch",
  merging: "Merging the base branch",
  fixing: "The agent is fixing it",
  committing: "Committing",
  pushing: "Pushing",
  replying: "Replying on the review threads",
  finishing: "Finishing",
  answering: "The agent is answering",
  "preparing a new session": "Starting a new agent session",
  "checking citations": "Checking the answer's citations",
};

/** Where a fix the person asked for stands before its job exists: on its way
 *  to the daemon, or refused with the reason in words. */
export type FixState = { starting: boolean; error: string };

/** What a running fix is doing, in words. */
export function phaseText(job: Job) {
  return job.state === "queued" ? "Queued" : (phases[job.phase] ?? job.phase);
}

/** The link to a fix's transcript: Watch while it runs. Nothing without one.
 *  `keyHint` draws `t` on it where that key opens it. */
export function TranscriptLink({ job, keyHint = false }: { job: Job; keyHint?: boolean }) {
  if (!job.has_transcript) return null;
  return (
    <ViewerLink href={jobHref(job)}>
      {isActiveJob(job) ? "Watch" : "Transcript"}
      {keyHint ? (
        <kbd aria-hidden="true" className="key-hint">
          t
        </kbd>
      ) : null}
    </ViewerLink>
  );
}

/** How many runs the history lists before sending the rest to Jobs. */
const RUNS_SHOWN = 5;

/** A pull request's fix runs, newest first, folded to one line: when each
 *  started, how it ended and its transcript. `earlier` says the fix notice
 *  above already shows the latest. */
export function FixRuns({ runs, earlier }: { runs: Job[]; earlier: boolean }) {
  if (!runs.length) return null;
  const count = `${formatCount(runs.length)} ${earlier ? "earlier " : ""}fix ${runs.length === 1 ? "run" : "runs"}`;
  const older = runs.length - RUNS_SHOWN;
  return (
    <details className="local-fix-details local-fix-runs">
      <summary>
        {earlier ? count : `${count}, the last ${formatRelative(runs[0]!.created_at)}`}
        {!earlier && runs.some((job) => job.has_transcript) ? (
          <kbd aria-hidden="true" className="key-hint">
            t
          </kbd>
        ) : null}
      </summary>
      <ol>
        {runs.slice(0, RUNS_SHOWN).map((job) => (
          <li key={job.id}>
            <span className="local-fix-run-when">{formatRelative(job.created_at)}</span>
            <span className="min-w-0 flex-1">
              {fixName(job.fix ?? {})}: <RunOutcome job={job} />
            </span>
            <TranscriptLink job={job} />
          </li>
        ))}
      </ol>
      {older > 0 ? (
        <ViewerLink href={appURL("?page=jobs")}>{formatCount(older)} older in Jobs</ViewerLink>
      ) : null}
    </details>
  );
}

/** How a fix run ended, or what it is doing, in a few words. */
function RunOutcome({ job }: { job: Job }) {
  if (isActiveJob(job)) return <>{phaseText(job)}</>;
  if (job.state === "completed" && job.commit)
    return (
      <>
        pushed{" "}
        <a href={job.commit_url} target="_blank" rel="noreferrer">
          <code>{job.commit.slice(0, 7)}</code>
        </a>
      </>
    );
  if (job.state === "failed")
    return <>failed{job.reason ? `: ${jobReasonText(job.reason)}` : ""}</>;
  return <>{job.state === "completed" ? "finished without pushing" : job.state}</>;
}

/** A conversation with the review's agent a fix is made from: its ids, and the
 *  agent's suggested change when the answer made one, for the dialog to show. */
export type FixChat = { conversation_id: string; turn: number; suggestion?: string };

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
        <TranscriptLink job={job} keyHint />
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
      <TranscriptLink job={job} keyHint />
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
