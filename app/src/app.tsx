import { SavedStateNotice } from "./ui-state";
import { PreferencesProvider } from "./preferences";
import { formatCount } from "@baum/viewer/components/format";
import { UsageDetails } from "./usage";
import { JobTiming, jobOrigin, jobTarget, jobType } from "./job-info";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import { LogoMark } from "@baum/viewer/components/editorial";
import { CircleCheckIcon, CircleCrossIcon, CircleDashedIcon } from "@baum/viewer/components/icons";
import { Alert, Button, LiveDot } from "@baum/viewer/components/ui";
import {
  UrlStateProvider,
  ViewerLinkProvider,
  type UrlState,
  type ViewerLinkProps,
} from "@baum/viewer/navigation";
import { CloneActions, type FoundClones } from "./repositories";
import { ConfirmDialog, GroupMissing, GroupPage, NameDialog } from "./group";
import { Inbox, UpdatedAgo } from "./inbox";
import { ListSection } from "./list";
import {
  EditInboxPage,
  editInboxHref,
  manageInboxesHref,
  ManageInboxesPage,
  NewInboxPage,
  newInboxHref,
} from "./inbox-pages";
import {
  freeName,
  unusedInboxColor,
  withQuery,
  matches,
  MAX_INBOXES,
  newInboxId,
  readInboxes,
  useInboxes,
  type Inbox as SavedInbox,
} from "./inboxes";
import { UpNextPage, useUpNext } from "./up-next";
import {
  FilterBar,
  FilterButton,
  FilterSaveError,
  SaveButton,
  type SaveOption,
} from "./filter-menu";
import {
  matchesQuery,
  MAX_TABS,
  queryProblem,
  quote,
  usePageFilter,
  useFilterDraft,
  useSavedTabs,
  type SavedTab,
} from "./pr-filter";
import {
  onBody,
  KeyBar,
  ShortcutsSheet,
  listKeys,
  useCommandKeys,
  useEscapeBlurs,
  useKeyHints,
  usePointerBlurs,
  useTabStops,
  useRowFocus,
  useShortcuts,
  VIEW_KEYS,
  type ShortcutsPlace,
  useReadlineArrows,
} from "./shortcuts";
import { GoTo, type GoToItem } from "./go-to";
import { Tour, TOUR_HREF, useTour } from "./tour";
import { PAGES, type Page } from "./pages";
import { LocalReview } from "./review";
import { DecisionsRevision } from "./decisions";
import { useCodexHookTrust } from "./codex-hooks";
import { pendingUpdate, useAppUpdates, useUpdateToast } from "./desktop/updates";
import { useDiagnosticProblems } from "./diagnostics";
import {
  RepositoriesPage,
  SettingsPage,
  settingsSection,
  useCommandLineCheck,
  type ReviewDefaults,
} from "./settings";
import { asksYou, SessionsPage, transcriptHref } from "./sessions";
import { SampleGuard } from "./sample";
import { ToastProvider } from "./toast";
import { MenuButton, useActionsKey, useContextMenu, type MenuItem } from "./context-menu";
import { SessionTranscriptPage } from "./session-transcript";
import { AnalyticsPage } from "./analytics";
import { TimelinePage } from "./timeline";
import {
  Notice,
  PageBar,
  PageRefreshProvider,
  RefreshButton,
  type RefreshRun,
  Sidebar,
  usePageRefresh,
} from "./shell";
import { ReportProblemDialog } from "./report-problem";
import {
  agentName,
  appBase,
  generateReviews,
  appURL,
  request,
  reviewJob,
  detailsJob,
  fixJob,
  jobName,
  chatJobs,
  fixJobs,
  isActiveJob,
  jobHref,
  navigate,
  startFix,
  filtered,
  rowKey,
  jobReasonText,
  parseLive,
  parseAgentRequests,
  noAgentRequests,
  type AgentRequests,
  LocalRequestError,
  parseGroups,
  backTo,
  backToOpener,
  sameRepository,
  MAX_GROUP_MEMBERS,
  type ReviewGroup,
  type GitHubConnection,
  type Automatic,
  type Clone,
  type Repository,
  type Entry,
  type FixOnly,
  type Snapshot,
  type Job,
  type LiveSession,
} from "./api";
import { useModal } from "./modal";
import { PageRead, ReadFailed, readState } from "./page-state";
import { LoadingStatus } from "./nine-dots";
import { FixDetails, type FixChat } from "./fix";
import { JobPage } from "./job-transcript";
import { useLocation } from "./location";
import { SMALL } from "./action-classes";

/** A saved tab's name as the daemon keeps it: trimmed as it trims, control
 *  characters too, at most 40 characters; "" when nothing is left. */
function tabName(text: string) {
  return text
    .replace(/\p{Cc}/gu, " ")
    .trim()
    .slice(0, 40)
    .trim();
}
/** A saved tab's name being typed, at the filter menu's foot: Enter or the
 *  button takes it, trimmed as the daemon keeps it, and Esc leaves it. */
function TabNameForm({
  name,
  action,
  focus = false,
  onName,
  onSubmit,
  onCancel,
}: {
  name: string;
  action: string;
  focus?: boolean;
  onName: (name: string) => void;
  onSubmit: (name: string) => void;
  onCancel?: () => void;
}) {
  return (
    <form
      className="local-tab-name"
      onSubmit={(event) => {
        event.preventDefault();
        const kept = tabName(name);
        if (kept) onSubmit(kept);
      }}
    >
      <input
        aria-label="Tab name"
        className="local-field filter-bar-name"
        // biome-ignore lint/a11y/noAutofocus: asked for by Create tab
        autoFocus={focus}
        maxLength={40}
        value={name}
        onChange={(event) => onName(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && onCancel) {
            event.preventDefault();
            onCancel();
          }
        }}
      />
      <Button size="tiny" type="submit" disabled={!tabName(name)}>
        {action}
      </Button>
    </form>
  );
}

/** Pull requests' own tabs: Open first, Closed last, saved tabs between. */
const VIEWS = [
  ["open", "Open"],
  ["history", "Closed"],
] as const;
const PULLS_TAB_KEY = "baum.pulls.tab.v1";
// Longer than the daemon's own limit on a refresh, so its answer comes first.
const REFRESH_TIMEOUT_MS = 60_000;
/** The Pull requests tab last shown in this window's browser, if any. */
function lastPullsTab(): string | null {
  try {
    return localStorage.getItem(PULLS_TAB_KEY);
  } catch {
    return null;
  }
}
function rememberPullsTab(tab: string) {
  try {
    localStorage.setItem(PULLS_TAB_KEY, tab);
  } catch {
    // Without storage the page opens on Open.
  }
}
function LocalLink({ href, onClick, ...props }: ViewerLinkProps) {
  return (
    <a
      {...props}
      href={href}
      onClick={(e) => {
        onClick?.(e);
        if (
          !e.defaultPrevented &&
          e.button === 0 &&
          !e.metaKey &&
          !e.ctrlKey &&
          !e.shiftKey &&
          href.startsWith(appBase())
        ) {
          e.preventDefault();
          navigate(href);
        }
      }}
    />
  );
}
function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}
export function App() {
  return (
    <PreferencesProvider>
      <Application />
    </PreferencesProvider>
  );
}

