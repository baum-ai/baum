import { useEffect, useState, type ComponentType, type ReactNode } from "react";
import { Alert, LiveDot } from "@baum/viewer/components/ui";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleCrossIcon,
} from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  appURL,
  jobName,
  isActiveJob,
  jobReasonText,
  LocalRequestError,
  request,
  threadOutcomesSummary,
  type Job,
} from "./api";
import { phaseText, ThreadOutcomes } from "./fix";
import { jobOrigin, jobTarget, jobType, useJobTimes } from "./job-info";
import { UsageBar } from "./usage";
import { LoadingStatus } from "./nine-dots";
import { ReadFailed } from "./page-state";
import { PageBar, pageScroll } from "./shell";
import { useFollowScroll } from "./session-transcript";

/** One line of a fix agent's transcript, from `api/jobs/{id}/transcript`.
 *  Entries are append-only; a tool's later entry with the same `id` updates
 *  the earlier one. */
export type TranscriptEntry = {
  seq: number;
  time: string;
  kind: "message" | "reasoning" | "tool" | "error" | "call";
  id?: string;
  tool?: string;
  title?: string;
  text?: string;
  status?: "running" | "completed" | "failed";
  /** Which of a review's calls the entry belongs to, as its "call" entry's
   *  title names it. */
  call?: string;
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

export type TranscriptState =
  | { kind: "loading" }
  | { kind: "ready"; entries: TranscriptEntry[]; done: boolean }
  | { kind: "missing" }
  | { kind: "failed"; message: string; entries: TranscriptEntry[] };

/** The transcript read from the start, then again at once while the daemon
 *  has more, otherwise every second until it is done. Stops when unmounted. */
export function useJobTranscript(id: string, attempt = 0): TranscriptState {
  const [state, setState] = useState<TranscriptState>({ kind: "loading" });
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
  }, [id, attempt]);
  return state;
}

function ToolStatus({ status }: { status?: TranscriptEntry["status"] }) {
  if (status === "failed") return <CircleCrossIcon size={14} className="local-tool-failed" />;
  if (status === "completed") return <CircleCheckIcon size={14} className="local-tool-done" />;
  return <LiveDot />;
}

/** Codex's tools by the names a reader knows them by. */
const TOOL_NAMES: Record<string, string> = {
  command: "Run",
  file_change: "Edit",
  mcp_tool_call: "MCP",
  web_search: "Search",
};

/** A tool's title without the tool's name, which Claude's titles start
 *  with. */
export function toolArgument(entry: TranscriptEntry): string {
  const { tool = "", title = "" } = entry;
  if (title === tool) return "";
  return tool && title.startsWith(`${tool} `) ? title.slice(tool.length + 1) : title;
}

function ToolView({ entry }: { entry: TranscriptEntry }) {
  const argument = toolArgument(entry);
  return (
    <details className="local-job-tool">
      <summary>
        <ToolStatus status={entry.status} />
        <strong>{TOOL_NAMES[entry.tool ?? ""] ?? entry.tool}</strong>
        {argument ? <span className="local-job-tool-title">{argument}</span> : null}
        <span className="sr-only">{entry.status ?? "running"}</span>
      </summary>
      {entry.text ? <pre>{entry.text}</pre> : <p className="local-job-tool-empty">No output.</p>}
    </details>
  );
}

/** A run of tool calls with nothing said between them: the latest call, with
 *  any earlier ones folded away above it. */
function ToolRun({ tools }: { tools: TranscriptEntry[] }) {
  const latest = tools[tools.length - 1]!;
  const earlier = tools.slice(0, -1);
  const failed = earlier.filter((tool) => tool.status === "failed").length;
  return (
    <div className="local-job-tools">
      {earlier.length > 0 ? (
        <details className="local-job-tools-earlier">
          <summary>
            <ChevronRightIcon size={14} className="local-job-tools-chevron" />
            <span>
              {earlier.length} earlier tool {earlier.length === 1 ? "call" : "calls"}
              {failed > 0 ? <span className="local-tool-failed">, {failed} failed</span> : null}
            </span>
          </summary>
          <ol>
            {earlier.map((tool) => (
              <li key={entryKey(tool)}>
                <ToolView entry={tool} />
              </li>
            ))}
          </ol>
        </details>
      ) : null}
      <ToolView entry={latest} />
    </div>
  );
}

