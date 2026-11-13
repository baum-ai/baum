import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import type { DetailState, OpenSessionSource } from "@baum/viewer/components/decision-log";
import type { FeatureState } from "@baum/viewer/components/feature-log";
import { formatCount } from "@baum/viewer/components/format";
import { Empty } from "@baum/viewer/components/states";
import { PageRead, readState } from "./page-state";
import {
  type TimelineList,
  TimelinePage as SharedTimelinePage,
} from "@baum/viewer/components/timeline-page";
import { Alert, Button } from "@baum/viewer/components/ui";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import {
  type AuthorCount,
  type DecisionDetail,
  type DecisionFile,
  type DecisionSummary,
  decisionQuery,
  type FeatureDetail,
  type FeatureSummary,
  featureQuery,
  type LogTab,
  parseDecisionFilters,
  parseFeatureStatuses,
  withDecisionFilters,
} from "@baum/viewer/decision-log";
import { useUrlState } from "@baum/viewer/navigation";
import { humanize } from "@baum/viewer/review-document";
import { appURL, isActiveJob, request, type Job } from "./api";
import {
  BuildStatus,
  buildJobOf,
  NO_CLONE,
  useBuildState,
  useFeatureBuildStart,
} from "./feature-build";
import { LoadingStatus } from "./nine-dots";
import {
  type LiveTranscriptAnswer,
  parseLiveTranscript,
  TranscriptCard,
} from "./session-transcript";
import { PageBar, RefreshButton, useReread } from "./shell";
import { listKeys, useRowFocus, useShortcuts } from "./shortcuts";
import { FilterBar, FilterButton } from "./filter-menu";
import { KeywordSearch } from "@baum/viewer/components/decision-filters";
import {
  timelineFields,
  timelineFilter,
  timelineQuery,
  withTimelineQuery,
} from "./timeline-filter";
import "./timeline.css";

/** The Timeline: the picked repositories' features and decisions from the
 *  local decision log (`api/timeline`), drawn by the review viewer's
 *  `TimelinePage` as the hosted Timeline is. Its address holds the
 *  repositories (`repo=`, repeated; none is all of them), tab, filters and
 *  what is open, so a reload shows the same. */

export type Coverage = {
  extracted: number;
  pending: number;
  skipped: number;
  skipped_by_reason: Record<string, number>;
};
export type FeaturePage = { features: FeatureSummary[]; next_cursor: string | null };
export type DecisionPage = {
  decisions: DecisionSummary[];
  next_cursor: string | null;
  coverage: Coverage;
};

const PAGE_DETAIL = "What was decided and built, from your agent sessions";
/** Rows a list reads at a time, and the most one request reads. */
const PAGE = 50;
const MAX_PAGE = 500;

/** The app page of a pull request, by its repository. */
export function pullRequestHref(repository: string, number: number): string {
  return appURL(`?repository=${encodeURIComponent(repository)}&pr=${number}`);
}

/** A decision's prompts are read from its session's transcript on this
 *  computer; once that is gone they are withheld. */
export const WITHHELD_NOTE = "The session's transcript is no longer on this computer.";

/** A decision's session opens in its detail at the turn it cites, read
 *  from its transcript on this computer whether it still runs or ended long
 *  ago (`api/timeline/sessions/<agent>:<id>/transcript`). */
const OPEN_SESSION: OpenSessionSource<LiveTranscriptAnswer> = {
  load: async (sessionId, signal) => {
    const answer = parseLiveTranscript(
      await request<unknown>(
        `api/timeline/sessions/${encodeURIComponent(sessionId)}/transcript`,
        undefined,
        "GET",
        signal,
      ),
    );
    if (!answer || (answer.available && !answer.transcript))
      throw Error("The daemon's answer was not a transcript.");
    return answer;
  },
  render: (answer, at) => {
    if (!answer.available || !answer.transcript)
      return (
        <p className="text-base text-tertiary italic">
          {answer.available ? WITHHELD_NOTE : answer.message}
        </p>
      );
    const { prompts } = answer.transcript.session;
    return (
      <>
        {/* The transcript lists the prompts it kept; a decision can cite a
            turn that is not one of them. */}
        {prompts.length === 0 || prompts.some((prompt) => prompt.ordinal === at.turn) ? null : (
          <p className="mb-2 text-base text-tertiary italic">
            Turn {formatCount(at.turn)} is not among the prompts shown for this session.
          </p>
        )}
        <TranscriptCard
          transcript={answer.transcript}
          version={answer.version}
          summary={answer.summary}
          live={answer.live}
          focusTurn={at.turn}
        />
      </>
    );
  },
};