function Application() {
  const [auth, setAuth] = useState<"loading" | "ready" | "failed">("loading");
  const [authError, setAuthError] = useState("");
  const url = useLocation();
  useEffect(() => {
    let active = true;
    // The server's bootstrap page already exchanged the launch capability;
    // the app only confirms that its namespaced session is authorized.
    request("api/health").then(
      () => {
        if (active) setAuth("ready");
      },
      (error) => {
        if (active) {
          setAuth("failed");
          setAuthError(errorText(error));
        }
      },
    );
    return () => {
      active = false;
    };
  }, []);
  return (
    <UrlStateProvider value={url}>
      <ToastProvider>
        <SampleGuard />
        <ViewerLinkProvider link={LocalLink}>
          {auth === "ready" ? (
            <Workspace url={url} />
          ) : (
            <main className="local-connecting">
              <LogoMark size={40} />
              {auth === "loading" ? (
                <LoadingStatus block>Connecting to the local application…</LoadingStatus>
              ) : (
                <Alert>{authError} Open this page from the baum desktop app.</Alert>
              )}
            </main>
          )}
        </ViewerLinkProvider>
      </ToastProvider>
    </UrlStateProvider>
  );
}
function Workspace({ url }: { url: UrlState }) {
  const [clones, setClones] = useState<Clone[]>([]);
  const [picked, setPicked] = useState<Repository[]>([]);
  const [attached, setAttached] = useState<FoundClones["attached"]>([]);
  const seenAttachments = useRef<Set<string> | null>(null);
  const [snapshot, setSnapshot] = useState<Snapshot>({ entries: [], problems: [] });
  const [jobs, setJobs] = useState<Job[]>([]);
  const [background, setBackground] = useState<
    { repository: string; error?: string; next_refresh: string }[]
  >([]);
  const [loading, setLoading] = useState(true);
  // Refreshes from GitHub asked for, from any page: reloads along the way
  // do not end them, and each waits for the one before it.
  const [refreshing, setRefreshing] = useState(0);
  const refreshQueue = useRef(Promise.resolve());
  const refreshesRunning = useRef(new Set<AbortController>());
  // Why the latest refresh from GitHub failed, on the page it was asked on.
  const [refreshError, setRefreshError] = useState("");
  const [error, setError] = useState("");
  // When the latest read of the daemon completed, for Pull requests' bar.
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [revision, setRevision] = useState(0);
  const [selection, setSelection] = useState(new Set<string>());
  const [generating, setGenerating] = useState(false);
  // The pull requests whose review request is on its way to the daemon: they
  // show as generating from the click, not from the job the answer brings.
  const [starting, setStarting] = useState(new Set<string>());
  const [generationError, setGenerationError] = useState("");
  // A generation that includes a fork, waiting for the user's confirmation.
  const [fork, setFork] = useState<{ entries: Entry[]; mode: "single" | "batch" } | null>(null);
  // The pull requests whose fix is on its way to the daemon: their Fix says
  // Starting from the click, not from the job the answer brings.
  const [fixStarting, setFixStarting] = useState(new Set<string>());
  // Why the latest fix asked for did not start, in words, shown beside the
  // pull request it was for (`key` is its rowKey).
  const [fixError, setFixError] = useState<{ key: string; number: number; text: string } | null>(
    null,
  );
  const [shortcuts, setShortcuts] = useState(false);
  // Opens the sidebar's help menu while its Help button is mounted.
  const openHelp = useRef<(() => void) | null>(null);
  const [reporting, setReporting] = useState(false);
  const [goingTo, setGoingTo] = useState(false);
  const tour = useTour(url.search.has("tour"), loading ? undefined : snapshot.entries.length > 0);
  const [rates, setRates] = useState<
    {
      repository: string;
      stale: boolean;
      error?: string;
      rate: { remaining: number | null; reset: string };
    }[]
  >([]);
  const [automatic, setAutomatic] = useState<Automatic | null>(null);
  const [github, setGitHub] = useState<GitHubConnection | null>(null);
  const [defaults, setDefaults] = useState<ReviewDefaults | null>(null);
  const [rawLive, setLive] = useState<LiveSession[]>([]);
  const live = useMemo<LiveSession[]>(() => {
    const indexedPRStates = new Map(
      snapshot.entries.map(({ pull_request: pr }) => [
        `${pr.repository.toLowerCase()}#${pr.number}`,
        pr.state,
      ]),
    );
    return rawLive.map((session) => {
      if (session.pr_state || !session.pr) return session;
      const state = indexedPRStates.get(`${session.repository.toLowerCase()}#${session.pr}`);
      return state === "OPEN" || state === "MERGED" || state === "CLOSED"
        ? { ...session, pr_state: state }
        : session;
    });
  }, [rawLive, snapshot.entries]);
  // Whether the sessions have been read, or the first read failed.
  const [liveRead, setLiveRead] = useState<"loading" | "loaded" | "failed">("loading");
  // A later read of the sessions that failed: they stay as they were.
  const [liveStale, setLiveStale] = useState(false);
  // What agents wait on and the messages queued for them; none from a daemon
  // without the route.
  const [agentRequests, setAgentRequests] = useState<AgentRequests>(noAgentRequests);
  // Counts the `live` events, which an open transcript reads itself again on.
  const [liveTick, setLiveTick] = useState(0);
  // Counts the decision log's changes and reconnections of the event
  // stream, for the Timeline to read again.
  const [timelineTick, setTimelineTick] = useState(0);
  // Counts the reads decision lists make again: on a `decisions` event,
  // several at once coalesced, and when the stream opens or resets or the
  // automatic switch changes.
  const [decisionsTick, setDecisionsTick] = useState(0);
  const [groups, setGroups] = useState<ReviewGroup[]>([]);
  // Why the groups could not be read, so a group is not called missing then.
  const [groupsError, setGroupsError] = useState("");
  const inboxes = useInboxes();
  const [draftInboxSource, setDraftInboxSource] = useState<string | null>(null);
  const draftSourceFilter = usePageFilter(`inbox:${draftInboxSource ?? ""}`);
  // An inbox started from a template, not saved until its edit page's Save.
  const [draftInbox, setDraftInbox] = useState<SavedInbox | null>(null);
  // Deleting an inbox says so on any page, with a way back.
  const [inboxToast, setInboxToast] = useState<{
    id: number;
    text: string;
    undo: () => void;
  } | null>(null);
  // The pull requests being grouped, while the group name is asked.
  const [grouping, setGrouping] = useState<Entry[] | null>(null);
  const lastLive = useRef(0);
  // Sessions in progress change far more often than the rest, so the live
  // event rereads only them. A daemon without them serves none.
  const reloadLive = useCallback(async () => {
    const sequence = ++lastLive.current;
    // A failed read keeps what the page shows: it is not the end of every session.
    const [sessions, pending] = await Promise.all([
      request<unknown>("api/live-sessions").then(parseLive, () => null),
      // A daemon without the route has no requests or messages to show.
      request<unknown>("api/agent-requests").then(parseAgentRequests, () => noAgentRequests),
    ]);
    if (sequence !== lastLive.current) return;
    if (sessions) {
      setLive(sessions);
      setLiveRead("loaded");
      setLiveStale(false);
    } else {
      setLiveRead((read) => (read === "loaded" ? read : "failed"));
      setLiveStale(true);
    }
    setAgentRequests(pending);
  }, []);
  const reloadPending = useRef(false);
  const reloadInFlight = useRef<Promise<void> | null>(null);
  const reloadAbort = useRef<AbortController | null>(null);
  // `restart` drops the read under way, which may never answer, for a new one.
  const reload = useCallback(({ restart = false }: { restart?: boolean } = {}) => {
    reloadPending.current = true;
    if (reloadInFlight.current && !restart) return reloadInFlight.current;
    reloadAbort.current?.abort();
    const abort = new AbortController();
    reloadAbort.current = abort;
    const { signal } = abort;
    const get = <T,>(path: string) => request<T>(path, undefined, "GET", signal);
    // Publish each completed snapshot; events during a read queue one more
    // instead of starting competing reads that discard each other's results.
    const drain = async () => {
      while (reloadPending.current && !signal.aborted) {
        reloadPending.current = false;
        let groupsFailure = "";
        try {
          const [
            repos,
            index,
            jobList,
            discovery,
            sync,
            auto,
            settings,
            connection,
            repositories,
            found,
            groupList,
            inboxList,
          ] = await Promise.all([
            get<Clone[]>("api/repos"),
            get<Snapshot>("api/index?history=1"),
            get<{ jobs?: Job[] }>("api/jobs"),
            get<typeof rates>("api/prs"),
            get<typeof background>("api/background"),
            // A daemon without automatic generation answers without repositories.
            get<Partial<Automatic>>("api/automatic").catch(() => null),
            get<ReviewDefaults>("api/settings").catch(() => null),
            get<GitHubConnection>("api/github-auth").catch(() => null),
            get<Repository[]>("api/repositories"),
            get<FoundClones>("api/clones/found").catch(() => null),
            // An older daemon (404) has no groups; another failure is kept
            // apart, so it is not taken for a group that is gone.
            get<unknown>("api/groups").catch((e) => {
              if (!(e instanceof LocalRequestError && e.status === 404))
                groupsFailure = errorText(e);
              return null;
            }),
            // An older daemon (404) has no inboxes; another failure is reported.
            readInboxes(signal),
          ]);
          if (signal.aborted) return;
          setClones(repos);
          setPicked(Array.isArray(repositories) ? repositories : []);
          if (Array.isArray(found?.attached)) {
            const keys = found.attached.map((a) => `${a.repository}:${a.at}`);
            if (seenAttachments.current)
              setAttached((previous) => [
                ...previous,
                ...found.attached.filter((_, i) => !seenAttachments.current!.has(keys[i]!)),
              ]);
            seenAttachments.current = new Set(keys);
          }
          setSnapshot(index);
          setJobs(jobList.jobs ?? []);
          // A failed read keeps the groups shown.
          if (!groupsFailure) setGroups(parseGroups(groupList));
          setGroupsError(groupsFailure);
          inboxes.read(inboxList);
          setRates(discovery);
          setBackground(Array.isArray(sync) ? sync : []);
          setAutomatic(auto && Array.isArray(auto.repositories) ? (auto as Automatic) : null);
          setDefaults(settings);
          setGitHub(connection);
          setError("");
          setUpdatedAt(Date.now());
          setRevision((n) => n + 1);
        } catch (e) {
          if (!signal.aborted) setError(errorText(e));
        } finally {
          if (!signal.aborted) setLoading(false);
        }
      }
    };
    const run: Promise<void> = drain().finally(() => {
      if (reloadInFlight.current === run) reloadInFlight.current = null;
    });
    reloadInFlight.current = run;
    return run;
  }, []);
  useEffect(() => {
    void reload();
    void reloadLive();
    const source = new EventSource(appURL("api/events"));
    const refresh = () => void reload();
    const refreshLive = () => {
      setLiveTick((n) => n + 1);
      void reloadLive();
    };
    source.addEventListener("open", refresh);
    source.addEventListener("open", refreshLive);
    source.addEventListener("live", refreshLive);
    let decisionsTimer: ReturnType<typeof setTimeout> | undefined;
    const refreshDecisions = () => {
      decisionsTimer ??= setTimeout(() => {
        decisionsTimer = undefined;
        setDecisionsTick((n) => n + 1);
      }, 250);
    };
    for (const kind of ["decisions", "open", "reset", "automatic"])
      source.addEventListener(kind, refreshDecisions);
    source.addEventListener("reset", refreshLive);
    source.addEventListener("index", refresh);
    source.addEventListener("progress", refresh);
    source.addEventListener("automatic", refresh);
    source.addEventListener("reset", refresh);
    const refreshTimeline = () => setTimelineTick((n) => n + 1);
    source.addEventListener("timeline", refreshTimeline);
    source.addEventListener("reset", refreshTimeline);
    // The first connection is the page's first read; a later one may have
    // missed events.
    let connected = false;
    source.addEventListener("open", () => {
      if (connected) refreshTimeline();
      connected = true;
    });
    return () => {
      clearTimeout(decisionsTimer);
      source.close();
    };
  }, [reload, reloadLive]);
  const authChanged = () => {
    void reload();
  };
  const cloneFor = (e: Entry) =>
    e.clone_id ??
    clones.find((c) => c.repository === e.pull_request.repository && c.available)?.id ??
    "";
  // Pull requests opens on the tab last shown when the address names none.
  const filter = url.search.get("filter") ?? lastPullsTab() ?? "open";
  useEffect(() => {
    if (url.search.get("page") === "pulls") rememberPullsTab(filter);
  }, [url.search.get("page"), filter]);
  const savedTabs = useSavedTabs();
  // A saved tab (`filter=tab:<id>`) is a view with a query of its own; one
  // that is gone opens Open.
  const savedTab = savedTabs.tabs.find((tab) => `tab:${tab.id}` === filter);
  // The page's filter. On a saved tab it starts as the tab's query, and a
  // change is a draft that narrows the tab until Update tab saves it (kept
  // per tab; none kept is the saved query).
  const pullsFilter = useFilterDraft(
    savedTab ? `tab:${savedTab.id}` : "pulls",
    savedTab ? savedTab.query : null,
  );
  const viewFilter = usePageFilter("pulls");
  const pullsQuery = pullsFilter.query;
  const setPullsQuery = (query: string) => {
    if (query === pullsQuery || queryProblem(query, false)) return;
    setSelection(new Set());
    pullsFilter.set(query);
  };
  // A saved tab's name changes while it is edited (Edit in its right-click
  // menu), at the filter menu's foot; `name` is the draft.
  const [editingTab, setEditingTab] = useState<{ id: string; name: string } | null>(null);
  const editing = savedTab && editingTab?.id === savedTab.id ? editingTab : null;
  const [deletingTab, setDeletingTab] = useState<SavedTab | null>(null);
  const tabMenu = useContextMenu();
  const openTab = (key: string) => {
    setSelection(new Set());
    setNamingTab(null);
    setEditingTab(null);
    url.update((current) => {
      current.set("filter", key);
      return current;
    });
  };
  const deleteTab = (tab: SavedTab) => {
    savedTabs.remove(tab.id);
    if (savedTab?.id === tab.id) openTab(tab.view);
  };
  const tabActions = (tab: SavedTab): MenuItem[] => [
    {
      label: "Edit…",
      onSelect: () => {
        if (savedTab?.id !== tab.id) openTab(`tab:${tab.id}`);
        setEditingTab({ id: tab.id, name: tab.name });
        setOpenPullsMenu(true);
      },
    },
    { label: "Delete…", danger: true, onSelect: () => setDeletingTab(tab) },
  ];
  const view = savedTab?.view ?? (VIEWS.some(([value]) => value === filter) ? filter : "open");
  // Asks the filter menu to open, as a saved tab's Edit does.
  const [openPullsMenu, setOpenPullsMenu] = useState(false);
  // The tabs in order, as h and l walk them: Open, the saved ones, Closed.
  const tabKeys = ["open", ...savedTabs.tabs.map((tab) => `tab:${tab.id}`), "history"];
  const activeTab = savedTab ? `tab:${savedTab.id}` : view;
  // Until the saved tabs are read, a tab's filter is not known to edit.
  const tabsPending = filter.startsWith("tab:") && !savedTabs.ready;
  const [namingTab, setNamingTab] = useState<string | null>(null);
  // A link from before the filter names one repository (`repo=`): it becomes
  // the page's filter once the saved filters are read. Analytics and Timeline
  // keep their own `repo=`.
  const linkedRepo = url.search.get("page") === "pulls" ? url.search.get("repo") : null;
  useEffect(() => {
    if (!linkedRepo || !pullsFilter.ready) return;
    setSelection(new Set());
    pullsFilter.set(`repo=${quote(linkedRepo)}`);
    url.update((current) => {
      current.delete("repo");
      return current;
    });
  }, [linkedRepo, pullsFilter.ready]);
  const indexedEntries = snapshot.entries.map((entry) =>
    isActiveJob(reviewJob(entry, jobs)) || starting.has(rowKey(entry))
      ? { ...entry, status: "generating" }
      : entry,
  );
  const filteredPulls = (view: string, query: string) =>
    filtered(indexedEntries, view).filter((e) => matchesQuery(query, e));
  const rows = filteredPulls(view, pullsQuery);
  // Saving the filter: into the saved tab shown, or as a new tab (named next).
  const changed = savedTab ? pullsFilter.dirty : pullsQuery !== "";
  const tabSaves: SaveOption[] =
    !changed || queryProblem(pullsQuery, false) || editing
      ? []
      : [
          ...(savedTab
            ? [
                {
                  label: "Update tab",
                  onSelect: () => {
                    savedTabs.setQuery(savedTab.id, pullsQuery);
                    pullsFilter.reset();
                  },
                },
              ]
            : []),
          ...(savedTab ? [{ label: "Discard changes", onSelect: () => pullsFilter.reset() }] : []),
          ...(savedTabs.ready && savedTabs.tabs.length < MAX_TABS
            ? [{ label: "Create tab", onSelect: () => setNamingTab("") }]
            : []),
        ];
  // What the filter's values and counts come from: the view before it.
  const filterBase = filtered(indexedEntries, view);
  // The person's repositories: picked, registered as clones, or holding a
  // pull request read. The filters offer each.
  const repositories = Array.from(
    new Set([
      ...picked.map((r) => r.repository),
      ...clones.map((c) => c.repository),
      ...indexedEntries.map((e) => e.pull_request.repository),
    ]),
  );
  // What the filters offer beyond the page's pull requests: every one read,
  // in any view, and every repository.
  const knownValues = { entries: indexedEntries, repositories };
  const requestedNumber = Number(url.search.get("pr"));
  const requestedClone = url.search.get("clone");
  const requestedRepository =
    url.search.get("repository") ?? clones.find((clone) => clone.id === requestedClone)?.repository;
  const selectedEntry =
    indexedEntries.find(
      (entry) =>
        entry.pull_request.number === requestedNumber &&
        !!requestedClone &&
        cloneFor(entry) === requestedClone,
    ) ??
    indexedEntries.find(
      (entry) =>
        requestedRepository &&
        entry.pull_request.number === requestedNumber &&
        entry.pull_request.repository.toLowerCase() === requestedRepository.toLowerCase(),
    );
  const groupId = url.search.get("group");
  const requestedPage = url.search.get("page") ?? "inbox";
  // The pages that make and change inboxes need a daemon that has them.
  const inboxPage =
    requestedPage === "inbox-new" || requestedPage === "inbox-edit" || requestedPage === "inboxes";
  const knownPages = [
    "inbox",
    "inbox-new",
    "inbox-edit",
    "inboxes",
    "pulls",
    "jobs",
    "repositories",
    "sessions",
    "analytics",
    "timeline",
    "settings",
    "diagnostics",
  ];
  // An address naming no page is the inbox page.
  const page =
    (inboxPage && inboxes.status === "older") || !knownPages.includes(requestedPage)
      ? "inbox"
      : requestedPage;
  // The inbox page shows a saved inbox's queue, never the whole one: it waits
  // for the inboxes, and the root or an address naming no inbox opens the
  // first.
  const inboxQueuePage =
    page === "inbox" &&
    !groupId &&
    !url.search.get("pr") &&
    !url.search.get("clone") &&
    !url.search.get("repository");
  // Before the first read they wait; after a failed one they say so; a daemon
  // without inboxes says it needs a newer baum.
  const inboxStatusView =
    inboxes.status === "loading" || inboxes.status === "failed" ? (
      <section>
        <PageBar title="Inboxes" />
        <div className="local-page-body">
          {inboxes.status === "failed" ? (
            <Alert>
              {inboxes.error} <Button onClick={() => void reload()}>Retry</Button>
            </Alert>
          ) : (
            <LoadingStatus block>Loading inboxes…</LoadingStatus>
          )}
        </div>
      </section>
    ) : inboxes.status === "older" ? (
      <section>
        <PageBar title="Inboxes" />
        <div className="local-page-body local-stack">
          <h2>Inboxes need a newer baum</h2>
          <p>Update baum to see your pull requests in an inbox.</p>
        </div>
      </section>
    ) : null;
  const inboxWaiting = inboxPage || inboxQueuePage ? inboxStatusView : null;
  // The inbox page of a pull request or group link that names no inbox (or
  // whose pull request is not read yet) is the first inbox's, address kept.
  const detailPage = page === "inbox" && !inboxQueuePage;
  // The inbox's queue counts in the sidebar on every page. Its record of what
  // was cleared waits for the first complete read, and pauses while reads
  // fail, so an empty or failed read clears nothing.
  const inbox = useUpNext(indexedEntries, live, jobs, revision > 0 && !error, groups);
  // What the sidebar highlights is the page that shows the queue, with the
  // inbox the address names, when the daemon still has it.
  const railPage: Page =
    page === "jobs" ||
    page === "repositories" ||
    page === "sessions" ||
    page === "analytics" ||
    page === "timeline" ||
    page === "pulls"
      ? page
      : page === "settings" || page === "diagnostics"
        ? "settings"
        : "inbox";
  const selectedInbox =
    railPage === "inbox"
      ? (inboxes.inboxes.find((candidate) => candidate.id === url.search.get("inbox")) ?? null)
      : null;
  const firstInbox = inboxes.status === "ready" ? (inboxes.inboxes[0] ?? null) : null;
  const queueInbox = page === "inbox" ? (selectedInbox ?? (detailPage ? firstInbox : null)) : null;
  // The root, a legacy link to the whole queue, or an inbox that is gone:
  // the current entry becomes the first inbox, so Back does not return here.
  const missingInbox = inboxQueuePage && !selectedInbox ? firstInbox : null;
  useEffect(() => {
    if (!missingInbox) return;
    url.update(
      (current) => {
        if (!knownPages.includes(requestedPage)) current.delete("page");
        current.set("inbox", missingInbox.id);
        return current;
      },
      { replace: true },
    );
  }, [missingInbox?.id, url.search]);
  useEffect(() => {
    const switchInbox = (event: KeyboardEvent) => {
      if (
        !selectedInbox ||
        page !== "inbox" ||
        selectedEntry ||
        groupId ||
        event.metaKey ||
        event.altKey ||
        event.isComposing ||
        document.querySelector("dialog[open]")
      )
        return;
      if (
        event.target instanceof HTMLElement &&
        event.target.closest("input, textarea, select, [contenteditable], .filter-menu[open]")
      )
        return;
      // Shift+J and Shift+K, or Ctrl+↓ and Ctrl+↑. Ctrl+K stays Go to's.
      const steps: Record<string, number> = event.shiftKey
        ? { j: 1, k: -1 }
        : { arrowdown: 1, arrowup: -1 };
      const step = event.shiftKey === event.ctrlKey ? 0 : (steps[event.key.toLowerCase()] ?? 0);
      if (!step) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      // Only the saved inboxes, stopping at the first and the last.
      const index = inboxes.inboxes.findIndex((inbox) => inbox.id === selectedInbox.id);
      const nextIndex = Math.max(0, Math.min(inboxes.inboxes.length - 1, index + step));
      if (nextIndex === index) return;
      const next = inboxes.inboxes[nextIndex];
      if (next) navigate(appURL(`?inbox=${encodeURIComponent(next.id)}`));
    };
    window.addEventListener("keydown", switchInbox, true);
    return () => window.removeEventListener("keydown", switchInbox, true);
  }, [selectedInbox, page, selectedEntry, groupId, inboxes.inboxes, navigate]);
  // Undo reads the list as it is then, not as it was at the delete.
  const currentInboxes = useRef(inboxes.inboxes);
  currentInboxes.current = inboxes.inboxes;
  // An inbox to delete once the person confirms, and what follows it.
  const [deletingInbox, setDeletingInbox] = useState<{
    inbox: SavedInbox;
    after?: () => void;
  } | null>(null);
  // The last inbox is never deleted: Inbox always opens one.
  const askRemoveInbox = (inbox: SavedInbox, after?: () => void) => {
    if (inboxes.inboxes.length > 1) setDeletingInbox({ inbox, after });
  };
  const removeInbox = (target: SavedInbox) => {
    const before = inboxes.inboxes;
    if (before.length <= 1) return;
    const at = before.findIndex((candidate) => candidate.id === target.id);
    const rest = before.filter((candidate) => candidate.id !== target.id);
    inboxes.save(rest);
    // Deleting the inbox that is open (its queue or its edit page) opens the
    // first one left, in its place.
    const first = rest[0];
    if (first && url.search.get("inbox") === target.id)
      url.update(
        (current) => {
          for (const key of ["page", "pr", "clone", "repository", "group", "walk"])
            current.delete(key);
          current.set("inbox", first.id);
          return current;
        },
        { replace: true },
      );
    setInboxToast({
      id: Date.now(),
      text: `Deleted ${target.name}.`,
      undo: () => {
        const now = currentInboxes.current;
        if (now.some((candidate) => candidate.id === target.id)) return;
        const index = Math.min(Math.max(at, 0), now.length);
        inboxes.save([...now.slice(0, index), target, ...now.slice(index)]);
      },
    });
  };
  useEffect(() => {
    if (!inboxToast) return;
    const timer = setTimeout(
      () => setInboxToast((t) => (t?.id === inboxToast.id ? null : t)),
      5000,
    );
    return () => clearTimeout(timer);
  }, [inboxToast?.id]);
  useEffect(() => {
    if (draftInbox && page !== "inbox-edit") {
      setDraftInbox(null);
      setDraftInboxSource(null);
    }
  }, [draftInbox, page]);
  // The pages that show the diagnostics run them themselves.
  // The diagnostics dialog runs them itself while it is open.
  const diagnostics = useDiagnosticProblems(page !== "diagnostics");
  const codexTrust = useCodexHookTrust();
  const commandLine = useCommandLineCheck();
  const appUpdates = useAppUpdates();
  useUpdateToast(
    appUpdates,
    () =>
      url.update((current) => {
        current.set("page", "settings");
        current.set("section", "computer");
        return current;
      }),
    page === "settings" && settingsSection(url.search.get("section")) === "computer",
  );
  const reviewTogether = () => {
    const chosen = snapshot.entries.filter((e) => selection.has(rowKey(e)));
    if (
      chosen.some(
        (e) => !sameRepository(e.pull_request.repository, chosen[0]!.pull_request.repository),
      )
    ) {
      setGenerationError(
        "Review together works with pull requests of one repository. Groups across repositories are not supported yet.",
      );
      return;
    }
    if (chosen.length > MAX_GROUP_MEMBERS) {
      setGenerationError(`A review group holds at most ${MAX_GROUP_MEMBERS} pull requests.`);
      return;
    }
    setGenerationError("");
    setGrouping(chosen);
  };
  const generate = async (entries: Entry[], mode: "single" | "batch", isolation = false) => {
    if (entries.some((entry) => isActiveJob(reviewJob(entry, jobs)))) {
      setGenerationError("A review of a selected pull request is already queued or running.");
      return;
    }
    if (entries.some((e) => e.needs_clone || !cloneFor(e))) {
      setGenerationError(
        "Needs a local clone. Choose a folder or clone the repository in Repositories.",
      );
      return;
    }
    setGenerationError("");
    setGenerating(true);
    const keys = entries.map(rowKey);
    setStarting((now) => new Set([...now, ...keys]));
    try {
      const answer = await generateReviews(
        entries.map((e) => ({ clone: cloneFor(e), pr: e.pull_request.number })),
        mode,
        isolation,
      );
      if (answer === "needs_clone") {
        setGenerationError(
          "Needs a local clone. Choose a folder or clone the repository in Repositories.",
        );
        await reload();
        return;
      }
      if (answer === "isolation") {
        setFork({ entries, mode });
        return;
      }
      setFork(null);
      setSelection(new Set());
      await reload();
    } catch (e) {
      setGenerationError(errorText(e));
    } finally {
      setGenerating(false);
      setStarting((now) => new Set([...now].filter((key) => !keys.includes(key))));
    }
  };
  // A fix starts at once, from its button or key: there is no dialog and no
  // price to confirm. One the daemon will not run (a fork, no push access,
  // nothing to fix) says why where it was asked for.
  const fix = async (entry: Entry, only?: FixOnly, chat?: FixChat) => {
    const clone = cloneFor(entry);
    const key = rowKey(entry);
    if (!clone || fixStarting.has(key) || isActiveJob(fixJob(entry, jobs))) return;
    setFixError(null);
    setFixStarting((now) => new Set([...now, key]));
    try {
      await startFix(clone, entry.pull_request.number, only, chat);
      await reload();
    } catch (e) {
      setFixError({ key, number: entry.pull_request.number, text: errorText(e) });
    } finally {
      setFixStarting((now) => new Set([...now].filter((k) => k !== key)));
    }
  };
  const fixState = (entry: Entry) => ({
    starting: fixStarting.has(rowKey(entry)),
    error: fixError?.key === rowKey(entry) ? fixError.text : "",
  });
  // One notice for every repository GitHub could not refresh, whichever
  // refresh failed: the cause and the fix are usually the same connection.
  const unsynced = [
    ...new Set([
      ...background.filter((s) => s.error).map((s) => s.repository),
      ...rates.filter((r) => r.error).map((r) => r.repository),
    ]),
  ];
  // A failed refresh is already in the notice above.
  const limited = rates.filter((r) => !r.error && r.rate.remaining === 0);
  // Reads the repositories' pull requests from GitHub at once rather than
  // after the polling backoff, then everything the pages show.
  // One at a time, as GitHub prefers; a repository that fails leaves the
  // others refreshed and is named.
  // A repository is named by its local clone where there is one, which
  // the daemon knows even when the repository is not picked.
  // A press that starts one again drops every refresh under way, on any page,
  // since one that never answers holds the queue and the lists' loader.
  const refreshFrom = (targets: { repository: string; clone?: string }[], press: RefreshRun) => {
    if (press.restart) for (const other of refreshesRunning.current) other.abort();
    const abort = new AbortController();
    press.signal.addEventListener("abort", () => abort.abort(), { once: true });
    // A press superseded before its refresh started does not run.
    if (press.signal.aborted) abort.abort();
    const { signal } = abort;
    const dropped = new Promise<void>((resolve) =>
      signal.addEventListener("abort", () => resolve(), { once: true }),
    );
    refreshesRunning.current.add(abort);
    setRefreshing((n) => n + 1);
    const before = refreshQueue.current;
    const run = (async () => {
      const failed: string[] = [];
      try {
        if (!press.restart) await Promise.race([before, dropped]);
        for (const { repository, clone } of targets) {
          if (signal.aborted) return;
          // A request that never answers gives up, so it cannot hold the
          // queue and the loader; the daemon gives up on GitHub sooner.
          const one = new AbortController();
          const stop = () => one.abort();
          signal.addEventListener("abort", stop, { once: true });
          const timer = setTimeout(stop, REFRESH_TIMEOUT_MS);
          await request<{ stale?: boolean; error?: string } | null>(
            "api/prs/refresh",
            clone ? { clone } : { repository },
            "POST",
            one.signal,
          )
            // The daemon answers a refresh GitHub did not complete (a
            // stalled read it gave up on, an exhausted allowance) with the
            // cached snapshot, marked stale with the error.
            .then((answer) => {
              if (answer?.stale && answer.error)
                failed.push(`${repository}: not refreshed from GitHub; baum will keep retrying.`);
            })
            .catch((e) => {
              if (signal.aborted) return;
              failed.push(
                `${repository}: ${one.signal.aborted ? "baum did not answer in time." : errorText(e)}`,
              );
            })
            .finally(() => {
              clearTimeout(timer);
              signal.removeEventListener("abort", stop);
            });
        }
        // The press that dropped this one reads the daemon again itself.
        if (signal.aborted) return;
        await reload({ restart: press.restart });
        // Only the page's latest refresh speaks, while the page is shown.
        if (press.current()) setRefreshError(failed.join(" "));
      } finally {
        refreshesRunning.current.delete(abort);
        setRefreshing((n) => n - 1);
      }
    })();
    // One that fails does not hold up the next.
    refreshQueue.current = run.catch(() => {});
    return run;
  };
  // The picked repositories, and those known only by a registered clone.
  const refresh = (press: RefreshRun) =>
    refreshFrom(
      [
        ...picked.map(({ repository }) => ({ repository })),
        ...clones
          .filter(
            (c, i) =>
              !picked.some((p) => sameRepository(p.repository, c.repository)) &&
              clones.findIndex((other) => sameRepository(other.repository, c.repository)) === i,
          )
          .map((c) => ({ repository: c.repository, clone: c.id })),
      ],
      press,
    );
  // A failed refresh belongs to the page it was asked for on.
  const shownPage = [
    page,
    url.search.get("inbox"),
    groupId,
    requestedClone ?? requestedRepository,
    requestedNumber,
  ].join(":");
  useEffect(() => setRefreshError(""), [shownPage]);
  // A page without a refresh of its own, as one still loading, reads
  // everything again.
  const pageRefresh = usePageRefresh(shownPage, () =>
    Promise.all([reload({ restart: true }), reloadLive()]),
  );
  // The pages that show what the first read brought: when it failed they say
  // so, once, rather than wait for it or call themselves empty.
  const readFailed =
    Boolean(error) &&
    revision === 0 &&
    !inboxPage &&
    !["settings", "diagnostics", "sessions", "timeline", "analytics"].includes(page);
  const hasRepositories = picked.length > 0 || clones.length > 0;
  // Before the first read there is nothing to refresh.
  const listBusy = refreshing > 0 || (loading && revision === 0);
  // The list's own keys live in `Inbox`, which knows its rows; these are the
  // page's: the shortcuts sheet, and the way back from a pull request or group
  // to the inbox or list it was opened from.
  const onList = page === "pulls" && !groupId && !selectedEntry;
  // A session's transcript, `?page=sessions&session=<agent>:<id>`.
  const openSession = url.search.get("session");
  // A job's transcript, `?page=jobs&job=<id>`.
  const openJob = page === "jobs" ? url.search.get("job") : null;
  // The shortcuts sheet lists the keys of where it was opened first.
  const shortcutsPlace: ShortcutsPlace = onList
    ? "pulls"
    : selectedEntry && url.search.get("walk") && url.search.get("walk") !== "full"
      ? "walkthrough"
      : (page === "inbox" || page === "pulls") && (groupId || selectedEntry)
        ? "review"
        : (page === "sessions" && openSession) || openJob
          ? "page"
          : page === "inbox" ||
              page === "sessions" ||
              page === "jobs" ||
              page === "repositories" ||
              page === "timeline" ||
              page === "settings"
            ? page
            : "anywhere";
  const [openAgent = "", openID = ""] = openSession
    ? [
        openSession.slice(0, openSession.indexOf(":")),
        openSession.slice(openSession.indexOf(":") + 1),
      ]
    : [];
  const sessionPR = (s: LiveSession) => {
    const entry = indexedEntries.find(
      (e) =>
        e.pull_request.number === s.pr &&
        e.pull_request.repository.toLowerCase() === s.repository.toLowerCase(),
    );
    if (!entry) return null;
    const clone = cloneFor(entry);
    return appURL(
      `?${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(entry.pull_request.repository)}`}&pr=${s.pr}`,
    );
  };
  // Everywhere Go to can open, pages first, then what waits on the person,
  // then the rest of the pull requests, the review groups and the sessions.
  const goToItems = (): GoToItem[] => {
    const queued = new Set(inbox.queue.map((item) => rowKey(item.entry)));
    const pulls = indexedEntries
      .slice()
      .sort((a, b) => Number(queued.has(rowKey(b))) - Number(queued.has(rowKey(a))));
    return [
      // Inbox is its saved inboxes below, not a page of its own.
      ...PAGES.filter((item) => item.page !== "inbox").map((item) => ({
        id: `page:${item.page}`,
        kind: "Page" as const,
        label: item.label,
        href: item.href(),
      })),
      { id: "tour", kind: "Page" as const, label: "Take the tour", href: TOUR_HREF() },
      ...(inboxes.available
        ? [
            ...inboxes.inboxes.map((one) => ({
              id: `inbox:${one.id}`,
              kind: "Inbox" as const,
              label: one.name,
              href: appURL(`?inbox=${encodeURIComponent(one.id)}`),
            })),
            { id: "inbox-new", kind: "Page" as const, label: "New inbox", href: newInboxHref() },
            {
              id: "inboxes",
              kind: "Page" as const,
              label: "Manage inboxes",
              href: manageInboxesHref(),
            },
          ]
        : []),
      ...pulls.map((entry) => {
        const pr = entry.pull_request;
        const clone = cloneFor(entry);
        return {
          id: `pr:${rowKey(entry)}`,
          kind: "Pull request" as const,
          label: pr.title,
          detail: `${pr.repository} #${pr.number}`,
          keywords: `${pr.number} ${pr.author ?? ""} ${pr.head_ref ?? ""}`,
          href: appURL(
            `?${clone ? `clone=${encodeURIComponent(clone)}` : `repository=${encodeURIComponent(pr.repository)}`}&pr=${pr.number}`,
          ),
        };
      }),
      ...groups.map((group) => ({
        id: `group:${group.id}`,
        kind: "Review group" as const,
        label: group.name,
        detail: group.repository,
        href: appURL(`?group=${encodeURIComponent(group.id)}`),
      })),
      ...live.map((s) => ({
        id: `session:${s.agent}:${s.session_id}`,
        kind: "Session" as const,
        label: s.title || `${agentName(s.agent)} on ${s.branch ?? s.repository}`,
        detail: [agentName(s.agent), s.repository, s.branch].filter(Boolean).join(" · "),
        href: transcriptHref(s),
      })),
    ];
  };
  const inboxPageProps = {
    inboxes: inboxes.inboxes,
    items: inbox.queue,
    login: github?.login,
    repositories,
    entries: indexedEntries,
    saves: inboxes.saves,
    save: inboxes.save,
    go: navigate,
    Link: LocalLink,
  };
  // The Inbox's filter as an inbox of its own.
  const saveAsInbox =
    inboxes.status === "ready" && inboxes.inboxes.length < MAX_INBOXES
      ? (query: string, base: SavedInbox) => {
          // From an inbox: a copy of it, named so no other inbox has the name.
          const made = withQuery(
            {
              ...base,
              id: newInboxId(),
              name: freeName(base.name, inboxes.inboxes),
              color: unusedInboxColor(inboxes.inboxes, base.color),
            },
            query,
          );
          setDraftInbox(made);
          setDraftInboxSource(base.id);
          navigate(editInboxHref(made.id));
        }
      : undefined;
  useKeyHints();
  useEscapeBlurs();
  usePointerBlurs();
  useTabStops();
  useActionsKey();
  useReadlineArrows();
  useCommandKeys((action) => {
    setShortcuts(false);
    if (action.kind === "go-to") {
      setGoingTo((open) => !open);
      return;
    }
    setGoingTo(false);
    if (action.kind === "refresh") pageRefresh.press();
    else if (action.kind === "back") history.back();
    else if (action.kind === "forward") history.forward();
    else navigate(PAGES.find((item) => item.page === action.page)!.href());
  });
  useShortcuts(
    (event) => {
      if (event.key === "?") {
        event.preventDefault();
        // `?` opens the help menu on Show shortcuts, so Enter shows the
        // sheet; with the sheet open it hides it, and without the sidebar's
        // button it shows the sheet.
        if (shortcuts) setShortcuts(false);
        else if (openHelp.current) openHelp.current();
        else setShortcuts(true);
      } else if (
        onList &&
        (VIEW_KEYS.previous.includes(event.key) || VIEW_KEYS.next.includes(event.key))
      ) {
        if (shortcuts) return;
        // One view along, stopping at either end, as the tabs read.
        event.preventDefault();
        const index = tabKeys.indexOf(activeTab);
        const step = VIEW_KEYS.next.includes(event.key) ? 1 : -1;
        const next = tabKeys[Math.min(tabKeys.length - 1, Math.max(0, index + step))]!;
        if (next === filter) return;
        setSelection(new Set());
        url.update((current) => {
          current.set("filter", next);
          return current;
        });
      } else if (openJob && (event.key === "u" || (event.key === "Escape" && onBody(event)))) {
        // From a job's transcript back to where it was opened from, else the list.
        event.preventDefault();
        if (backToOpener()) return;
        url.update((current) => {
          current.delete("job");
          return current;
        });
      } else if (
        (page === "inbox" || page === "pulls") &&
        (groupId || requestedNumber) &&
        (event.key === "u" || (event.key === "Escape" && onBody(event)))
      ) {
        // Only the review page's own keys use a focused Escape (its sheets).
        // Back to where it was opened from (the Inbox, an inbox, Pull
        // requests, a group), past moves within it; a window opened on it
        // goes to its list.
        event.preventDefault();
        if (backToOpener()) return;
        url.update((current) => {
          for (const key of ["pr", "clone", "repository", "group", "walk"]) current.delete(key);
          return current;
        });
      } else if (
        page === "sessions" &&
        openSession &&
        (event.key === "u" || (event.key === "Escape" && onBody(event)))
      ) {
        // The same way back, from a session's transcript.
        event.preventDefault();
        if (backToOpener()) return;
        url.update((current) => {
          current.delete("session");
          return current;
        });
      }
    },
    { allowSheet: true },
  );
  const shell = (
    <div className="local-shell">
      <Sidebar
        page={railPage}
        inbox={inbox.queue.length}
        inboxes={
          inboxes.available
            ? {
                list: inboxes.inboxes,
                counts: Object.fromEntries(
                  inboxes.inboxes.map((candidate) => [
                    candidate.id,
                    inbox.queue.filter((item) => matches(candidate, item)).length,
                  ]),
                ),
                selected: selectedInbox?.id ?? null,
                remove: askRemoveInbox,
                edit: (target) => navigate(editInboxHref(target.id)),
                create: () => navigate(newInboxHref()),
                manage: () => navigate(manageInboxesHref()),
              }
            : undefined
        }
        activeJobs={jobs.filter(isActiveJob).length}
        runningSessions={live.filter((s) => s.state === "running").length}
        needsYou={asksYou(live).length}
        problems={diagnostics.problems}
        settingsWarning={
          [codexTrust.warning, commandLine.warning].filter(Boolean).join(" ") || null
        }
        update={pendingUpdate(appUpdates.view)}
        help={{
          shortcuts: () => setShortcuts(true),
          report: () => setReporting(true),
          opener: openHelp,
        }}
        Link={LocalLink}
      />
      <div className="local-frame">
        <div className="local-notices">
          {attached.map((a) => (
            <Notice key={`${a.repository}:${a.at}`}>
              Found a local clone for {a.repository}.{" "}
              <LocalLink href={appURL("?page=repositories")}>View repositories</LocalLink>
            </Notice>
          ))}
          {unsynced.length > 0 && page !== "settings" ? (
            <Notice
              action={
                <LocalLink href={appURL("?page=settings&section=connections")}>
                  Open settings
                </LocalLink>
              }
            >
              {syncNotice(github, unsynced)}
            </Notice>
          ) : null}
          {diagnostics.problems > 0 && page !== "diagnostics" ? (
            <Notice>
              baum cannot fully work on this computer: diagnostics found {diagnostics.problems}{" "}
              {diagnostics.problems === 1 ? "problem" : "problems"}.{" "}
              <LocalLink href={appURL("?page=diagnostics")}>Show the fixes</LocalLink>
            </Notice>
          ) : null}
        </div>
        <main className="local-main">
          <SavedStateNotice state={inbox.persistence} label="inbox state" />
          {refreshError ? (
            <div className="local-list-note">
              <Alert className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span>Couldn't refresh from GitHub: {refreshError}</span>
                <Button variant="secondary" size="compact" onClick={() => setRefreshError("")}>
                  Dismiss
                </Button>
              </Alert>
            </div>
          ) : null}
          {error && !readFailed ? (
            <div className="local-list-note">
              <Alert className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span>{error}</span>
                <Button variant="secondary" size="compact" onClick={() => void reload()}>
                  Retry
                </Button>
              </Alert>
            </div>
          ) : null}
          {groupsError && groupId && groups.some((g) => g.id === groupId) ? (
            <Alert>
              Couldn't read the review groups again: {groupsError}{" "}
              <Button onClick={() => void reload()}>Retry</Button>
            </Alert>
          ) : null}
          {inboxes.error && !(inboxWaiting && inboxes.status === "failed") ? (
            <Alert>{inboxes.error}</Alert>
          ) : null}
          {readFailed ? (
            <section>
              <PageBar title={readPageTitle(page, groupId)} />
              <div className="local-page-body local-list-page">
                <ReadFailed what={readPageWhat(page, groupId)} message={error} retry={reload} />
              </div>
            </section>
          ) : page === "repositories" ? (
            <RepositoriesPage
              repositories={picked}
              loading={loading}
              automatic={automatic}
              entries={snapshot.entries}
              jobs={jobs}
              changed={reload}
            />
          ) : page === "settings" || page === "diagnostics" ? (
            <SettingsPage
              authChanged={authChanged}
              automatic={automatic}
              defaults={defaults}
              github={github}
              section={settingsSection(url.search.get("section"))}
              setSection={(section) =>
                url.update((current) => {
                  current.set("page", "settings");
                  if (section === "general") current.delete("section");
                  else current.set("section", section);
                  return current;
                })
              }
              changed={reload}
              problems={diagnostics.problems}
              diagnosticsOpen={page === "diagnostics"}
              setDiagnosticsOpen={(open) =>
                url.update((current) => {
                  current.set("page", open ? "diagnostics" : "settings");
                  return current;
                })
              }
              onDiagnostics={diagnostics.update}
              codexTrust={codexTrust}
              commandLine={commandLine}
              updates={appUpdates}
            />
          ) : page === "sessions" && openSession ? (
            <SessionTranscriptPage
              key={openSession}
              agent={openAgent}
              id={openID}
              session={live.find((s) => s.agent === openAgent && s.session_id === openID) ?? null}
              queued={agentRequests.messages.find(
                (m) => m.agent === openAgent && m.session_id === openID,
              )}
              tick={liveTick}
              prHref={sessionPR}
              Link={LocalLink}
            />
          ) : page === "analytics" ? (
            <AnalyticsPage Link={LocalLink} />
          ) : page === "timeline" ? (
            <TimelinePage
              revision={timelineTick}
              jobs={jobs}
              cloneOf={(repository) =>
                picked
                  .find((each) => sameRepository(each.repository, repository))
                  ?.clones.find((clone) => clone.available)?.id ?? null
              }
              jobsChanged={() => void reload()}
            />
          ) : page === "sessions" ? (
            <>
              <PageBar
                title="Sessions"
                detail="Agent sessions on this computer, live"
                actions={<RefreshButton refresh={reloadLive} />}
              />
              <div className="local-page-body local-list-page">
                <PageRead
                  state={readState({
                    loaded: liveRead === "loaded",
                    failed: liveRead === "failed",
                  })}
                  what="sessions"
                  message="baum could not read the agent sessions on this computer."
                  retry={reloadLive}
                >
                  {commandLine.missing || commandLine.hooksMissing ? (
                    <div className="local-list-note">
                      <Notice
                        action={
                          <>
                            {commandLine.hooksMissing ? (
                              <LocalLink href={appURL("?page=settings&section=connections")}>
                                Connect an agent
                              </LocalLink>
                            ) : null}
                            {commandLine.missing ? (
                              <LocalLink href={appURL("?page=settings&section=computer")}>
                                Install baum CLI
                              </LocalLink>
                            ) : null}
                          </>
                        }
                      >
                        {sessionsSetupWarning(commandLine)}
                      </Notice>
                    </div>
                  ) : null}
                  {liveStale ? (
                    <div className="local-list-note">
                      <Alert className="flex flex-wrap items-center gap-x-3 gap-y-2">
                        <span>Couldn't read the sessions again. They may be out of date.</span>
                        <Button
                          variant="secondary"
                          size="compact"
                          onClick={() => void reloadLive()}
                        >
                          Retry
                        </Button>
                      </Alert>
                    </div>
                  ) : null}
                  <SessionsPage
                    peekKey={url.search.get("peek")}
                    sessions={live}
                    requests={agentRequests}
                    Link={LocalLink}
                    prHref={sessionPR}
                    navigate={navigate}
                  />
                </PageRead>
              </div>
            </>
          ) : page === "jobs" && url.search.get("job") ? (
            <JobPage
              job={jobs.find((j) => j.id === url.search.get("job")) ?? null}
              title={jobPRTitle(
                snapshot.entries,
                jobs.find((j) => j.id === url.search.get("job")),
              )}
              loading={loading}
              Link={LocalLink}
            />
          ) : page === "jobs" ? (
            <>
              <PageBar
                title="Jobs"
                detail="Reviews and fixes run on this computer"
                actions={<RefreshButton refresh={(run) => reload({ restart: run.restart })} />}
              />
              <div className="local-page-body local-list-page">
                <Jobs jobs={jobs} entries={snapshot.entries} loading={loading} changed={reload} />
              </div>
            </>
          ) : inboxWaiting ? (
            inboxWaiting
          ) : page === "inbox-new" ? (
            <NewInboxPage
              {...inboxPageProps}
              start={(inbox) => {
                setDraftInboxSource(null);
                setDraftInbox(inbox);
                navigate(editInboxHref(inbox.id));
              }}
            />
          ) : page === "inbox-edit" ? (
            <EditInboxPage
              {...inboxPageProps}
              id={url.search.get("inbox") ?? ""}
              fresh={false}
              onCreated={() => {
                if (draftInboxSource) draftSourceFilter.reset();
                setDraftInboxSource(null);
              }}
              remove={askRemoveInbox}
              draft={draftInbox}
              setDraft={setDraftInbox}
            />
          ) : page === "inboxes" ? (
            <ManageInboxesPage {...inboxPageProps} />
          ) : groupId ? (
            (() => {
              const group = groups.find((g) => g.id === groupId);
              return group ? (
                <GroupPage
                  key={group.id}
                  group={group}
                  entries={indexedEntries}
                  jobs={jobs}
                  clones={clones}
                  cloneFor={cloneFor}
                  live={live}
                  revision={revision}
                  changed={reload}
                  refresh={(press) =>
                    refreshFrom(
                      [
                        {
                          repository: group.repository,
                          clone: clones.find((c) => sameRepository(c.repository, group.repository))
                            ?.id,
                        },
                      ],
                      press,
                    )
                  }
                />
              ) : loading ? (
                <LoadingStatus block>Loading review group…</LoadingStatus>
              ) : groupsError ? (
                <section>
                  <PageBar title="Review group" />
                  <div className="local-page-body">
                    <ReadFailed what="the review group" message={groupsError} retry={reload} />
                  </div>
                </section>
              ) : (
                <GroupMissing />
              );
            })()
          ) : selectedEntry?.needs_clone && !cloneFor(selectedEntry) ? (
            <section>
              <PageBar
                title={selectedEntry.pull_request.title}
                detail={`${selectedEntry.pull_request.repository} #${selectedEntry.pull_request.number}`}
                actions={
                  <RefreshButton
                    refresh={(press) =>
                      refreshFrom([{ repository: selectedEntry.pull_request.repository }], press)
                    }
                    label="Refresh from GitHub"
                  />
                }
              />
              <div className="local-page-body local-stack">
                {page === "pulls" ? (
                  <LocalLink href={appURL("?page=pulls")}>Pull requests</LocalLink>
                ) : (
                  <LocalLink href={backTo(url.search).href}>Inbox</LocalLink>
                )}
                <h2>Needs a local clone</h2>
                <p>
                  Pull requests are available from GitHub. Choose a local folder before generating a
                  review.
                </p>
                <CloneActions repository={selectedEntry.pull_request.repository} changed={reload} />
                <a href={selectedEntry.pull_request.url} target="_blank" rel="noreferrer">
                  Open on GitHub
                </a>
              </div>
            </section>
          ) : selectedEntry ? (
            <DecisionsRevision.Provider value={decisionsTick}>
              <LocalReview
                key={rowKey(selectedEntry)}
                entry={selectedEntry}
                clone={cloneFor(selectedEntry)}
                revision={revision}
                job={reviewJob(selectedEntry, jobs)}
                detailsJob={detailsJob(selectedEntry, jobs)}
                changed={reload}
                refresh={(press) =>
                  refreshFrom(
                    [
                      {
                        repository: selectedEntry.pull_request.repository,
                        clone: cloneFor(selectedEntry) || undefined,
                      },
                    ],
                    press,
                  )
                }
                live={live}
                groups={groups.filter(
                  (g) =>
                    sameRepository(g.repository, selectedEntry.pull_request.repository) &&
                    g.members.includes(selectedEntry.pull_request.number),
                )}
                generate={() => void generate([selectedEntry], "single")}
                starting={starting.has(rowKey(selectedEntry))}
                fix={
                  cloneFor(selectedEntry)
                    ? (chat) => void fix(selectedEntry, undefined, chat)
                    : undefined
                }
                fixState={fixState(selectedEntry)}
                fixJob={fixJob(selectedEntry, jobs)}
                fixRuns={fixJobs(selectedEntry, jobs)}
                chats={chatJobs(selectedEntry, jobs)}
              />
            </DecisionsRevision.Provider>
          ) : page === "inbox" && !queueInbox ? (
            // The first inbox is being opened; the whole queue is never shown.
            (inboxStatusView ?? (
              <section>
                <PageBar title="Inboxes" />
                <div className="local-page-body">
                  <LoadingStatus block>Loading inboxes…</LoadingStatus>
                </div>
              </section>
            ))
          ) : page === "inbox" && queueInbox ? (
            <UpNextPage
              inbox={inbox}
              jobs={jobs}
              inboxes={inboxes.inboxes}
              selected={queueInbox}
              onEdit={() => navigate(editInboxHref(queueInbox.id))}
              // Until a read succeeds there is nothing to say an empty inbox about.
              loading={revision === 0}
              hasRepositories={hasRepositories}
              cloneFor={cloneFor}
              fix={(entry, only) => void fix(entry, only)}
              fixState={fixState}
              changed={reload}
              refresh={refresh}
              refreshing={listBusy}
              reviewGroups={groups}
              saveAsInbox={saveAsInbox}
              inboxSaves={inboxes.saves}
              repositories={repositories}
              updateInbox={
                inboxes.status === "ready"
                  ? (patch) =>
                      inboxes.save(
                        inboxes.inboxes.map((one) =>
                          one.id === queueInbox.id ? { ...one, ...patch } : one,
                        ),
                      )
                  : undefined
              }
              entries={indexedEntries}
              Link={LocalLink}
            />
          ) : page === "pulls" ? (
            <>
              <PageBar
                title="Pull requests"
                actions={
                  <>
                    <UpdatedAgo at={updatedAt} busy={loading || refreshing > 0} />
                    <button
                      type="button"
                      aria-label="Keyboard shortcuts"
                      title="Keyboard shortcuts (?)"
                      onClick={() => setShortcuts(true)}
                      className="local-icon-button"
                    >
                      ?
                    </button>
                    <RefreshButton refresh={refresh} busy={listBusy} label="Refresh from GitHub" />
                  </>
                }
              />
              <div className="local-filter-bar">
                <div role="group" aria-label="Show" className="local-tabs">
                  {[
                    {
                      key: "open",
                      label: "Open",
                      count: filteredPulls("open", viewFilter.query).length,
                    },
                    ...savedTabs.tabs.map((tab) => ({
                      key: `tab:${tab.id}`,
                      label: tab.name,
                      count: filteredPulls(tab.view, tab.query).length,
                      tab,
                    })),
                    // Closed stays last, after the saved tabs.
                    {
                      key: "history",
                      label: "Closed",
                      count: filteredPulls("history", viewFilter.query).length,
                    },
                  ].map(
                    ({
                      key,
                      label,
                      count,
                      tab,
                    }: {
                      key: string;
                      label: string;
                      count: number;
                      tab?: SavedTab;
                    }) => (
                      <button
                        key={key}
                        type="button"
                        data-tab={key}
                        aria-pressed={key === activeTab}
                        className={count === 0 ? "local-tab empty" : "local-tab"}
                        onClick={() => openTab(key)}
                        onContextMenu={
                          tab
                            ? (event) => tabMenu.open(event, tabActions(tab), tab.name)
                            : undefined
                        }
                      >
                        {label}
                        <span className="local-tab-count">{formatCount(count)}</span>
                      </button>
                    ),
                  )}
                </div>
                {tabMenu.menu}
                {tabsPending ? null : (
                  <FilterButton
                    query={pullsQuery}
                    onChange={setPullsQuery}
                    entries={filterBase}
                    known={knownValues}
                    active={pullsQuery !== ""}
                    openRequest={openPullsMenu}
                    onOpened={() => setOpenPullsMenu(false)}
                    onClosed={() => {
                      setEditingTab(null);
                      setNamingTab(null);
                    }}
                    footer={
                      <>
                        <FilterBar
                          key={editing ? `${activeTab}:edit` : activeTab}
                          query={pullsQuery}
                          onChange={setPullsQuery}
                          entries={filterBase}
                          known={knownValues}
                          actions={
                            editing ? (
                              <TabNameForm
                                name={editing.name}
                                action="Done"
                                onName={(name) => setEditingTab({ id: savedTab!.id, name })}
                                onSubmit={(name) => {
                                  if (name !== savedTab!.name) savedTabs.rename(savedTab!.id, name);
                                  setEditingTab(null);
                                }}
                              />
                            ) : namingTab !== null ? (
                              <TabNameForm
                                name={namingTab}
                                action="Save"
                                focus
                                onName={setNamingTab}
                                onCancel={() => setNamingTab(null)}
                                onSubmit={(name) => {
                                  const id = newInboxId();
                                  // From a saved tab, the new one holds its query and the filter.
                                  savedTabs.add(
                                    { id, name, view: savedTab?.view ?? view, query: pullsQuery },
                                    savedTab ? `tab:${savedTab.id}` : "pulls",
                                  );
                                  setNamingTab(null);
                                  openTab(`tab:${id}`);
                                }}
                              />
                            ) : (
                              <SaveButton options={tabSaves} />
                            )
                          }
                        />
                        <FilterSaveError error={pullsFilter.error} retry={pullsFilter.retry} />
                      </>
                    }
                  />
                )}
              </div>
              <div className="local-page-body">
                {/* A cache older than the poll interval is normal; only a failed refresh
                    or a spent GitHub allowance is worth a word. */}
                {limited.length > 0 ? (
                  <Notice>
                    GitHub's request limit for {repositoryList(limited.map((r) => r.repository))} is
                    used up until{" "}
                    {new Date(
                      Math.max(...limited.map((r) => Date.parse(r.rate.reset))),
                    ).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
                    . Showing what was saved.
                  </Notice>
                ) : null}
                {snapshot.problems.map((p) => (
                  <Notice key={p.clone_id + p.reason}>
                    {clones.find((c) => c.id === p.clone_id)?.repository ?? "Clone"}: {p.reason}
                  </Notice>
                ))}
                {loading ? (
                  <LoadingStatus block>Loading local inbox…</LoadingStatus>
                ) : rows.length === 0 && !(error && revision === 0) ? (
                  <div className="local-empty">
                    <h2>No pull requests here</h2>
                    <p>
                      {!hasRepositories ? (
                        <>
                          Pick GitHub repositories in{" "}
                          <LocalLink href={appURL("?page=repositories")}>Repositories</LocalLink> to
                          get started.
                        </>
                      ) : pullsQuery ? (
                        "Nothing in this view matches the filter."
                      ) : view === "history" ? (
                        "Reviews saved for pull requests that are closed, merged or no longer listed show here."
                      ) : (
                        "Nothing matches this view. Refresh from GitHub or choose another tab."
                      )}
                    </p>
                  </div>
                ) : null}
              </div>
              {!loading && rows.length > 0 ? (
                <Inbox
                  rows={rows}
                  cloneFor={cloneFor}
                  selection={selection}
                  setSelection={setSelection}
                  Link={LocalLink}
                  live={live}
                  changed={reload}
                  groups={groups}
                  fix={(entry) => void fix(entry)}
                  fixStarting={(entry) => fixState(entry).starting}
                  jobs={jobs}
                  generate={(entry) => void generate([entry], "single")}
                  refresh={() => void pageRefresh.press()}
                  bulk={
                    <>
                      <button
                        type="button"
                        className="inbox-bulk"
                        disabled={
                          generating ||
                          indexedEntries.some(
                            (entry) =>
                              selection.has(rowKey(entry)) &&
                              (entry.needs_clone || isActiveJob(reviewJob(entry, jobs))),
                          )
                        }
                        onClick={() =>
                          void generate(
                            snapshot.entries.filter((e) => selection.has(rowKey(e))),
                            "batch",
                          )
                        }
                      >
                        {generating
                          ? "Starting reviews…"
                          : `Generate reviews (${formatCount(selection.size)})`}
                      </button>
                      {selection.size > 1 ? (
                        <button
                          type="button"
                          className="inbox-bulk"
                          disabled={generating}
                          onClick={reviewTogether}
                        >
                          Review together ({formatCount(selection.size)})
                        </button>
                      ) : null}
                    </>
                  }
                />
              ) : null}
            </>
          ) : null}
          {generationError ? <Alert>{generationError}</Alert> : null}
          {/* The PR page and Up next show a fix's refusal beside its button. */}
          {fixError && onList ? (
            <Alert>
              Couldn't start the fix of #{fixError.number}: {fixError.text}
            </Alert>
          ) : null}
          {deletingTab ? (
            <ConfirmDialog
              title={`Delete the ${deletingTab.name} tab?`}
              action="Delete tab"
              danger
              pending={false}
              close={() => {
                setDeletingTab(null);
                // Back to the tab its menu was opened on.
                const key = `tab:${deletingTab.id}`;
                setTimeout(() => {
                  if (document.activeElement !== document.body) return;
                  document.querySelector<HTMLElement>(`[data-tab="${CSS.escape(key)}"]`)?.focus();
                });
              }}
              confirm={() => {
                const tab = deletingTab;
                setDeletingTab(null);
                deleteTab(tab);
              }}
            >
              Only this tab goes. Its pull requests and their reviews stay, under Open or Closed.
            </ConfirmDialog>
          ) : null}
          {deletingInbox ? (
            <ConfirmDialog
              title={`Delete ${deletingInbox.inbox.name}?`}
              action="Delete inbox"
              danger
              pending={false}
              close={() => {
                setDeletingInbox(null);
                // Back where it was asked from, or the inbox's ⋯ when that menu has gone.
                const name = deletingInbox.inbox.name;
                setTimeout(() => {
                  if (document.activeElement !== document.body) return;
                  document
                    .querySelector<HTMLElement>(`[aria-label="More for ${CSS.escape(name)}"]`)
                    ?.focus();
                });
              }}
              confirm={() => {
                const { inbox: target, after } = deletingInbox;
                setDeletingInbox(null);
                removeInbox(target);
                // After the dialog has handed the focus back, so it lands here.
                setTimeout(() => after?.());
              }}
            >
              Only this view goes. Its pull requests, reviews and snoozes stay.
            </ConfirmDialog>
          ) : null}
          {inboxToast ? (
            <div role="status" className="upnext-toast inbox-toast">
              <span>{inboxToast.text}</span>
              <button
                type="button"
                className={SMALL}
                onClick={() => {
                  inboxToast.undo();
                  setInboxToast(null);
                }}
              >
                Undo
              </button>
            </div>
          ) : null}
          {shortcuts ? (
            <ShortcutsSheet current={shortcutsPlace} close={() => setShortcuts(false)} />
          ) : null}
          {reporting ? <ReportProblemDialog close={() => setReporting(false)} /> : null}
          {goingTo ? (
            <GoTo items={goToItems()} go={navigate} close={() => setGoingTo(false)} />
          ) : null}
          {tour.open ? <Tour close={tour.close} /> : null}
          {grouping ? (
            <NameDialog
              title="Review these pull requests together"
              detail={`${grouping.length} pull requests of ${grouping[0]!.pull_request.repository}. baum writes one review across them: how they fit together, the risks only visible across them, and the order to merge them.`}
              initial={grouping[0]!.pull_request.title.slice(0, 80)}
              action="Create group"
              close={() => setGrouping(null)}
              submit={async (name) => {
                const created = await request<ReviewGroup>("api/groups", {
                  name,
                  members: grouping.map((e) => ({
                    repository: e.pull_request.repository,
                    number: e.pull_request.number,
                  })),
                });
                setGrouping(null);
                setSelection(new Set());
                // Groups are made from Pull requests, so the group page goes back there.
                url.update(() => new URLSearchParams({ page: "pulls", group: created.id }));
                await reload();
              }}
            />
          ) : null}
          {fork ? (
            <ForkConfirmation
              entries={fork.entries}
              close={() => setFork(null)}
              confirm={() => void generate(fork.entries, fork.mode, true)}
              pending={generating}
            />
          ) : null}
        </main>
        <KeyBar place={shortcutsPlace} className="local-key-bar" />
      </div>
    </div>
  );
  return <PageRefreshProvider value={pageRefresh}>{shell}</PageRefreshProvider>;
}
/** The one confirmation generation still asks for: an agent runs with code
 *  from a fork, or from an author without known write access. */