export function EntryView({ entry }: { entry: TranscriptEntry }) {
  switch (entry.kind) {
    case "message":
      return (
        <Markdown html={false} className="local-job-message">
          {entry.text ?? ""}
        </Markdown>
      );
    case "reasoning":
      return (
        <details className="local-job-reasoning">
          <summary>Thinking</summary>
          <Markdown html={false}>{entry.text ?? ""}</Markdown>
        </details>
      );
    case "error":
      return <Alert>{entry.text || entry.title}</Alert>;
    case "tool":
      return <ToolRun tools={[entry]} />;
    case "call":
      return (
        <p className="local-job-call">
          <ToolStatus status={entry.status} />
          <strong>{entry.title}</strong>
          <span className="sr-only">{entry.status ?? "running"}</span>
        </p>
      );
  }
}

const entryKey = (entry: TranscriptEntry) =>
  entry.id ? `${entry.kind}:${entry.id}` : String(entry.seq);

type Item = { kind: "entry"; entry: TranscriptEntry } | { kind: "tools"; tools: TranscriptEntry[] };

const isReasoning = (item: Item) => item.kind === "entry" && item.entry.kind === "reasoning";

const itemCall = (item: Item) => (item.kind === "tools" ? item.tools[0]!.call : item.entry.call);

/** Entries with each run of consecutive tool calls gathered into one item.
 *  Reasoning does not break a run: the run moves below it and goes on.
 *  Anything the agent says does. */
export function groupEntries(entries: TranscriptEntry[]): Item[] {
  const items: Item[] = [];
  for (const entry of entries) {
    if (entry.kind !== "tool") {
      items.push({ kind: "entry", entry });
      continue;
    }
    let at = items.length - 1;
    while (at >= 0 && isReasoning(items[at]!)) at--;
    const run = items[at];
    // Calls of a review that run at once interleave: a run stays one call's.
    if (run?.kind === "tools" && run.tools[0]!.call === entry.call) {
      items.splice(at, 1);
      run.tools.push(entry);
      items.push(run);
    } else {
      items.push({ kind: "tools", tools: [entry] });
    }
  }
  return items;
}

/** What a job is called on its page: a fix, question or details job by its
 *  own name, any other by its kind. */
function pageName(job: Job) {
  return job.kind && job.kind !== "feature_build" ? jobName(job) : jobType(job);
}
const endings: Record<string, string> = {
  cancelled: "was cancelled",
  interrupted: "was interrupted",
  failed: "failed",
};
/** What a job is doing or did: its phase while it runs, its result once it
 *  is done. */