type ServedRepository = { repository: string };

type RepositoryList = {
  available?: false;
  decisions_enabled?: boolean;
  repositories?: ServedRepository[];
  turned_off?: string[];
};

/** Whether the repository list leads to the log rather than a page saying
 *  why there is none. */
function showsLog(list: RepositoryList) {
  return (
    (list.repositories?.length ?? 0) > 0 &&
    list.available !== false &&
    list.decisions_enabled !== false
  );
}

/** The Timeline page. `revision` changes when the daemon says the decision
 *  log changed (its `timeline` event) or the event stream reconnected, and
 *  the page reads everything it shows again. */
export function TimelinePage({
  revision: logRevision,
  jobs = [],
  cloneOf = () => null,
  jobsChanged = () => {},
}: {
  revision: number;
  /** The job queue's jobs, for the repository's feature build. */
  jobs?: Job[];
  /** The available clone a repository's feature build runs in. */
  cloneOf?: (repository: string) => string | null;
  /** Reads the jobs again, once a build was started. */
  jobsChanged?: () => void;
}) {
  const url = useUrlState();
  const [list, setList] = useState<RepositoryList | null>(null);
  const [failed, setFailed] = useState(false);
  // The page's refresh reads everything again, as a change in the log does.
  // It settles once the repositories are read and, where they lead to the
  // log, the log's list too: by the refresh count each was read at.
  const reread = useReread();
  const revision = logRevision + reread.count;
  const reads = useRef({ list: -1, log: -1, withLog: false });
  const settle = () => {
    const { list, log, withLog } = reads.current;
    if (list >= 0 && (!withLog || log >= list)) reread.settled(list);
  };
  useEffect(() => {
    const abort = new AbortController();
    const count = reread.count;
    request<RepositoryList>("api/timeline/repositories", undefined, "GET", abort.signal)
      .then((body) => {
        setList(body);
        setFailed(false);
        reads.current = { ...reads.current, list: count, withLog: showsLog(body) };
        settle();
      })
      .catch(() => {
        if (abort.signal.aborted) return;
        setFailed(true);
        reads.current = { ...reads.current, list: count, withLog: false };
        settle();
      });
    return () => abort.abort();
  }, [revision]);
  const served = list?.repositories ?? [];
  const asked = url.search.getAll("repo").map((name) => name.toLowerCase());
  // The repositories the address names that are served; none is all.
  const selected = served
    .map((each) => each.repository)
    .filter((name) => asked.includes(name.toLowerCase()));
  const scope = selected.length > 0 ? selected : served.map((each) => each.repository);
  // A later read that failed leaves the page as it was and says so.
  const stale =
    list && failed ? (
      <div className="local-list-note">
        <Alert className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span>Couldn't read the Timeline again. It may be out of date.</span>
          <Button variant="secondary" size="compact" onClick={() => void reread.again()}>
            Retry
          </Button>
        </Alert>
      </div>
    ) : null;
  if (list && showsLog(list))
    return (
      <div className="timeline-page">
        {stale}
        <TimelineLog
          key={scope.join("\n")}
          scope={scope}
          all={selected.length === 0}
          served={served}
          revision={revision}
          jobs={jobs}
          cloneOf={cloneOf}
          jobsChanged={jobsChanged}
          refresh={reread.again}
          settled={() => {
            reads.current.log = Math.max(reads.current.log, reread.count);
            settle();
          }}
        />
      </div>
    );
  return (
    <div className="timeline-page">
      <PageBar
        title="Timeline"
        detail={PAGE_DETAIL}
        actions={<RefreshButton refresh={reread.again} />}
      />
      {stale}
      {!list ? (
        <div className="local-page-body">
          <PageRead
            state={readState({ loaded: false, failed })}
            what="the Timeline"
            retry={reread.again}
          />
        </div>
      ) : list.available === false ? (
        <div className="local-page-body">
          <Empty title="The Timeline is not available">
            baum could not open its decision store on this computer, so there is nothing to show.
            Diagnostics in Settings may say why.
          </Empty>
        </div>
      ) : list.decisions_enabled === false ? (
        <div className="local-page-body">
          <Empty title="Decision capture is off">
            baum's decisions switch is off, so it reads no agent sessions and shows no Timeline.
          </Empty>
        </div>
      ) : (
        <div className="local-page-body">
          <Empty title="No repositories with decisions">
            {list.turned_off && list.turned_off.length > 0
              ? `Decisions are turned off for ${list.turned_off.join(", ")}. `
              : null}
            The Timeline shows the decisions baum reads from agent sessions in your picked
            repositories with decisions on. Pick one in Repositories to start.
          </Empty>
        </div>
      )}
    </div>
  );
}

