import { useCallback, useEffect, useRef, useState } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import type { DetailState, OpenSessionSource } from "@baum/viewer/components/decision-log";
import type { FeatureState } from "@baum/viewer/components/feature-log";
import { formatCount } from "@baum/viewer/components/format";
import { ChevronDownIcon, RepositoryIcon } from "@baum/viewer/components/icons";
import { Empty, Failed } from "@baum/viewer/components/states";
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
import { PageBar, RefreshButton } from "./shell";
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
        <p className="text-base text-(--text-tertiary) italic">
          {answer.available ? WITHHELD_NOTE : answer.message}
        </p>
      );
    const { prompts } = answer.transcript.session;
    return (
      <>
        {/* The transcript lists the prompts it kept; a decision can cite a
            turn that is not one of them. */}
        {prompts.length === 0 || prompts.some((prompt) => prompt.ordinal === at.turn) ? null : (
          <p className="mb-2 text-base text-(--text-tertiary) italic">
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

type ServedRepository = { repository: string; has_features?: boolean };

type RepositoryList = {
  available?: false;
  decisions_enabled?: boolean;
  repositories?: ServedRepository[];
  turned_off?: string[];
};

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
  const [reading, setReading] = useState(true);
  // The page's refresh reads everything again, as a change in the log does.
  const [refreshes, setRefreshes] = useState(0);
  const revision = logRevision + refreshes;
  const refresh = () => setRefreshes((n) => n + 1);
  useEffect(() => {
    const abort = new AbortController();
    setReading(true);
    request<RepositoryList>("api/timeline/repositories", undefined, "GET", abort.signal)
      .then((body) => {
        setList(body);
        setFailed(false);
      })
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      })
      .finally(() => {
        if (!abort.signal.aborted) setReading(false);
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
  if (scope.length > 0 && list?.available !== false && list?.decisions_enabled !== false)
    return (
      <TimelineLog
        key={scope.join("\n")}
        scope={scope}
        all={selected.length === 0}
        served={served}
        revision={revision}
        jobs={jobs}
        cloneOf={cloneOf}
        jobsChanged={jobsChanged}
        refresh={refresh}
      />
    );
  return (
    <>
      <PageBar
        title="Timeline"
        detail={PAGE_DETAIL}
        actions={<RefreshButton refresh={refresh} busy={reading} />}
      />
      {failed && !list ? (
        <div className="local-page-body">
          <Failed>The Timeline could not be loaded.</Failed>
        </div>
      ) : !list ? (
        <div className="local-page-body">
          <LoadingStatus block>Loading the Timeline…</LoadingStatus>
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
    </>
  );
}

type Pages<T> = {
  rows: T[];
  cursor: string | null;
  status: "loading" | "idle" | "failed";
  /** The first page has been read for the current query. */
  loaded: boolean;
  coverage: Coverage | null;
  /** The list is read from its first page; more rows don't count. */
  reading: boolean;
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
    rows: T[];
    cursor: string | null;
    status: "loading" | "idle" | "failed";
    loaded: boolean;
    coverage: Coverage | null;
    /** The read under way is of more rows. */
    more: boolean;
  };
  const [state, setState] = useState<State>({
    query,
    revision,
    rows: [],
    cursor: null,
    status: "loading",
    loaded: false,
    coverage: null,
    more: false,
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
    setState((current) => ({ ...current, status: "loading", more: false }));
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
        rows,
        cursor,
        status: "idle",
        loaded: true,
        coverage,
        more: false,
      });
    };
    read().catch(() => {
      if (!abort.signal.aborted) setState((current) => ({ ...current, status: "failed" }));
    });
    return () => abort.abort();
  }, [path, query, key, revision, active]);
  const loadMore = useCallback(() => {
    const base = latest.current;
    if (!base.cursor || base.status === "loading") return;
    const abort = new AbortController();
    more.current = abort;
    const started = generation.current;
    setState({ ...base, status: "loading", more: true });
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
  return {
    ...state,
    loaded: state.loaded && state.query === query,
    reading: state.status === "loading" && !state.more,
    loadMore,
  };
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
  /** Reads the Timeline again, from the bar's refresh. */
  refresh: () => void;
}) {
  // A view of one repository has its feature build: its job and its
  // building version. A build that finishes changes the features, so the
  // lists are read again.
  const single = scope.length === 1 ? (scope[0] ?? null) : null;
  const several = single === null;
  const clone = single ? cloneOf(single) : null;
  const buildJob = single ? buildJobOf(jobs, single) : undefined;
  const buildActive = isActiveJob(buildJob);
  const [buildFinished, setBuildFinished] = useState(0);
  const wasActive = useRef(buildActive);
  useEffect(() => {
    if (wasActive.current && !buildActive) setBuildFinished((n) => n + 1);
    wasActive.current = buildActive;
  }, [buildActive]);
  const revision = logRevision + buildFinished;
  const buildState = useBuildState(
    single,
    `${revision}:${buildJob?.id ?? ""}:${buildJob?.state ?? ""}:${buildJob?.calls ?? 0}`,
  );
  const { building, hasFeatures } = buildState;
  const build = useFeatureBuildStart(clone, jobsChanged);
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
  // Another set of repositories keeps the tab and filters; what was open
  // was one of the old set's.
  const openRepositories = (names: string[]) =>
    url.update((current) => {
      const next = new URLSearchParams(current);
      next.delete("repo");
      next.delete("open");
      next.delete("feature");
      for (const name of names) next.append("repo", name);
      return next;
    });

  const coverage = decisions.coverage;
  // Whether the repository has features at all, not whether the filters
  // leave any.
  const canRebuild = clone !== null && !buildActive && !building && hasFeatures;
  return (
    <>
      <PageBar
        title="Timeline"
        detail={PAGE_DETAIL}
        actions={
          <>
            {canRebuild ? (
              <Button
                variant="ghost"
                size="compact"
                disabled={build.starting}
                onClick={() => void build.start()}
              >
                {build.starting ? "Starting…" : "Rebuild feature history"}
              </Button>
            ) : null}
            <RefreshButton
              refresh={refresh}
              busy={(tab === "features" ? features : decisions).reading}
            />
          </>
        }
      />
      {build.error ? (
        <Alert className="mx-5 mt-3">Couldn't start the feature build: {build.error}</Alert>
      ) : null}
      <SharedTimelinePage
        withFeatures
        features={timelineList(features)}
        decisions={timelineList(decisions)}
        readDecision={readDecision}
        readFeature={readFeature}
        readPeople={readPeople}
        revision={revision}
        files={files}
        openSession={OPEN_SESSION}
        pullRequestHref={pullRequestHref}
        repository={single ?? ""}
        repositoryLabel={several ? (name) => name : undefined}
        withheldNote={WITHHELD_NOTE}
        // The hosted Timeline's filter bar: the repository picker, then the
        // viewer's shared controls over the same address.
        filterBar={(controls) => (
          <div className="local-filter-bar timeline-filters">
            {served.length > 1 ? (
              <RepositoryPicker
                repositories={served.map((each) => each.repository)}
                selected={all ? [] : scope}
                onChange={openRepositories}
              />
            ) : null}
            {controls}
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
          ) : (
            <WithoutHistory
              repositories={served.filter(
                (each) => scope.includes(each.repository) && !each.has_features,
              )}
              open={(name) => openRepositories([name])}
            />
          )
        }
        decisionsIntro={
          coverage && coverage.extracted + coverage.skipped + coverage.pending > 0 ? (
            <CoverageLine coverage={coverage} />
          ) : null
        }
        noFeatures={(showAll) => (
          <>
            {several
              ? "Features appear here as baum places these repositories' pull requests and agent sessions in them, from now on. Open one repository to build its feature history. The decisions read so far are under All decisions."
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

/** The repositories shown that have no feature history yet, a line each,
 *  each opening its own view, where it can be built. */
function WithoutHistory({
  repositories,
  open,
}: {
  repositories: ServedRepository[];
  open: (name: string) => void;
}) {
  if (repositories.length === 0) return null;
  return (
    <ul aria-label="Repositories without feature history" className="timeline-history-lines">
      {repositories.map((each) => (
        <li key={each.repository}>
          <span className="font-mono">{each.repository}</span> has no feature history yet.{" "}
          <button type="button" className="underline" onClick={() => open(each.repository)}>
            Open {each.repository}
          </button>
        </li>
      ))}
    </ul>
  );
}

/** Which repositories the Timeline shows, drawn as the hosted log's picker:
 *  a box for each, in a popover; none ticked in the address is all of them.
 *  The last box left ticked cannot be cleared. */
function RepositoryPicker({
  repositories,
  selected,
  onChange,
}: {
  repositories: string[];
  /** Empty for all of them. */
  selected: string[];
  onChange: (names: string[]) => void;
}) {
  const { detailsProps } = useDisclosure();
  const all = selected.length === 0;
  const ticked = all ? repositories : selected;
  const label = all
    ? "All repositories"
    : selected.length === 1
      ? selected[0]
      : `${formatCount(selected.length)} repositories`;
  const toggle = (name: string) => {
    const next = ticked.includes(name) ? ticked.filter((each) => each !== name) : [...ticked, name];
    onChange(next.length === repositories.length ? [] : next);
  };
  return (
    <details {...detailsProps} className="group relative">
      <summary
        aria-label={`Repositories: ${label}`}
        className={cn(
          "flex max-w-[280px] cursor-pointer list-none items-center gap-1.5 rounded-(--radius-sm) border px-2.5 py-1 text-base [&::-webkit-details-marker]:hidden",
          FOCUS_RING,
          // A narrowed scope stands out: every number on the page depends on it.
          all
            ? "border-(--border-default) bg-(--surface-card) text-(--text-secondary) hover:bg-(--surface-hover)"
            : "border-(--action-accent) bg-(--surface-pressed) font-medium text-(--text-primary)",
        )}
      >
        <RepositoryIcon size={13} className="flex-none text-(--text-tertiary)" />
        <span className="truncate">{label}</span>
        <ChevronDownIcon
          size={12}
          className="flex-none text-(--text-tertiary) transition-transform duration-150 group-open:rotate-180"
        />
      </summary>
      <fieldset className="absolute left-0 z-20 mt-1 flex max-h-80 w-72 flex-col overflow-y-auto rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) p-1 shadow-(--shadow-overlay)">
        <legend className="sr-only">Repositories</legend>
        {repositories.map((name) => {
          const on = ticked.includes(name);
          return (
            <label
              key={name}
              className="flex cursor-pointer items-center gap-2 rounded-(--radius-sm) px-2 py-1.5 text-base text-(--text-primary) hover:bg-(--surface-hover)"
            >
              <input
                type="checkbox"
                checked={on}
                disabled={on && ticked.length === 1}
                onChange={() => toggle(name)}
              />
              <span className="truncate">{name}</span>
            </label>
          );
        })}
        {all ? null : (
          <>
            <div aria-hidden className="my-1 h-px bg-(--border-subtle)" />
            <button
              type="button"
              className="rounded-(--radius-sm) px-2 py-1.5 text-left text-base text-(--text-secondary) hover:bg-(--surface-hover)"
              onClick={() => onChange([])}
            >
              All repositories
            </button>
          </>
        )}
      </fieldset>
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
  return <p className="text-xs text-(--text-tertiary)">{parts.join(" · ")}</p>;
}