function JobHeader({ job }: { job: Job }) {
  const name = pageName(job);
  const active = isActiveJob(job);
  const times = useJobTimes(job);
  const meta = [
    jobOrigin(job),
    times.took
      ? times.running
        ? `Running for ${times.took}`
        : `Ran ${times.took}${times.finished ? `, finished ${times.finished}` : ""}`
      : times.queued
        ? `Queued ${times.queued}`
        : null,
  ].filter(Boolean);
  return (
    <section aria-label="Job" className="local-panel local-job-head">
      {active ? (
        <p role="status" className="local-job-phase">
          <LiveDot /> {name}: {phaseText(job)}
        </p>
      ) : (
        <p role="status" className="local-job-phase">
          {job.state === "completed" ? (
            <CircleCheckIcon size={16} className="local-job-pushed" />
          ) : job.state === "failed" ? (
            <CircleCrossIcon size={16} className="local-tool-failed" />
          ) : null}
          {job.state === "completed" && job.commit ? (
            <>
              {name}: pushed{" "}
              <a href={job.commit_url} target="_blank" rel="noreferrer">
                <code>{job.commit.slice(0, 7)}</code>
              </a>
            </>
          ) : job.state === "completed" ? (
            `${name} ${job.kind === "chat" ? "was answered" : "completed"}`
          ) : (
            `${name} ${endings[job.state] ?? `was ${job.state}`}`
          )}
        </p>
      )}
      {meta.length ? <p className="local-job-head-meta">{meta.join(" · ")}</p> : null}
      {!active && job.reason ? <p>{jobReasonText(job.reason, job.kind)}</p> : null}
      {!active && job.detail ? <pre className="local-job-detail">{job.detail}</pre> : null}
      {!active && job.summary ? (
        <Markdown html={false} className="local-job-summary">
          {job.summary}
        </Markdown>
      ) : null}
      {job.thread_outcomes?.length ? (
        <div aria-label="Review threads" role="group" className="local-job-threads">
          <h2>{threadOutcomesSummary(job.thread_outcomes)}</h2>
          <ThreadOutcomes job={job} />
        </div>
      ) : null}
    </section>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
/** Everything the daemon keeps about a job beyond its outcome: what it ran
 *  on, who started it, when, and with which agent. */
function JobFacts({
  job,
  title,
  Link,
}: {
  job: Job;
  title?: string;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const times = useJobTimes(job);
  const target = jobTarget(job);
  return (
    <section aria-label="Details" className="local-panel local-job-section local-job-facts">
      <h2>Details</h2>
      <dl>
        {target ? (
          <Fact label={job.group ? "Review group" : "Pull request"}>
            <Link href={target}>
              {job.group ? job.group_name || job.group : `#${job.pr}${title ? ` ${title}` : ""}`}
            </Link>
          </Fact>
        ) : null}
        <Fact label="Repository">{job.repository}</Fact>
        {job.kind === "fix" && job.fix?.head_ref ? (
          <Fact label="Branch">
            <code>{job.fix.head_ref}</code>
          </Fact>
        ) : null}
        {job.head_sha ? (
          <Fact label="Head commit">
            <code>{job.head_sha.slice(0, 7)}</code>
          </Fact>
        ) : null}
        {job.version ? <Fact label="Review version">{job.version}</Fact> : null}
        {times.queued ? <Fact label="Queued">{times.queued}</Fact> : null}
        {times.started ? <Fact label="Started">{times.started}</Fact> : null}
        {times.finished ? <Fact label="Finished">{times.finished}</Fact> : null}
        {times.took ? (
          <Fact label="Run time">{times.running ? `${times.took} so far` : times.took}</Fact>
        ) : null}
        {job.backend ? <Fact label="Agent">{job.backend}</Fact> : null}
        {job.model ? <Fact label="Model">{job.model}</Fact> : null}
        <Fact label="Model calls">
          {job.calls}
          {job.repairs ? ` (${job.repairs} ${job.repairs === 1 ? "repair" : "repairs"})` : ""}
        </Fact>
        {job.failed_sections?.length ? (
          <Fact label="Published without details">{job.failed_sections.join(", ")}</Fact>
        ) : null}
        {job.context_failed ? <Fact label="Codebase context">Missing</Fact> : null}
        <Fact label="Job ID">
          <code title={job.id}>{job.id}</code>
        </Fact>
      </dl>
    </section>
  );
}

/** A fix agent's transcript, following the end while the reader is there. */
export function JobTranscript({
  job,
  usage,
  children,
}: {
  job: Job;
  /** What the job used, above its state. */
  usage?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <>
      {usage}
      <JobHeader job={job} />
      {children}
      {job.has_transcript ? (
        <TranscriptView job={job} />
      ) : (
        <section aria-label="Transcript" className="local-panel local-job-section">
          <h2>Transcript</h2>
          <p>
            {isActiveJob(job)
              ? "The agent's transcript shows here once it starts."
              : job.kind === "feature_build"
                ? "A feature build doesn't keep a transcript."
                : "No transcript was kept for this job. Reviews keep one from this version of baum on."}
          </p>
        </section>
      )}
    </>
  );
}
function TranscriptView({ job }: { job: Job }) {
  const [attempt, setAttempt] = useState(0);
  const state = useJobTranscript(job.id, attempt);
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
      {state.kind === "loading" ? (
        <LoadingStatus block>Reading the agent's transcript…</LoadingStatus>
      ) : state.kind === "missing" ? (
        <section className="local-panel local-transcript-missing">
          <h2>No transcript to show</h2>
          <p>The agent left no transcript for this job.</p>
        </section>
      ) : state.kind === "failed" && state.entries.length === 0 ? (
        <ReadFailed
          what="the agent's transcript"
          message={state.message}
          retry={() => setAttempt((n) => n + 1)}
        />
      ) : (
        <section aria-label="Transcript" className="local-transcript">
          {state.kind === "failed" ? <Alert>{state.message}</Alert> : null}
          <ol className="local-panel local-job-transcript">
            {entries.length === 0 ? (
              <li className="local-job-tool-empty">Nothing yet.</li>
            ) : (
              groupEntries(entries).map((item, i, items) => {
                const call = itemCall(item);
                // Where calls running at once interleave, a step names its
                // call when the one before it was another's.
                const named =
                  call &&
                  !(item.kind === "entry" && item.entry.kind === "call") &&
                  i > 0 &&
                  itemCall(items[i - 1]!) !== call ? (
                    <span className="local-job-call-tag">{call}</span>
                  ) : null;
                return item.kind === "tools" ? (
                  // Keyed by the run's first call, so a run that grows keeps
                  // its open or closed state.
                  <li key={entryKey(item.tools[0]!)}>
                    {named}
                    <ToolRun tools={item.tools} />
                  </li>
                ) : (
                  <li key={entryKey(item.entry)}>
                    {named}
                    <EntryView entry={item.entry} />
                  </li>
                );
              })
            )}
          </ol>
        </section>
      )}
    </>
  );
}

/** `?page=jobs&job=<id>`: what the job is doing or did, everything kept
 *  about it, its usage, then the agent's transcript. `job` is null when the
 *  list does not have it; `title` is its pull request's, when baum knows it. */
export function JobPage({
  job,
  title,
  loading,
  Link,
}: {
  job: Job | null;
  title?: string;
  loading: boolean;
  Link: ComponentType<ViewerLinkProps>;
}) {
  useEffect(() => {
    pageScroll().to(0);
  }, [job?.id]);
  return (
    <>
      <PageBar
        title={job ? jobType(job) : "Job"}
        detail={
          !job
            ? "Agent run on this computer"
            : job.kind === "feature_build"
              ? job.repository
              : job.group
                ? `${job.group_name || job.group} · ${job.repository}`
                : `${job.repository} #${job.pr}${title ? ` ${title}` : ""}`
        }
        back={
          <Link
            href={appURL("?page=jobs")}
            className="local-page-back"
            aria-label="Back to jobs"
            title="Back to jobs (Esc)"
          >
            <ChevronLeftIcon size={18} />
            <kbd aria-hidden="true" className="key-hint">
              Esc
            </kbd>
          </Link>
        }
      />
      <div className="local-page-body local-job-page">
        {job ? (
          <JobTranscript
            key={job.id}
            job={job}
            usage={
              <UsageBar
                accounting={job.accounting}
                charged={job.tokens}
                amount={job.actual_cost_usd}
                reason={job.cost_reason}
              />
            }
          >
            <JobFacts job={job} title={title} Link={Link} />
          </JobTranscript>
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