type Pages<T> = {
  /** The revision of the latest read from the first page that ended. */
  readAt: number;
  rows: T[];
  cursor: string | null;
  status: "loading" | "idle" | "failed";
  /** The first page has been read for the current query. */
  loaded: boolean;
  coverage: Coverage | null;
  loadMore: () => void;
};

/** A paged list of `path` under `query`, read again when either or the
 *  revision changes; older rows load by button. */
function usePages<T>(
  path: string,
  query: string,
  key: "features" | "decisions",
  revision: number,
  active: boolean,
): Pages<T> {
  type State = {
    query: string;
    revision: number;
    readAt: number;
    rows: T[];
    cursor: string | null;
    status: "loading" | "idle" | "failed";
    loaded: boolean;
    coverage: Coverage | null;
  };
  const [state, setState] = useState<State>({
    query,
    revision,
    readAt: -1,
    rows: [],
    cursor: null,
    status: "loading",
    loaded: false,
    coverage: null,
  });
  const latest = useRef(state);
  latest.current = state;
  const more = useRef<AbortController | null>(null);
  // Counts the list's reads from its first page: a load of more rows
  // belongs to the one it started in and is dropped after another.
  const generation = useRef(0);
  useEffect(() => {
    // Only the tab shown reads its list.
    if (!active) return;
    const abort = new AbortController();
    const base = latest.current;
    // Read again for a change in the log, the list keeps as many rows as
    // it had loaded.
    const again = base.query === query && base.loaded;
    if (again && base.revision === revision) return;
    // A load of more rows started before is for the old list.
    more.current?.abort();
    generation.current += 1;
    const want = again ? Math.max(PAGE, base.rows.length) : PAGE;
    setState((current) => ({ ...current, status: "loading" }));
    const read = async () => {
      // As many pages as it takes, each at most MAX_PAGE rows.
      let rows: T[] = [];
      let cursor: string | null = null;
      let coverage: Coverage | null = null;
      do {
        const limit = Math.min(MAX_PAGE, want - rows.length);
        const params = new URLSearchParams(query);
        if (limit !== PAGE) params.set("limit", String(limit));
        if (cursor) params.set("cursor", cursor);
        const body = await request<Record<string, unknown>>(
          withQuery(path, params),
          undefined,
          "GET",
          abort.signal,
        );
        rows = [...rows, ...((body[key] as T[]) ?? [])];
        cursor = (body.next_cursor as string | null) ?? null;
        coverage = (body.coverage as Coverage | undefined) ?? null;
      } while (cursor && rows.length < want);
      setState({
        query,
        revision,
        readAt: revision,
        rows,
        cursor,
        status: "idle",
        loaded: true,
        coverage,
      });
    };
    read().catch(() => {
      if (abort.signal.aborted) return;
      setState((current) => ({ ...current, readAt: revision, status: "failed" }));
    });
    return () => abort.abort();
  }, [path, query, key, revision, active]);
  const loadMore = useCallback(() => {
    const base = latest.current;
    if (!base.cursor || base.status === "loading") return;
    const abort = new AbortController();
    more.current = abort;
    const started = generation.current;
    setState({ ...base, status: "loading" });
    const params = new URLSearchParams(query);
    params.set("cursor", base.cursor);
    request<Record<string, unknown>>(withQuery(path, params), undefined, "GET", abort.signal)
      .then((body) =>
        setState((current) =>
          generation.current !== started || current.query !== base.query
            ? current
            : {
                ...current,
                rows: [...current.rows, ...((body[key] as T[]) ?? [])],
                cursor: (body.next_cursor as string | null) ?? null,
                status: "idle",
              },
        ),
      )
      .catch(() => {
        if (!abort.signal.aborted && generation.current === started)
          setState((current) => ({ ...current, status: "failed" }));
      });
  }, [path, query, key]);
  return { ...state, loaded: state.loaded && state.query === query, loadMore };
}

