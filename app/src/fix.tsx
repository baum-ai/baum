import { useEffect, useRef, useState } from "react";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import { WarningIcon } from "@baum/viewer/components/icons";
import {
  appURL,
  cost,
  estimateFix,
  fixName,
  isActiveJob,
  jobReasonText,
  joinAnd,
  request,
  startFix,
  threadOutcomesSummary,
  threadOutcomeText,
  type Entry,
  type Estimate,
  type FixInputs,
  type FixOnly,
  type FixThread,
  type Job,
} from "./api";
import { ViewerLink } from "@baum/viewer/navigation";
import { LoadingStatus } from "./nine-dots";
import { harnessName } from "./desktop/reviews";

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

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** What the agent does, in a sentence. */
function fixSentence(fix: FixInputs, branch: string) {
  const tasks = [
    fix.conflicts ? "resolves the conflicts" : "",
    fix.checks ? "fixes the failing checks" : "",
    fix.threads ? "addresses the unresolved review comments" : "",
  ].filter(Boolean);
  if (fix.conflicts)
    return `An agent merges ${fix.base_ref ?? "the base branch"} into ${branch}${tasks.length === 1 ? " and " : ", "}${joinAnd(tasks)}.`;
  return `An agent ${joinAnd(tasks.length ? tasks : ["fixes the failing checks"])} on ${branch}.`;
}

/** Where a thread is: its file and line. */
function threadPlace(thread: FixThread) {
  return thread.line ? `${thread.path}:${thread.line}` : thread.path;
}

/** The review threads a fix addresses, as the confirmation lists them. */
function FixThreads({ item }: { item: Estimate["items"][number] }) {
  const threads = item.fix_threads ?? [];
  if (!threads.length) return null;
  return (
    <div className="local-fix-threads">
      <p>
        baum replies on each thread the agent addressed and resolves it, as your GitHub account,
        after the push. Threads it skips stay open.
      </p>
      <ul aria-label="Review threads">
        {threads.map((thread) => (
          <li key={thread.ref}>
            <code>{threadPlace(thread)}</code>
            {thread.outdated ? <span className="local-fix-thread-note"> outdated</span> : null}
            <span className="local-fix-thread-excerpt">
              {thread.author ? `${thread.author}: ` : ""}
              {thread.excerpt}
            </span>
          </li>
        ))}
      </ul>
      {item.fix_threads_capped ? (
        <p className="local-fix-thread-note">
          Only the first {threads.length} unresolved threads are included.
        </p>
      ) : null}
      {item.fix_threads_untrusted ? (
        <p className="local-fix-thread-note">
          {item.fix_threads_untrusted === 1
            ? "1 thread started by an account without access to the repository is left out."
            : `${item.fix_threads_untrusted} threads started by accounts without access to the repository are left out.`}
        </p>
      ) : null}
    </div>
  );
}

/** Confirms a fix before anything runs: what is broken, the harness and model
 *  that will fix it, the branch baum pushes to, and that the agent gets write
 *  access to a temporary checkout. A fix never starts without it. */
export function FixDialog({
  entry,
  clone,
  only,
  close,
  started,
}: {
  entry: Entry;
  clone: string;
  /** Limits the fix, as Fix CI on the Inbox's failing checks does. */
  only?: FixOnly;
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
    estimateFix(clone, entry.pull_request.number, only).then(
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
  }, [clone, entry.pull_request.number, only]);
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
            {fixSentence(fix, branch)} baum then commits the result and pushes it to {branch} on
            GitHub with your own git credentials. It never force-pushes.
          </p>
          <FixThreads item={item} />
          <dl className="local-fix-facts">
            <dt>Harness</dt>
            <dd>{harnessName(item.backend ?? "")}</dd>
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
