import {
  Component,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  ReviewViewer,
  type ReviewListSlot,
  type ReviewSteps,
} from "@baum/viewer/components/review-viewer";
import { sessionsSlot, commitsSlot } from "@baum/viewer/components/list-slots";
import type {
  HostSessionView,
  SessionListState,
  SessionView,
} from "@baum/viewer/components/session-list";
import { useVisibleInterval } from "./visible-interval";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import {
  ChevronDownIcon,
  SparkleIcon,
  StackIcon,
  WarningIcon,
} from "@baum/viewer/components/icons";
import { type MenuItem, useContextMenu } from "./context-menu";
import { useHistoryView } from "@baum/viewer/use-review-version";
import { AttachmentUrlProvider } from "@baum/viewer/host";
import { ViewerLink, useUrlState } from "@baum/viewer/navigation";
import {
  contextRefUrl,
  sectionDetails,
  type ReviewContextRef,
  type ReviewDocument,
  type ReviewVersionState,
} from "@baum/viewer/review-document";
import {
  createCommitLoaders,
  parseSessionsAnswer,
  sessionLists,
  type SessionsLoad,
} from "@baum/viewer/local-sessions";
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
  LocalRequestError,
  liveFor,
  listedFor,
  fixEligible,
  fixLabel,
  jobHref,
  navigate,
  type LiveSession,
  type Provenance,
} from "./api";
import { FixNotice, FixRuns, type FixState, type FixChat, useDismissedFixes } from "./fix";
import { CloneActions } from "./repositories";
import { useModal } from "./modal";
import { Notice, RefreshButton, type RefreshRun, usePageRefreshAction } from "./shell";
import { LiveSessionCard, LiveSessions, liveSessionLabel } from "./sessions";
import { EmbeddedLiveTranscript, TranscriptCard } from "./session-transcript";
import { withCommitRows } from "./commit-rows";
import {
  PullRequestDecisionsList,
  PullRequestDecisionsPane,
  usePullRequestDecisions,
} from "./decisions";
import { LoadingStatus } from "./nine-dots";
import { DescriptionDialog } from "./description";
import { RepositoryFileDialog } from "./repository-file";
import { canMerge, MergeDialog } from "./merge";
import { openActiveBlock, reviewKeyStep, toggleActiveBlock, useShortcuts } from "./shortcuts";
import { PullRequestState, pullRequestStates } from "./inbox";
import { canRerunChecks, canUpdateBranch, RerunChecksDialog, UpdateBranchDialog } from "./unblock";
import { Walkthrough, type WalkMode } from "./walkthrough";
import { buildTour } from "./walkthrough-model";
import { AnswerActions } from "./answer-actions";
import { AskPanel } from "./chat";
import { documentFilesSlot, factsFilesSlot, factsQuery, useFacts } from "./facts";
import { liveDetailsChecks, useChecks } from "./checks";
import { SECONDARY } from "./action-classes";
import {
  CommentsPane,
  ReviewButton,
  ReviewDialog,
  usePendingReview,
  type Composer,
} from "./pending-review";
import { commentTarget, reviewable } from "./pending-review-model";
import type { ReviewEvent } from "./walkthrough-model";
import { readSelection } from "@baum/viewer/review-selection";

/** `commitsSlot`'s loaders over the daemon's commit routes under `route`
 *  (`api` for a saved version, `api/facts` for the facts), with the page's
 *  `query`, as `Transcript` reads a session. A 404 (a commit or file the
 *  daemon does not answer for) is no detail or no diff. */
function localCommitLoaders(route: string, query: string) {
  return routedCommitLoaders(route, () => query);
}

/** `localCommitLoaders` where the query follows the commit: a stack's page
 *  reads each commit through the member whose review linked it. */
export function routedCommitLoaders(route: string, queryFor: (oid: string) => string) {
  return createCommitLoaders(async (name, params) => {
    try {
      return await request<unknown>(
        `${route}/${name}?${queryFor(params.oid ?? "")}&${new URLSearchParams(params)}`,
      );
    } catch (error) {
      if (error instanceof LocalRequestError && error.status === 404) return undefined;
      throw error;
    }
  });
}

export function Transcript({
  id,
  query,
  route = "api/sessions",
  title,
  focusTurn,
}: {
  id: string;
  query: string;
  /** Where the pull request's sessions are read: a version's, or its facts'. */
  route?: string;
  /** The listed session's title, for a transcript that holds none. */
  title?: string;
  focusTurn?: number;
}) {
  const [load, setLoad] = useState<LocalTranscript | null>(null);
  const [error, setError] = useState(false);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const abort = new AbortController();
    setLoad(null);
    setError(false);
    request<unknown>(`${route}/${encodeURIComponent(id)}?${query}`, undefined, "GET", abort.signal)
      .then((body) => {
        const parsed = parseTranscript(body);
        if (!parsed) throw Error("Invalid transcript");
        if (!abort.signal.aborted) setLoad(parsed);
      })
      .catch(() => {
        if (!abort.signal.aborted) setError(true);
      });
    return () => abort.abort();
  }, [id, query, route, retry]);
  if (error)
    return (
      <Alert>
        Transcript could not be read. <Button onClick={() => setRetry((n) => n + 1)}>Retry</Button>
      </Alert>
    );
  if (!load) return <LoadingStatus block>Reading redacted local transcript…</LoadingStatus>;
  // Drawn as the Sessions page draws a session, not as a chat.
  return <TranscriptCard transcript={load} title={title} focusTurn={focusTurn} embedded />;
}

