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
import {
  request,
  filtered,
  rowKey,
  cost,
  type Clone,
  type Entry,
  type Snapshot,
  type Estimate,
  type Job,
} from "./api";

function useLocation() {
  const [search, setSearch] = useState(() => new URLSearchParams(location.search));
  const current = useRef(search);
  const update = useCallback<UrlState["update"]>((patch) => {
    const next = patch(new URLSearchParams(current.current));
    current.current = next;
    history.pushState(null, "", `/?${next}`);
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
          href.startsWith("/")
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
    const token = new URLSearchParams(location.hash.slice(1)).get("token");
    history.replaceState(null, "", location.pathname + location.search);
    (async () => {
      if (token) await request("/api/auth/exchange", { token });
      await request("/api/health");
      if (active) setAuth("ready");
    })().catch((error) => {
      if (active) {
        setAuth("failed");
        setAuthError(errorText(error));
      }
    });
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
              <Alert>{authError} Open the URL printed by baum ui to authorize this browser.</Alert>
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
  const lastReload = useRef(0);
  const reload = useCallback(async () => {
    const sequence = ++lastReload.current;
    try {
      const [repos, index, jobList, discovery] = await Promise.all([
        request<Clone[]>("/api/repos"),
        request<Snapshot>("/api/index?history=1"),
        request<{ jobs?: Job[] }>("/api/jobs"),
        request<typeof rates>("/api/prs"),
      ]);
      if (sequence !== lastReload.current) return;
      setClones(repos);
      setSnapshot(index);
      setJobs(jobList.jobs ?? []);
      setRates(discovery);
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
    const source = new EventSource("/api/events");
    const refresh = () => void reload();
    source.addEventListener("open", refresh);
    source.addEventListener("index", refresh);
    source.addEventListener("progress", refresh);
    source.addEventListener("reset", refresh);
    return () => source.close();
  }, [reload]);
  const cloneFor = (e: Entry) =>
    e.clone_id ??
    clones.find((c) => c.repository === e.pull_request.repository && c.available)?.id ??
    "";
  const filter = url.search.get("filter") ?? "all";
  const repo = url.search.get("repo") ?? "";
  const rows = filtered(snapshot.entries, filter, repo);
  const selectedEntry = snapshot.entries.find(
    (e) =>
      e.pull_request.number === Number(url.search.get("pr")) &&
      cloneFor(e) === url.search.get("clone"),
  );
  const page = url.search.get("page") ?? "inbox";
  const generate = async (entries: Entry[], mode: "single" | "batch") => {
    setEstimate(null);
    setEstimateError("");
    setIsolation(false);
    setEstimating(true);
    try {
      setEstimate(
        await request<Estimate>("/api/jobs/estimate", {
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
        await request("/api/prs/refresh", { clone: clone.id });
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
        <LocalLink href="/">Inbox</LocalLink>
        <LocalLink href="/?page=jobs">Jobs</LocalLink>
        <LocalLink href="/?page=settings">Repositories & settings</LocalLink>
        <span className="text-xs text-(--text-tertiary)">On this machine</span>
      </header>
      <main className="local-main">
        {error ? (
          <Alert>
            {error} <Button onClick={() => void reload()}>Retry</Button>
          </Alert>
        ) : null}
        {page === "settings" ? (
          <Settings clones={clones} changed={reload} />
        ) : page === "jobs" ? (
          <Jobs jobs={jobs} changed={reload} />
        ) : selectedEntry ? (
          <LocalReview
            key={rowKey(selectedEntry)}
            entry={selectedEntry}
            clone={cloneFor(selectedEntry)}
            revision={revision}
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
                disabled={selection.size === 0 || estimating}
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
              />
            )}
          </>
        )}
        {jobs.some((j) => j.state === "running" || j.state === "queued") && page !== "jobs" ? (
          <div className="mt-4">
            <Jobs
              jobs={jobs.filter((j) => j.state === "running" || j.state === "queued")}
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
            confirm={async () => {
              try {
                await request("/api/jobs", {
                  estimate_id: estimate.id,
                  confirm_estimate: true,
                  confirm_isolation: isolation,
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
  confirm: () => Promise<void>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [pending, setPending] = useState(false);
  const needed = estimate.items.some((i) => i.requires_isolation_confirmation && !i.blocked_reason);
  const allowed = estimate.items.some((i) => !i.blocked_reason);
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
        Estimated cost: <strong>{cost(estimate.estimate_usd)}</strong>. This is an estimate, not a
        spending limit.
      </p>
      <p className="text-xs">
        Prices: {estimate.price_version}. Expires{" "}
        {new Date(estimate.expires_at).toLocaleTimeString()}.
      </p>
      <ul className="my-3 space-y-2">
        {estimate.items.map((item) => (
          <li key={`${item.clone}:${item.pr}`}>
            #{item.pr}: {item.backend} / {item.model || "No model configured"} —{" "}
            {cost(item.estimate_usd)}
            {item.blocked_reason ? (
              <p role="alert">
                Excluded: {item.blocked_reason}.{" "}
                {item.blocked_reason.includes("model")
                  ? "Choose a priced model in repository settings."
                  : ""}
              </p>
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
      <div className="flex gap-3">
        <Button
          disabled={pending || !allowed || (needed && !isolation)}
          onClick={() => {
            setPending(true);
            void confirm().finally(() => setPending(false));
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
            <p>
              Calls {job.calls} · Tokens {job.tokens} · Repairs {job.repairs} · Recorded cost{" "}
              {cost(job.actual_cost_usd)}
            </p>
            {job.reason ? <p role="status">{job.reason}</p> : null}
            {job.state === "queued" || job.state === "running" ? (
              <Button
                variant="secondary"
                onClick={() => {
                  void request(`/api/jobs/${encodeURIComponent(job.id)}/cancel`, {})
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
function Settings({ clones, changed }: { clones: Clone[]; changed: () => Promise<void> }) {
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
      <form
        className="flex flex-wrap gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void action(() => request("/api/repos", { path })).then(() => setPath(""));
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
          <p>
            {clone.path} — {clone.available ? "Available" : "Unavailable; history retained"}
          </p>
          <RepositorySettings
            key={JSON.stringify(clone.settings)}
            clone={clone}
            save={(settings) =>
              action(() => request(`/api/repos/${clone.id}/settings`, settings, "PUT"))
            }
          />
          <Button
            variant="secondary"
            onClick={() => void action(() => request(`/api/repos/${clone.id}`, {}, "DELETE"))}
          >
            Remove registration
          </Button>
        </div>
      ))}
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
function RepositorySettings({
  clone,
  save,
}: {
  clone: Clone;
  save: (settings: Clone["settings"]) => Promise<void>;
}) {
  const [automatic, setAutomatic] = useState(clone.settings.automatic);
  const [cap, setCap] = useState(String(clone.settings.daily_cap ?? ""));
  const [backend, setBackend] = useState(clone.settings.backend ?? "");
  const [model, setModel] = useState(clone.settings.model ?? "");
  const [pending, setPending] = useState(false);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    void save({
      automatic,
      daily_cap: cap === "" ? null : Number(cap),
      backend: backend || null,
      model: model || null,
    }).finally(() => setPending(false));
  };
  return (
    <form onSubmit={submit} className="space-y-3">
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
        <Field label="Daily automatic cap (USD)">
          <input
            className="local-field"
            type="number"
            min="0"
            step="any"
            value={cap}
            onChange={(e) => setCap(e.target.value)}
            placeholder="Unset"
          />
        </Field>
      </div>
      <label className="block">
        <input
          type="checkbox"
          checked={automatic}
          onChange={(e) => setAutomatic(e.target.checked)}
        />{" "}
        Opt in to automatic generation
      </label>
      <p className="text-xs">
        Automatic generation requires the background scheduler. Unset cap is not zero; a zero cap
        prevents automatic spending.
      </p>
      <Button type="submit" disabled={pending}>
        {pending ? "Saving…" : "Save repository settings"}
      </Button>
    </form>
  );
}
