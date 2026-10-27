import { useEffect, useState, type ComponentType } from "react";
import { Alert, LiveDot } from "@baum/viewer/components/ui";
import { CircleCheckIcon, CircleCrossIcon, CircleDashedIcon } from "@baum/viewer/components/icons";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  appURL,
  fixName,
  isActiveJob,
  jobReasonText,
  LocalRequestError,
  request,
  type Job,
} from "./api";
import { phaseText } from "./fix";
import { LoadingStatus } from "./nine-dots";
import { PageBar } from "./shell";
import { useFollowScroll } from "./session-transcript";

/** One line of a fix agent's transcript, from `api/jobs/{id}/transcript`.
 *  Entries are append-only; a tool's later entry with the same `id` updates
 *  the earlier one. */
export type TranscriptEntry = {
  seq: number;
  time: string;
  kind: "message" | "reasoning" | "tool" | "error";
  id?: string;
  tool?: string;
  title?: string;
  text?: string;
  status?: "running" | "completed" | "failed";
};
type Page = { entries: TranscriptEntry[]; next: number; more: boolean; done: boolean };

const POLL_MS = 1000;
const RETRY_MS = 3000;

/** Entries as they are shown: a tool's update takes the place of its first
 *  entry. */
export function mergeEntries(
  shown: TranscriptEntry[],
  incoming: TranscriptEntry[],
): TranscriptEntry[] {
  const merged = shown.slice();
  for (const entry of incoming) {
    const at = entry.id ? merged.findIndex((e) => e.id === entry.id && e.kind === entry.kind) : -1;
    if (at >= 0) merged[at] = { ...merged[at]!, ...entry, seq: merged[at]!.seq };
    else merged.push(entry);
  }
  return merged;
}

type State =
  | { kind: "loading" }
  | { kind: "ready"; entries: TranscriptEntry[]; done: boolean }
  | { kind: "missing" }
  | { kind: "failed"; message: string; entries: TranscriptEntry[] };

/** The transcript read from the start, then again at once while the daemon
 *  has more, otherwise every second until it is done. Stops when unmounted. */
function useJobTranscript(id: string): State {
  const [state, setState] = useState<State>({ kind: "loading" });
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const controller = new AbortController();
    let entries: TranscriptEntry[] = [];
    let after = 0;
    setState({ kind: "loading" });
    const read = async () => {
      let wait = POLL_MS;
      try {
        const page = await request<Page>(
          `api/jobs/${encodeURIComponent(id)}/transcript?after=${after}`,
          undefined,
          "GET",
          controller.signal,
        );
        if (stopped) return;
        entries = mergeEntries(entries, page.entries ?? []);
        after = page.next;
        setState({ kind: "ready", entries, done: page.done });
        if (page.done && !page.more) return;
        wait = page.more ? 0 : POLL_MS;
      } catch (e) {
        if (stopped) return;
        if (e instanceof LocalRequestError && e.status === 404 && entries.length === 0) {
          setState({ kind: "missing" });
          return;
        }
        setState({
          kind: "failed",
          message: e instanceof Error ? e.message : "Local request failed.",
          entries,
        });
        wait = RETRY_MS;
      }
      timer = setTimeout(() => void read(), wait);
    };
    void read();
    return () => {
      stopped = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [id]);
  return state;
}

function ToolStatus({ status }: { status?: TranscriptEntry["status"] }) {
  if (status === "failed") return <CircleCrossIcon size={14} className="local-tool-failed" />;
  if (status === "completed") return <CircleCheckIcon size={14} className="local-tool-done" />;
  return <CircleDashedIcon size={14} className="local-spin" />;
}

function EntryView({ entry }: { entry: TranscriptEntry }) {
  switch (entry.kind) {
    case "message":
      return <p className="local-job-message">{entry.text}</p>;
    case "reasoning":
      return (
        <details className="local-job-reasoning">
          <summary>Thinking</summary>
          <p>{entry.text}</p>
        </details>
      );
    case "error":
      return <Alert>{entry.text || entry.title}</Alert>;
    case "tool":
      return (
        <details className="local-job-tool">
          <summary>
            <ToolStatus status={entry.status} />
            <strong>{entry.tool}</strong>
            {entry.title ? <span>{entry.title}</span> : null}
            <span className="sr-only">{entry.status ?? "running"}</span>
          </summary>
          {entry.text ? (
            <pre>{entry.text}</pre>
          ) : (
            <p className="local-job-tool-empty">No output.</p>
          )}
        </details>
      );
  }
}