/** The pull request's Sessions group: one list of the sessions linked to it
 *  through its commits (`linked`) and the sessions the daemon lists on it
 *  (`listed`: working, waiting, or ended and still listed). A listed session
 *  is a row of its own, first, marked on the rail while it works (amber while
 *  it waits on the reader), and counted once when it is linked too. */
export function pullRequestSessions(
  linked: SessionListState,
  listed: LiveSession[],
  renderSession: (session: SessionView, focusTurn?: number) => ReactNode,
): ReviewListSlot {
  return sessionsSlot(linked, {
    renderSession,
    hosted: listedSessionRows(listed, linked),
    empty:
      "Sessions on this computer show here while they work on this pull request, and once one of their commits is in it.",
  });
}

/** `listed` as the Sessions list's own rows. One that ended and is linked is
 *  left to its linked row, which reads the full session from disk. */
export function listedSessionRows(
  listed: LiveSession[],
  linked: SessionListState,
): HostSessionView[] {
  const linkedIds = new Set(
    linked.status === "ready" ? linked.sessions.map((session) => session.id) : [],
  );
  return listed
    .filter((s) => s.state !== "ended" || !linkedIds.has(s.session_id))
    .map((s) => ({
      id: s.session_id,
      label: liveSessionLabel(s),
      indicator:
        s.state === "ended"
          ? undefined
          : s.state !== "running" && s.waiting_reason
            ? "attention"
            : "live",
      renderCard: (open) => <LiveSessionCard session={s} open={open} Link={ViewerLink} />,
      renderPane: () => (
        <>
          <div className="pb-3">
            <LiveSessionCard session={s} titled={false} Link={ViewerLink} />
          </div>
          <EmbeddedLiveTranscript session={s} />
        </>
      ),
    }));
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
  detailsJob,
  changed,
  refresh,
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
  /** The latest retry of this review's failed section details. */
  detailsJob?: Job;
  changed: () => Promise<void>;
  /** Reads the pull request from GitHub again, from the head's refresh. */
  refresh?: (run: RefreshRun) => Promise<unknown>;
  /** Agent sessions in progress; those of this pull request are listed. */
  live?: LiveSession[];
  /** The review groups this pull request belongs to. */
  groups?: { id: string; name: string; stack?: boolean }[];
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
  // The refresh key works on every state of the page, also where the button
  // is not drawn, as while the review loads or has nothing to walk through.
  usePageRefreshAction(refresh);
  // One of the head's actions, on the walkthrough and on the full review alike.
  const refreshButton = refresh ? (
    <RefreshButton refresh={refresh} label="Refresh from GitHub" />
  ) : undefined;
  // A pull request opens on the full review; walk=landing is the
  // walkthrough's overview and walk=tour the tour one step in. walk=all named
  // the list view the walkthrough once had, and walk=full the full review when
  // the overview was the default.
  const walk = url.search.get("walk");
  const walkMode: WalkMode | null =
    walk === "tour" || walk === "all" ? "tour" : walk === "landing" ? "landing" : null;
  const setWalk = (mode: WalkMode | null) =>
    url.update((current) => {
      if (mode) current.set("walk", mode);
      else current.delete("walk");
      current.delete("pane");
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
  // The pull request's description, opened from a source chip that cites it.
  const [description, setDescription] = useState<{ section?: string } | null>(null);
  // A repository file a context chip opened, with the selector and GitHub
  // link of the version it was opened from.
  const [repositoryFile, setRepositoryFile] = useState<{
    reference: ReviewContextRef;
    source: string;
    href: string;
  } | null>(null);
  // An answer's Open the draft asks the walkthrough for its wrap-up page.
  const [toDraft, setToDraft] = useState(false);
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
  // A finished fix's notice the person dismissed joins the fix runs below.
  const [dismissedFixes, dismissFix] = useDismissedFixes();
  const showFix =
    !!fixJob &&
    (fixing || fixJob.head_sha === pr.head_sha || fixJob.commit === pr.head_sha) &&
    (fixing || !dismissedFixes.includes(fixJob.id));
  // The runs the notice does not show stay one fold away, transcripts and all.
  const earlierRuns = fixRuns.filter((run) => !showFix || run.id !== fixJob?.id);
  const watchable = [fixJob, ...fixRuns].find((run) => run?.has_transcript);
  const running = liveFor(live, pr);
  const listed = listedFor(live, pr);
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
  // The reader's own review: the verdict being confirmed, the comment being
  // written, and the Comments tab, which c opens on the item being read.
  const [reviewing, setReviewing] = useState<ReviewEvent | null>(null);
  const [composer, setComposer] = useState<Composer | null>(null);
  const [revealComments, setRevealComments] = useState(0);
  // The review just sent, which the Comments tab opens on under Sent.
  const [sentReview, setSentReview] = useState<string | null>(null);
  const pending = usePendingReview(entry);
  const pendingCount = pending.value.comments.length;
  const canReview = reviewable(entry);
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
  // The decisions of its sessions, in their own pane beside Sessions and
  // Commits rather than above the review.
  const decisions = usePullRequestDecisions(
    pr.repository,
    pr.number,
    `${ended}:${doc?.review.version ?? 0}`,
  );
  // Where an answer's comment lands when it is added to the draft.
  const tour = useMemo(() => (doc ? buildTour(doc) : null), [doc]);
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [provenance, setProvenance] = useState<Provenance[]>([]);
  const [versions, setVersions] = useState<number[]>([]);
  const [historyFailed, setHistoryFailed] = useState(false);
  const [sessions, setSessions] = useState<{
    query: string;
    version: number;
    load: SessionsLoad;
  } | null>(null);
  // The page's keys: j and k step through the review as the navigator does,
  // and the header's actions have the list's keys.
  const steps = useRef<ReviewSteps>(null);
  useShortcuts((event) => {
    // a opens the Ask panel, or puts the cursor in its box when open; it
    // works in the walkthrough too.
    if (event.key === "a" && doc && !showingDraft) {
      event.preventDefault();
      if (askOpen) window.document.getElementById("ask-question")?.focus();
      else setAsk(true);
      return;
    }
    // The tour has keys of its own; the overview leaves the page's to it.
    if (walkMode === "tour" && doc && !showingDraft) return;
    // Space opens or closes the fold of the block j and k are on; on a
    // focused button or link it still presses that.
    if (
      event.key === " " &&
      !(event.target instanceof HTMLElement && event.target.closest("a, button, summary")) &&
      toggleActiveBlock()
    ) {
      event.preventDefault();
      return;
    }
    // Else Space opens or closes the selected group (Changes, Sessions,
    // Commits, Files) in the rail.
    if (
      event.key === " " &&
      !(event.target instanceof HTMLElement && event.target.closest("a, button, summary")) &&
      steps.current?.toggleGroup
    ) {
      event.preventDefault();
      steps.current.toggleGroup();
      return;
    }
    // Enter opens the block j and k are on, such as a commit from the list.
    if (
      event.key === "Enter" &&
      !(
        event.target instanceof HTMLElement &&
        event.target.closest("a, button, summary, input, textarea")
      ) &&
      openActiveBlock()
    ) {
      event.preventDefault();
      return;
    }
    // j and k (↓ ↑) move through the blocks of the section read; with Shift,
    // or ← and →, to the next and previous section, as the navigator does.
    const step = reviewKeyStep(event, steps.current);
    if (step) {
      event.preventDefault();
      step.go();
    } else if (event.key === "m" && canMerge(entry)) {
      event.preventDefault();
      setMerging(true);
    } else if (event.key === "f" && fix && fixLabelText && !fixing && !fixStarting) {
      event.preventDefault();
      fix();
    } else if (event.key === "t" && watchable) {
      event.preventDefault();
      navigate(jobHref(watchable));
    } else if (event.key === "c" && canReview && !showingDraft) {
      event.preventDefault();
      setComposer({ kind: "new", ...commentOn });
      setRevealComments((n) => n + 1);
    } else if (event.key === "v") {
      // As a click on the review button: its menu of verdicts.
      const button = window.document.querySelector<HTMLElement>("[data-review-button]");
      if (!button) return;
      event.preventDefault();
      button.click();
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
  // What c comments on: the section, risk or file being read.
  const commentOn = commentTarget(viewedDocument ?? null, readSelection(url.search));
  const query = selector(clone, pr.number, viewedVersion);
  const latestQuery = selector(clone, pr.number);
  // While the daemon writes a review in stages it holds a draft of it. It is
  // never a version: nothing is saved, listed or asked about until published.
  const draftCount = isActiveJob(job) ? (job?.draft ?? 0) : 0;
  const [draft, setDraft] = useState<ReviewDocument | null>(null);
  useEffect(() => {
    if (!job || draftCount === 0) {
      setDraft(null);
      return;
    }
    const abort = new AbortController();
    request<{ document?: ReviewDocument }>(
      `api/jobs/${encodeURIComponent(job.id)}/draft`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((body) => {
        if (!abort.signal.aborted && Array.isArray(body.document?.sections))
          setDraft(body.document);
      })
      .catch(() => {
        /* The draft is gone, or the next one is on its way. */
      });
    return () => abort.abort();
  }, [job?.id, draftCount]);
  // A page without a review shows the draft as soon as there is one; one with
  // a saved version keeps showing that version until the new one is saved.
  const showingDraft = draft !== null && !doc && entry.version_count === 0;
  // The failed sections of the latest saved version, which a retry writes
  // again; they read as being written while that job runs.
  const failedSections = useMemo(
    () =>
      doc?.sections
        .filter((s) => s.derived?.state !== "stale" && sectionDetails(s) === "failed")
        .map((s) => s.id) ?? [],
    [doc],
  );
  const retrying =
    isActiveJob(detailsJob) &&
    (detailsJob?.version === undefined || detailsJob.version === doc?.review.version);
  const shownDocument = useMemo(
    () =>
      showingDraft
        ? draft
        : retrying && doc && failedSections.length > 0
          ? {
              ...doc,
              sections: doc.sections.map((s) =>
                failedSections.includes(s.id) ? { ...s, details: "pending" as const } : s,
              ),
            }
          : doc,
    [showingDraft, draft, retrying, doc, failedSections],
  );
  const contextPending = showingDraft && isActiveJob(job) && !draft?.summary.context;
  // Why the last retry did not write the details, said until it is dismissed.
  const [headChanged, setHeadChanged] = useState(false);
  const [retryError, setRetryError] = useState("");
  const [confirmingIsolation, setConfirmingIsolation] = useState(false);
  const [retryStarting, setRetryStarting] = useState(false);
  const isolationDialog = useModal(confirmingIsolation);
  const [watchedDetails, setWatchedDetails] = useState("");
  useEffect(() => {
    if (detailsJob && isActiveJob(detailsJob)) setWatchedDetails(detailsJob.id);
  }, [detailsJob?.id, detailsJob?.state]);
  const retryDetails = async (confirmIsolation = false) => {
    if (!doc) return;
    setHeadChanged(false);
    setRetryError("");
    setRetryStarting(true);
    try {
      await request("api/jobs", {
        items: [{ clone, pr: pr.number, kind: "details", version: doc.review.version }],
        mode: "single",
        ...(confirmIsolation ? { confirm_isolation: true } : {}),
      });
      setConfirmingIsolation(false);
      await changed();
    } catch (e) {
      if (e instanceof LocalRequestError && e.code === "isolation_required") {
        setConfirmingIsolation(true);
        return;
      }
      if (
        e instanceof LocalRequestError &&
        (e.code === "head_changed" || e.code === "base_changed")
      )
        setHeadChanged(true);
      else setRetryError(e instanceof Error ? e.message : "The details could not be retried.");
    } finally {
      setRetryStarting(false);
    }
  };
  // What the saved review lacks: sections' details and the context. A review
  // from before contexts existed lacks one without having failed to write it,
  // so only the job that published the review says that.
  const missing = failedSections.length;
  const contextMissing =
    Boolean(doc) &&
    !doc?.summary.context &&
    job?.context_failed === true &&
    job.state === "completed";
  const gaps =
    !doc || showingDraft || viewedVersion || (!missing && !contextMissing) ? null : (
      <Notice>
        {retrying
          ? "Writing the details that couldn't be written."
          : [
              missing
                ? `${missing === 1 ? "1 section" : `${missing} sections`} couldn't be written.`
                : "",
              contextMissing ? "The codebase context couldn't be written." : "",
            ]
              .filter(Boolean)
              .join(" ")}
      </Notice>
    );
  const retryNotices = (
    <>
      {headChanged ? (
        <Alert>
          This pull request changed since this review (new commits on it or on its base), so its
          details can't be retried. Regenerate the review instead.{" "}
          <Button variant="secondary" size="compact" disabled={generating} onClick={generate}>
            Regenerate
          </Button>
        </Alert>
      ) : null}
      {retryError ? <Alert>{retryError}</Alert> : null}
      {detailsJob &&
      detailsJob.id === watchedDetails &&
      !isActiveJob(detailsJob) &&
      detailsJob.state !== "completed" ? (
        <div className="local-job-outcome">
          <GenerationOutcome job={detailsJob} compact />
          <Button variant="secondary" size="compact" onClick={() => setWatchedDetails("")}>
            Dismiss
          </Button>
        </div>
      ) : null}
    </>
  );
  const sessionsVersion = viewedDocument?.review.version;
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
  const refreshSessions = useRef<(() => void) | null>(null);
  useVisibleInterval(() => refreshSessions.current?.(), 30_000);
  useEffect(() => {
    if (sessionsVersion == null) return;
    const abort = new AbortController();
    let pending = false;
    const refresh = async () => {
      if (pending || abort.signal.aborted) return;
      pending = true;
      try {
        const body = await request<unknown>(
          `api/sessions?${query}`,
          undefined,
          "GET",
          abort.signal,
        );
        const answer = parseSessionsAnswer(body);
        if (!abort.signal.aborted)
          setSessions((previous) => {
            const current =
              previous?.query === query && previous.version === sessionsVersion
                ? previous.load
                : null;
            // The resolver reports transient errors as HTTP 200. Keep the
            // last successful linkage until a genuine answer replaces it.
            if (
              current &&
              answer?.available === false &&
              typeof body === "object" &&
              body !== null &&
              "code" in body &&
              body.code === "error" &&
              current.state === "answered" &&
              current.answer.available
            )
              return previous;
            return {
              query,
              version: sessionsVersion,
              load: answer ? { state: "answered", answer } : { state: "request-failed" },
            };
          });
      } catch {
        if (!abort.signal.aborted)
          setSessions((previous) =>
            previous?.query === query &&
            previous.version === sessionsVersion &&
            previous.load.state === "answered"
              ? previous
              : { query, version: sessionsVersion, load: { state: "request-failed" } },
          );
      } finally {
        pending = false;
      }
    };
    void refresh();
    refreshSessions.current = () => void refresh();
    return () => {
      refreshSessions.current = null;
      abort.abort();
    };
  }, [sessionsVersion, query, revision, retry, ended]);
  // Without a saved version the page shows the pull request's facts: no
  // model's text, only what GitHub, the clone and the sessions on this
  // machine say. They are never a version: not listed, read or asked about.
  const wantsFacts = entry.version_count === 0 && !entry.needs_clone && Boolean(clone);
  const [factsRetry, setFactsRetry] = useState(0);
  const facts = useFacts(clone, pr.number, pr.head_sha, revision + factsRetry, wantsFacts);
  const shownFacts = !doc && facts.state === "ready" ? facts.facts : null;
  // The head's checks as GitHub lists them now, for the Details tab: the
  // review's own are a snapshot from when it was generated.
  const liveChecks = useChecks(pr.repository, pr.number, pr.head_sha);
  const factsHead = shownFacts?.review.head ?? "";
  const factsBase = shownFacts?.review.base ?? "";
  const factsMergeBase = shownFacts?.review.merge_base ?? "";
  // Each read names the head of the facts shown, so a newer build of them
  // never answers for what is on screen.
  const factsSelector = factsQuery(clone, pr.number, factsHead);
  const [factsSessions, setFactsSessions] = useState<SessionsLoad>({ state: "loading" });
  useEffect(() => {
    if (!factsHead) return;
    setFactsSessions({ state: "loading" });
    const abort = new AbortController();
    request<unknown>(`api/facts/sessions?${factsSelector}`, undefined, "GET", abort.signal)
      .then((body) => {
        const answer = parseSessionsAnswer(body);
        if (!abort.signal.aborted)
          setFactsSessions(answer ? { state: "answered", answer } : { state: "request-failed" });
      })
      .catch(() => {
        if (!abort.signal.aborted) setFactsSessions({ state: "request-failed" });
      });
    return () => abort.abort();
  }, [factsHead, factsSelector, ended]);
  // A new comparison base changes patches even when the head stays the same.
  const factsPatches = useMemo(() => {
    const cache = new Map<string, Promise<string | null>>();
    return (path: string) => {
      const key = `${factsHead}:${factsBase}:${factsMergeBase}:${path}`;
      let pending = cache.get(key);
      if (!pending) {
        pending = request<{ patch: string | null }>(
          `api/facts/patch?${factsSelector}&path=${encodeURIComponent(path)}`,
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
  }, [factsSelector, factsHead, factsBase, factsMergeBase]);
  const factsCommits = useMemo(
    () => localCommitLoaders("api/facts", factsSelector),
    // A new comparison base is another document, whose commits are read anew.
    [factsSelector, factsBase, factsMergeBase],
  );
  const versionCommits = useMemo(() => localCommitLoaders("api", query), [query]);
  const factsMode = !doc && wantsFacts && facts.state !== "unavailable";
  const lists = viewedDocument
    ? sessionLists(
        sessions?.query === query && sessions.version === sessionsVersion
          ? sessions.load
          : { state: "loading" },
        viewedDocument,
      )
    : !factsMode
      ? null
      : shownFacts
        ? sessionLists(factsSessions, shownFacts)
        : facts.state === "failed"
          ? { sessions: { status: "failed" as const }, commits: null }
          : { sessions: { status: "loading" as const }, commits: { status: "loading" as const } };
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
  // A file's whole diff for the Files pane, for the version on screen; kept
  // the same function while it is, so an opened file is not read again.
  const filesVersion = viewedDocument?.review.version ?? 0;
  const filePatches = useCallback(
    (path: string) => patches(path, filesVersion),
    [patches, filesVersion],
  );
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
            {walkMode && doc && !showingDraft ? (
              // A new version starts afresh, with that version's own progress.
              <Walkthrough
                key={doc.review.version}
                entry={entry}
                document={doc}
                mode={walkMode}
                setMode={setWalk}
                close={() => setWalk(null)}
                back={back}
                patches={patches}
                changed={changed}
                ask={{
                  open: askOpen,
                  toggle: () => (askOpen ? closeAsk() : setAsk(true)),
                  about: askAbout,
                }}
                toDraft={toDraft}
                drafted={() => setToDraft(false)}
                refresh={refreshButton}
                actions={
                  <>
                    {fix && fixLabelText ? (
                      <button
                        type="button"
                        className={SECONDARY}
                        disabled={fixing || fixStarting}
                        onClick={() => fix()}
                      >
                        <SparkleIcon size={14} className="flex-none text-(--status-critical)" />
                        {fixing ? "Fixing…" : fixStarting ? "Starting…" : fixLabelText}
                      </button>
                    ) : null}
                    {canMerge(entry) ? (
                      <button type="button" className={SECONDARY} onClick={() => setMerging(true)}>
                        Merge…
                      </button>
                    ) : null}
                    {canUpdateBranch(entry) ? (
                      <button type="button" className={SECONDARY} onClick={() => setUpdating(true)}>
                        Update branch
                      </button>
                    ) : null}
                    {canRerunChecks(entry) ? (
                      <button
                        type="button"
                        className={SECONDARY}
                        onClick={() => setRerunning(true)}
                      >
                        Re-run checks
                      </button>
                    ) : null}
                    {entry.status === "outdated" || generating ? (
                      <button
                        type="button"
                        className={SECONDARY}
                        disabled={generating || entry.needs_clone}
                        onClick={generate}
                      >
                        {generating ? "Generating review…" : "Regenerate review"}
                      </button>
                    ) : null}
                  </>
                }
                notices={
                  <>
                    {fixState.error ? (
                      <Alert>Couldn't start the fix: {fixState.error}</Alert>
                    ) : null}
                    {showFix && fixJob ? (
                      <FixNotice
                        key={fixJob.id}
                        job={fixJob}
                        changed={changed}
                        dismiss={fixing ? undefined : () => dismissFix(fixJob.id)}
                      />
                    ) : null}
                    <PullRequestDecisionsList
                      repository={pr.repository}
                      number={pr.number}
                      live={running}
                      revision={`${ended}:${doc.review.version}`}
                    />
                    <LiveSessions sessions={running} Link={ViewerLink} />
                    {gaps}
                    {retryNotices}
                    {failed ? (
                      <Alert>
                        The latest review could not be read. This saved version remains available.{" "}
                        <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
                      </Alert>
                    ) : null}
                    {generating ? (
                      <Notice>
                        A new review is being generated. This saved version remains available while
                        it runs.
                      </Notice>
                    ) : null}
                  </>
                }
              />
            ) : (
              <ReviewViewer
                document={shownDocument}
                openSource={(anchor) => {
                  // The description and other pull requests of this repository
                  // open here; the chip's link to GitHub is the fallback.
                  if (anchor.type === "pr_body") {
                    setDescription({ section: anchor.section ?? undefined });
                    return true;
                  }
                  if (anchor.type === "pr") {
                    navigate(
                      appURL(
                        `?${back.prefix}${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${anchor.number}`,
                      ),
                    );
                    return true;
                  }
                  return false;
                }}
                openRepositoryFile={
                  // A draft is no saved version, so the daemon has no
                  // document to read its files for; its chips keep GitHub.
                  showingDraft
                    ? undefined
                    : (reference) => {
                        const document = viewedDocument ?? shownDocument;
                        if (!document) return false;
                        setRepositoryFile({
                          reference,
                          source: `api/file?${query}`,
                          href: contextRefUrl(document, reference.path),
                        });
                        return true;
                      }
                }
                history={showingDraft ? undefined : history}
                versions={showingDraft ? [] : versions.filter((v) => v !== doc?.review.version)}
                loadVersion={showingDraft ? undefined : loadVersion}
                versionsFailed={
                  historyFailed && !showingDraft
                    ? { retry: () => setRetry((n) => n + 1) }
                    : undefined
                }
                contextPending={contextPending}
                onRetryDetails={
                  doc && !showingDraft && !generating && failedSections.length > 0
                    ? () => retryDetails()
                    : undefined
                }
                steps={steps}
                detailsChecks={(shown) =>
                  liveDetailsChecks(
                    shown,
                    {
                      repository: pr.repository,
                      number: pr.number,
                      head_sha: pr.head_sha,
                      url: pr.url,
                      canRetry: pr.viewer_can_push === true,
                    },
                    liveChecks,
                  )
                }
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
                  actions: refreshButton,
                  // The stacks and groups it belongs to, under the title with its
                  // other facts rather than as a notice over the review.
                  subtitle:
                    groups.length > 0 ? (
                      <>
                        {groups.map((group) => (
                          <ViewerLink
                            key={group.id}
                            href={appURL(`?${back.prefix}group=${encodeURIComponent(group.id)}`)}
                            className="local-group-link"
                            title={`Open the ${group.stack ? "stack" : "group"}`}
                          >
                            <StackIcon size={14} className="flex-none" />
                            <span className="local-group-link-kind">
                              {group.stack ? "Stack" : "Group"}
                            </span>
                            <span className="local-group-link-name">{group.name}</span>
                          </ViewerLink>
                        ))}
                      </>
                    ) : undefined,
                  // Generating the review again is about the review, so it sits
                  // with its versions.
                  versionActions: doc
                    ? [
                        {
                          label: generating
                            ? starting
                              ? "Starting review…"
                              : job?.state === "queued"
                                ? "Review queued"
                                : "Generating review…"
                            : "Regenerate review",
                          onSelect: generate,
                          disabled: generating || entry.needs_clone,
                        },
                      ]
                    : undefined,
                  // Without a review, the button in the page's middle generates it.
                  // The GitHub state leads, so a reader sees why a fix, a re-run or an
                  // update is offered; a fix comes first of those, re-running checks
                  // and updating the branch are its smaller remedies.
                  beside:
                    doc ||
                    canReview ||
                    pullRequestStates(entry).length > 0 ||
                    (fix && fixLabelText) ||
                    canMerge(entry) ||
                    canRerunChecks(entry) ||
                    canUpdateBranch(entry) ? (
                      // The pull request's state stays by the version and its age,
                      // holding its remedies (a fix, re-running the checks, updating
                      // the branch) in its menu; Ask and Merge stay at the row's end.
                      <div
                        data-tour="review-actions"
                        className="flex flex-wrap items-center gap-x-4 gap-y-2"
                      >
                        <StatusMenu
                          entry={entry}
                          items={[
                            ...(fix && fixLabelText && !fixing && !fixStarting
                              ? [{ label: fixLabelText, onSelect: () => fix() }]
                              : []),
                            ...(canRerunChecks(entry)
                              ? [{ label: "Re-run checks", onSelect: () => setRerunning(true) }]
                              : []),
                            ...(canUpdateBranch(entry)
                              ? [{ label: "Update branch", onSelect: () => setUpdating(true) }]
                              : []),
                          ]}
                        />
                        {fixing || fixStarting ? (
                          <span className="inline-flex items-center gap-1.5 text-xs text-(--text-secondary)">
                            <LiveDot />
                            {fixing ? "Fixing" : "Starting the fix"}
                          </span>
                        ) : null}
                        <div className="ml-auto flex flex-wrap items-center gap-2">
                          {doc && !showingDraft ? (
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
                          {canReview ? (
                            <ReviewButton
                              entry={entry}
                              count={pendingCount}
                              review={setReviewing}
                            />
                          ) : null}
                          {canMerge(entry) ? (
                            <Button size="compact" onClick={() => setMerging(true)}>
                              Merge…
                            </Button>
                          ) : null}
                        </div>
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
                    {fixState.error ? (
                      <Alert>Couldn't start the fix: {fixState.error}</Alert>
                    ) : null}
                    {showFix && fixJob ? (
                      <FixNotice
                        key={fixJob.id}
                        job={fixJob}
                        changed={changed}
                        dismiss={fixing ? undefined : () => dismissFix(fixJob.id)}
                      />
                    ) : null}
                    <FixRuns runs={earlierRuns} earlier={showFix} />
                    {factsMode && facts.state === "failed" ? (
                      <Alert>
                        {facts.rateLimited
                          ? "GitHub's rate limit is reached, so this pull request's checks, sessions and files load once it resets."
                          : "This pull request's checks, sessions and files couldn't be read."}{" "}
                        <Button onClick={() => setFactsRetry((n) => n + 1)}>Retry</Button>
                      </Alert>
                    ) : null}
                    {entry.needs_clone && doc ? (
                      <Notice>
                        Needs a local clone. This saved review remains available.
                        <CloneActions repository={pr.repository} changed={changed} />
                      </Notice>
                    ) : null}
                    {showingDraft ? (
                      <Notice>
                        Draft — the review is still being written. Sections fill in as they're
                        written.
                      </Notice>
                    ) : null}
                    {job && isActiveJob(job) && (doc || showingDraft) ? (
                      <ReviewJob key={job.id} job={job} changed={changed} saved={Boolean(doc)} />
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
                    {/* The job's own card says so, once there is one. */}
                    {generating && doc && !showingDraft && !(job && isActiveJob(job)) ? (
                      <Notice>
                        A new review is being generated. This saved version remains available while
                        it runs.
                      </Notice>
                    ) : null}
                    {gaps}
                    {retryNotices}
                  </>
                }
                sessions={
                  (lists && !(showingDraft && doc)) || listed.length > 0
                    ? pullRequestSessions(
                        lists && !(showingDraft && doc)
                          ? lists.sessions
                          : { status: "ready", sessions: [] },
                        listed,
                        (session, focusTurn) =>
                          viewedDocument ? (
                            <Transcript
                              id={session.id}
                              query={query}
                              title={session.title ?? undefined}
                              focusTurn={focusTurn}
                            />
                          ) : (
                            <Transcript
                              id={session.id}
                              query={factsSelector}
                              route="api/facts/sessions"
                              title={session.title ?? undefined}
                              focusTurn={focusTurn}
                            />
                          ),
                      )
                    : undefined
                }
                commits={
                  lists?.commits && !(showingDraft && doc)
                    ? withCommitRows(
                        commitsSlot(lists.commits, viewedDocument ? versionCommits : factsCommits),
                        lists.commits,
                      )
                    : undefined
                }
                decisions={
                  decisions
                    ? {
                        count: decisions.total,
                        renderPane: () => (
                          <PullRequestDecisionsPane read={decisions} live={running} heading />
                        ),
                      }
                    : undefined
                }
                facts={shownFacts}
                comments={
                  canReview
                    ? {
                        count: pendingCount,
                        reveal: revealComments,
                        render: () => (
                          <CommentsPane
                            entry={entry}
                            current={commentOn}
                            composer={composer}
                            setComposer={setComposer}
                            review={setReviewing}
                            fresh={sentReview}
                          />
                        ),
                      }
                    : undefined
                }
                files={
                  factsMode
                    ? factsFilesSlot(facts, () => setFactsRetry((n) => n + 1), factsPatches, pr.url)
                    : viewedDocument && !showingDraft
                      ? // Every file of the change, as GitHub's Files changed shows them,
                        // each diff read for the version on screen.
                        documentFilesSlot(
                          viewedDocument.index.hunks,
                          filePatches,
                          `${pr.url}/files`,
                        )
                      : undefined
                }
                hunkPatches={
                  showingDraft ? (doc ? undefined : (path: string) => factsPatches(path)) : patches
                }
              />
            )}
          </div>
          {askOpen && doc && !showingDraft ? (
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
                document={!walkMode && viewedDocument ? viewedDocument : doc}
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
                        setWalk("tour");
                        setToDraft(true);
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
        {description ? (
          <DescriptionDialog
            repository={pr.repository}
            number={pr.number}
            section={description.section}
            close={() => setDescription(null)}
          />
        ) : null}
        {repositoryFile ? (
          <RepositoryFileDialog
            reference={repositoryFile.reference}
            source={repositoryFile.source}
            href={repositoryFile.href}
            close={() => setRepositoryFile(null)}
          />
        ) : null}
        {merging ? (
          <MergeDialog entry={entry} close={() => setMerging(false)} merged={changed} />
        ) : null}
        {reviewing ? (
          <ReviewDialog
            entry={entry}
            event={reviewing}
            close={() => setReviewing(null)}
            sent={(url) => {
              // Where it shows: the Comments tab opens on it under Sent.
              setSentReview(url);
              setRevealComments((n) => n + 1);
              void changed();
            }}
          />
        ) : null}
        {rerunning ? (
          <RerunChecksDialog entry={entry} close={() => setRerunning(false)} rerun={changed} />
        ) : null}
        {updating ? (
          <UpdateBranchDialog entry={entry} close={() => setUpdating(false)} updated={changed} />
        ) : null}
        {confirmingIsolation ? (
          <dialog
            ref={isolationDialog}
            onCancel={() => setConfirmingIsolation(false)}
            aria-labelledby="retry-isolation-title"
          >
            <h2 id="retry-isolation-title" className="text-lg font-semibold">
              Review code from outside your team?
            </h2>
            <p className="my-3">
              This pull request comes from a fork or from an author without write access. Retrying
              its details runs your agent on its code. Only continue if you trust it, or if your
              agent runs in an isolated environment.
            </p>
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="compact" onClick={() => setConfirmingIsolation(false)}>
                Cancel
              </Button>
              <Button size="compact" disabled={retryStarting} onClick={() => retryDetails(true)}>
                {retryStarting ? "Starting…" : "Retry anyway"}
              </Button>
            </div>
          </dialog>
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
/** The pull request's GitHub state beside the version, as a menu of what can
 *  be done about it (`items`): a fix, re-running its checks, updating its
 *  branch. Without any it is the state alone, as text. */
function StatusMenu({ entry, items }: { entry: Entry; items: MenuItem[] }) {
  const menu = useContextMenu();
  const ref = useRef<HTMLButtonElement>(null);
  if (pullRequestStates(entry).length === 0) return null;
  if (items.length === 0) return <PullRequestState entry={entry} />;
  const open = menu.anchor !== null && menu.anchor === ref.current;
  return (
    <>
      <button
        ref={ref}
        type="button"
        className="local-status-menu"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menu.id : undefined}
        onClick={(event) => {
          // A click on the open menu's own button closes it, as the row menus do.
          const button = event.currentTarget;
          if (button.dataset.closing !== undefined) delete button.dataset.closing;
          else menu.openFrom(button, items, "Pull request actions");
        }}
        onPointerLeave={(event) => delete event.currentTarget.dataset.closing}
      >
        <PullRequestState entry={entry} />
        <ChevronDownIcon size={12} className="flex-none text-(--text-tertiary)" />
      </button>
      {menu.menu}
    </>
  );
}

export function GeneratedBy({
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
            href={jobHref(made)}
            onClick={(event) => {
              event.preventDefault();
              navigate(event.currentTarget.href);
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
  const details = job.kind === "details";
  const title = details
    ? failed
      ? "The details couldn't be written"
      : "Writing the details was stopped"
    : failed
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
        {job.reason ? <p>{jobReasonText(job.reason, job.kind)}</p> : null}
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
export function ReviewJob({
  job,
  changed,
  saved = true,
}: {
  job: Job;
  changed: () => Promise<void>;
  /** A saved version stays readable while this one is written. */
  saved?: boolean;
}) {
  const started = automaticLabel(job);
  return (
    <section aria-label="Review generation" className="local-job-outcome">
      <LiveDot />
      <div className="min-w-0 flex-1">
        <p role="status" className="font-medium">
          {job.state === "queued" ? "New version queued" : "Generating a new version"}
          {/* The title already says it generates; another phase is news. */}
          {job.state === "running" && job.phase && job.phase !== "generating" ? (
            <span className="font-normal text-(--text-tertiary)"> · {job.phase}</span>
          ) : null}
        </p>
        {started || saved ? (
          <p className="text-(--text-tertiary)">
            {[started, saved ? "The saved version stays available while it runs." : ""]
              .filter(Boolean)
              .join(". ")}
          </p>
        ) : null}
      </div>
      <CancelJob job={job} changed={changed} />
    </section>
  );
}
