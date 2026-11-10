import { useCallback, useState } from "react";
import { formatCount, formatRelative } from "@baum/viewer/components/format";
import { Alert, Button, IconButton, LiveDot } from "@baum/viewer/components/ui";
import {
  CloseIcon,
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
} from "@baum/viewer/components/icons";
import { SMALL } from "./action-classes";
import {
  appURL,
  changedNothing,
  fixName,
  isActiveJob,
  jobHref,
  jobReasonText,
  request,
  summaryLead,
  threadOutcomesSummary,
  threadOutcomeText,
  type FixThread,
  type Job,
} from "./api";
import { Markdown } from "@baum/viewer/components/markdown";
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

/** A running fix's step, for a line that already says an agent is fixing
 *  it: empty while the agent itself works, which would say that again. */
export function fixStep(job: Job) {
  return job.state === "running" && job.phase === "fixing" ? "" : phaseText(job);
}

/** The link to a fix's transcript: Watch while it runs. Nothing without one.
 *  `keyHint` draws `t` on it where that key opens it. */
export function TranscriptLink({
  job,
  keyHint = false,
  boxed = false,
}: {
  job: Job;
  keyHint?: boolean;
  /** Drawn as a small button, as the one action of a notice. */
  boxed?: boolean;
}) {
  if (!job.has_transcript) return null;
  return (
    <ViewerLink href={jobHref(job)} className={boxed ? SMALL : undefined}>
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
  if (changedNothing(job)) return <>changed nothing</>;
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
export function FixNotice({
  job,
  changed,
  dismiss,
}: {
  job: Job;
  changed: () => Promise<void>;
  /** Hides a finished fix's notice; it stays in the fix runs. */
  dismiss?: () => void;
}) {
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
  const pushed = job.state === "completed" && !!job.commit;
  // A fix that changed nothing is the agent's judgement, not an error: no
  // red cross, and its reason is what the agent said.
  const unchanged = changedNothing(job);
  const why = unchanged ? summaryLead(job.summary) : "";
  const Icon = pushed
    ? CircleCheckIcon
    : job.state === "failed" && !unchanged
      ? CircleCrossIcon
      : CircleDashedIcon;
  return (
    <section aria-label="Fix" className="local-job-outcome local-fix-notice">
      <Icon
        size={16}
        className={`local-fix-notice-icon local-fix-notice-${pushed ? "pushed" : unchanged ? "unchanged" : job.state}`}
      />
      <div className="min-w-0 flex-1">
        <p role="status" className="local-fix-notice-title">
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
          ) : unchanged ? (
            `${name} changed nothing, so nothing was pushed`
          ) : job.state === "failed" ? (
            `${name} failed`
          ) : (
            `${name} was ${job.state}`
          )}
        </p>
        {why ? (
          <Markdown html={false} className="local-fix-notice-why">
            {why}
          </Markdown>
        ) : job.reason && !unchanged ? (
          <p>{jobReasonText(job.reason)}</p>
        ) : null}
        <FixDetails job={job} leadShown={Boolean(why)} />
      </div>
      <div className="local-fix-notice-actions">
        <TranscriptLink job={job} keyHint boxed />
        {dismiss ? (
          <IconButton label={`Dismiss: ${name}`} onClick={dismiss}>
            <CloseIcon size={14} />
          </IconButton>
        ) : null}
      </div>
    </section>
  );
}

// The finished fixes whose notice the person dismissed, the latest few, kept
// in this window's browser: a dismissed fix stays in the page's fix runs.
const DISMISSED_KEY = "baum.pr.dismissed-fixes.v1";
function readDismissed(): string[] {
  try {
    const ids = JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? "[]");
    return Array.isArray(ids) ? ids.filter((id): id is string => typeof id === "string") : [];
  } catch {
    return [];
  }
}

/** The fix jobs whose notice was dismissed, and dismissing one. */
export function useDismissedFixes(): [string[], (id: string) => void] {
  const [ids, setIds] = useState(readDismissed);
  const dismiss = useCallback((id: string) => {
    const next = [...readDismissed().filter((known) => known !== id), id].slice(-50);
    try {
      localStorage.setItem(DISMISSED_KEY, JSON.stringify(next));
    } catch {
      // Dismissed until the page is opened again.
    }
    setIds(next);
  }, []);
  return [ids, dismiss];
}

/** What became of each review thread a fix answered. */
export function ThreadOutcomes({ job }: { job: Job }) {
  const threads = new Map((job.fix_threads ?? []).map((thread) => [thread.ref, thread]));
  return (
    <ul className="local-fix-outcomes">
      {(job.thread_outcomes ?? []).map((outcome) => {
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
  );
}
/** What the agent said it did, what became of each review thread, and a
 *  failure's own words. `leadShown` says the caller already shows the first
 *  paragraph of the agent's summary above (`summaryLead`). */
export function FixDetails({ job, leadShown = false }: { job: Job; leadShown?: boolean }) {
  const outcomes = job.thread_outcomes ?? [];
  if (!outcomes.length && !job.summary && !job.detail) return null;
  // A fold holding only the lead shown above again would add nothing.
  if (
    leadShown &&
    !outcomes.length &&
    !job.detail &&
    job.summary?.trim() === summaryLead(job.summary)
  )
    return null;
  // One fold: what happened to the threads names it, the agent's account follows.
  return (
    <details className="local-fix-details">
      <summary>
        {outcomes.length
          ? threadOutcomesSummary(outcomes)
          : job.summary
            ? leadShown
              ? "Everything the agent said"
              : "What the agent did"
            : "What happened"}
      </summary>
      {outcomes.length ? <ThreadOutcomes job={job} /> : null}
      {job.summary ? <p>{job.summary}</p> : null}
      {job.detail ? <pre>{job.detail}</pre> : null}
    </details>
  );
}
