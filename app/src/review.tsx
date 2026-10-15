import { Component, type ReactNode, useEffect, useMemo, useState } from "react";
import { ReviewViewer } from "@baum/viewer/components/review-viewer";
import { sessionsSlot, commitsSlot } from "@baum/viewer/components/list-slots";
import { SessionCard } from "@baum/viewer/components/session-card";
import { Alert, Button } from "@baum/viewer/components/ui";
import { useUrlState } from "@baum/viewer/navigation";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import type { ReviewDocument, ReviewVersionState } from "@baum/viewer/review-document";
import { parseSessionsAnswer, sessionLists, type SessionsLoad } from "@baum/viewer/local-sessions";
import { parseTranscript, type LocalTranscript } from "@baum/viewer/local-transcript";
import { request, selector, documentRequest, type Entry } from "./api";
function Transcript({ id, query }: { id: string; query: string }) {
  const [load, setLoad] = useState<LocalTranscript | null>(null);
  const [error, setError] = useState(false);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    setLoad(null);
    setError(false);
    request<unknown>(
      `/api/sessions/${encodeURIComponent(id)}?${query}`,
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
}: {
  entry: Entry;
  clone: string;
  revision: number;
  generate: () => void;
}) {
  const pr = entry.pull_request;
  const { search } = useUrlState();
  const selectedVersion = Number(search.get("review")) || undefined;
  const [doc, setDoc] = useState<ReviewDocument | null>(null);
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [versions, setVersions] = useState<number[]>([]);
  const [historyFailed, setHistoryFailed] = useState(false);
  const [sessions, setSessions] = useState<SessionsLoad>({ state: "loading" });
  const query = selector(clone, pr.number);
  useEffect(() => {
    const abort = new AbortController();
    setFailed(false);
    documentRequest(clone, pr.number, undefined, abort.signal)
      .then(setDoc)
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    request<{ version: number }[]>(`/api/versions?${query}`, undefined, "GET", abort.signal)
      .then((rows) => {
        setVersions(rows.map((row) => row.version));
        setHistoryFailed(false);
      })
      .catch(() => {
        if (!abort.signal.aborted) setHistoryFailed(true);
      });
    return () => abort.abort();
  }, [clone, pr.number, query, revision, retry]);
  useEffect(() => {
    if (!doc) return;
    const abort = new AbortController();
    request<unknown>(`/api/sessions?${query}`, undefined, "GET", abort.signal)
      .then((body) => {
        const answer = parseSessionsAnswer(body);
        if (!abort.signal.aborted)
          setSessions(answer ? { state: "answered", answer } : { state: "request-failed" });
      })
      .catch(() => {
        if (!abort.signal.aborted) setSessions({ state: "request-failed" });
      });
    return () => abort.abort();
  }, [doc, query]);
  const lists = doc ? sessionLists(sessions, doc) : null;
  const patches = useMemo(() => {
    const cache = new Map<string, Promise<string | null>>();
    return (path: string, version: number) => {
      const key = `${version}:${path}`;
      let pending = cache.get(key);
      if (!pending) {
        pending = request<{ patch: string | null }>(
          `/api/patch?${selector(clone, pr.number, version)}&path=${encodeURIComponent(path)}`,
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
    () => (id: string) =>
      `/api/attachments/${encodeURIComponent(id)}?${selector(clone, pr.number, selectedVersion)}`,
    [clone, pr.number, selectedVersion],
  );
  const loadVersion = async (version: number): Promise<ReviewVersionState> => {
    try {
      return { status: "ready", document: await documentRequest(clone, pr.number, version) };
    } catch {
      return { status: "failed" };
    }
  };
  return (
    <ReviewBoundary key={`${clone}:${pr.number}`}>
      <AttachmentUrlProvider resolve={attachments}>
        <ReviewViewer
          document={doc}
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
            back: { href: "/", label: "Inbox" },
            generation: {
              state:
                entry.status === "generating"
                  ? "pending"
                  : entry.status === "current" ||
                      entry.status === "outdated" ||
                      entry.status === "failed"
                    ? entry.status
                    : undefined,
            },
            beside: (
              <Button onClick={generate}>{doc ? "Regenerate review" : "Generate review"}</Button>
            ),
          }}
          placeholder={
            <div className="p-4 space-y-3">
              {failed && entry.version_count > 0 ? (
                <Alert>
                  Review could not be read.{" "}
                  <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
                </Alert>
              ) : (
                <p>{entry.version_count === 0 ? "No generated review yet." : "Loading review…"}</p>
              )}
              <Button onClick={generate}>Generate review</Button>
            </div>
          }
          notices={
            entry.stale ? (
              <Alert>Discovery is stale. Refresh the inbox to check the current head.</Alert>
            ) : undefined
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