function ForkConfirmation({
  entries,
  close,
  confirm,
  pending,
}: {
  entries: Entry[];
  close: () => void;
  confirm: () => void;
  pending: boolean;
}) {
  const dialog = useModal();
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="fork-title">
      <h2 id="fork-title" className="text-lg font-semibold">
        Review code from outside your team?
      </h2>
      <p className="my-3">
        {entries.length === 1
          ? "This pull request comes from a fork or from an author without write access."
          : "One of these pull requests comes from a fork or from an author without write access."}{" "}
        Generating the review runs your agent on its code. Only continue if you trust it, or if your
        agent runs in an isolated environment.
      </p>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="compact" onClick={close}>
          Cancel
        </Button>
        <Button size="compact" disabled={pending} onClick={confirm}>
          {pending ? "Starting…" : "Generate anyway"}
        </Button>
      </div>
    </dialog>
  );
}
/** The jobs, newest first: those running or waiting to, then those that
 *  finished. The keyboard moves through them as through any list; Enter
 *  opens the job's pull request or group and `t` its transcript. */
function Jobs({
  jobs,
  entries,
  loading,
  changed,
}: {
  jobs: Job[];
  /** The pull requests baum knows, for their titles; a job of one it no longer
   *  lists shows its number alone. */
  entries: Entry[];
  loading: boolean;
  changed: () => Promise<void>;
}) {
  const [error, setError] = useState("");
  const [folded, setFolded] = useState<ReadonlySet<string>>(new Set());
  const newest = jobs.slice().reverse();
  const sections = [
    {
      id: "active",
      title: "Running now",
      tone: "positive" as const,
      jobs: newest.filter(isActiveJob),
    },
    {
      id: "finished",
      title: "Finished",
      tone: "quiet" as const,
      jobs: newest.filter((job) => !isActiveJob(job)),
    },
  ].filter((section) => section.jobs.length > 0);
  const rows = useRowFocus<HTMLElement>(
    "jobs",
    sections.flatMap((section) =>
      folded.has(section.id) ? [] : section.jobs.map((job) => job.id),
    ),
  );
  // The jobs being cancelled, and those whose usage shows under them.
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set());
  const [usage, setUsage] = useState<ReadonlySet<string>>(new Set());
  const flip = (set: ReadonlySet<string>, id: string) => {
    const next = new Set(set);
    if (!next.delete(id)) next.add(id);
    return next;
  };
  const cancel = (job: Job) => {
    setCancelling((previous) => new Set(previous).add(job.id));
    void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
      .then(changed)
      .catch((e) => setError(errorText(e)))
      .finally(() => setCancelling((previous) => flip(previous, job.id)));
  };
  // A job's actions, behind its ⋯ and a right click on it.
  const menu = useContextMenu();
  const actionsFor = (job: Job): MenuItem[] => {
    const items: MenuItem[] = [
      { label: "Open job", hint: "↵", onSelect: () => navigate(jobHref(job)) },
    ];
    const target = jobTarget(job);
    if (target)
      items.push({
        label: job.group ? "Open review group" : "Open pull request",
        onSelect: () => navigate(target),
      });
    items.push({
      label: usage.has(job.id) ? "Hide usage" : "Show usage",
      onSelect: () => setUsage((previous) => flip(previous, job.id)),
    });
    if (isActiveJob(job) && !cancelling.has(job.id))
      items.push({ label: "Cancel job", danger: true, onSelect: () => cancel(job) });
    return items;
  };
  const jobMenu = (event: MouseEvent) => {
    const id = (event.target as HTMLElement).closest("[data-row-id]")?.getAttribute("data-row-id");
    const job = jobs.find((each) => each.id === id);
    if (job) menu.open(event, actionsFor(job), jobLabel(job));
  };
  useShortcuts((event) => {
    if (listKeys(event, rows) || event.key !== "t" || !rows.focused) return;
    const job = jobs.find((each) => each.id === rows.focused);
    if (!job?.has_transcript) return;
    event.preventDefault();
    navigate(jobHref(job));
  });
  // A link from a review opens the job that generated it.
  useEffect(() => {
    if (location.hash.startsWith("#job-"))
      document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView({
        block: "center",
      });
  }, []);
  return (
    <section
      ref={rows.list}
      aria-label="Generation jobs"
      className="local-jobs"
      onContextMenu={jobMenu}
    >
      {menu.menu}
      {error ? (
        <div className="local-list-note">
          <Alert>{error}</Alert>
        </div>
      ) : null}
      {jobs.length === 0 && loading ? (
        <LoadingStatus block>Loading jobs…</LoadingStatus>
      ) : jobs.length === 0 ? (
        <div className="local-empty">
          <h2>No jobs yet</h2>
          <p>Reviews and fixes you start, or that run automatically, show here.</p>
        </div>
      ) : (
        sections.map((section) => (
          <ListSection
            key={section.id}
            title={section.title}
            tone={section.tone}
            count={section.jobs.length}
            collapsed={folded.has(section.id)}
            toggle={() =>
              setFolded((previous) => {
                const next = new Set(previous);
                if (!next.delete(section.id)) next.add(section.id);
                return next;
              })
            }
          >
            {section.jobs.map((job) => (
              <JobRow
                key={job.id}
                job={job}
                title={jobPRTitle(entries, job)}
                focused={rows.focused === job.id}
                cancelling={cancelling.has(job.id)}
                usage={usage.has(job.id)}
                actions={
                  <MenuButton label={jobLabel(job)} items={() => actionsFor(job)} menu={menu} />
                }
              />
            ))}
          </ListSection>
        ))
      )}
    </section>
  );
}
const jobStates: Record<string, { label: string; tone: string }> = {
  queued: { label: "Queued", tone: "waiting" },
  running: { label: "Running", tone: "running" },
  completed: { label: "Completed", tone: "done" },
  failed: { label: "Failed", tone: "failed" },
  cancelled: { label: "Cancelled", tone: "stopped" },
  interrupted: { label: "Interrupted", tone: "stopped" },
};
function JobState({ job }: { job: Job }) {
  const state = jobStates[job.state] ?? { label: job.state, tone: "stopped" };
  const Icon =
    state.tone === "done"
      ? CircleCheckIcon
      : state.tone === "failed" || state.tone === "stopped"
        ? CircleCrossIcon
        : CircleDashedIcon;
  return (
    <span className={`local-job-state ${state.tone}`}>
      {state.tone === "running" ? <LiveDot /> : <Icon size={14} />}
      {state.label}
    </span>
  );
}
/** The title of a job's pull request, when baum still lists it. */
function jobPRTitle(entries: Entry[], job: Job | undefined) {
  if (!job || job.group || job.kind === "feature_build") return undefined;
  return entries.find(
    (entry) =>
      entry.pull_request.number === job.pr &&
      entry.pull_request.repository.toLowerCase() === job.repository.toLowerCase(),
  )?.pull_request.title;
}
/** How a job is named to assistive technology and in its menu. */
function jobLabel(job: Job) {
  if (job.kind === "feature_build") return `Feature build of ${job.repository}`;
  if (job.group) return `Review group ${job.group_name || job.group}`;
  return `${jobName(job)} of ${job.repository} #${job.pr}`;
}
function JobRow({
  job,
  title,
  focused,
  cancelling,
  usage,
  actions,
}: {
  job: Job;
  /** The pull request's title, when baum knows it. */
  title?: string;
  focused: boolean;
  cancelling: boolean;
  /** Whether its usage shows under it. */
  usage: boolean;
  /** The row's ⋯ button. */
  actions: ReactNode;
}) {
  const href = jobHref(job);
  const origin = jobOrigin(job);
  return (
    <li
      id={`job-${job.id}`}
      data-row-id={job.id}
      data-focused={focused || undefined}
      className="list-row local-job-row"
    >
      <JobState job={job} />
      <div className="min-w-0">
        <div className="local-job-heading">
          <span className="local-job-type">{jobType(job)}</span>
          {job.kind === "feature_build" ? null : (
            <span className="local-job-repository">{job.repository}</span>
          )}
        </div>
        {job.kind === "feature_build" ? (
          <LocalLink data-row-link="" href={href} className="local-job-title">
            {job.repository}
          </LocalLink>
        ) : job.group ? (
          <LocalLink data-row-link="" href={href} className="local-job-title">
            {job.group_name || job.group}
          </LocalLink>
        ) : (
          <LocalLink data-row-link="" href={href} className="local-job-title" title={title}>
            <span>#{job.pr}</span>
            {title ? ` ${title}` : null}
          </LocalLink>
        )}
        <p className="local-job-meta">
          {origin}
          {job.kind === "fix" && job.commit ? (
            <>
              {origin ? " · " : null}Pushed{" "}
              <a href={job.commit_url} target="_blank" rel="noreferrer">
                <code>{job.commit.slice(0, 7)}</code>
              </a>
              {job.fix?.head_ref ? (
                <>
                  {" "}
                  to <code>{job.fix.head_ref}</code>
                </>
              ) : null}
            </>
          ) : null}
        </p>
        {job.reason ? (
          <p role="status" className="local-job-meta">
            {jobReasonText(job.reason, job.kind)}
          </p>
        ) : null}
        {cancelling ? (
          <p role="status" className="local-job-meta">
            Cancelling…
          </p>
        ) : null}
        {job.kind === "fix" ? <FixDetails job={job} /> : null}
      </div>
      <JobTiming job={job} />
      <span className="local-job-figure">{formatCount(job.tokens)} tokens</span>
      <div className="local-job-actions">{actions}</div>
      {usage ? (
        <div className="local-job-usage">
          <UsageDetails
            accounting={job.accounting}
            charged={job.tokens}
            amount={job.actual_cost_usd}
            reason={job.cost_reason}
          />
        </div>
      ) : null}
    </li>
  );
}

