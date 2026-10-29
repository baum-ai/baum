import {
  Fragment,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import {
  type AuthorCount,
  clearDecisionFilters,
  type DecisionFilters,
  type DecisionSummary,
  decisionSections,
  type FeatureStatus,
  type FeatureSummary,
  featureDay,
  featureHeadings,
  hasDecisionFilters,
  hasFeatureFilters,
  type KeyDecision,
  type ListOrder,
  type LogTab,
  parseDecisionFilters,
  parseFeatureStatuses,
  parseLogTab,
  type Reordered,
  reorderedNote,
  withDecisionFilters,
  withFeatureStatuses,
  withLogTab,
} from "../decision-log";
import { useUrlState } from "../navigation";
import { cn } from "./classnames";
import { DecisionFilterControls, type DecisionFilterControlsProps } from "./decision-filters";
import {
  DecisionDetailBody,
  DecisionRow,
  type DetailState,
  type OpenSessionSource,
} from "./decision-log";
import {
  type DecisionOpening,
  FeatureDrillDown,
  FeatureItem,
  FeatureSidePanel,
  type FeatureState,
  TimelineHeading,
} from "./feature-log";
import { Empty, Failed, Stale } from "./states";
import { tabIds, tabPanelProps } from "./tabs";
import { Button, Card, SegmentedControl, Spinner } from "./ui";

/** The decision log's page: its filter bar, the Features and All decisions
 *  tabs, each list with its paging and empty states, and which decision or
 *  feature is open, kept in the address (`useUrlState`) so a reload or a
 *  shared link shows the same. The host reads the lists and details; this
 *  draws them and keeps the details it asked for. */

/** One of the page's lists as the host has read it. */
export type TimelineList<T> = {
  rows: T[];
  /** The first page: still being read, failed, or read. */
  first: "loading" | "failed" | "ready";
  /** Where the next page starts; null at the end. */
  cursor: string | null;
  /** The read of the next page. */
  status: "idle" | "loading" | "failed";
  loadMore: () => void;
  /** Placed at the list's end, for a host that loads the next page as the
   *  reader reaches it; the button loads it either way. */
  sentinel?: RefObject<HTMLDivElement | null>;
  order?: ListOrder;
  reordered?: Reordered;
  /** A newer read is under way, so the list shows as stale. */
  pending?: boolean;
};

export type TimelinePageProps<S> = {
  /** The log has features: the page has its two tabs. Without, it is the
   *  decision timeline alone. */
  withFeatures: boolean;
  features: TimelineList<FeatureSummary>;
  decisions: TimelineList<DecisionSummary>;
  /** Reads a decision's detail, and a feature's, once each until `revision`
   *  changes. */
  readDecision: (id: string, signal: AbortSignal) => Promise<DetailState>;
  readFeature: (id: string, signal: AbortSignal) => Promise<FeatureState>;
  /** The people the person filter offers on a tab: decisions' authors on All
   *  decisions, features' on Features. Stable while the log is. */
  readPeople: (tab: LogTab, signal: AbortSignal) => Promise<AuthorCount[]>;
  /** Changes when the host's log changed, so details and people are read
   *  again. */
  revision?: number;
  files: DecisionFilterControlsProps["files"];
  openSession: OpenSessionSource<S>;
  /** Where a pull request opens, by its repository's full name. */
  pullRequestHref: (repository: string, number: number) => string;
  /** The repository of a row or detail that does not name its own. */
  repository: string;
  /** How a row names its repository, for a log of several; without, rows
   *  name none. */
  repositoryLabel?: (fullName: string) => string;
  /** The decision `?open=` names, or null for a value that names none. */
  parseOpen?: (value: string | null) => string | null;
  withheldNote?: ReactNode;
  /** Wraps the shared filter controls in the host's bar, with its own scope
   *  picker first. */
  filterBar: (controls: ReactNode) => ReactNode;
  tabsClassName: string;
  bodyClassName: string;
  /** Shown first on the Features tab and above the decisions, such as a
   *  build's status or what capture covered. */
  featuresIntro?: ReactNode;
  decisionsIntro?: ReactNode;
  /** What an empty, unfiltered list says inside its `Empty`; `showAll`
   *  switches to All decisions. */
  noFeatures: (showAll: () => void) => ReactNode;
  noDecisions: ReactNode;
  /** A list whose first page is being read. */
  loading?: (label: string) => ReactNode;
};

const TABLIST = "decision-log";
const TABS: { value: LogTab; label: string }[] = [
  { value: "features", label: "Features" },
  { value: "all", label: "All decisions" },
];
/** The id of the panel that holds the opened feature on a wide screen. */
const FEATURE_PANEL = "opened-feature-panel";
const NO_AUTHORS: AuthorCount[] = [];
const keepOpen = (value: string | null) => value;

export function TimelinePage<S>({
  withFeatures,
  features,
  decisions,
  readDecision,
  readFeature,
  readPeople,
  revision = 0,
  files,
  openSession,
  pullRequestHref,
  repository,
  repositoryLabel,
  parseOpen = keepOpen,
  withheldNote,
  filterBar,
  tabsClassName,
  bodyClassName,
  featuresIntro,
  decisionsIntro,
  noFeatures,
  noDecisions,
  loading = defaultLoading,
}: TimelinePageProps<S>) {
  const url = useUrlState();
  const search = url.search;
  const write = url.update;
  const filters = parseDecisionFilters(search);
  const statuses = parseFeatureStatuses(search);
  const open = parseOpen(search.get("open"));
  const tab: LogTab = withFeatures ? parseLogTab(search) : "all";
  const fullNameOf = (item: { repository?: { full_name: string } }) =>
    item.repository?.full_name ?? repository;
  const labelOf = (item: { repository?: { full_name: string } }) =>
    repositoryLabel?.(fullNameOf(item));

  const setFilters = useCallback(
    (changes: Partial<DecisionFilters>) =>
      write((current) => withDecisionFilters(current, changes), { preventScrollReset: true }),
    [write],
  );
  const setStatuses = (next: FeatureStatus[]) =>
    write((current) => withFeatureStatuses(current, next), { preventScrollReset: true });
  const [resets, setResets] = useState(0);
  const clearFilters = () => {
    setResets((count) => count + 1);
    write((current) => clearDecisionFilters(current, tab === "features"), {
      preventScrollReset: true,
    });
  };
  const switchTab = (next: LogTab) =>
    write((current) => withLogTab(current, next), { preventScrollReset: true });

  const rows = decisions.rows;
  const ranked = decisions.order === "relevance";
  const sections = useMemo(() => decisionSections(rows, decisions.order), [rows, decisions.order]);
  const openRow = open && tab === "all" ? rows.find((decision) => decision.id === open) : undefined;

  const [details, loadDetail] = useDetails(readDecision, revision);
  useEffect(() => {
    if (open) loadDetail(open);
  }, [open, loadDetail]);

  // A shared link to a decision brings its row into view once, when it first
  // shows; a row the reader opens is already where they are looking.
  const scrolledFor = useRef<string | null>(null);
  const rowElements = useRef(new Map<string, HTMLLIElement>());
  const openRowKey = openRow?.id ?? null;
  useEffect(() => {
    if (!open || !openRowKey || scrolledFor.current === open) return;
    scrolledFor.current = open;
    rowElements.current.get(openRowKey)?.scrollIntoView({ block: "center" });
  }, [open, openRowKey]);

  const writeOpen = (decision: DecisionSummary, expanded: boolean) => {
    write(
      (current) => {
        const next = new URLSearchParams(current);
        if (expanded) next.delete("open");
        else next.set("open", decision.id);
        return next;
      },
      { preventScrollReset: true, replace: true },
    );
  };
  const toggle = (decision: DecisionSummary) => {
    const expanded = openRow === decision;
    scrolledFor.current = expanded ? null : decision.id;
    writeOpen(decision, expanded);
  };

  // Each feature is read once, whatever the filters, which never pick its
  // decisions, and kept, as decisions are.
  const [featureStates, loadFeature] = useDetails(readFeature, revision);
  const openFeature = tab === "features" ? search.get("feature") : null;
  useEffect(() => {
    if (openFeature) loadFeature(openFeature);
  }, [openFeature, loadFeature]);
  // A feature merged into another opens the one it was merged into, under
  // that one's address.
  const openState = openFeature ? featureStates[openFeature] : undefined;
  const movedTo = openState?.status === "moved" ? openState.id : null;
  // The feature it moved to takes focus once it shows, so the reader is not
  // left on the page's start.
  const focusOnArrival = useRef<{ from: string; to: string } | null>(null);
  useEffect(() => {
    if (!movedTo || !openFeature) return;
    focusOnArrival.current = { from: openFeature, to: movedTo };
    write(
      (current) => {
        const next = new URLSearchParams(current);
        next.set("feature", movedTo);
        return next;
      },
      { preventScrollReset: true, replace: true },
    );
  }, [movedTo, openFeature, write]);

  // A shared link to a feature brings it into view once, as for a decision.
  const featureScrolledFor = useRef<string | null>(null);
  const featureElements = useRef(new Map<string, HTMLLIElement>());
  const featureRows = features.rows;
  const openFeatureListed = featureRows.some((feature) => feature.id === openFeature);
  useEffect(() => {
    if (!openFeature || !openFeatureListed || featureScrolledFor.current === openFeature) return;
    featureScrolledFor.current = openFeature;
    featureElements.current.get(openFeature)?.scrollIntoView({ block: "start" });
  }, [openFeature, openFeatureListed]);
  const openFeatureStatus = openState?.status;
  useEffect(() => {
    const arrival = focusOnArrival.current;
    if (!arrival) return;
    // The reader went elsewhere before it arrived.
    if (openFeature !== arrival.to && openFeature !== arrival.from) {
      focusOnArrival.current = null;
      return;
    }
    if (openFeature !== arrival.to) return;
    const button = featureElements.current.get(openFeature)?.querySelector("button[aria-expanded]");
    if (!(button instanceof HTMLElement)) return;
    focusOnArrival.current = null;
    // Only focus left nowhere, as when the replaced feature went from under
    // it, moves; focus the reader put somewhere stays there.
    const active = document.activeElement;
    if (!active || active === document.body) button.focus();
  }, [openFeature, openFeatureListed, openFeatureStatus]);

  const [reveals, setReveals] = useState(0);
  const writeFeature = (id: string | null, decision: KeyDecision | null) => {
    featureScrolledFor.current = id;
    if (decision) setReveals((count) => count + 1);
    write(
      (current) => {
        const next = new URLSearchParams(current);
        if (id) next.set("feature", id);
        else next.delete("feature");
        if (decision) next.set("open", decision.id);
        else next.delete("open");
        return next;
      },
      { preventScrollReset: true, replace: true },
    );
  };
  const toggleFeature = (feature: FeatureSummary) => {
    const closing = openFeature === feature.id;
    // Closing a feature the list does not show takes it off the page, and
    // focus with it: it goes to the Features tab instead.
    if (closing && !openFeatureListed)
      document.getElementById(tabIds(TABLIST, "features").tab)?.focus();
    writeFeature(closing ? null : feature.id, null);
  };

  // The person filter offers the authors the tab's filter matches. Each
  // tab's list is kept, so returning to a tab shows the list it had while it
  // is read again; another tab's list is never shown.
  const [authors, setAuthors] = useState<Partial<Record<LogTab, AuthorCount[]>>>({});
  // Read again when the log changes (`revision`).
  useEffect(() => {
    void revision;
    const controller = new AbortController();
    readPeople(tab, controller.signal)
      .then((people) => setAuthors((known) => ({ ...known, [tab]: people })))
      .catch(() => {
        // The person filter keeps the list it had, or offers nobody but the
        // one in the address.
      });
    return () => controller.abort();
  }, [readPeople, tab, revision]);
  const people = authors[tab] ?? NO_AUTHORS;

  const detailProps = {
    openSession,
    withheldNote,
    onFile: (path: string) => setFilters({ file: path }),
  };
  const decisionDetail = (decision: DecisionSummary) => (
    <DecisionDetailBody
      {...detailProps}
      pullRequestHref={(number: number) => pullRequestHref(fullNameOf(decision), number)}
      state={details[decision.id]}
      decisionId={decision.id}
      at={{ sessionId: decision.session_id, turn: decision.turn }}
      onRetry={() => loadDetail(decision.id, true)}
    />
  );
  const openedDecision = (id: string) => {
    const state = details[id];
    return (
      <Card size="panel" className="mb-6 p-4" aria-label="Opened decision">
        <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
          Opened decision
        </h2>
        <DecisionDetailBody
          {...detailProps}
          pullRequestHref={(number: number) =>
            pullRequestHref(fullNameOf(state?.status === "ready" ? state.detail : {}), number)
          }
          state={state}
          decisionId={id}
          withSummary
          onRetry={() => loadDetail(id, true)}
        />
      </Card>
    );
  };
  const opening: DecisionOpening = {
    open,
    onToggle: writeOpen,
    renderDetail: decisionDetail,
    renderOpened: openedDecision,
    reveals,
  };
  // On a wide screen the opened feature's drill-down sits in a panel beside
  // the timeline rather than inside its entry.
  const wide = useWideScreen();
  const sideFeature = wide ? featureRows.find((feature) => feature.id === openFeature) : undefined;
  // A feature's pull requests belong to its repository.
  const featureFullName = (id: string) => {
    const state = featureStates[id];
    return fullNameOf(
      state?.status === "ready"
        ? state.detail.feature
        : (featureRows.find((feature) => feature.id === id) ?? {}),
    );
  };
  const drillDown = (id: string) => (
    <FeatureDrillDown
      state={featureStates[id]}
      decisions={opening}
      pullRequestHref={(number: number) => pullRequestHref(featureFullName(id), number)}
      onRetry={() => loadFeature(id, true)}
    />
  );
  const featureItem = (feature: FeatureSummary, day?: string | null, inline = false) => (
    <FeatureItem
      key={feature.id}
      feature={feature}
      day={day}
      panelElsewhere={wide && !inline ? FEATURE_PANEL : undefined}
      repository={labelOf(feature)}
      expanded={openFeature === feature.id}
      onToggle={() => toggleFeature(feature)}
      onKeyDecision={(decision) => writeFeature(feature.id, decision)}
      itemRef={(element) => {
        if (element) featureElements.current.set(feature.id, element);
        else featureElements.current.delete(feature.id);
      }}
    >
      {drillDown(feature.id)}
    </FeatureItem>
  );

  const clearButton = (
    <Button variant="secondary" size="tiny" className="mt-2" onClick={clearFilters}>
      Clear filters
    </Button>
  );

  const timelineBody = (
    <>
      {open && decisions.first === "ready" && !openRow ? openedDecision(open) : null}
      {decisionsIntro}
      <Stale pending={decisions.pending ?? false}>
        {decisions.first === "failed" ? (
          <Failed>The decision log could not be loaded.</Failed>
        ) : decisions.first === "loading" ? (
          loading("Loading decisions…")
        ) : rows.length === 0 && !decisions.cursor ? (
          hasDecisionFilters(filters) ? (
            <Empty title="No decisions match these filters">{clearButton}</Empty>
          ) : (
            <Empty title="No decisions yet">{noDecisions}</Empty>
          )
        ) : (
          <div className="flex flex-col gap-6">
            {sections.map((section) => (
              <section key={section.key} aria-label={section.heading}>
                <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
                  {section.heading}
                </h2>
                <ol className="rounded-(--radius-md) border border-(--border-subtle)">
                  {section.decisions.map((decision) => {
                    const key = decision.id;
                    return (
                      <DecisionRow
                        key={key}
                        decision={decision}
                        expanded={openRow === decision}
                        withDate={section.dated}
                        repository={labelOf(decision)}
                        onToggle={() => toggle(decision)}
                        rowRef={(element) => {
                          if (element) rowElements.current.set(key, element);
                          else rowElements.current.delete(key);
                        }}
                      >
                        {decisionDetail(decision)}
                      </DecisionRow>
                    );
                  })}
                </ol>
              </section>
            ))}
            <OlderPages
              list={decisions}
              loading={ranked ? "Loading more matches…" : "Loading older decisions…"}
              failed={
                ranked
                  ? "More matches could not be loaded."
                  : "Older decisions could not be loaded."
              }
              action={ranked ? "Load more matches" : "Load older decisions"}
              end={ranked ? "No more matches." : "No older decisions."}
            />
          </div>
        )}
      </Stale>
    </>
  );

  const featuresRanked = features.order === "relevance";
  const headings = useMemo(() => featureHeadings(featureRows), [featureRows]);
  const featuresBody = (
    <>
      {featuresIntro}
      {open && !openFeature ? openedDecision(open) : null}
      {openFeature && features.first === "ready" && !openFeatureListed ? (
        <OpenedFeature
          state={openState}
          item={(feature) => featureItem(feature, null, true)}
          drillDown={drillDown(openFeature)}
        />
      ) : null}
      <Stale pending={features.pending ?? false}>
        {features.first === "failed" ? (
          <Failed>The features could not be loaded.</Failed>
        ) : features.first === "loading" ? (
          loading("Loading features…")
        ) : featureRows.length === 0 && !features.cursor ? (
          statuses.length > 0 || hasFeatureFilters(filters) ? (
            <Empty title="No features match these filters">{clearButton}</Empty>
          ) : (
            <Empty title="No features yet">{noFeatures(() => switchTab("all"))}</Empty>
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
                {featuresRanked ? (
                  <>
                    {/* A ranked search reads in the server's order, best
                        match first: grouping by month or day would undo it,
                        so each entry names its own day. */}
                    <TimelineHeading level="relevance" />
                    {featureRows.map((feature) => featureItem(feature, featureDay(feature)))}
                  </>
                ) : (
                  featureRows.map((feature, index) => {
                    const { month, day } = headings[index]!;
                    return (
                      <Fragment key={feature.id}>
                        {month ? <TimelineHeading level="month" iso={month} /> : null}
                        {day ? <TimelineHeading level="day" iso={day} /> : null}
                        {featureItem(feature, day)}
                      </Fragment>
                    );
                  })
                )}
              </ol>
              <OlderPages
                list={features}
                loading={featuresRanked ? "Loading more matches…" : "Loading more features…"}
                failed={
                  featuresRanked
                    ? "More matches could not be loaded."
                    : "More features could not be loaded."
                }
                action={featuresRanked ? "Load more matches" : "Load more features"}
                end={featuresRanked ? "No more matches." : "No more features."}
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

  return (
    <>
      {filterBar(
        <DecisionFilterControls
          filters={filters}
          files={files}
          people={people}
          onChange={setFilters}
          onClear={clearFilters}
          resets={resets}
          statuses={tab === "features" ? statuses : undefined}
          onStatuses={setStatuses}
        />,
      )}
      {withFeatures ? (
        <>
          <div className={tabsClassName}>
            <SegmentedControl
              label="Timeline views"
              variant="tabs"
              tablist={TABLIST}
              value={tab}
              onChange={switchTab}
              items={TABS}
            />
          </div>
          {TABS.map((each) => (
            <div key={each.value} {...tabPanelProps(TABLIST, each.value, each.value === tab)}>
              {each.value === tab ? (
                <div className={bodyClassName}>
                  {tab === "features" ? featuresBody : timelineBody}
                </div>
              ) : null}
            </div>
          ))}
        </>
      ) : (
        <div className={bodyClassName}>{timelineBody}</div>
      )}
    </>
  );
}

function defaultLoading(label: string) {
  return (
    <p className="flex items-center gap-2 text-base text-(--text-tertiary)">
      <Spinner />
      {label}
    </p>
  );
}

type Known<S> = Record<string, S | { status: "loading" } | { status: "failed" }>;

/** Details read once per revision and kept, so closing and reopening one
 *  costs no request; a retry (`force`) reads one again. */
function useDetails<S>(
  read: (id: string, signal: AbortSignal) => Promise<S>,
  revision: number,
): [Known<S>, (id: string, force?: boolean) => void] {
  const [known, setKnown] = useState<{ revision: number; states: Known<S> }>({
    revision,
    states: {},
  });
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
      const set = (state: Known<S>[string]) => {
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

const WIDE = "(min-width: 96rem)";

/** Whether the screen is wide enough (`2xl`) for the opened feature to sit
 *  beside the timeline; false on the server and before hydration. */
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

/** A feature in the address that the loaded list does not show: one the
 *  filters leave out, or further down than the list has loaded. */
function OpenedFeature({
  state,
  item,
  drillDown,
}: {
  state: FeatureState | undefined;
  item: (feature: FeatureSummary) => ReactNode;
  drillDown: ReactNode;
}) {
  return (
    <section aria-label="Opened feature" className="mb-6">
      <h2 className="mb-2 text-xs tracking-[0.08em] text-(--text-tertiary) uppercase">
        Opened feature
      </h2>
      {state?.status === "ready" ? (
        <ol>{item(state.detail.feature)}</ol>
      ) : (
        <Card size="panel" className="p-4">
          {drillDown}
        </Card>
      )}
    </section>
  );
}

/** The end of a paged list: older pages load as the reader reaches it, where
 *  the host placed a sentinel, or by its button. When ranking stopped the
 *  list, its note already says why no more follow, so `end` is not said. */
function OlderPages<T>({
  list,
  loading,
  failed,
  action,
  end,
}: {
  list: TimelineList<T>;
  loading: string;
  failed: string;
  action: string;
  end: string;
}) {
  const reordered = list.reordered ?? false;
  return (
    <div ref={list.sentinel} className="flex flex-col items-center gap-2 pb-4">
      {/* Why the list's order changed, said where the reader is when it
          happens. The live region stays mounted, so screen readers announce
          the note when its text arrives; empty, it is sr-only. */}
      <p role="status" className={reordered ? "text-base text-(--text-tertiary)" : "sr-only"}>
        {reorderedNote(reordered)}
      </p>
      {list.cursor ? (
        list.status === "loading" ? (
          <p className="flex items-center gap-2 text-base text-(--text-tertiary)">
            <Spinner />
            {loading}
          </p>
        ) : (
          <>
            {list.status === "failed" ? (
              <p role="alert" className="text-base text-(--status-critical)">
                {failed}
              </p>
            ) : null}
            <Button variant="secondary" size="compact" onClick={list.loadMore}>
              {action}
            </Button>
          </>
        )
      ) : list.rows.length > 0 && reordered !== "stopped" ? (
        <p className="text-base text-(--text-tertiary)">{end}</p>
      ) : null}
    </div>
  );
}