function withQuery(path: string, params: URLSearchParams): string {
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}

const readDecision = async (id: string, signal: AbortSignal): Promise<DetailState> => {
  try {
    const detail = await request<DecisionDetail>(
      `api/timeline/decisions/${encodeURIComponent(id)}`,
      undefined,
      "GET",
      signal,
    );
    return { status: "ready", detail };
  } catch (e) {
    if ((e as { status?: number }).status === 404) return { status: "missing" };
    throw e;
  }
};

const readFeature = async (id: string, signal: AbortSignal): Promise<FeatureState> => {
  try {
    const body = await request<FeatureDetail | { archived: true }>(
      `api/timeline/features/${encodeURIComponent(id)}`,
      undefined,
      "GET",
      signal,
    );
    if ("archived" in body) return { status: "archived" };
    // A feature merged into another answers with the one it was merged into.
    if (body.feature.id !== id) return { status: "moved", id: body.feature.id };
    return { status: "ready", detail: body };
  } catch (e) {
    if ((e as { status?: number }).status === 404) return { status: "missing" };
    throw e;
  }
};

function TimelineLog({
  scope,
  all,
  served,
  revision: logRevision,
  jobs,
  cloneOf,
  jobsChanged,
  refresh,
  settled,
}: {
  /** The repositories shown. */
  scope: string[];
  /** The address names none: every served repository is shown. */
  all: boolean;
  served: ServedRepository[];
  revision: number;
  jobs: Job[];
  cloneOf: (repository: string) => string | null;
  jobsChanged: () => void;
  /** Reads the Timeline again, from the bar's refresh, until `settled`. */
  refresh: () => Promise<void>;
  settled: () => void;
}) {
  // A view of one repository has its feature build: its job and its
  // building version. Every repository shown with a clone can be built, and
  // a build that finishes changes the features, so the lists are read again.
  const single = scope.length === 1 ? (scope[0] ?? null) : null;
  const several = single === null;
  const clone = single ? cloneOf(single) : null;
  const buildJob = single ? buildJobOf(jobs, single) : undefined;
  const targets = scope.flatMap((repository) => {
    const each = cloneOf(repository);
    return each ? [{ repository, clone: each }] : [];
  });
  const running = scope.filter((repository) => isActiveJob(buildJobOf(jobs, repository)));
  const buildActive = running.length > 0;
  const [buildFinished, setBuildFinished] = useState(0);
  const wasRunning = useRef(running);
  useEffect(() => {
    if (wasRunning.current.some((repository) => !running.includes(repository)))
      setBuildFinished((n) => n + 1);
    wasRunning.current = running;
  }, [running.join("\n")]);
  const revision = logRevision + buildFinished;
  const buildState = useBuildState(
    single,
    `${revision}:${buildJob?.id ?? ""}:${buildJob?.state ?? ""}:${buildJob?.calls ?? 0}`,
  );
  const { building, hasFeatures } = buildState;
  const build = useFeatureBuildStart(targets, jobsChanged);
  const url = useUrlState();
  const search = url.search;
  const tab: LogTab = search.get("tab") === "all" ? "all" : "features";
  // The filters live in the address in the shared decision log's form.
  const filters = parseDecisionFilters(search);
  const statuses = parseFeatureStatuses(search);

  const scopeKey = scopeQuery(scope, all);
  const scoped = (query: URLSearchParams) => {
    const params = new URLSearchParams(scopeKey);
    for (const [key, value] of query) params.append(key, value);
    return params.toString();
  };
  const features = usePages<FeatureSummary>(
    "api/timeline/features",
    scoped(featureQuery(filters, statuses)),
    "features",
    revision,
    tab === "features",
  );
  const decisions = usePages<DecisionSummary>(
    "api/timeline/decisions",
    scoped(decisionQuery(filters)),
    "decisions",
    revision,
    tab === "all",
  );
  // The open tab's list has been read at the revision shown: a refresh,
  // which changes the revision, has its answer. Read from what the list
  // holds, so a read the list skips does not leave the refresh waiting.
  const shownList = tab === "features" ? features : decisions;
  const read = shownList.status !== "loading" && shownList.readAt === revision;
  useEffect(() => {
    if (read) settled();
  }, [read, revision]);
  // The keys move through the open tab's features or decisions and open
  // them, as through any list.
  const rows = useRowFocus<HTMLDivElement>(
    `timeline:${tab}`,
    (tab === "features" ? features.rows : decisions.rows).map((row) => row.id),
  );
  // Whether the keys moved the focus, which an open feature's panel follows;
  // a row the list comes back on leaves the panel as the address has it.
  const moved = useRef(false);
  useShortcuts((event) => {
    moved.current = ["j", "k", "ArrowDown", "ArrowUp"].includes(event.key);
    if (listKeys(event, rows)) return;
    if (event.key === "/") {
      event.preventDefault();
      rows.list.current?.querySelector<HTMLInputElement>(".timeline-search input")?.focus();
    }
  });
  // A feature open in the panel beside the list follows the keys: the row
  // they move to opens there in its place. A row that opens in place waits
  // for Enter.
  useEffect(() => {
    if (!moved.current) return;
    moved.current = false;
    const link = rows.focused ? rows.rowLink(rows.focused) : null;
    const panel = document.getElementById(link?.getAttribute("aria-controls") ?? "");
    const beside = !!panel && !link?.closest("[data-row-id]")?.contains(panel);
    if (beside && link?.getAttribute("aria-expanded") !== "true") link?.click();
  }, [rows.focused]);
  // The people the person filter offers: those with decisions on All
  // decisions, those among the features' people on Features.
  const readPeople = useCallback(
    async (on: LogTab, signal: AbortSignal) => {
      const path = on === "features" ? "api/timeline/features/people" : "api/timeline/people";
      const body = await request<{ people?: AuthorCount[] }>(
        withQuery(path, new URLSearchParams(scopeKey)),
        undefined,
        "GET",
        signal,
      );
      return body.people ?? [];
    },
    [scopeKey],
  );
  // Stable while the repositories are, so a suggestion under way is not
  // restarted by every render.
  const files = useCallback(
    async (prefix: string, signal?: AbortSignal) => {
      const params = new URLSearchParams(scopeKey);
      if (prefix) params.set("prefix", prefix);
      const body = await request<{ files?: DecisionFile[] }>(
        withQuery("api/timeline/files", params),
        undefined,
        "GET",
        signal,
      );
      return body.files ?? [];
    },
    [scopeKey],
  );
  // The filter is written as the Inbox's is (`timeline-filter.ts`), over the
  // filters the address holds. The people and files it offers are the open
  // tab's and repositories'.
  const onFeatures = tab === "features";
  const [people, setPeople] = useState<AuthorCount[]>([]);
  useEffect(() => {
    const abort = new AbortController();
    readPeople(tab, abort.signal).then(setPeople, () => {});
    return () => abort.abort();
  }, [tab, readPeople, revision]);
  // The files and folders under what is typed, as `file=oss/app/` lists the
  // folder's; a search in the menu reads paths only once it looks like one.
  const [filePrefix, setFilePrefix] = useState("");
  const [topFiles, setTopFiles] = useState<DecisionFile[]>([]);
  useEffect(() => {
    const abort = new AbortController();
    files(filePrefix, abort.signal).then(setTopFiles, () => {});
    return () => abort.abort();
  }, [filePrefix, files, revision]);
  const typed = useCallback((key: string | null, partial: string) => {
    if (key === "file" || (key === null && (partial === "" || /[/.]/.test(partial))))
      setFilePrefix(partial);
  }, []);
  const repositoryNames = served.map((each) => each.repository).join("\n");
  const servedNames = useMemo(
    () => (repositoryNames ? repositoryNames.split("\n") : []),
    [repositoryNames],
  );
  const filterFields = useMemo(
    () =>
      timelineFields({
        features: onFeatures,
        repositories: servedNames,
        people,
        files: topFiles,
      }),
    [onFeatures, servedNames, people, topFiles],
  );
  const pageFilter = useMemo(
    () => ({ ...timelineFilter(filterFields), typed }),
    [filterFields, typed],
  );
  const query = timelineQuery(search, onFeatures, servedNames);
  // What was typed stays as typed while it asks for what the address holds;
  // a change from elsewhere (Clear, Back) shows the address's own.
  const [sent, setSent] = useState("");
  const shown =
    sent &&
    !pageFilter.problem(sent) &&
    timelineQuery(
      withTimelineQuery(search, sent, filterFields, onFeatures),
      onFeatures,
      servedNames,
    ) === query
      ? sent
      : query;
  const setQuery = (next: string) => {
    setSent(next);
    url.update((current) => withTimelineQuery(current, next, filterFields, onFeatures));
  };
  const coverage = decisions.coverage;
  // Over several repositories the menu is always there; one repository needs
  // features at all, not whether the filters leave any, since its empty
  // state offers the first build.
  const showMore = several || hasFeatures;
  const rebuildBlocked = buildActive
    ? several
      ? `A feature build is running for ${running.join(", ")}. Rebuild when it finishes.`
      : "A feature build is running. Rebuild when it finishes."
    : targets.length === 0
      ? several
        ? "None of these repositories has a local clone"
        : NO_CLONE
      : "";
  const rebuildNote =
    several && targets.length > 0 && targets.length < scope.length
      ? `For ${targets.length} of ${scope.length} repositories; the others need a local clone`
      : "";
  return (
    <>
      <div ref={rows.list} className="timeline-log">
        <SharedTimelinePage
          withFeatures
          focusedRow={rows.focused}
          features={timelineList(features)}
          decisions={timelineList(decisions)}
          readDecision={readDecision}
          readFeature={readFeature}
          revision={revision}
          files={files}
          openSession={OPEN_SESSION}
          pullRequestHref={pullRequestHref}
          repository={single ?? ""}
          repositoryLabel={several ? (name) => name : undefined}
          withheldNote={WITHHELD_NOTE}
          // The Inbox's filter in place of the hosted Timeline's controls.
          filterBar={() => (
            <div className="timeline-top">
              <PageBar
                title="Timeline"
                detail={PAGE_DETAIL}
                actions={<RefreshButton refresh={refresh} />}
              />
              {build.error ? (
                <Alert className="mx-5 mt-3">Couldn't start the feature build: {build.error}</Alert>
              ) : null}
              <div className="local-filter-bar timeline-filters">
                <div className="timeline-search">
                  <KeywordSearch
                    label={onFeatures ? "Search features" : "Search decisions"}
                    value={filters.q}
                    onChange={(q) => url.update((current) => withDecisionFilters(current, { q }))}
                  />
                  <kbd aria-hidden="true" className="key-hint">
                    /
                  </kbd>
                </div>
                <FilterButton
                  query={shown}
                  onChange={setQuery}
                  entries={[]}
                  page={pageFilter}
                  slashOpens={false}
                  footer={
                    <FilterBar
                      key={tab}
                      query={shown}
                      onChange={setQuery}
                      entries={[]}
                      page={pageFilter}
                    />
                  }
                />
                {showMore ? (
                  <MoreMenu
                    starting={build.starting}
                    blocked={rebuildBlocked}
                    note={rebuildNote}
                    onRebuild={() => void build.start()}
                  />
                ) : null}
              </div>
            </div>
          )}
          tabsClassName="timeline-tabs"
          bodyClassName="local-page-body timeline-body"
          loading={(label) => <LoadingStatus block>{label}</LoadingStatus>}
          featuresIntro={
            single ? (
              <BuildStatus
                job={buildJob}
                state={buildState}
                clone={clone}
                onBuild={() => void build.start()}
                starting={build.starting}
              />
            ) : null
          }
          decisionsIntro={
            coverage && coverage.extracted + coverage.skipped + coverage.pending > 0 ? (
              <CoverageLine coverage={coverage} />
            ) : null
          }
          noFeatures={(showAll) => (
            <>
              {several
                ? "Features appear here as baum places these repositories' pull requests and agent sessions in them, from now on. Build their feature history from the menu at the end of the filters, or open one repository. The decisions read so far are under All decisions."
                : "Features appear here as baum places this repository's pull requests and agent sessions in them, from now on. Build its feature history to place the pull requests of the last 180 days too. The decisions read so far are under All decisions."}
              <span className="mt-3 flex flex-wrap justify-center gap-2">
                {single && !buildActive && !building ? (
                  <Button
                    size="tiny"
                    disabled={!clone || build.starting}
                    title={clone ? undefined : NO_CLONE}
                    onClick={() => void build.start()}
                  >
                    {build.starting ? "Starting…" : "Build feature history"}
                  </Button>
                ) : null}
                <Button variant="secondary" size="tiny" onClick={showAll}>
                  Show all decisions
                </Button>
              </span>
            </>
          )}
          noDecisions={
            <>
              Decisions appear here as baum reads the agent sessions in{" "}
              {several ? "these repositories" : "this repository"}.
            </>
          }
        />
      </div>
    </>
  );
}

