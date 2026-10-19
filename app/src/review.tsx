import { UsageDetails } from "./usage";
import { Component, type ReactNode, useCallback, useEffect, useMemo, useState } from "react";
import { ReviewViewer } from "@baum/viewer/components/review-viewer";
import { sessionsSlot, commitsSlot } from "@baum/viewer/components/list-slots";
import { SessionCard } from "@baum/viewer/components/session-card";
import { Alert, Button } from "@baum/viewer/components/ui";
import { useHistoryView } from "@baum/viewer/use-review-version";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import type { ReviewDocument, ReviewVersionState } from "@baum/viewer/review-document";
import { parseSessionsAnswer, sessionLists, type SessionsLoad } from "@baum/viewer/local-sessions";
import { parseTranscript, type LocalTranscript } from "@baum/viewer/local-transcript";
import {
  appURL,
  request,
  selector,
  documentRequest,
  type Entry,
  type Job,
  isActiveJob,
  type Provenance,
} from "./api";
function Transcript({ id, query }: { id: string; query: string }) {
  const [load, setLoad] = useState<LocalTranscript | null>(null);
  const [error, setError] = useState(false);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    setLoad(null);
    setError(false);
    request<unknown>(
      `api/sessions/${encodeURIComponent(id)}?${query}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((body) => {
        const parsed = parseTranscript(body);
        if (!parsed) throw Error("Invalid transcript");
        if (!abort.signal.aborted) setLoad(parsed);
      })
      .catch(() => {
        if (!abort.signal.aborted) setError(true);
      });
    return () => abort.abort();
  }, [id, query, retry]);
  if (error)
    return (
      <Alert>
        Transcript could not be read. <Button onClick={() => setRetry((n) => n + 1)}>Retry</Button>
      </Alert>
    );
  if (!load) return <p role="status">Reading redacted local transcript…</p>;
  return (
    <>
      <p>
        Local transcript; secrets and e-mail addresses masked.
        {load.truncated ? " Some content was omitted." : ""}
      </p>
      <SessionCard
        defaultOpen={true}
        session={load.session}
        source={{ loadTurnActivity: (_run, turn) => Promise.resolve(load.activity[turn]!) }}
      />
    </>
  );
}
/** The hosted PR page's shared viewer, with local sources for its slots. */
export function LocalReview({
  entry,
  clone,
  revision,
  generate,
  job,
  changed,
}: {
  entry: Entry;
  clone: string;
  revision: number;
  generate: () => void;
  job?: Job;
  changed: () => Promise<void>;
}) {
  const pr = entry.pull_request;
  const generating = isActiveJob(job) || (!job && entry.status === "generating");
  const generationState = generating
    ? "pending"
    : job?.state === "failed" || job?.state === "interrupted"
      ? "failed"
      : entry.status === "generating"
        ? undefined
        : entry.status === "current" || entry.status === "outdated" || entry.status === "failed"
          ? entry.status
          : undefined;
  const [doc, setDoc] = useState<ReviewDocument | null>(null);
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [provenance, setProvenance] = useState<Provenance[]>([]);
  const [versions, setVersions] = useState<number[]>([]);
  const [historyFailed, setHistoryFailed] = useState(false);
  const [sessions, setSessions] = useState<SessionsLoad>({ state: "loading" });
  const loadVersion = useCallback(
    async (version: number): Promise<ReviewVersionState> => {
      try {
        return { status: "ready", document: await documentRequest(clone, pr.number, version) };
      } catch {
        return { status: "failed" };
      }
    },
    [clone, pr.number],
  );
  const history = useHistoryView(doc?.review.version, loadVersion);
  const viewedVersion =
    history.selection && !history.selection.compare ? history.selection.version : undefined;
  const viewedDocument = viewedVersion
    ? history.state?.status === "ready"
      ? history.state.document
      : null
    : doc;
  const query = selector(clone, pr.number, viewedVersion);
  const latestQuery = selector(clone, pr.number);
  useEffect(() => {
    const abort = new AbortController();
    setFailed(false);
    documentRequest(clone, pr.number, undefined, abort.signal)
      .then(setDoc)
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    request<Provenance[]>(`api/versions?${latestQuery}`, undefined, "GET", abort.signal)
      .then((rows) => {
        setProvenance(rows);
        setVersions(rows.map((row) => row.version));
        setHistoryFailed(false);
      })
      .catch(() => {
        if (!abort.signal.aborted) setHistoryFailed(true);
      });
    return () => abort.abort();
  }, [clone, pr.number, latestQuery, revision, retry]);
  useEffect(() => {
    if (!viewedDocument) return;
    setSessions({ state: "loading" });
    const abort = new AbortController();
    request<unknown>(`api/sessions?${query}`, undefined, "GET", abort.signal)
      .then((body) => {
        const answer = parseSessionsAnswer(body);
        if (!abort.signal.aborted)
          setSessions(answer ? { state: "answered", answer } : { state: "request-failed" });
      })
      .catch(() => {
        if (!abort.signal.aborted) setSessions({ state: "request-failed" });
      });
    return () => abort.abort();
  }, [viewedDocument, query]);
  const lists = viewedDocument ? sessionLists(sessions, viewedDocument) : null;
  const patches = useMemo(() => {
    const cache = new Map<string, Promise<string | null>>();
    return (path: string, version: number) => {
      const key = `${version}:${path}`;
      let pending = cache.get(key);
      if (!pending) {
        pending = request<{ patch: string | null }>(
          `api/patch?${selector(clone, pr.number, version)}&path=${encodeURIComponent(path)}`,
        )
          .then((body) => body.patch)
          .catch(() => {
            cache.delete(key);
            return null;
          });
        cache.set(key, pending);
      }
      return pending;
    };
  }, [clone, pr.number]);
  const attachments = useMemo(
    () => (id: string, document?: ReviewDocument) =>
      appURL(
        `api/attachments/${encodeURIComponent(id)}?${selector(clone, pr.number, document?.review.version)}`,
      ),
    [clone, pr.number],
  );
  return (
    <ReviewBoundary key={`${clone}:${pr.number}`}>
      <AttachmentUrlProvider resolve={attachments}>
        <ReviewViewer
          document={doc}
          history={history}
          versions={versions.filter((v) => v !== doc?.review.version)}
          loadVersion={loadVersion}
          versionsFailed={historyFailed ? { retry: () => setRetry((n) => n + 1) } : undefined}
          header={{
            pullRequest: {
              number: pr.number,
              title: pr.title,
              repository: pr.repository,
              url: pr.url,
              state: pr.state.toLowerCase(),
            },
            back: { href: appURL(), label: "Inbox" },
            generation: { state: generationState, rewriting: generating && Boolean(doc) },
            beside: (
              <Button disabled={generating} onClick={generate}>
                {generating
                  ? job?.state === "queued"
                    ? "Review queued"
                    : "Generating review…"
                  : doc
                    ? "Regenerate review"
                    : "Generate review"}
              </Button>
            ),
          }}
          placeholder={
            <div className="p-4 space-y-3">
              {generating ? (
                <p role="status">
                  {job?.state === "queued"
                    ? "Review generation is queued."
                    : "Generating review… This page will update when it is ready."}
                </p>
              ) : failed && entry.version_count > 0 ? (
                <Alert>
                  Review could not be read.{" "}
                  <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
                </Alert>
              ) : (
                <p>{entry.version_count === 0 ? "No generated review yet." : "Loading review…"}</p>
              )}
              {!generating ? <Button onClick={generate}>Generate review</Button> : null}
            </div>
          }
          notices={
            <>
              {viewedDocument
                ? (() => {
                    const saved = provenance.find(
                      (p) => p.version === viewedDocument.review.version,
                    );
                    return saved ? (
                      <details className="mb-3 local-panel">
                        <summary>Saved review usage · {saved.model}</summary>
                        <UsageDetails
                          accounting={saved.accounting}
                          charged={saved.accounting?.charged_tokens ?? saved.tokens}
                          amount={saved.recorded_cost_usd}
                          reason={saved.cost_reason}
                        />
                      </details>
                    ) : null;
                  })()
                : null}
              {job && (job.state !== "completed" || !doc) ? (
                <ReviewJob key={job.id} job={job} changed={changed} />
              ) : null}
              {failed && doc ? (
                <Alert>
                  The latest review could not be read. This saved version remains available.{" "}
                  <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
                </Alert>
              ) : null}
              {generating && doc ? (
                <Alert>
                  A new review is being generated. This saved version remains available while it
                  runs.
                </Alert>
              ) : null}
              {entry.stale ? (
                <Alert>Discovery is stale. Refresh the inbox to check the current head.</Alert>
              ) : null}
            </>
          }
          sessions={
            lists
              ? sessionsSlot(lists.sessions, {
                  renderSession: (session) => <Transcript id={session.id} query={query} />,
                })
              : undefined
          }
          commits={lists?.commits ? commitsSlot(lists.commits) : undefined}
          hunkPatches={patches}
        />
      </AttachmentUrlProvider>
    </ReviewBoundary>
  );
}

class ReviewBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  override state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  override render() {
    return this.state.failed ? (
      <Alert>
        The stored document could not be rendered.{" "}
        <Button onClick={() => this.setState({ failed: false })}>Retry rendering</Button>
      </Alert>
    ) : (
      this.props.children
    );
  }
}

function ReviewJob({ job, changed }: { job: Job; changed: () => Promise<void> }) {
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState("");
  const active = isActiveJob(job);
  const title =
    job.state === "queued"
      ? "Review queued"
      : job.state === "running"
        ? "Generating review"
        : job.state === "completed"
          ? "Review completed; loading the saved review…"
          : `Review generation ${job.state}`;
  return (
    <section aria-label="Review generation" className="mb-3 local-panel space-y-2">
      <p role="status">
        <strong>{title}</strong>
        {active ? ` · ${job.phase}` : ""}
      </p>
      <p className="text-xs text-(--text-tertiary)">
        Calls {job.calls} · Repairs {job.repairs}
      </p>
      <UsageDetails
        accounting={job.accounting}
        charged={job.tokens}
        amount={job.actual_cost_usd}
        reason={job.cost_reason}
      />
      {job.reason ? <p>{job.reason}</p> : null}
      {error ? <Alert>{error}</Alert> : null}
      {active ? (
        <Button
          variant="secondary"
          disabled={cancelling}
          onClick={() => {
            setCancelling(true);
            setError("");
            void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
              .then(changed)
              .catch(() => setError("The job could not be cancelled. Try again."))
              .finally(() => setCancelling(false));
          }}
        >
          {cancelling ? "Cancelling…" : "Cancel job"}
        </Button>
      ) : null}
    </section>
  );
}
