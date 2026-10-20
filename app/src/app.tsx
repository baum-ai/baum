import { formatCount } from "@baum/viewer/components/format";
import { UsageDetails } from "./usage";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type FormEvent,
} from "react";
import { LogoMark } from "@baum/viewer/components/editorial";
import { Button, Alert } from "@baum/viewer/components/ui";
import {
  UrlStateProvider,
  ViewerLinkProvider,
  type UrlState,
  type ViewerLinkProps,
} from "@baum/viewer/navigation";
import { Inbox } from "./inbox";
import { LocalReview } from "./review";
import { DiagnosticsSection, useDiagnosticProblems } from "./diagnostics";
import {
  appBase,
  appURL,
  request,
  reviewJob,
  isActiveJob,
  filtered,
  rowKey,
  cost,
  automaticLabel,
  automaticNotice,
  parseLive,
  type Automatic,
  type AutomaticStatus,
  type Clone,
  type DiagnosticReport,
  type Entry,
  type Snapshot,
  type Estimate,
  type Job,
  type LiveSession,
} from "./api";

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
          <main className="local-main">
            <h1 className="text-2xl">baum local</h1>
            {auth === "loading" ? (
              <p role="status">Connecting to the local application…</p>
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
  const [snapshot, setSnapshot] = useState<Snapshot>({ entries: [], problems: [] });
  const [jobs, setJobs] = useState<Job[]>([]);
  const [background, setBackground] = useState<
    { repository: string; error?: string; next_refresh: string }[]
  >([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [selection, setSelection] = useState(new Set<string>());
  const [estimate, setEstimate] = useState<Estimate | null>(null);
  const [estimating, setEstimating] = useState(false);
  const [estimateError, setEstimateError] = useState("");
  const [isolation, setIsolation] = useState(false);
  const [rates, setRates] = useState<
    {
      repository: string;
      stale: boolean;
      error?: string;
      rate: { remaining: number | null; reset: string };
    }[]
  >([]);
  const [automatic, setAutomatic] = useState<Automatic | null>(null);
  const [live, setLive] = useState<LiveSession[]>([]);
  const lastLive = useRef(0);
  // Sessions in progress change far more often than the rest, so the live
  // event rereads only them. A daemon without them serves none.
  const reloadLive = useCallback(async () => {
    const sequence = ++lastLive.current;
    // A failed read keeps what the page shows: it is not the end of every session.
    const sessions = await request<unknown>("api/live-sessions").then(parseLive, () => null);
    if (sessions && sequence === lastLive.current) setLive(sessions);
  }, []);
  const lastReload = useRef(0);
  const reload = useCallback(async () => {
    const sequence = ++lastReload.current;
    try {
      const [repos, index, jobList, discovery, sync, auto] = await Promise.all([
        request<Clone[]>("api/repos"),
        request<Snapshot>("api/index?history=1"),
        request<{ jobs?: Job[] }>("api/jobs"),
        request<typeof rates>("api/prs"),
        request<typeof background>("api/background"),
        // A daemon without automatic generation answers without repositories.
        request<Partial<Automatic>>("api/automatic").catch(() => null),
      ]);
      if (sequence !== lastReload.current) return;
      setClones(repos);
      setSnapshot(index);
      setJobs(jobList.jobs ?? []);
      setRates(discovery);
      setBackground(Array.isArray(sync) ? sync : []);
      setAutomatic(auto && Array.isArray(auto.repositories) ? (auto as Automatic) : null);
      setError("");
      setRevision((n) => n + 1);
    } catch (e) {
      if (sequence === lastReload.current) setError(errorText(e));
    } finally {
      if (sequence === lastReload.current) setLoading(false);
    }
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
  const requestedNumber = Number(url.search.get("pr"));
  const requestedClone = url.search.get("clone");
  const requestedRepository = clones.find((clone) => clone.id === requestedClone)?.repository;
  const selectedEntry =
    indexedEntries.find(
      (entry) =>
        entry.pull_request.number === requestedNumber && cloneFor(entry) === requestedClone,
    ) ??
    indexedEntries.find(
      (entry) =>
        requestedRepository &&
        entry.pull_request.number === requestedNumber &&
        entry.pull_request.repository.toLowerCase() === requestedRepository.toLowerCase(),
    );
  const page = url.search.get("page") ?? "inbox";
  // The pages that show the diagnostics run them themselves.
  const diagnostics = useDiagnosticProblems(page !== "settings" && page !== "diagnostics");
  const generate = async (entries: Entry[], mode: "single" | "batch") => {
    if (entries.some((entry) => isActiveJob(reviewJob(entry, jobs)))) {
      setEstimateError("A review of a selected pull request is already queued or running.");
      return;
    }
    setEstimate(null);
    setEstimateError("");
    setIsolation(false);
    setEstimating(true);
    try {
      setEstimate(
        await request<Estimate>("api/jobs/estimate", {
          items: entries.map((e) => ({ clone: cloneFor(e), pr: e.pull_request.number })),
          mode,
        }),
      );
    } catch (e) {
      setEstimateError(errorText(e));
    } finally {
      setEstimating(false);
    }
  };
  const refresh = async () => {
    setLoading(true);
    try {
      for (const clone of clones.filter(
        (c, i, list) => list.findIndex((r) => r.repository === c.repository) === i,
      ))
        await request("api/prs/refresh", { clone: clone.id });
      await reload();
    } catch (e) {
      setError(errorText(e));
      setLoading(false);
    }
  };
  return (
    <>
      <header className="local-chrome">
        <LogoMark className="h-7 w-7" />
        <strong>baum local</strong>
        <LocalLink href={appURL()}>Inbox</LocalLink>
        <LocalLink href={appURL("?page=jobs")}>Jobs</LocalLink>
        <LocalLink href={appURL("?page=settings")}>Repositories & settings</LocalLink>
        <span className="text-xs text-(--text-tertiary)">On this machine</span>
      </header>
      {background
        .filter((state) => state.error)
        .map((state) => (
          <Alert key={state.repository}>
            {state.repository}: background metadata refresh is unavailable ({state.error}). Cached
            reviews remain readable. Next retry: {new Date(state.next_refresh).toLocaleTimeString()}
            .
          </Alert>
        ))}
      {(automatic?.repositories ?? [])
        .filter((status) => status.enabled && status.paused_until)
        .map((status) => (
          <Alert key={`automatic-${status.repository}`}>
            {status.repository}: {automaticNotice(status)}
          </Alert>
        ))}
      {diagnostics.problems > 0 && page !== "diagnostics" ? (
        <Alert>
          baum cannot fully work on this computer: diagnostics found {diagnostics.problems}{" "}
          {diagnostics.problems === 1 ? "problem" : "problems"}.{" "}
          <LocalLink href={appURL("?page=diagnostics")}>Show the fixes</LocalLink>
        </Alert>
      ) : null}
      <main className="local-main">
        {error ? (
          <Alert>
            {error} <Button onClick={() => void reload()}>Retry</Button>
          </Alert>
        ) : null}
        {page === "settings" ? (
          <Settings
            clones={clones}
            automatic={automatic}
            changed={reload}
            onDiagnostics={diagnostics.update}
          />
        ) : page === "diagnostics" ? (
          <>
            <h1 className="text-2xl mb-4">Check this computer</h1>
            <p className="mb-4">
              What baum needs on this computer, what it found, and how to fix each problem.
            </p>
            <DiagnosticsSection onReport={diagnostics.update} />
          </>
        ) : page === "jobs" ? (
          <Jobs jobs={jobs} changed={reload} />
        ) : selectedEntry ? (
          <LocalReview
            key={rowKey(selectedEntry)}
            entry={selectedEntry}
            clone={cloneFor(selectedEntry)}
            revision={revision}
            job={reviewJob(selectedEntry, jobs)}
            changed={reload}
            live={live}
            generate={() => void generate([selectedEntry], "single")}
          />
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-3 mb-4">
              <h1 className="text-2xl mr-auto">Pull requests</h1>
              <Button variant="secondary" disabled={loading} onClick={() => void refresh()}>
                Refresh from GitHub
              </Button>
              <Button
                disabled={
                  selection.size === 0 ||
                  estimating ||
                  indexedEntries.some(
                    (entry) => selection.has(rowKey(entry)) && isActiveJob(reviewJob(entry, jobs)),
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
            </div>
            <div className="flex flex-wrap gap-3 mb-4">
              <label>
                Show{" "}
                <select
                  className="local-field"
                  value={filter}
                  onChange={(e) => {
                    setSelection(new Set());
                    url.update((current) => {
                      current.set("filter", e.target.value);
                      return current;
                    });
                  }}
                >
                  {[
                    ["all", "Open pull requests"],
                    ["requested", "Review requested"],
                    ["mine", "Authored by me"],
                    ["outdated", "Outdated"],
                    ["no_review", "No review"],
                    ["history", "Saved history"],
                  ].map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Repository{" "}
                <select
                  className="local-field"
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
                  {Array.from(new Set(clones.map((c) => c.repository))).map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
              </label>
            </div>
            {rates
              .filter((r) => r.stale || r.error || r.rate.remaining === 0)
              .map((r) => (
                <Alert key={r.repository}>
                  {r.repository}: {r.error ?? "Cached discovery is stale"}. GitHub requests
                  remaining: {r.rate.remaining ?? "unknown"}; reset: {r.rate.reset}.
                </Alert>
              ))}
            {snapshot.problems.map((p) => (
              <Alert key={p.clone_id + p.reason}>
                {clones.find((c) => c.id === p.clone_id)?.repository ?? "Clone"}: {p.reason}
              </Alert>
            ))}
            {loading ? (
              <p role="status">Loading local inbox…</p>
            ) : rows.length === 0 ? (
              <div className="local-panel">
                <h2 className="text-lg">No pull requests here</h2>
                <p>
                  {clones.length
                    ? "Refresh from GitHub or choose another filter. Cached reviews remain in Saved history."
                    : "Register a local clone in Repositories & settings to get started."}
                </p>
              </div>
            ) : (
              <Inbox
                rows={rows}
                cloneFor={cloneFor}
                selection={selection}
                setSelection={setSelection}
                Link={LocalLink}
                live={live}
              />
            )}
          </>
        )}
        {jobs.some(
          (j) =>
            isActiveJob(j) &&
            (!selectedEntry ||
              j.repository.toLowerCase() !== selectedEntry.pull_request.repository.toLowerCase() ||
              j.pr !== selectedEntry.pull_request.number),
        ) && page !== "jobs" ? (
          <div className="mt-4">
            <Jobs
              jobs={jobs.filter(
                (j) =>
                  isActiveJob(j) &&
                  (!selectedEntry ||
                    j.repository.toLowerCase() !==
                      selectedEntry.pull_request.repository.toLowerCase() ||
                    j.pr !== selectedEntry.pull_request.number),
              )}
              changed={reload}
            />
          </div>
        ) : null}
        {estimating ? <p role="status">Estimating generation cost…</p> : null}
        {estimateError ? <Alert>{estimateError}</Alert> : null}
        {estimate ? (
          <Confirmation
            estimate={estimate}
            error={estimateError}
            isolation={isolation}
            setIsolation={setIsolation}
            close={() => setEstimate(null)}
            confirm={async (unpriced) => {
              try {
                await request("api/jobs", {
                  estimate_id: estimate.id,
                  confirm_estimate: true,
                  confirm_isolation: isolation,
                  confirm_unpriced: unpriced,
                });
                setEstimate(null);
                setSelection(new Set());
                await reload();
              } catch (e) {
                setEstimateError(errorText(e));
              }
            }}
          />
        ) : null}
      </main>
    </>
  );
}
// estimateReasons explains, in words, why an item has no estimate on purpose.
const estimateReasons: Record<string, string> = {
  billed_by_cursor_subscription:
    "Cost is billed by your Cursor subscription; baum can't estimate it.",
};
// blockedReasons explains why an item is left out of the generation.
const blockedReasons: Record<string, string> = {
  unknown_model_price:
    "No price is known for this model. Choose a priced model in repository settings.",
  isolation_confirmation_required:
    "A fork or an author without known write access needs its own confirmation: generate this pull request on its own.",
};
export function estimateReasonText(reason: string): string {
  return estimateReasons[reason] ?? "baum can't estimate the cost of this run.";
}
export function blockedReasonText(reason: string): string {
  return blockedReasons[reason] ?? "It cannot be generated with the current settings.";
}
function Confirmation({
  estimate,
  error,
  isolation,
  setIsolation,
  close,
  confirm,
}: {
  estimate: Estimate;
  error: string;
  isolation: boolean;
  setIsolation: (value: boolean) => void;
  close: () => void;
  confirm: (unpriced: boolean) => Promise<void>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [pending, setPending] = useState(false);
  const [unpriced, setUnpriced] = useState(false);
  const needed = estimate.items.some((i) => i.requires_isolation_confirmation && !i.blocked_reason);
  const allowed = estimate.items.some((i) => !i.blocked_reason);
  const unpricedNeeded = estimate.items.some((i) => i.estimate_reason && !i.blocked_reason);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="estimate-title">
      <h2 id="estimate-title" className="text-lg">
        Confirm review generation
      </h2>
      {error ? <Alert>{error}</Alert> : null}
      <p>
        Budget planning amount: <strong>{cost(estimate.estimate_usd)}</strong>. This uses configured
        budgets, not the size of this pull request, and is not a spending limit or a prediction of
        its cost.
      </p>
      <p className="text-xs">
        Prices: {estimate.price_version}. Expires{" "}
        {new Date(estimate.expires_at).toLocaleTimeString()}.
      </p>
      <ul className="my-3 space-y-2">
        {estimate.items.map((item) => (
          <li key={`${item.clone}:${item.pr}`}>
            #{item.pr}: {item.backend} / {item.model || "No model configured"} —{" "}
            {item.estimate_reason ? "cost unknown" : cost(item.estimate_usd)}
            {item.estimate_reason ? (
              <p>{estimateReasonText(item.estimate_reason)}</p>
            ) : (
              <p>
                Assumes {formatCount(item.max_tokens)} charged tokens and {item.max_calls} backend
                calls, with one budget-sized cache read per call at the highest listed rates. Cache
                reads can exceed this assumption.
              </p>
            )}
            {item.blocked_reason ? (
              <p role="alert">Excluded: {blockedReasonText(item.blocked_reason)}</p>
            ) : null}
          </li>
        ))}
      </ul>
      {needed ? (
        <label className="block my-3">
          <input
            type="checkbox"
            checked={isolation}
            onChange={(e) => setIsolation(e.target.checked)}
          />{" "}
          I confirm running an agent with repository content from a fork or author without known
          write access in the configured isolation environment.
        </label>
      ) : null}
      {unpricedNeeded ? (
        <label className="block my-3">
          <input
            type="checkbox"
            checked={unpriced}
            onChange={(e) => setUnpriced(e.target.checked)}
          />{" "}
          I understand that baum can't estimate the cost of these runs: my Cursor subscription bills
          them.
        </label>
      ) : null}
      <div className="flex gap-3">
        <Button
          disabled={pending || !allowed || (needed && !isolation) || (unpricedNeeded && !unpriced)}
          onClick={() => {
            setPending(true);
            void confirm(unpricedNeeded && unpriced).finally(() => setPending(false));
          }}
        >
          {pending ? "Starting…" : "Confirm estimate and generate"}
        </Button>
        <Button variant="secondary" onClick={close}>
          Cancel
        </Button>
      </div>
    </dialog>
  );
}
function Jobs({ jobs, changed }: { jobs: Job[]; changed: () => Promise<void> }) {
  const [error, setError] = useState("");
  return (
    <section aria-label="Generation jobs" className="local-panel space-y-3">
      <h2 className="text-lg">Generation jobs</h2>
      {error ? <Alert>{error}</Alert> : null}
      {jobs.length === 0 ? (
        <p>No generation jobs yet.</p>
      ) : (
        jobs.map((job) => (
          <div key={job.id} className="border-b border-(--border-default) pb-3">
            <strong>
              {job.repository} #{job.pr}
            </strong>{" "}
            — {job.state} · {job.phase}
            {automaticLabel(job) ? (
              <p className="text-xs font-semibold text-(--text-secondary)">{automaticLabel(job)}</p>
            ) : null}
            <p>
              Calls {job.calls} · Repairs {job.repairs}
            </p>
            <UsageDetails
              accounting={job.accounting}
              charged={job.tokens}
              amount={job.actual_cost_usd}
              reason={job.cost_reason}
            />
            {job.reason ? <p role="status">{job.reason}</p> : null}
            {job.state === "queued" || job.state === "running" ? (
              <Button
                variant="secondary"
                onClick={() => {
                  void request(`api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
                    .then(changed)
                    .catch((e) => setError(errorText(e)));
                }}
              >
                Cancel job
              </Button>
            ) : null}
          </div>
        ))
      )}
    </section>
  );
}
function Settings({
  clones,
  automatic,
  changed,
  onDiagnostics,
}: {
  clones: Clone[];
  automatic: Automatic | null;
  changed: () => Promise<void>;
  onDiagnostics: (report: DiagnosticReport | null) => void;
}) {
  const [path, setPath] = useState("");
  const [error, setError] = useState("");
  const action = async (fn: () => Promise<unknown>) => {
    try {
      await fn();
      setError("");
      await changed();
    } catch (e) {
      setError(errorText(e));
    }
  };
  return (
    <section className="space-y-4">
      <h1 className="text-2xl">Repositories & settings</h1>
      <p>
        Global backend and credentials come from your local baum configuration. Overrides below are
        shared by this repository’s registered clones.
      </p>
      {error ? <Alert>{error}</Alert> : null}
      {automatic ? (
        <AutomaticSettings
          automatic={automatic}
          save={(patch) => action(() => request("api/automatic", patch, "PUT"))}
        />
      ) : null}
      <form
        className="flex flex-wrap gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void action(() => request("api/repos", { path })).then(() => setPath(""));
        }}
      >
        <label>
          Local clone path{" "}
          <input
            required
            className="local-field"
            value={path}
            onChange={(e) => setPath(e.target.value)}
          />
        </label>
        <Button type="submit">Register clone</Button>
      </form>
      {clones.map((clone) => (
        <div
          role="region"
          aria-label={`Repository ${clone.repository}`}
          className="local-panel space-y-3"
          key={clone.id}
        >
          <h2 className="text-lg">{clone.repository}</h2>
          <p className="[overflow-wrap:anywhere]">
            {clone.path} — {clone.available ? "Available" : "Unavailable; history retained"}
          </p>
          <RepositorySettings
            key={JSON.stringify(clone.settings)}
            clone={clone}
            automatic={automatic}
            status={automatic?.repositories.find(
              (status) => status.repository === clone.repository.toLowerCase(),
            )}
            save={(settings) =>
              action(() => request(`api/repos/${clone.id}/settings`, settings, "PUT"))
            }
          />
          <Button
            variant="secondary"
            onClick={() => void action(() => request(`api/repos/${clone.id}`, {}, "DELETE"))}
          >
            Remove registration
          </Button>
        </div>
      ))}
      <DiagnosticsSection onReport={onDiagnostics} />
    </section>
  );
}
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      {label}
      {children}
    </label>
  );
}
/** The global automatic generation switch and the one-time cursor-agent
 * acknowledgement. The server enforces both. */
function AutomaticSettings({
  automatic,
  save,
}: {
  automatic: Automatic;
  save: (patch: Partial<Omit<Automatic, "repositories">>) => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const update = (patch: Partial<Omit<Automatic, "repositories">>) => {
    setPending(true);
    void save(patch).finally(() => setPending(false));
  };
  return (
    <section aria-label="Automatic reviews" className="local-panel space-y-3">
      <h2 className="text-lg">Automatic reviews</h2>
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          className="mt-1"
          checked={automatic.enabled}
          disabled={pending}
          onChange={(e) => update({ enabled: e.target.checked })}
        />
        <span>
          Generate reviews automatically
          <span className="block text-xs text-(--text-tertiary)">
            When a pull request you wrote or were asked to review gets new commits or a new review
            request, the daemon generates its review with the configured backend and budgets, and
            your provider bills it. Pull requests from forks or authors without write access always
            need your confirmation. Pull requests already open when this is turned on are not caught
            up. Turning this off pauses automatic reviews in every repository, including those set
            to On; while it is on, a repository can be turned off below.
          </span>
        </span>
      </label>
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          className="mt-1"
          checked={automatic.cursor_agent_acknowledged}
          disabled={pending}
          onChange={(e) => update({ cursor_agent_acknowledged: e.target.checked })}
        />
        <span>
          Allow automatic reviews with cursor-agent
          <span className="block text-xs text-(--text-tertiary)">
            cursor-agent is billed by Cursor and baum cannot estimate or cap its cost. Without this
            acknowledgement automatic generation skips repositories that use it.
          </span>
        </span>
      </label>
      <p className="text-xs">
        The daemon keeps generating after you close this window. Quit the desktop app with “Quit and
        stop background reviews”, or run <code>baum daemon stop</code>, to stop it.
      </p>
    </section>
  );
}
function RepositorySettings({
  clone,
  automatic,
  status,
  save,
}: {
  clone: Clone;
  automatic: Automatic | null;
  status?: AutomaticStatus;
  save: (settings: Clone["settings"]) => Promise<void>;
}) {
  const [backend, setBackend] = useState(clone.settings.backend ?? "");
  const [model, setModel] = useState(clone.settings.model ?? "");
  const [mode, setMode] = useState(
    clone.settings.automatic === null || clone.settings.automatic === undefined
      ? "default"
      : clone.settings.automatic
        ? "on"
        : "off",
  );
  const [cap, setCap] = useState(
    clone.settings.daily_cap === null || clone.settings.daily_cap === undefined
      ? ""
      : String(clone.settings.daily_cap),
  );
  // Removing the cap is an explicit choice: an empty or unreadable amount is
  // an error, never "no cap".
  const [noCap, setNoCap] = useState(
    clone.settings.daily_cap === null || clone.settings.daily_cap === undefined,
  );
  const [capError, setCapError] = useState("");
  const [pending, setPending] = useState(false);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const amount = cap.trim() === "" ? Number.NaN : Number(cap);
    if (automatic && !noCap && !(Number.isFinite(amount) && amount >= 0)) {
      setCapError("Enter a daily cap of 0 or more in USD, or choose No daily cap.");
      return;
    }
    setCapError("");
    setPending(true);
    void save({
      automatic: mode === "default" ? null : mode === "on",
      daily_cap: !automatic ? (clone.settings.daily_cap ?? null) : noCap ? null : amount,
      backend: backend || null,
      model: model || null,
    }).finally(() => setPending(false));
  };
  const notice = status ? automaticNotice(status) : null;
  return (
    // The form validates the cap itself, with one message for every invalid
    // amount, rather than the browser's range bubble.
    <form onSubmit={submit} className="space-y-3" noValidate>
      {automatic ? (
        <div className="flex flex-wrap gap-3">
          <Field label="Automatic reviews">
            <select className="local-field" value={mode} onChange={(e) => setMode(e.target.value)}>
              <option value="default">Global setting ({automatic.enabled ? "on" : "off"})</option>
              <option value="on">
                {automatic.enabled
                  ? "On for this repository"
                  : "On for this repository (paused by the global switch)"}
              </option>
              <option value="off">Off for this repository</option>
            </select>
          </Field>
          <Field label="Daily automatic cap (USD)">
            <input
              className="local-field"
              type="number"
              min="0"
              step="0.01"
              value={noCap ? "" : cap}
              disabled={noCap}
              aria-invalid={capError ? true : undefined}
              onChange={(e) => {
                setCap(e.target.value);
                setCapError("");
              }}
              placeholder={noCap ? "No cap" : "0.00"}
            />
          </Field>
          <label className="flex items-center gap-2 self-end pb-2">
            <input
              type="checkbox"
              checked={noCap}
              onChange={(e) => {
                setNoCap(e.target.checked);
                setCapError("");
              }}
            />
            No daily cap
          </label>
        </div>
      ) : null}
      {capError ? <Alert>{capError}</Alert> : null}
      {status ? (
        <p className="text-xs" role="status">
          {status.enabled
            ? "Automatic reviews are on for this repository."
            : clone.settings.automatic === false
              ? "Automatic reviews are off for this repository."
              : `Automatic reviews are paused by the global switch${
                  clone.settings.automatic ? ", although this repository is set to On" : ""
                }. Turn on “Generate reviews automatically” above to resume them.`}
          {status.enabled
            ? ` Spent automatically today: ${status.spent_known ? "" : "at least "}${cost(status.spent_today)}${
                status.daily_cap === null ? "" : ` of ${cost(status.daily_cap)}`
              }.`
            : ""}
          {status.pending ? ` ${status.pending} waiting to start.` : ""}
        </p>
      ) : null}
      {notice ? <Alert>{notice}</Alert> : null}
      <div className="flex flex-wrap gap-3">
        <Field label="Backend">
          <select
            className="local-field"
            value={backend}
            onChange={(e) => setBackend(e.target.value)}
          >
            <option value="">Global default</option>
            {["auto", "codex", "claude", "anthropic", "cursor-agent"].map((name) => (
              <option key={name}>{name}</option>
            ))}
          </select>
        </Field>
        <Field label="Model override">
          <input
            className="local-field"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="Global default"
          />
        </Field>
      </div>
      <p className="text-xs">
        GitHub metadata and saved review evidence refresh in the background without model calls.
        Model generation runs when you request and confirm it, or automatically as set above. Every
        run stays within the configured budgets.
      </p>
      <Button type="submit" disabled={pending}>
        {pending ? "Saving…" : "Save repository settings"}
      </Button>
    </form>
  );
}