/** A list as the shared page draws it: loading until its first read for
 *  the query, then stale while it is read again. */
function timelineList<T>(pages: Pages<T>): TimelineList<T> {
  return {
    rows: pages.rows,
    first: pages.loaded ? "ready" : pages.status === "failed" ? "failed" : "loading",
    cursor: pages.cursor,
    status: pages.status,
    loadMore: pages.loadMore,
    pending: pages.status === "loading" && pages.loaded,
  };
}

/** The filter bar's kebab menu, at its end: the quieter actions, such as
 *  rebuilding the feature history. `blocked` says why it cannot start;
 *  `note` qualifies what it starts. */
function MoreMenu({
  starting,
  blocked,
  note,
  onRebuild,
}: {
  starting: boolean;
  blocked: string;
  note: string;
  onRebuild: () => void;
}) {
  const { detailsProps, close } = useDisclosure();
  return (
    <details {...detailsProps} className="group relative ml-auto">
      <summary
        aria-label="More"
        className={cn(
          "flex cursor-pointer list-none items-center rounded-(--radius-sm) p-1.5 text-secondary group-open:bg-surface-hover hover:bg-surface-hover [&::-webkit-details-marker]:hidden",
          FOCUS_RING,
        )}
      >
        <svg aria-hidden width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
          <circle cx="8" cy="3" r="1.5" />
          <circle cx="8" cy="8" r="1.5" />
          <circle cx="8" cy="13" r="1.5" />
        </svg>
      </summary>
      <div className="absolute right-0 z-20 mt-1 flex w-60 flex-col rounded-(--radius-md) border border-default bg-surface-card p-1 shadow-overlay">
        <button
          type="button"
          disabled={starting || blocked !== ""}
          title={blocked || undefined}
          className="flex flex-col rounded-(--radius-sm) px-2 py-1.5 text-left text-base text-primary hover:bg-surface-hover disabled:opacity-50"
          onClick={() => {
            close();
            onRebuild();
          }}
        >
          {starting ? "Starting…" : "Rebuild feature history"}
          {blocked || note ? (
            <span className="text-xs text-tertiary">{blocked || note}</span>
          ) : null}
        </button>
      </div>
    </details>
  );
}

/** The repositories a list reads: none named when it is all of them. */
function scopeQuery(scope: string[], all: boolean): string {
  const params = new URLSearchParams();
  if (!all) for (const name of scope) params.append("repository", name);
  return params.toString();
}

/** What capture made of the repository's sessions, above its decisions. */
function CoverageLine({ coverage }: { coverage: Coverage }) {
  const parts = [`${formatCount(coverage.extracted)} sessions read`];
  if (coverage.skipped > 0) {
    const reasons = Object.entries(coverage.skipped_by_reason)
      .map(([reason, n]) => `${humanize(reason)}: ${formatCount(n)}`)
      .join(", ");
    parts.push(`${formatCount(coverage.skipped)} skipped (${reasons})`);
  }
  if (coverage.pending > 0) parts.push(`${formatCount(coverage.pending)} being read`);
  return <p className="text-xs text-tertiary">{parts.join(" · ")}</p>;
}
