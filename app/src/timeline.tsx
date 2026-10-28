import { Fragment, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { cn } from "@baum/viewer/components/classnames";
import {
  DecisionDetailBody,
  DecisionRow,
  type DetailState,
  type OpenSessionSource,
} from "@baum/viewer/components/decision-log";
import {
  type DecisionOpening,
  FeatureDrillDown,
  FeatureItem,
  FeatureSidePanel,
  type FeatureState,
  TimelineHeading,
} from "@baum/viewer/components/feature-log";
import { formatCount, formatDate } from "@baum/viewer/components/format";
import { CloseIcon } from "@baum/viewer/components/icons";
import { Empty, Failed, Stale } from "@baum/viewer/components/states";
import { tabPanelProps } from "@baum/viewer/components/tabs";
import { Button, Card, SegmentedControl, Spinner } from "@baum/viewer/components/ui";
import {
  CATEGORIES,
  type DecisionDetail,
  type DecisionSummary,
  FEATURE_STATUSES,
  type FeatureDetail,
  type FeatureSummary,
  type KeyDecision,
  localDayKey,
} from "@baum/viewer/decision-log";
import { useUrlState, ViewerLink } from "@baum/viewer/navigation";
import { humanize } from "@baum/viewer/review-document";
import { appURL, isActiveJob, request, type Job } from "./api";
import {
  BuildStatus,
  buildJobOf,
  FeatureBuildDialog,
  NO_CLONE,
  useBuildState,
} from "./feature-build";
import { LoadingStatus } from "./nine-dots";
import { PageBar } from "./shell";
import "./timeline.css";

/** The Timeline: a repository's features and decisions from the local
 *  decision log (`api/timeline`), drawn by the review viewer's decision log
 *  components as the hosted Timeline draws them. Its address holds the
 *  repository, tab, filters and what is open, so a reload shows the same. */

export type TimelineTab = "features" | "all";

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

const TABLIST = "timeline";
const TABS: { value: TimelineTab; label: string }[] = [
  { value: "features", label: "Features" },
  { value: "all", label: "All decisions" },
];
const FEATURE_PANEL = "timeline-feature-panel";
const SEARCH_DELAY_MS = 300;
const PAGE_DETAIL = "What was decided and built, from your agent sessions";
/** Rows a list reads at a time, and the most one request reads. */
const PAGE = 50;
const MAX_PAGE = 500;

/** The app page of a pull request, by its repository. */
export function pullRequestHref(repository: string, number: number): string {
  return appURL(`?repository=${encodeURIComponent(repository)}&pr=${number}`);
}

/** The app page of a session, by the Timeline's `<agent>:<id>`. */
export function sessionHref(id: string): string {
  return appURL(`?page=sessions&session=${encodeURIComponent(id)}`);
}

/** The local timeline serves no prompts: the session page has them while
 *  this computer still tracks the session. */
function WithheldNote({ sessionId }: { sessionId: string | null }) {
  return (
    <>
      The Timeline does not show prompts.{" "}
      {sessionId ? (
        <ViewerLink href={sessionHref(sessionId)} className="underline not-italic">
          Open the session
        </ViewerLink>
      ) : null}
    </>
  );
}

/** A decision's session opens as a link to its page. */
const OPEN_SESSION: OpenSessionSource<string> = {
  load: async (sessionId) => sessionId,
  render: (sessionId) => (
    <ViewerLink href={sessionHref(sessionId)} className="text-base underline">
      Open the session
    </ViewerLink>
  ),
};

type RepositoryList = {
  available?: false;
  decisions_enabled?: boolean;
  repositories?: { repository: string }[];
  turned_off?: string[];
};

/** The Timeline page. `revision` changes when the daemon says the decision
 *  log changed (its `timeline` event) or the event stream reconnected, and
 *  the page reads everything it shows again. */
export function TimelinePage({
  revision,
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
  useEffect(() => {
    const abort = new AbortController();
    request<RepositoryList>("api/timeline/repositories", undefined, "GET", abort.signal)
      .then((body) => {
        setList(body);
        setFailed(false);
      })
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    return () => abort.abort();
  }, [revision]);
  const repositories = list?.repositories?.map((each) => each.repository) ?? null;
  const asked = url.search.get("repo");
  const repository =
    repositories?.find((name) => name.toLowerCase() === asked?.toLowerCase()) ??
    repositories?.[0] ??
    null;
  if (repositories && repository && list?.available !== false && list?.decisions_enabled !== false)
    return (
      <TimelineLog
        key={repository}
        repository={repository}
        repositories={repositories}
        revision={revision}
        jobs={jobs}
        clone={cloneOf(repository)}
        jobsChanged={jobsChanged}
      />
    );
  return (
    <>
      <PageBar title="Timeline" detail={PAGE_DETAIL} />
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
  };
  const [state, setState] = useState<State>({
    query,
    revision,
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
          `${path}?${params}`,
          undefined,
          "GET",
          abort.signal,
        );
        rows = [...rows, ...((body[key] as T[]) ?? [])];
        cursor = (body.next_cursor as string | null) ?? null;
        coverage = (body.coverage as Coverage | undefined) ?? null;
      } while (cursor && rows.length < want);
      setState({ query, revision, rows, cursor, status: "idle", loaded: true, coverage });
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
    setState({ ...base, status: "loading" });
    request<Record<string, unknown>>(
      `${path}?${query}&cursor=${encodeURIComponent(base.cursor)}`,
      undefined,
      "GET",
      abort.signal,
    )
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

/** Details read once per revision and kept, so closing and reopening one
 *  costs no request. */
function useDetails<S>(
  read: (id: string, signal: AbortSignal) => Promise<S>,
  revision: number,
): [
  Record<string, S | { status: "loading" } | { status: "failed" }>,
  (id: string, force?: boolean) => void,
] {
  type Known = Record<string, S | { status: "loading" } | { status: "failed" }>;
  const [known, setKnown] = useState<{ revision: number; states: Known }>({ revision, states: {} });
  // Each id's latest request, which alone may write its state: a newer one
  // (a new revision or a retry) aborts it.
  const requests = useRef(new Map<string, { revision: number; abort: AbortController }>());
  useEffect(
    () => () => {
      for (const each of requests.current.values()) each.abort.abort();
      // A remount of the effect reads them again rather than waiting on
      // aborted requests.
      requests.current.clear();
    },
    [],
  );
  const states = known.revision === revision ? known.states : {};
  const load = useCallback(
    (id: string, force = false) => {
      const previous = requests.current.get(id);
      if (previous?.revision === revision && !force) return;
      previous?.abort.abort();
      const request = { revision, abort: new AbortController() };
      requests.current.set(id, request);
      const set = (state: Known[string]) => {
        if (requests.current.get(id) !== request) return;
        setKnown((current) =>
          current.revision > revision
            ? current
            : {
                revision,
                states: { ...(current.revision === revision ? current.states : {}), [id]: state },
              },
        );
      };
      set({ status: "loading" });
      read(id, request.abort.signal)
        .then(set)
        .catch(() => {
          if (!request.abort.signal.aborted) set({ status: "failed" });
        });
    },
    [read, revision],
  );
  return [states, load];
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
  repository,
  repositories,
  revision: logRevision,
  jobs,
  clone,
  jobsChanged,
}: {
  repository: string;
  repositories: string[];
  revision: number;
  jobs: Job[];
  clone: string | null;
  jobsChanged: () => void;
}) {
  // The repository's feature build: its job and its building version. A
  // build that finishes changes the features, so the lists are read again.
  const buildJob = buildJobOf(jobs, repository);
  const buildActive = isActiveJob(buildJob);
  const [buildFinished, setBuildFinished] = useState(0);
  const wasActive = useRef(buildActive);
  useEffect(() => {
    if (wasActive.current && !buildActive) setBuildFinished((n) => n + 1);
    wasActive.current = buildActive;
  }, [buildActive]);
  const revision = logRevision + buildFinished;
  const { building, hasFeatures } = useBuildState(
    repository,
    `${revision}:${buildJob?.id ?? ""}:${buildJob?.state ?? ""}:${buildJob?.calls ?? 0}`,
  );
  const [buildDialog, setBuildDialog] = useState<"build" | "resume" | null>(null);
  const url = useUrlState();
  const search = url.search;
  const tab: TimelineTab = search.get("tab") === "all" ? "all" : "features";
  const q = search.get("q") ?? "";
  const category = search.get("category") ?? "";
  const status = search.get("status") ?? "";
  const file = search.get("file") ?? "";
  const open = search.get("open");
  const openFeature = tab === "features" ? search.get("feature") : null;
  const write = useCallback(
    (changes: Record<string, string | null>) =>
      url.update(
        (current) => {
          for (const [key, value] of Object.entries(changes)) {
            if (value) current.set(key, value);
            else current.delete(key);
          }
          return current;
        },
        { preventScrollReset: true },
      ),
    [url],
  );

  const base = new URLSearchParams({ repository });
  if (q) base.set("q", q);
  const featureQuery = new URLSearchParams(base);
  if (status) featureQuery.set("status", status);
  const decisionQuery = new URLSearchParams(base);
  if (category) decisionQuery.set("category", category);
  if (file) decisionQuery.set("file", file);
  const features = usePages<FeatureSummary>(
    "api/timeline/features",
    featureQuery.toString(),
    "features",
    revision,
    tab === "features",
  );
  const decisions = usePages<DecisionSummary>(
    "api/timeline/decisions",
    decisionQuery.toString(),
    "decisions",
    revision,
    tab === "all",
  );

  const [details, loadDetail] = useDetails(readDecision, revision);
  const [featureStates, loadFeature] = useDetails(readFeature, revision);
  useEffect(() => {
    if (open) loadDetail(open);
  }, [open, loadDetail]);
  useEffect(() => {
    if (openFeature) loadFeature(openFeature);
  }, [openFeature, loadFeature]);
  const openState = openFeature ? featureStates[openFeature] : undefined;
  const movedTo = openState?.status === "moved" ? openState.id : null;
  useEffect(() => {
    if (movedTo) write({ feature: movedTo });
  }, [movedTo, write]);

  const [reveals, setReveals] = useState(0);
  const writeOpen = (decision: DecisionSummary, expanded: boolean) =>
    write({ open: expanded ? null : decision.id });
  const writeFeature = (id: string | null, decision: KeyDecision | null) => {
    if (decision) setReveals((n) => n + 1);
    write({ feature: id, open: decision?.id ?? null });
  };
  const toggleFeature = (feature: FeatureSummary) =>
    writeFeature(openFeature === feature.id ? null : feature.id, null);

  const decisionDetail = (id: string, withSummary: boolean, at?: DecisionSummary) => {
    const state = details[id] as DetailState | undefined;
    return (
      <DecisionDetailBody
        state={state}
        decisionId={id}
        at={at ? { sessionId: at.session_id, turn: at.turn } : undefined}
        withSummary={withSummary}
        pullRequestHref={(number) => pullRequestHref(repository, number)}
        openSession={OPEN_SESSION}
        onFile={(path) => write({ tab: "all", file: path, feature: null })}
        onRetry={() => loadDetail(id, true)}
        withheldNote={
          <WithheldNote sessionId={state?.status === "ready" ? state.detail.session.id : null} />
        }
      />
    );
  };
  const openedDecision = (id: string) => (
    <Card size="panel" className="mb-6 p-4" aria-label="Opened decision">
      <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
        Opened decision
      </h2>
      {decisionDetail(id, true)}
    </Card>
  );
  const opening: DecisionOpening = {
    open,
    onToggle: writeOpen,
    renderDetail: (decision) => decisionDetail(decision.id, false, decision),
    renderOpened: openedDecision,
    reveals,
  };

  const wide = useWideScreen();
  const sideFeature = wide ? features.rows.find((f) => f.id === openFeature) : undefined;
  const drillDown = (id: string) => (
    <FeatureDrillDown
      state={featureStates[id] as FeatureState | undefined}
      decisions={opening}
      pullRequestHref={(number) => pullRequestHref(repository, number)}
      onRetry={() => loadFeature(id, true)}
    />
  );
  const featureItem = (feature: FeatureSummary, day: string | null) => (
    <FeatureItem
      key={feature.id}
      feature={feature}
      day={day}
      panelElsewhere={wide ? FEATURE_PANEL : undefined}
      expanded={openFeature === feature.id}
      onToggle={() => toggleFeature(feature)}
      onKeyDecision={(decision) => writeFeature(feature.id, decision)}
    >
      {drillDown(feature.id)}
    </FeatureItem>
  );
  // A feature in the address the loaded list does not show: further down,
  // filtered out, archived or gone.
  const openFeatureListed = features.rows.some((f) => f.id === openFeature);
  const openedFeature =
    openFeature && features.loaded && !openFeatureListed ? (
      <section aria-label="Opened feature" className="mb-6">
        <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
          Opened feature
        </h2>
        {openState?.status === "ready" ? (
          <ol>
            <FeatureItem
              feature={openState.detail.feature}
              expanded
              onToggle={() => writeFeature(null, null)}
              onKeyDecision={(decision) => writeFeature(openFeature, decision)}
            >
              {drillDown(openFeature)}
            </FeatureItem>
          </ol>
        ) : (
          <Card size="panel" className="p-4">
            {drillDown(openFeature)}
          </Card>
        )}
      </section>
    ) : null;
  const featureFiltered = Boolean(q || status);
  const decisionFiltered = Boolean(q || category || file);
  const clear = () => write({ q: null, status: null, category: null, file: null });

  const featuresBody = (
    <>
      <BuildStatus
        job={buildJob}
        building={building}
        clone={clone}
        onBuild={(resume) => setBuildDialog(resume ? "resume" : "build")}
      />
      {openedFeature}
      <Stale pending={features.status === "loading" && features.loaded}>
        {features.status === "failed" && !features.loaded ? (
          <Failed>The features could not be loaded.</Failed>
        ) : !features.loaded ? (
          <LoadingStatus block>Loading features…</LoadingStatus>
        ) : features.rows.length === 0 ? (
          featureFiltered ? (
            <Empty title="No features match these filters">
              <Button variant="secondary" size="tiny" className="mt-2" onClick={clear}>
                Clear filters
              </Button>
            </Empty>
          ) : (
            <Empty title="No features yet">
              Features appear here as baum places this repository's pull requests and agent sessions
              in them, from now on. Build its feature history to place the pull requests of the last
              180 days too. The decisions read so far are under All decisions.
              <span className="mt-3 flex flex-wrap justify-center gap-2">
                {!buildActive && !building ? (
                  <Button
                    size="tiny"
                    disabled={!clone}
                    title={clone ? undefined : NO_CLONE}
                    onClick={() => setBuildDialog("build")}
                  >
                    Build feature history
                  </Button>
                ) : null}
                <Button variant="secondary" size="tiny" onClick={() => write({ tab: "all" })}>
                  Show all decisions
                </Button>
              </span>
            </Empty>
          )
        ) : (
          <div
            className={cn(
              "grid items-start gap-6",
              sideFeature && "grid-cols-[minmax(0,1fr)_minmax(0,1fr)]",
            )}
          >
            <div className={cn("flex min-w-0 flex-col gap-6", !sideFeature && "max-w-7xl")}>
              <ol aria-label="Features" className="pt-1">
                {features.rows.map((feature, index) => {
                  // A feature with no activity yet takes no heading, so the
                  // next one compares with the last that had one.
                  const previous = features.rows
                    .slice(0, index)
                    .findLast((each) => featureDay(each) !== null);
                  const month = featureMonth(feature);
                  const day = featureDay(feature);
                  const startsDay = day !== null && day !== featureDay(previous);
                  return (
                    <Fragment key={feature.id}>
                      {month !== null && month !== featureMonth(previous) ? (
                        <TimelineHeading level="month" iso={month} />
                      ) : null}
                      {startsDay ? <TimelineHeading level="day" iso={day} /> : null}
                      {featureItem(feature, startsDay ? day : null)}
                    </Fragment>
                  );
                })}
              </ol>
              <MorePages
                pages={features}
                action="Load more features"
                end={features.rows.length > 0 ? "No more features." : null}
              />
            </div>
            {sideFeature ? (
              <FeatureSidePanel
                id={FEATURE_PANEL}
                feature={sideFeature}
                onClose={() => toggleFeature(sideFeature)}
              >
                {drillDown(sideFeature.id)}
              </FeatureSidePanel>
            ) : null}
          </div>
        )}
      </Stale>
    </>
  );

  const openRow = open && tab === "all" ? decisions.rows.find((d) => d.id === open) : undefined;
  const coverage = decisions.coverage;
  const decisionsBody = (
    <>
      {open && decisions.loaded && !openRow ? openedDecision(open) : null}
      {coverage && coverage.extracted + coverage.skipped + coverage.pending > 0 ? (
        <CoverageLine coverage={coverage} />
      ) : null}
      <Stale pending={decisions.status === "loading" && decisions.loaded}>
        {decisions.status === "failed" && !decisions.loaded ? (
          <Failed>The decisions could not be loaded.</Failed>
        ) : !decisions.loaded ? (
          <LoadingStatus block>Loading decisions…</LoadingStatus>
        ) : decisions.rows.length === 0 ? (
          decisionFiltered ? (
            <Empty title="No decisions match these filters">
              <Button variant="secondary" size="tiny" className="mt-2" onClick={clear}>
                Clear filters
              </Button>
            </Empty>
          ) : (
            <Empty title="No decisions yet">
              Decisions appear here as baum reads the agent sessions in this repository.
            </Empty>
          )
        ) : (
          <div className="flex flex-col gap-6">
            {daySections(decisions.rows).map((section) => (
              <section key={section.day} aria-label={section.heading}>
                <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
                  {section.heading}
                </h2>
                <ol className="rounded-(--radius-md) border border-(--border-subtle)">
                  {section.decisions.map((decision) => (
                    <DecisionRow
                      key={decision.id}
                      decision={decision}
                      expanded={openRow === decision}
                      onToggle={() => writeOpen(decision, openRow === decision)}
                    >
                      {decisionDetail(decision.id, false, decision)}
                    </DecisionRow>
                  ))}
                </ol>
              </section>
            ))}
            <MorePages pages={decisions} action="Load older decisions" end="No older decisions." />
          </div>
        )}
      </Stale>
    </>
  );

  // Whether the repository has features at all, not whether the filters
  // leave any.
  const canRebuild = clone !== null && !buildActive && !building && hasFeatures;
  return (
    <>
      <PageBar
        title="Timeline"
        detail={PAGE_DETAIL}
        actions={
          canRebuild ? (
            <Button variant="ghost" size="compact" onClick={() => setBuildDialog("build")}>
              Rebuild feature history
            </Button>
          ) : null
        }
      />
      {buildDialog && clone ? (
        <FeatureBuildDialog
          repository={repository}
          clone={clone}
          resume={buildDialog === "resume"}
          close={() => setBuildDialog(null)}
          started={jobsChanged}
        />
      ) : null}
      <div className="local-filter-bar timeline-filters">
        <div className="timeline-controls">
          <label className="local-repo-filter">
            <span className="sr-only">Repository</span>
            <select
              value={repository}
              onChange={(e) =>
                url.update(() => new URLSearchParams({ page: "timeline", repo: e.target.value }))
              }
            >
              {repositories.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <SearchBox
            key={tab}
            label={tab === "features" ? "Search features" : "Search decisions"}
            value={q}
            onChange={(value) => write({ q: value })}
          />
          {tab === "features" ? (
            <label className="local-repo-filter">
              <span className="sr-only">Status</span>
              <select value={status} onChange={(e) => write({ status: e.target.value })}>
                <option value="">All statuses</option>
                {FEATURE_STATUSES.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
          ) : (
            <label className="local-repo-filter">
              <span className="sr-only">Category</span>
              <select value={category} onChange={(e) => write({ category: e.target.value })}>
                <option value="">All categories</option>
                {CATEGORIES.map((each) => (
                  <option key={each} value={each}>
                    {humanize(each)}
                  </option>
                ))}
              </select>
            </label>
          )}
          {tab === "all" && file ? (
            <button
              type="button"
              className="timeline-pill"
              aria-label={`Remove filter: File ${file}`}
              onClick={() => write({ file: null })}
            >
              <span className="truncate font-mono">{file}</span>
              <CloseIcon size={10} />
            </button>
          ) : null}
        </div>
      </div>
      <div className="timeline-tabs">
        <SegmentedControl
          label="Timeline views"
          variant="tabs"
          tablist={TABLIST}
          value={tab}
          onChange={(next) => write({ tab: next === "features" ? null : next, open: null })}
          items={TABS}
        />
      </div>
      {TABS.map((each) => (
        <div key={each.value} {...tabPanelProps(TABLIST, each.value, each.value === tab)}>
          {each.value === tab ? (
            <div className="local-page-body timeline-body">
              {tab === "features" ? featuresBody : decisionsBody}
            </div>
          ) : null}
        </div>
      ))}
    </>
  );
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

function MorePages<T>({
  pages,
  action,
  end,
}: {
  pages: Pages<T>;
  action: string;
  end: string | null;
}) {
  return (
    <div className="flex flex-col items-center gap-2 pb-4">
      {pages.cursor ? (
        pages.status === "loading" ? (
          <p className="flex items-center gap-2 text-base text-(--text-tertiary)">
            <Spinner />
            Loading…
          </p>
        ) : (
          <Button variant="secondary" size="compact" onClick={pages.loadMore}>
            {action}
          </Button>
        )
      ) : end ? (
        <p className="text-base text-(--text-tertiary)">{end}</p>
      ) : null}
    </div>
  );
}

/** Applied a moment after typing stops, so a word is one request. */
function SearchBox({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const [text, setText] = useState(value);
  // The value this box last sent; one arriving from elsewhere (a cleared
  // filter) replaces what is typed.
  const sent = useRef(value);
  const latest = useRef(onChange);
  latest.current = onChange;
  useEffect(() => {
    if (value !== sent.current) {
      sent.current = value;
      setText(value);
    }
  }, [value]);
  useEffect(() => {
    if (text.trim() === sent.current) return;
    const timer = setTimeout(() => {
      sent.current = text.trim();
      latest.current(text.trim());
    }, SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [text]);
  return (
    <input
      type="search"
      aria-label={label}
      placeholder={label}
      value={text}
      autoComplete="off"
      onChange={(e) => setText(e.target.value)}
      className="timeline-search"
    />
  );
}

/** The decisions by the local day they were made, newest first. */
export function daySections(
  rows: DecisionSummary[],
): { day: string; heading: string; decisions: DecisionSummary[] }[] {
  const sections: { day: string; heading: string; decisions: DecisionSummary[] }[] = [];
  for (const decision of rows) {
    const day = localDayKey(decision.decided_at);
    const last = sections[sections.length - 1];
    if (last?.day === day) last.decisions.push(decision);
    else sections.push({ day, heading: formatDate(decision.decided_at), decisions: [decision] });
  }
  return sections;
}

const WIDE = "(min-width: 96rem)";

/** Whether the screen is wide enough for the opened feature to sit beside
 *  the timeline, as on the hosted Timeline. */
function useWideScreen(): boolean {
  return useSyncExternalStore(
    (onChange) => {
      if (typeof window.matchMedia !== "function") return () => {};
      const query = window.matchMedia(WIDE);
      query.addEventListener("change", onChange);
      return () => query.removeEventListener("change", onChange);
    },
    () => typeof window.matchMedia === "function" && window.matchMedia(WIDE).matches,
    () => false,
  );
}

/** The local month and day a feature was last active, as ISO times at their
 *  start, for the timeline's headings; null for one with no activity. */
export function featureMonth(feature: FeatureSummary | undefined): string | null {
  const at = lastActive(feature);
  return at ? new Date(at.getFullYear(), at.getMonth(), 1).toISOString() : null;
}

export function featureDay(feature: FeatureSummary | undefined): string | null {
  const at = lastActive(feature);
  return at ? new Date(at.getFullYear(), at.getMonth(), at.getDate()).toISOString() : null;
}

function lastActive(feature: FeatureSummary | undefined): Date | null {
  const at = feature?.last_activity_at ?? feature?.first_activity_at;
  return at ? new Date(at) : null;
}