function repositoryList(names: string[]) {
  return names.length === 1 ? names[0] : `${names.length} repositories`;
}

function syncNotice(github: GitHubConnection | null, repositories: string[]) {
  const saved = "Saved reviews are still available.";
  if (github && !github.saved && !github.override && !github.pending)
    return `GitHub is not connected, so baum cannot refresh pull requests. ${saved}`;
  if (github?.error)
    return `The GitHub connection needs attention, so baum cannot refresh pull requests. ${saved}`;
  return `baum could not refresh ${repositoryList(repositories)} from GitHub and will keep retrying. ${saved}`;
}

/** The title a page that failed to read has over its error. */
function readPageTitle(page: string, group: string | null) {
  if (group) return "Review group";
  if (page === "jobs") return "Jobs";
  if (page === "repositories") return "Repositories";
  return page === "pulls" ? "Pull requests" : "Inbox";
}
/** What a page that failed to read could not load. */
function readPageWhat(page: string, group: string | null) {
  if (group) return "the review group";
  if (page === "jobs") return "jobs";
  if (page === "repositories") return "repositories";
  return page === "pulls" ? "pull requests" : "your inbox";
}

/** Why the Sessions page may stay empty: the desktop setup baum's session
 *  reporting needs is incomplete. */
function sessionsSetupWarning(check: { missing: boolean; hooksMissing: boolean }) {
  if (check.missing && check.hooksMissing)
    return "baum can't see your agent sessions: the baum CLI isn't installed and no coding agent is connected.";
  if (check.hooksMissing)
    return "baum can't see your agent sessions: no coding agent has baum's hooks installed.";
  return "baum can't see your agent sessions: the baum CLI isn't installed, and the agent hooks need it to report sessions.";
}
