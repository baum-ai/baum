import { formatCount } from "@baum/viewer/components/format";
import { UsageDetails } from "./usage";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LogoMark } from "@baum/viewer/components/editorial";
import {
  CircleCheckIcon,
  CircleCrossIcon,
  CircleDashedIcon,
  RefreshIcon,
} from "@baum/viewer/components/icons";
import { Button, Alert } from "@baum/viewer/components/ui";
import {
  UrlStateProvider,
  ViewerLinkProvider,
  type UrlState,
  type ViewerLinkProps,
} from "@baum/viewer/navigation";
import { CloneActions, type FoundClones } from "./repositories";
import { GroupMissing, GroupPage, NameDialog } from "./group";
import { Inbox, UpdatedAgo } from "./inbox";
import { UpNextPage, useUpNext } from "./up-next";
import {
  onBody,
  ShortcutsSheet,
  listKeys,
  useCommandKeys,
  useEscapeBlurs,
  useKeyHints,
  useNoTab,
  useRowFocus,
  useShortcuts,
  VIEW_KEYS,
  type ShortcutsPlace,
} from "./shortcuts";
import { GoTo, type GoToItem } from "./go-to";
import { PAGES } from "./pages";
import { LocalReview } from "./review";
import { DecisionsRevision } from "./decisions";
import { useDiagnosticProblems } from "./diagnostics";
import { RepositoriesPage, SettingsPage, settingsSection, type ReviewDefaults } from "./settings";
import { needsYou, SessionsPage, transcriptHref } from "./sessions";
import { SessionTranscriptPage } from "./session-transcript";
import { AnalyticsPage } from "./analytics";
import { Notice, PageBar, Sidebar } from "./shell";
import {
  agentName,
  appBase,
  generateReviews,
  appURL,
  request,
  reviewJob,
  fixJob,
  fixName,
  isActiveJob,
  startFix,
  filtered,
  rowKey,
  automaticLabel,
  jobReasonText,
  parseLive,
  parseAgentRequests,
  noAgentRequests,
  type AgentRequests,
  parseGroups,
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
import { LoadingStatus } from "./nine-dots";
import { FixDetails, TranscriptLink } from "./fix";
import { JobPage } from "./job-transcript";

function useLocation() {
  const [search, setSearch] = useState(() => new URLSearchParams(location.search));
  const current = useRef(search);
  const update = useCallback<UrlState["update"]>((patch) => {
    const next = patch(new URLSearchParams(current.current));
    current.current = next;
    history.pushState(null, "", appURL(`?${next}`));
    setSearch(next);
  }, []);
  useEffect(() => {
    const pop = () => {
      current.current = new URLSearchParams(location.search);
      setSearch(current.current);
    };
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, []);
  return useMemo(() => ({ search, update }), [search, update]);
}
/** Pull requests' views, in the order of their tabs and of `h` and `l`. */
const VIEWS = [
  ["inbox", "For you"],
  ["requested", "Requested"],
  ["mine", "Mine"],
  ["other", "Others"],
  ["no_review", "Not reviewed"],
  ["history", "Saved history"],
] as const;
/** Opens one of the app's own addresses in place, as a link in it does. */
function navigate(href: string) {
  history.pushState(null, "", href);
  dispatchEvent(new PopStateEvent("popstate"));
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
  const [error, setError] = useState("");
  // When the latest read of the daemon completed, for Pull requests' bar.
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [revision, setRevision] = useState(0);
  const [authRevision, setAuthRevision] = useState(0);
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
  const [goingTo, setGoingTo] = useState(false);
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
  const [live, setLive] = useState<LiveSession[]>([]);
  // What agents wait on and the messages queued for them; none from a daemon
  // without the route.
  const [agentRequests, setAgentRequests] = useState<AgentRequests>(noAgentRequests);
  // Counts the `live` events, which an open transcript reads itself again on.
  const [liveTick, setLiveTick] = useState(0);
  // Counts the reads decision lists make again: on a `decisions` event,
  // several at once coalesced, and when the stream opens or resets or the
  // automatic switch changes.
  const [decisionsTick, setDecisionsTick] = useState(0);
  const [groups, setGroups] = useState<ReviewGroup[]>([]);
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
    if (sessions) setLive(sessions);
    setAgentRequests(pending);
  }, []);
  const reloadPending = useRef(false);
  const reloadInFlight = useRef<Promise<void> | null>(null);
  const reload = useCallback(() => {
    reloadPending.current = true;
    if (reloadInFlight.current) return reloadInFlight.current;
    // Publish each completed snapshot; events during a read queue one more
    // instead of starting competing reads that discard each other's results.
    const drain = async () => {
      while (reloadPending.current) {
        reloadPending.current = false;
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
          ] = await Promise.all([
            request<Clone[]>("api/repos"),
            request<Snapshot>("api/index?history=1"),
            request<{ jobs?: Job[] }>("api/jobs"),
            request<typeof rates>("api/prs"),
            request<typeof background>("api/background"),
            // A daemon without automatic generation answers without repositories.
            request<Partial<Automatic>>("api/automatic").catch(() => null),
            request<ReviewDefaults>("api/settings").catch(() => null),
            request<GitHubConnection>("api/github-auth").catch(() => null),
            request<Repository[]>("api/repositories"),
            request<FoundClones>("api/clones/found").catch(() => null),
            // An older daemon has no groups.
            request<unknown>("api/groups").catch(() => null),
          ]);
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
          setGroups(parseGroups(groupList));
          setRates(discovery);
          setBackground(Array.isArray(sync) ? sync : []);
          setAutomatic(auto && Array.isArray(auto.repositories) ? (auto as Automatic) : null);
          setDefaults(settings);
          setGitHub(connection);
          setError("");
          setUpdatedAt(Date.now());
          setRevision((n) => n + 1);
        } catch (e) {
          setError(errorText(e));
        } finally {
          setLoading(false);
        }
      }
    };
    reloadInFlight.current = drain().finally(() => {
      reloadInFlight.current = null;
    });
    return reloadInFlight.current;
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
    return () => {
      clearTimeout(decisionsTimer);
      source.close();
    };
  }, [reload, reloadLive]);
  const authChanged = () => {
    setAuthRevision((n) => n + 1);
    void reload();
  };
  const cloneFor = (e: Entry) =>
    e.clone_id ??
    clones.find((c) => c.repository === e.pull_request.repository && c.available)?.id ??
    "";
  const filter = url.search.get("filter") ?? "inbox";
  const repo = url.search.get("repo") ?? "";
  const indexedEntries = snapshot.entries.map((entry) =>
    isActiveJob(reviewJob(entry, jobs)) || starting.has(rowKey(entry))
      ? { ...entry, status: "generating" }
      : entry,
  );
  const rows = filtered(indexedEntries, filter, repo);
  const repositories = Array.from(
    new Set([
      ...picked.map((r) => r.repository),
      ...indexedEntries.map((e) => e.pull_request.repository),
    ]),
  );
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
  const page = url.search.get("page") ?? "inbox";
  // The inbox's queue counts in the sidebar on every page. Its record of what
  // was cleared waits for the first complete read, and pauses while reads
  // fail, so an empty or failed read clears nothing.
  const inbox = useUpNext(indexedEntries, live, jobs, revision > 0 && !error);
  // The pages that show the diagnostics run them themselves.
  // The diagnostics dialog runs them itself while it is open.
  const diagnostics = useDiagnosticProblems(page !== "diagnostics");
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
  const fix = async (entry: Entry, only?: FixOnly) => {
    const clone = cloneFor(entry);
    const key = rowKey(entry);
    if (!clone || fixStarting.has(key) || isActiveJob(fixJob(entry, jobs))) return;
    setFixError(null);
    setFixStarting((now) => new Set([...now, key]));
    try {
      await startFix(clone, entry.pull_request.number, only);
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
  const refresh = async () => {
    setLoading(true);
    try {
      for (const repository of picked)
        await request("api/prs/refresh", { repository: repository.repository });
      await reload();
    } catch (e) {
      setError(errorText(e));
      setLoading(false);
    }
  };
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
    : selectedEntry && url.search.get("walk")
      ? "walkthrough"
      : (page === "inbox" || page === "pulls") && (groupId || selectedEntry)
        ? "review"
        : (page === "sessions" && openSession) || openJob
          ? "page"
          : page === "inbox" ||
              page === "sessions" ||
              page === "jobs" ||
              page === "repositories" ||
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
      ...PAGES.map((item) => ({
        id: `page:${item.page}`,
        kind: "Page" as const,
        label: item.label,
        href: item.href(),
      })),
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
  useKeyHints();
  useEscapeBlurs();
  useNoTab();
  useCommandKeys((action) => {
    setShortcuts(false);
    if (action.kind === "go-to") {
      setGoingTo((open) => !open);
      return;
    }
    setGoingTo(false);
    if (action.kind === "back") history.back();
    else if (action.kind === "forward") history.forward();
    else navigate(PAGES.find((item) => item.page === action.page)!.href());
  });
  useShortcuts(
    (event) => {
      if (event.key === "?") {
        event.preventDefault();
        setShortcuts((open) => !open);
      } else if (
        onList &&
        (VIEW_KEYS.previous.includes(event.key) || VIEW_KEYS.next.includes(event.key))
      ) {
        if (shortcuts) return;
        // One view along, stopping at either end, as the tabs read.
        event.preventDefault();
        const index = VIEWS.findIndex(([value]) => value === filter);
        const step = VIEW_KEYS.next.includes(event.key) ? 1 : -1;
        const next = VIEWS[Math.min(VIEWS.length - 1, Math.max(0, index + step))]![0];
        if (next === filter) return;
        setSelection(new Set());
        url.update((current) => {
          current.set("filter", next);
          return current;
        });
      } else if (openJob && (event.key === "u" || (event.key === "Escape" && onBody(event)))) {
        // From a job's transcript back to the list.
        event.preventDefault();
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
        event.preventDefault();
        url.update((current) => {
          for (const key of ["pr", "clone", "repository", "group", "walk"]) current.delete(key);
          return current;
        });
      } else if (
        page === "sessions" &&
        openSession &&
        (event.key === "u" || (event.key === "Escape" && onBody(event)))
      ) {
        // The same way back, from a session's transcript to the list.
        event.preventDefault();
        url.update((current) => {
          current.delete("session");
          return current;
        });
      }
    },
    { allowSheet: true },
  );
  return (
    <div className="local-shell">
      <Sidebar
        page={
          page === "jobs" ||
          page === "repositories" ||
          page === "sessions" ||
          page === "analytics" ||
          page === "pulls"
            ? page
            : page === "settings" || page === "diagnostics"
              ? "settings"
              : "inbox"
        }
        inbox={inbox.queue.length}
        activeJobs={jobs.filter(isActiveJob).length}
        runningSessions={live.filter((s) => s.state === "running").length}
        needsYou={needsYou(live).length}
        problems={diagnostics.problems}
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
          {error ? (
            <Alert>
              {error} <Button onClick={() => void reload()}>Retry</Button>
            </Alert>
          ) : null}
          {page === "repositories" ? (
            <RepositoriesPage
              repositories={picked}
              automatic={automatic}
              defaults={defaults}
              changed={reload}
            />
          ) : page === "settings" || page === "diagnostics" ? (
            <SettingsPage
              authRevision={authRevision}
              authChanged={authChanged}
              automatic={automatic}
              defaults={defaults}
              github={github}
              section={settingsSection(url.search.get("section"))}
              setSection={(section) =>
                url.update((current) => {
                  current.set("page", "settings");
                  if (section === "overview") current.delete("section");
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
            />
          ) : page === "sessions" && openSession ? (
            <SessionTranscriptPage
              key={openSession}
              agent={openAgent}
              id={openID}
              session={live.find((s) => s.agent === openAgent && s.session_id === openID) ?? null}
              tick={liveTick}
              prHref={sessionPR}
              Link={LocalLink}
            />
          ) : page === "analytics" ? (
            <AnalyticsPage Link={LocalLink} />
          ) : page === "sessions" ? (
            <>
              <PageBar title="Sessions" detail="Agent sessions on this computer, live" />
              <div className="local-page-body">
                <SessionsPage
                  sessions={live}
                  requests={agentRequests}
                  Link={LocalLink}
                  prHref={sessionPR}
                />
              </div>
            </>
          ) : page === "jobs" && url.search.get("job") ? (
            <JobPage
              job={jobs.find((j) => j.id === url.search.get("job")) ?? null}
              loading={loading}
              Link={LocalLink}
            />
          ) : page === "jobs" ? (
            <>
              <PageBar title="Jobs" detail="Reviews and fixes run on this computer" />
              <div className="local-page-body">
                <Jobs jobs={jobs} changed={reload} />
              </div>
            </>
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
                  revision={revision}
                  changed={reload}
                />
              ) : loading ? (
                <LoadingStatus block>Loading review group…</LoadingStatus>
              ) : (
                <GroupMissing />
              );
            })()
          ) : selectedEntry?.needs_clone && !cloneFor(selectedEntry) ? (
            <section>
              <PageBar
                title={selectedEntry.pull_request.title}
                detail={`${selectedEntry.pull_request.repository} #${selectedEntry.pull_request.number}`}
              />
              <div className="local-page-body local-stack">
                {page === "pulls" ? (
                  <LocalLink href={appURL("?page=pulls")}>Pull requests</LocalLink>
                ) : (
                  <LocalLink href={appURL()}>Inbox</LocalLink>
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
                changed={reload}
                live={live}
                groups={groups.filter(
                  (g) =>
                    sameRepository(g.repository, selectedEntry.pull_request.repository) &&
                    g.members.includes(selectedEntry.pull_request.number),
                )}
                generate={() => void generate([selectedEntry], "single")}
                starting={starting.has(rowKey(selectedEntry))}
                fix={cloneFor(selectedEntry) ? () => void fix(selectedEntry) : undefined}
                fixState={fixState(selectedEntry)}
                fixJob={fixJob(selectedEntry, jobs)}
              />
            </DecisionsRevision.Provider>
          ) : page !== "pulls" ? (
            <UpNextPage
              inbox={inbox}
              // Until a read succeeds there is nothing to say an empty inbox about.
              loading={revision === 0}
              hasRepositories={picked.length > 0 || clones.length > 0}
              cloneFor={cloneFor}
              fix={(entry, only) => void fix(entry, only)}
              fixState={fixState}
              changed={reload}
              Link={LocalLink}
            />
          ) : (
            <>
              <PageBar
                title="Pull requests"
                actions={
                  <>
                    <UpdatedAgo at={updatedAt} busy={loading} />
                    <button
                      type="button"
                      aria-label="Keyboard shortcuts"
                      title="Keyboard shortcuts (?)"
                      onClick={() => setShortcuts(true)}
                      className="local-icon-button"
                    >
                      ?
                    </button>
                    <button
                      type="button"
                      aria-label="Refresh from GitHub"
                      title="Refresh from GitHub"
                      disabled={loading}
                      onClick={() => void refresh()}
                      className="local-icon-button"
                    >
                      <RefreshIcon size={16} className={loading ? "local-spin" : undefined} />
                    </button>
                  </>
                }
              />
              <div className="local-filter-bar">
                <div role="group" aria-label="Show" className="local-tabs">
                  {VIEWS.map(([value, label]) => {
                    const count = filtered(indexedEntries, value, repo).length;
                    return (
                      <button
                        key={value}
                        type="button"
                        aria-pressed={filter === value}
                        className={count === 0 ? "local-tab empty" : "local-tab"}
                        onClick={() => {
                          setSelection(new Set());
                          url.update((current) => {
                            current.set("filter", value);
                            return current;
                          });
                        }}
                      >
                        {label}
                        <span className="local-tab-count">{formatCount(count)}</span>
                      </button>
                    );
                  })}
                </div>
                {repositories.length > 1 ? (
                  <label className="local-repo-filter">
                    <span className="sr-only">Repository</span>
                    <select
                      value={repo}
                      onChange={(e) => {
                        setSelection(new Set());
                        url.update((current) => {
                          if (e.target.value) current.set("repo", e.target.value);
                          else current.delete("repo");
                          return current;
                        });
                      }}
                    >
                      <option value="">All repositories</option>
                      {repositories.map((name) => (
                        <option key={name}>{name}</option>
                      ))}
                    </select>
                  </label>
                ) : null}
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
                ) : rows.length === 0 ? (
                  <div className="local-empty">
                    <h2>
                      {filter === "inbox" && clones.length
                        ? "Nothing is waiting on you"
                        : "No pull requests here"}
                    </h2>
                    <p>
                      {!clones.length
                        ? "Pick GitHub repositories in Repositories to get started."
                        : filter === "inbox"
                          ? "No pull request asks for your review and none of yours is open. Others has the rest of your repositories' pull requests."
                          : filter === "history"
                            ? "Reviews saved for pull requests that are closed, merged or no longer listed show here."
                            : "Nothing matches this view. Refresh from GitHub or choose another tab."}
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
                  refresh={loading ? undefined : () => void refresh()}
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
          )}
          {generationError ? <Alert>{generationError}</Alert> : null}
          {/* The PR page and Up next show a fix's refusal beside its button. */}
          {fixError && onList ? (
            <Alert>
              Couldn't start the fix of #{fixError.number}: {fixError.text}
            </Alert>
          ) : null}
          {shortcuts ? (
            <ShortcutsSheet current={shortcutsPlace} close={() => setShortcuts(false)} />
          ) : null}
          {goingTo ? (
            <GoTo items={goToItems()} go={navigate} close={() => setGoingTo(false)} />
          ) : null}
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
      </div>
    </div>
  );
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
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
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
/** The jobs, newest first. The keyboard moves through them as through any
 *  list; Enter opens the job's pull request or group and `t` its transcript. */
function Jobs({ jobs, changed }: { jobs: Job[]; changed: () => Promise<void> }) {
  const [error, setError] = useState("");
  const newest = jobs.slice().reverse();
  const rows = useRowFocus<HTMLUListElement>(
    "jobs",
    newest.map((job) => job.id),
  );
  useShortcuts((event) => {
    if (listKeys(event, rows) || event.key !== "t" || !rows.focused) return;
    const transcript = rows
      .rowLink(rows.focused)
      ?.closest("[data-row-id]")
      ?.querySelector<HTMLElement>(".local-job-actions a");
    if (!transcript) return;
    event.preventDefault();
    transcript.click();
  });
  // A link from a review opens the job that generated it.
  useEffect(() => {
    if (location.hash.startsWith("#job-"))
      document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView({
        block: "center",
      });
  }, []);
  return (
    <section aria-label="Generation jobs" className="local-jobs">
      {error ? <Alert>{error}</Alert> : null}
      {jobs.length === 0 ? (
        <div className="local-empty">
          <h2>No jobs yet</h2>
          <p>Reviews and fixes you start, or that run automatically, show here.</p>
        </div>
      ) : (
        <ul ref={rows.list} className="local-panel local-job-list">
          {newest.map((job) => (
            <JobRow
              key={job.id}
              job={job}
              focused={rows.focused === job.id}
              cancel={() =>
                request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
                  .then(changed)
                  .catch((e) => setError(errorText(e)))
              }
            />
          ))}
        </ul>
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
      <Icon size={14} className={state.tone === "running" ? "local-spin" : undefined} />
      {state.label}
    </span>
  );
}
function JobRow({
  job,
  focused,
  cancel,
}: {
  job: Job;
  focused: boolean;
  cancel: () => Promise<void>;
}) {
  const [cancelling, setCancelling] = useState(false);
  const started = automaticLabel(job);
  const active = job.state === "queued" || job.state === "running";
  const calls = `${formatCount(job.calls)} ${job.calls === 1 ? "call" : "calls"}${
    job.repairs ? ` · ${formatCount(job.repairs)} ${job.repairs === 1 ? "repair" : "repairs"}` : ""
  }`;
  return (
    <li
      id={`job-${job.id}`}
      data-row-id={job.id}
      data-focused={focused || undefined}
      className="local-job-row"
    >
      <JobState job={job} />
      <div className="min-w-0">
        <LocalLink
          data-row-link=""
          href={appURL(
            job.group
              ? `?group=${encodeURIComponent(job.group)}`
              : `?${job.clone ? `clone=${encodeURIComponent(job.clone)}` : `repository=${encodeURIComponent(job.repository)}`}&pr=${job.pr}`,
          )}
          className="local-job-title"
        >
          {job.group ? (
            <>
              Review group: {job.group_name || job.group} <span>{job.repository}</span>
            </>
          ) : (
            <>
              {job.repository} <span>#{job.pr}</span>
            </>
          )}
        </LocalLink>
        <p className="local-job-meta">
          {job.kind === "fix" ? `${fixName(job.fix ?? {})} · ` : null}
          {started ?? "Started by you"}
        </p>
        {job.kind === "fix" && job.commit ? (
          <p className="local-job-meta">
            Pushed{" "}
            <a href={job.commit_url} target="_blank" rel="noreferrer">
              <code>{job.commit.slice(0, 7)}</code>
            </a>
            {job.fix?.head_ref ? (
              <>
                {" "}
                to <code>{job.fix.head_ref}</code>
              </>
            ) : null}
          </p>
        ) : null}
        {job.reason ? (
          <p role="status" className="local-job-meta">
            {jobReasonText(job.reason)}
          </p>
        ) : null}
        {job.kind === "fix" ? <FixDetails job={job} /> : null}
      </div>
      <span className="local-job-figure">{calls}</span>
      <span className="local-job-figure">{formatCount(job.tokens)} tokens</span>
      <div className="local-job-actions">
        <TranscriptLink job={job} />
        {active ? (
          <Button
            variant="secondary"
            size="compact"
            disabled={cancelling}
            onClick={() => {
              setCancelling(true);
              void cancel().finally(() => setCancelling(false));
            }}
          >
            {cancelling ? "Cancelling…" : "Cancel job"}
          </Button>
        ) : null}
      </div>
      <details className="local-job-usage">
        <summary>Usage</summary>
        <UsageDetails
          accounting={job.accounting}
          charged={job.tokens}
          amount={job.actual_cost_usd}
          reason={job.cost_reason}
        />
      </details>
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
