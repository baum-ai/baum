import {
  Component,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ReviewViewer, type ReviewSteps } from "@baum/viewer/components/review-viewer";
import { sessionsSlot, commitsSlot } from "@baum/viewer/components/list-slots";
import { SessionCard } from "@baum/viewer/components/session-card";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import { SparkleIcon, WarningIcon } from "@baum/viewer/components/icons";
import { useHistoryView } from "@baum/viewer/use-review-version";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import { ViewerLink, useUrlState } from "@baum/viewer/navigation";
import type { ReviewDocument, ReviewVersionState } from "@baum/viewer/review-document";
import { parseSessionsAnswer, sessionLists, type SessionsLoad } from "@baum/viewer/local-sessions";
import { parseTranscript, type LocalTranscript } from "@baum/viewer/local-transcript";
import {
  appURL,
  backTo,
  request,
  selector,
  documentRequest,
  type ChatFocus,
  type Entry,
  type Job,
  isActiveJob,
  automaticLabel,
  jobReasonText,
  liveFor,
  fixEligible,
  fixLabel,
  jobHref,
  navigate,
  type LiveSession,
  type Provenance,
} from "./api";
import { FixNotice, FixRuns, type FixState, type FixChat } from "./fix";
import { CloneActions } from "./repositories";
import { Notice } from "./shell";
import { LiveSessions } from "./sessions";
import { PullRequestDecisionsList } from "./decisions";
import { LoadingStatus } from "./nine-dots";
import { canMerge, MergeDialog } from "./merge";
import { useShortcuts } from "./shortcuts";
import { PullRequestState, pullRequestStates } from "./inbox";
import { canRerunChecks, canUpdateBranch, RerunChecksDialog, UpdateBranchDialog } from "./unblock";
import { Walkthrough, type WalkView } from "./walkthrough";
import { buildTour } from "./walkthrough-model";
import { AnswerActions } from "./answer-actions";
import { AskPanel, sectionFocus } from "./chat";
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
  if (!load) return <LoadingStatus block>Reading redacted local transcript…</LoadingStatus>;
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
/** Whether the window matches a media query, as it changes. */
function useMedia(query: string) {
  const [matches, setMatches] = useState(
    () => typeof matchMedia === "function" && matchMedia(query).matches,
  );
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const list = matchMedia(query);
    const change = () => setMatches(list.matches);
    change();
    list.addEventListener("change", change);
    return () => list.removeEventListener("change", change);
  }, [query]);
  return matches;
}
/** The hosted PR page's shared viewer, with local sources for its slots. */
export function LocalReview({
  entry,
  clone,
  revision,
  generate,
  starting = false,
  job,
  changed,
  live = [],
  groups = [],
  fix,
  fixState = { starting: false, error: "" },
  fixJob,
  chats = [],
  fixRuns = [],
}: {
  entry: Entry;
  clone: string;
  revision: number;
  generate: () => void;
  /** The review was asked for and the daemon has not answered yet. */
  starting?: boolean;
  job?: Job;
  changed: () => Promise<void>;
  /** Agent sessions in progress; those of this pull request are listed. */
  live?: LiveSession[];
  /** The review groups this pull request belongs to. */
  groups?: { id: string; name: string }[];
  /** Starts the fix at once; absent when the pull request has no clone. */
  fix?: (chat?: FixChat) => void;
  /** The fix asked for here: starting, or why it did not start. */
  fixState?: FixState;
  /** The pull request's latest fix job. */
  fixJob?: Job;
  /** Every fix job of the pull request, newest first. */
  fixRuns?: Job[];
  chats?: Job[];
}) {
  const pr = entry.pull_request;
  const url = useUrlState();
  const back = backTo(url.search);
  // The walkthrough is opt-in: the full review stays the page's default.
  const walk = url.search.get("walk");
  const walkView: WalkView | null = walk === "all" ? "all" : walk === "tour" ? "tour" : null;
  const setWalk = (view: WalkView | null) =>
    url.update((current) => {
      if (view) current.set("walk", view);
      else current.delete("walk");
      return current;
    });
  // The Ask panel docks beside the review; the URL keeps it open over a reload.
  const askOpen = url.search.get("ask") === "1";
  const setAsk = (open: boolean) =>
    url.update((current) => {
      if (open) current.set("ask", "1");
      else current.delete("ask");
      return current;
    });
  // The part of the review the next question is about.
  const [askFocus, setAskFocus] = useState<ChatFocus | null>(null);
  // Below 72rem the panel overlays the review, which is then inert behind it.
  const narrow = useMedia("(width < 72rem)");
  // Docked on a window under 104rem, the review has too little width for its
  // side pane, which folds to its strip meanwhile.
  const tight = useMedia("(72rem <= width < 104rem)");
  // Closing hands the keyboard back to the Ask button that opened it.
  const closeAsk = () => {
    setAsk(false);
    requestAnimationFrame(() =>
      window.document.querySelector<HTMLElement>("[data-ask-button]")?.focus(),
    );
  };
  const askAbout = (focus: ChatFocus) => {
    setAskFocus(focus);
    if (!askOpen) setAsk(true);
  };
  const fixLabelText = fixLabel(entry);
  const fixing = isActiveJob(fixJob);
  const fixStarting = fixState.starting && !fixing;
  // A finished fix stays on the page while it concerns the head shown: the
  // one it started from, or the commit it pushed.
  const showFix =
    !!fixJob && (fixing || fixJob.head_sha === pr.head_sha || fixJob.commit === pr.head_sha);
  // The runs the notice does not show stay one fold away, transcripts and all.
  const earlierRuns = fixRuns.filter((run) => !showFix || run.id !== fixJob?.id);
  const watchable = [fixJob, ...fixRuns].find((run) => run?.has_transcript);
  const running = liveFor(live, pr);
  // A session that ends is read from disk like any other: reload the list
  // when one leaves the live section.
  const [ended, setEnded] = useState(0);
  const liveKeys = running.map((s) => `${s.agent}:${s.session_id}`).join("\n");
  const previousKeys = useRef(liveKeys);
  useEffect(() => {
    const now = new Set(liveKeys.split("\n"));
    if (previousKeys.current.split("\n").some((key) => key && !now.has(key)))
      setEnded((n) => n + 1);
    previousKeys.current = liveKeys;
  }, [liveKeys]);
  const [merging, setMerging] = useState(false);
  const [rerunning, setRerunning] = useState(false);
  const [updating, setUpdating] = useState(false);
  const generating = starting || isActiveJob(job) || (!job && entry.status === "generating");
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
  // Where an answer's comment lands when it is added to the draft.
  const tour = useMemo(() => (doc ? buildTour(doc) : null), [doc]);
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [provenance, setProvenance] = useState<Provenance[]>([]);
  const [versions, setVersions] = useState<number[]>([]);
  const [historyFailed, setHistoryFailed] = useState(false);
  const [sessions, setSessions] = useState<SessionsLoad>({ state: "loading" });
  // The page's keys: j and k step through the review as the navigator does,
  // and the header's actions have the list's keys.
  const steps = useRef<ReviewSteps>(null);
  useShortcuts((event) => {
    // a opens the Ask panel, or puts the cursor in its box when open; it
    // works in the walkthrough too.
    if (event.key === "a" && doc) {
      event.preventDefault();
      if (askOpen) window.document.getElementById("ask-question")?.focus();
      else setAsk(true);
      return;
    }
    // The walkthrough has keys of its own.
    if (walkView && doc) return;
    const step =
      event.key === "j" ? steps.current?.next : event.key === "k" ? steps.current?.previous : null;
    if (step) {
      event.preventDefault();
      step.go();
    } else if (event.key === "m" && canMerge(entry)) {
      event.preventDefault();
      setMerging(true);
    } else if (event.key === "f" && fix && fixLabelText && !fixing && !fixStarting) {
      event.preventDefault();
      fix();
    } else if (event.key === "g" && !generating && !entry.needs_clone) {
      event.preventDefault();
      generate();
    } else if (event.key === "t" && watchable) {
      event.preventDefault();
      navigate(jobHref(watchable));
    }
  });
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
  }, [viewedDocument, query, ended]);
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
        <div className="ask-layout">
          <div className="ask-main" inert={narrow && askOpen ? true : undefined}>
            {walkView && doc ? (
              // A new version starts afresh, with that version's own progress.
              <Walkthrough
                key={doc.review.version}
                entry={entry}
                document={doc}
                view={walkView}
                setView={setWalk}
                close={() => setWalk(null)}
                patches={patches}
                changed={changed}
                ask={{
                  open: askOpen,
                  toggle: () => (askOpen ? closeAsk() : setAsk(true)),
                  about: askAbout,
                }}
              />
            ) : (
              <ReviewViewer
                document={doc}
                history={history}
                versions={versions.filter((v) => v !== doc?.review.version)}
                loadVersion={loadVersion}
                versionsFailed={historyFailed ? { retry: () => setRetry((n) => n + 1) } : undefined}
                steps={steps}
                foldSidePane={askOpen && tight}
                header={{
                  pullRequest: {
                    number: pr.number,
                    title: pr.title,
                    repository: pr.repository,
                    url: pr.url,
                    state: pr.state.toLowerCase(),
                  },
                  back: { href: back.href, label: back.label },
                  generation: { state: generationState, rewriting: generating && Boolean(doc) },
                  // Without a review, the button in the page's middle generates it.
                  // The GitHub state leads, so a reader sees why a fix, a re-run or an
                  // update is offered; a fix comes first of those, re-running checks
                  // and updating the branch are its smaller remedies.
                  beside:
                    doc ||
                    pullRequestStates(entry).length > 0 ||
                    (fix && fixLabelText) ||
                    canMerge(entry) ||
                    canRerunChecks(entry) ||
                    canUpdateBranch(entry) ? (
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="mr-2 empty:hidden">
                          <PullRequestState entry={entry} />
                        </span>
                        {fix && fixLabelText ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            disabled={fixing || fixStarting}
                            onClick={() => fix()}
                            className="gap-1.5"
                          >
                            <SparkleIcon size={14} className="flex-none text-(--status-critical)" />
                            {fixing ? "Fixing…" : fixStarting ? "Starting…" : fixLabelText}
                          </Button>
                        ) : null}
                        {canRerunChecks(entry) ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            onClick={() => setRerunning(true)}
                          >
                            Re-run checks
                          </Button>
                        ) : null}
                        {canUpdateBranch(entry) ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            onClick={() => setUpdating(true)}
                          >
                            Update branch
                          </Button>
                        ) : null}
                        {doc ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            disabled={generating || entry.needs_clone}
                            onClick={generate}
                          >
                            {generating
                              ? starting
                                ? "Starting review…"
                                : job?.state === "queued"
                                  ? "Review queued"
                                  : "Generating review…"
                              : "Regenerate review"}
                          </Button>
                        ) : null}
                        {doc ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            aria-pressed={askOpen}
                            aria-keyshortcuts="a"
                            data-ask-button=""
                            onClick={() => (askOpen ? closeAsk() : setAsk(true))}
                          >
                            Ask
                          </Button>
                        ) : null}
                        {doc ? (
                          <Button
                            variant="secondary"
                            size="compact"
                            onClick={() => setWalk("tour")}
                          >
                            Walk through
                          </Button>
                        ) : null}
                        {canMerge(entry) ? (
                          <Button size="compact" onClick={() => setMerging(true)}>
                            Merge…
                          </Button>
                        ) : null}
                      </div>
                    ) : null,
                }}
                placeholder={
                  entry.needs_clone ? (
                    <div className="local-review-empty">
                      <h2>Needs a local clone</h2>
                      <CloneActions repository={pr.repository} changed={changed} />
                    </div>
                  ) : generating ? (
                    <section aria-label="Review generation" className="local-review-empty">
                      <LiveDot />
                      <h2 role="status">
                        {starting
                          ? "Starting the review"
                          : job?.state === "queued"
                            ? "Review queued"
                            : "Generating the review"}
                      </h2>
                      <p>
                        {!starting && job && automaticLabel(job) ? `${automaticLabel(job)}. ` : ""}
                        This page updates when it is ready.
                      </p>
                      {!starting && job ? <CancelJob job={job} changed={changed} /> : null}
                    </section>
                  ) : failed && entry.version_count > 0 ? (
                    <div className="local-review-empty">
                      <h2>The saved review couldn't be read</h2>
                      <Button size="compact" onClick={() => setRetry((n) => n + 1)}>
                        Retry review
                      </Button>
                    </div>
                  ) : entry.version_count > 0 ? (
                    <div className="local-review-empty">
                      <LoadingStatus block>Loading review…</LoadingStatus>
                    </div>
                  ) : job && job.state !== "completed" && !isActiveJob(job) ? (
                    <div className="local-review-empty">
                      <GenerationOutcome job={job} />
                      <Button size="compact" onClick={generate}>
                        {job.state === "failed" ? "Try again" : "Generate review"}
                      </Button>
                    </div>
                  ) : (
                    <div className="local-review-empty">
                      <h2>No review yet</h2>
                      <p>Generate one to see this pull request's summary, risks and changes.</p>
                      <Button size="compact" onClick={generate}>
                        Generate review
                      </Button>
                    </div>
                  )
                }
                detailsFooter={(document) => (
                  <GeneratedBy
                    saved={provenance.find((p) => p.version === document.review.version)}
                    entry={entry}
                    version={document.review.version}
                    job={job}
                  />
                )}
                notices={
                  <>
                    {groups.map((group) => (
                      <Notice key={group.id}>
                        In review group{" "}
                        <ViewerLink
                          href={appURL(`?${back.prefix}group=${encodeURIComponent(group.id)}`)}
                        >
                          {group.name}
                        </ViewerLink>
                      </Notice>
                    ))}
                    {fixState.error ? (
                      <Alert>Couldn't start the fix: {fixState.error}</Alert>
                    ) : null}
                    {showFix && fixJob ? (
                      <FixNotice key={fixJob.id} job={fixJob} changed={changed} />
                    ) : null}
                    <FixRuns runs={earlierRuns} earlier={showFix} />
                    {doc || failed || entry.version_count === 0 ? (
                      <PullRequestDecisionsList
                        repository={pr.repository}
                        number={pr.number}
                        live={running}
                        revision={`${ended}:${doc?.review.version ?? 0}`}
                      />
                    ) : null}
                    <LiveSessions sessions={running} Link={ViewerLink} />
                    {entry.needs_clone && doc ? (
                      <Notice>
                        Needs a local clone. This saved review remains available.
                        <CloneActions repository={pr.repository} changed={changed} />
                      </Notice>
                    ) : null}
                    {job && isActiveJob(job) && doc ? (
                      <ReviewJob key={job.id} job={job} changed={changed} />
                    ) : job && doc && job.state !== "completed" ? (
                      <div className="local-job-outcome">
                        <GenerationOutcome job={job} compact />
                      </div>
                    ) : null}
                    {failed && doc ? (
                      <Alert>
                        The latest review could not be read. This saved version remains available.{" "}
                        <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
                      </Alert>
                    ) : null}
                    {generating && doc ? (
                      <Notice>
                        A new review is being generated. This saved version remains available while
                        it runs.
                      </Notice>
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
                renderSectionAside={
                  doc
                    ? (id) => (
                        <div className="ask-aside">
                          <Button
                            variant="secondary"
                            size="compact"
                            onClick={() => {
                              const focus = sectionFocus(doc, id);
                              if (focus) askAbout(focus);
                            }}
                          >
                            Ask about this section
                          </Button>
                        </div>
                      )
                    : undefined
                }
              />
            )}
          </div>
          {askOpen && doc ? (
            <>
              <div className="ask-backdrop" aria-hidden onClick={closeAsk} />
              <AskPanel
                key={`${clone}:${pr.number}`}
                clone={clone}
                pr={pr.number}
                pullRequestUrl={pr.url}
                latestVersion={doc.review.version}
                // Questions are about the version on screen, which is an older
                // one while the viewer reads it.
                document={!walkView && viewedDocument ? viewedDocument : doc}
                patches={patches}
                focus={askFocus}
                clearFocus={() => setAskFocus(null)}
                close={closeAsk}
                jobs={chats}
                answerActions={({ conversation, turn, answer }) =>
                  // Only an answer about the newest review goes to its draft or
                  // to a fix of the pull request as it is now.
                  tour && conversation.version === doc.review.version ? (
                    <AnswerActions
                      conversation={conversation}
                      turn={turn}
                      answer={answer}
                      document={doc}
                      tour={tour}
                      fix={fix && fixEligible(entry) ? fix : undefined}
                      fixRunning={fixing || fixStarting}
                      openDraft={() => {
                        setWalk("all");
                        // The overlay would cover the draft and keep it inert.
                        if (narrow) closeAsk();
                      }}
                    />
                  ) : null
                }
              />
            </>
          ) : null}
        </div>
        {merging ? (
          <MergeDialog entry={entry} close={() => setMerging(false)} merged={changed} />
        ) : null}
        {rerunning ? (
          <RerunChecksDialog entry={entry} close={() => setRerunning(false)} rerun={changed} />
        ) : null}
        {updating ? (
          <UpdateBranchDialog entry={entry} close={() => setUpdating(false)} updated={changed} />
        ) : null}
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

/** Where the review being read came from, at the foot of its Details: the
 *  harness and model, and the job that generated it while the jobs list
 *  still has it. */
function GeneratedBy({
  saved,
  entry,
  version,
  job,
}: {
  saved: Provenance | undefined;
  entry: Entry;
  version: number;
  job: Job | undefined;
}) {
  if (!saved) return null;
  const latest = version === entry.latest?.version;
  const made = latest && job?.state === "completed" ? job : null;
  return (
    <p className="local-generated-by">
      Generated with {latest && entry.latest ? `${entry.latest.backend} · ` : ""}
      {saved.model}
      {made ? (
        <>
          {" · "}
          <a
            href={appURL(`?page=jobs#job-${encodeURIComponent(made.id)}`)}
            onClick={(event) => {
              event.preventDefault();
              window.history.pushState(null, "", event.currentTarget.href);
              dispatchEvent(new PopStateEvent("popstate"));
            }}
          >
            View job
          </a>
        </>
      ) : null}
    </p>
  );
}
/** What became of the PR's last generation that did not complete: what
 *  happened in a sentence, and the generator's own words behind a toggle. */
export function GenerationOutcome({ job, compact = false }: { job: Job; compact?: boolean }) {
  const failed = job.state === "failed";
  const title = failed
    ? compact
      ? "The latest regeneration failed"
      : "The review couldn't be generated"
    : job.state === "interrupted"
      ? "Review generation was interrupted"
      : "Review generation was cancelled";
  const started = automaticLabel(job);
  return (
    <section aria-label="Review generation" className="local-outcome">
      <span className={failed ? "local-outcome-icon failed" : "local-outcome-icon"}>
        <WarningIcon size={16} />
      </span>
      <div className="min-w-0">
        <h2 role="status">{title}</h2>
        {job.reason ? <p>{jobReasonText(job.reason)}</p> : null}
        {started ? <p className="local-outcome-meta">{started}</p> : null}
        {job.detail ? (
          <details>
            <summary>What happened</summary>
            <pre>{job.detail}</pre>
          </details>
        ) : null}
      </div>
    </section>
  );
}
/** Stops the PR's queued or running job. */
export function CancelJob({ job, changed }: { job: Job; changed: () => Promise<void> }) {
  const [cancelling, setCancelling] = useState(false);
  const [error, setError] = useState("");
  return (
    <>
      <Button
        variant="secondary"
        size="compact"
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
      {error ? <Alert>{error}</Alert> : null}
    </>
  );
}
/** A new version being generated above the review the reader is on. */
export function ReviewJob({ job, changed }: { job: Job; changed: () => Promise<void> }) {
  const started = automaticLabel(job);
  return (
    <section aria-label="Review generation" className="local-job-outcome">
      <LiveDot />
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {job.state === "queued" ? "New version queued" : "Generating a new version"}
          {job.state === "running" && job.phase ? (
            <span className="font-normal text-(--text-tertiary)"> · {job.phase}</span>
          ) : null}
        </p>
        {started ? <p className="text-(--text-tertiary)">{started}</p> : null}
      </div>
      <CancelJob job={job} changed={changed} />
    </section>
  );
}