/** What a fix job is doing or did: its phase while it runs, its result once
 *  it is done. */
function JobHeader({ job }: { job: Job }) {
  const name = fixName(job.fix ?? {});
  const active = isActiveJob(job);
  return (
    <section aria-label="Job" className="local-panel local-job-head">
      {active ? (
        <p role="status" className="local-job-phase">
          <LiveDot /> {name}: {phaseText(job)}
        </p>
      ) : (
        <p role="status" className="local-job-phase">
          {job.state === "completed" && job.commit ? (
            <>
              {name}: pushed{" "}
              <a href={job.commit_url} target="_blank" rel="noreferrer">
                <code>{job.commit.slice(0, 7)}</code>
              </a>
            </>
          ) : job.state === "failed" ? (
            `${name} failed`
          ) : (
            `${name} was ${job.state}`
          )}
        </p>
      )}
      {!active && job.reason ? <p>{jobReasonText(job.reason)}</p> : null}
      {!active && job.detail ? <pre className="local-job-detail">{job.detail}</pre> : null}
      {!active && job.summary ? <p className="local-job-summary">{job.summary}</p> : null}
    </section>
  );
}

/** A fix agent's transcript, following the end while the reader is there. */
export function JobTranscript({ job }: { job: Job }) {
  const state = useJobTranscript(job.id);
  const entries = state.kind === "ready" || state.kind === "failed" ? state.entries : [];
  const last = entries[entries.length - 1];
  useFollowScroll(
    state.kind === "loading"
      ? undefined
      : `${entries.length}:${last?.status ?? ""}:${last?.text?.length ?? 0}`,
    isActiveJob(job),
  );
  return (
    <>
      <JobHeader job={job} />
      {state.kind === "loading" ? (
        <LoadingStatus block>Reading the agent's transcript…</LoadingStatus>
      ) : state.kind === "missing" ? (
        <section className="local-panel local-transcript-missing">
          <h2>No transcript to show</h2>
          <p>The agent left no transcript for this job.</p>
        </section>
      ) : (
        <section aria-label="Transcript" className="local-transcript">
          {state.kind === "failed" ? <Alert>{state.message}</Alert> : null}
          <p className="local-transcript-note">Secrets are masked.</p>
          <ol className="local-panel local-job-transcript">
            {entries.length === 0 ? (
              <li className="local-job-tool-empty">Nothing yet.</li>
            ) : (
              entries.map((entry) => (
                <li key={entry.id ? `${entry.kind}:${entry.id}` : entry.seq}>
                  <EntryView entry={entry} />
                </li>
              ))
            )}
          </ol>
        </section>
      )}
    </>
  );
}

/** `?page=jobs&job=<id>`: the job, then the agent's transcript. `job` is null
 *  when the list does not have it. */
export function JobPage({
  job,
  loading,
  Link,
}: {
  job: Job | null;
  loading: boolean;
  Link: ComponentType<ViewerLinkProps>;
}) {
  useEffect(() => {
    window.scrollTo({ top: 0 });
  }, [job?.id]);
  return (
    <>
      <PageBar
        title={job ? fixName(job.fix ?? {}) : "Job"}
        detail={job ? `${job.repository} #${job.pr}` : "Agent run on this computer"}
        actions={
          <Link href={appURL("?page=jobs")} className="inbox-action">
            Back to jobs
          </Link>
        }
      />
      <div className="local-page-body">
        {job ? (
          <JobTranscript key={job.id} job={job} />
        ) : loading ? (
          <LoadingStatus block>Loading job…</LoadingStatus>
        ) : (
          <section className="local-panel local-transcript-missing">
            <h2>Job not found</h2>
            <p>It may be older than the jobs this computer keeps.</p>
          </section>
        )}
      </div>
    </>
  );
}
