import { useEffect, useState, type ComponentType } from "react";
import { Alert, LiveDot } from "@baum/viewer/components/ui";
import {
  ChevronRightIcon,
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
} from "@baum/viewer/components/icons";
import { Markdown } from "@baum/viewer/components/markdown";
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

function EntryView({ entry }: { entry: TranscriptEntry }) {
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
  }
}

const entryKey = (entry: TranscriptEntry) =>
  entry.id ? `${entry.kind}:${entry.id}` : String(entry.seq);

type Item = { kind: "entry"; entry: TranscriptEntry } | { kind: "tools"; tools: TranscriptEntry[] };

const isReasoning = (item: Item) => item.kind === "entry" && item.entry.kind === "reasoning";

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
    if (run?.kind === "tools") {
      items.splice(at, 1);
      run.tools.push(entry);
      items.push(run);
    } else {
      items.push({ kind: "tools", tools: [entry] });
    }
  }
  return items;
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
          ) : job.state === "failed" ? (
            `${name} failed`
          ) : (
            `${name} was ${job.state}`
          )}
        </p>
      )}
      {!active && job.reason ? <p>{jobReasonText(job.reason, job.kind)}</p> : null}
      {!active && job.detail ? <pre className="local-job-detail">{job.detail}</pre> : null}
      {!active && job.summary ? (
        <Markdown html={false} className="local-job-summary">
          {job.summary}
        </Markdown>
      ) : null}
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
              groupEntries(entries).map((item) =>
                item.kind === "tools" ? (
                  // Keyed by the run's first call, so a run that grows keeps
                  // its open or closed state.
                  <li key={entryKey(item.tools[0]!)}>
                    <ToolRun tools={item.tools} />
                  </li>
                ) : (
                  <li key={entryKey(item.entry)}>
                    <EntryView entry={item.entry} />
                  </li>
                ),
              )
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
      <div className="local-page-body local-job-page">
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
