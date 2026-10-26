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
import { Inbox } from "./inbox";
import { LocalReview } from "./review";
import { useDiagnosticProblems } from "./diagnostics";
import { RepositoriesPage, SettingsPage, type ReviewDefaults } from "./settings";
import { Notice, PageBar, Sidebar } from "./shell";
import {
  appBase,
  generateReviews,
  appURL,
  request,
  reviewJob,
  isActiveJob,
  filtered,
  rowKey,
  automaticLabel,
  jobReasonText,
  automaticNotice,
  parseLive,
  parseGroups,
  sameRepository,
  MAX_GROUP_MEMBERS,
  type ReviewGroup,
  type GitHubConnection,
  type Automatic,
  type Clone,
  type Repository,
  type Entry,
  type Snapshot,
  type Job,
  type LiveSession,
} from "./api";
import { LoadingStatus } from "./nine-dots";

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
          history.pushState(null, "", href);
          dispatchEvent(new PopStateEvent("popstate"));
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
  const [revision, setRevision] = useState(0);
  const [authRevision, setAuthRevision] = useState(0);
  const [selection, setSelection] = useState(new Set<string>());
  const [generating, setGenerating] = useState(false);
  const [generationError, setGenerationError] = useState("");
  // A generation that includes a fork, waiting for the user's confirmation.
  const [fork, setFork] = useState<{ entries: Entry[]; mode: "single" | "batch" } | null>(null);
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
  const [groups, setGroups] = useState<ReviewGroup[]>([]);
  // The pull requests being grouped, while the group name is asked.
  const [grouping, setGrouping] = useState<Entry[] | null>(null);
  const lastLive = useRef(0);
  // Sessions in progress change far more often than the rest, so the live
  // event rereads only them. A daemon without them serves none.
  const reloadLive = useCallback(async () => {
    const sequence = ++lastLive.current;
    // A failed read keeps what the page shows: it is not the end of every session.
    const sessions = await request<unknown>("api/live-sessions").then(parseLive, () => null);
    if (sessions && sequence === lastLive.current) setLive(sessions);
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
    const refreshLive = () => void reloadLive();
    source.addEventListener("open", refresh);
    source.addEventListener("open", refreshLive);
    source.addEventListener("live", refreshLive);
    source.addEventListener("reset", refreshLive);
    source.addEventListener("index", refresh);
    source.addEventListener("progress", refresh);
    source.addEventListener("automatic", refresh);
    source.addEventListener("reset", refresh);
    return () => source.close();
  }, [reload, reloadLive]);
  const authChanged = () => {
    setAuthRevision((n) => n + 1);
    void reload();
  };
  const cloneFor = (e: Entry) =>
    e.clone_id ??
    clones.find((c) => c.repository === e.pull_request.repository && c.available)?.id ??
    "";
  const filter = url.search.get("filter") ?? "all";
  const repo = url.search.get("repo") ?? "";
  const indexedEntries = snapshot.entries.map((entry) =>
    isActiveJob(reviewJob(entry, jobs)) ? { ...entry, status: "generating" } : entry,
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
    }
  };
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
  return (
    <div className="local-shell">
      <Sidebar
        page={
          page === "jobs" || page === "repositories"
            ? page
            : page === "settings" || page === "diagnostics"
              ? "settings"
              : "inbox"
        }
        activeJobs={jobs.filter(isActiveJob).length}
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
            <Notice action={<LocalLink href={appURL("?page=settings")}>Open settings</LocalLink>}>
              {syncNotice(github, unsynced)}
            </Notice>
          ) : null}
          {(automatic?.repositories ?? [])
            .filter((status) => status.enabled && status.paused_until)
            .map((status) => (
              <Notice key={`automatic-${status.repository}`}>
                {status.repository}: {automaticNotice(status)}
              </Notice>
            ))}
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
          ) : page === "jobs" ? (
            <>
              <PageBar title="Jobs" detail="Reviews generated on this computer" />
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
                <LocalLink href={appURL()}>Inbox</LocalLink>
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
            />
          ) : (
            <>
              <PageBar
                title="Pull requests"
                detail={loading ? null : `${formatCount(rows.length)} shown`}
                actions={
                  <>
                    {selection.size > 1 ? (
                      <Button
                        variant="secondary"
                        size="compact"
                        disabled={generating}
                        onClick={reviewTogether}
                      >
                        Review together ({selection.size})
                      </Button>
                    ) : null}
                    {selection.size > 0 ? (
                      <Button
                        size="compact"
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
                        Generate selected ({selection.size})
                      </Button>
                    ) : null}
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
                  {(
                    [
                      ["all", "Open"],
                      ["requested", "Requested"],
                      ["mine", "Mine"],
                      ["no_review", "Not reviewed"],
                      ["history", "Saved history"],
                    ] as const
                  ).map(([value, label]) => (
                    <button
                      key={value}
                      type="button"
                      aria-pressed={filter === value}
                      className="local-tab"
                      onClick={() => {
                        setSelection(new Set());
                        url.update((current) => {
                          current.set("filter", value);
                          return current;
                        });
                      }}
                    >
                      {label}
                      <span className="local-tab-count">
                        {formatCount(filtered(indexedEntries, value, repo).length)}
                      </span>
                    </button>
                  ))}
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
                    <h2>No pull requests here</h2>
                    <p>
                      {clones.length
                        ? "Refresh from GitHub or choose another filter. Cached reviews remain in Saved history."
                        : "Pick GitHub repositories in Repositories to get started."}
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
                  groups={groups}
                  jobs={jobs}
                />
              ) : null}
            </>
          )}
          {generationError ? <Alert>{generationError}</Alert> : null}
          {grouping ? (
            <NameDialog
              title="Review these pull requests together"
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
                url.update(() => new URLSearchParams({ group: created.id }));
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
function Jobs({ jobs, changed }: { jobs: Job[]; changed: () => Promise<void> }) {
  const [error, setError] = useState("");
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
          <h2>No reviews generated yet</h2>
          <p>Reviews you generate, or that run automatically, show here.</p>
        </div>
      ) : (
        <ul className="local-panel local-job-list">
          {jobs
            .slice()
            .reverse()
            .map((job) => (
              <JobRow
                key={job.id}
                job={job}
                cancel={() =>
                  void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
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
function JobRow({ job, cancel }: { job: Job; cancel: () => void }) {
  const started = automaticLabel(job);
  const active = job.state === "queued" || job.state === "running";
  const calls = `${formatCount(job.calls)} ${job.calls === 1 ? "call" : "calls"}${
    job.repairs ? ` · ${formatCount(job.repairs)} ${job.repairs === 1 ? "repair" : "repairs"}` : ""
  }`;
  return (
    <li id={`job-${job.id}`} className="local-job-row">
      <JobState job={job} />
      <div className="min-w-0">
        <LocalLink
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
        <p className="local-job-meta">{started ?? "Started by you"}</p>
        {job.reason ? (
          <p role="status" className="local-job-meta">
            {jobReasonText(job.reason)}
          </p>
        ) : null}
      </div>
      <span className="local-job-figure">{calls}</span>
      <span className="local-job-figure">{formatCount(job.tokens)} tokens</span>
      <div className="local-job-actions">
        {active ? (
          <Button variant="secondary" size="compact" onClick={cancel}>
            Cancel job
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
