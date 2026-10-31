import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { request, type Repository } from "./api";
import { invoke } from "./desktop/native";
import { displayPath, repositoryPath } from "./desktop/setup-model";
import { LoadingStatus } from "./nine-dots";
import "./repositories.css";

export type RepositoryItem = {
  repository: string;
  description: string;
  picked: boolean;
  clone: string;
  earlier_names?: string[];
  private?: boolean;
  open_pull_requests?: number;
};
export type FoundClones = {
  running: boolean;
  scanned_at: string | null;
  repositories: Record<string, { path: string }[]>;
  attached: { repository: string; path: string; at: string }[];
};
type CloneState = {
  state: "running" | "done" | "failed";
  error?: string;
  code?: string;
  dir: string;
};
export type RepositoryAction =
  | "list"
  | "picked"
  | "pick"
  | "unpick"
  | "found"
  | "refresh"
  | "target"
  | "clone"
  | "status"
  | "attach";
export type RepositoryCall = <T>(
  action: RepositoryAction,
  args?: {
    repository?: string;
    query?: string;
    path?: string;
    dir?: string;
    include_documents?: boolean;
  },
) => Promise<T>;
export const repositoryCall: RepositoryCall = (action, args = {}) => {
  const repository = args.repository?.split("/").map(encodeURIComponent).join("/") ?? "";
  const base = `api/repositories/${repository}`;
  switch (action) {
    case "list":
      return request(`api/github/repositories?query=${encodeURIComponent(args.query ?? "")}`);
    case "picked":
      return request("api/repositories");
    case "pick":
      return request("api/repositories", { repository: args.repository });
    case "unpick":
      return request(base, {}, "DELETE");
    case "found":
      return request("api/clones/found");
    case "refresh":
      return request("api/clones/found/refresh", {
        include_documents: args.include_documents ?? false,
      });
    case "target":
      return request(`${base}/clone-target`);
    case "clone":
      return request(`${base}/clone`, args.dir ? { dir: args.dir } : {});
    case "status":
      return request(`${base}/clone`);
    case "attach":
      return request("api/repos", {
        path: args.path,
        ...(args.repository ? { repository: args.repository } : {}),
      });
  }
};
export const setupRepositoryCall: RepositoryCall = (action, args = {}) =>
  invoke("repositories", { action, ...args });
const shownRepositories = 8;
/** The clone's own folder inside the folder the person picked, unless they
 *  picked a folder already named after the repository. */
export function cloneDirectory(parent: string, repository: string) {
  const name = repository.split("/").pop() ?? repository;
  const trimmed = parent.replace(/[\\/]+$/, "");
  if (trimmed.split(/[\\/]/).pop() === name) return trimmed;
  const separator = trimmed.includes("\\") && !trimmed.includes("/") ? "\\" : "/";
  return `${trimmed}${separator}${name}`;
}
/** Said for a repository a clone was registered for with a remote using an
 *  earlier name. */
export const earlierNamesWarning = (names: string[], repository: string) =>
  `GitHub forwards ${names.join(" and ")} to ${repository}; clones whose remote still uses that earlier name count as its clones.`;
const message = (error: unknown) => (error instanceof Error ? error.message : String(error));
export function hasFolderPicker() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}

/** What a repository without a local clone can do about it: `choose` a
 *  folder that holds one (the native picker, or a path form where there is
 *  none) and `clone` it (a form with the folder to clone into). `progress`
 *  reports a running clone, `form` is the open path form and `errors` what
 *  went wrong; a list row draws them under itself and offers the two from
 *  its menu, while `CloneActions` draws them as buttons. */
export function useCloneActions({
  repository,
  call = repositoryCall,
  home,
  changed,
}: {
  repository: string;
  call?: RepositoryCall;
  home?: string;
  changed: () => Promise<void>;
}) {
  const [mode, setMode] = useState<"" | "folder" | "clone">("");
  const fieldId = useId();
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  // Which button started the running action, so only it changes its label.
  const [doing, setDoing] = useState("");
  const [error, setError] = useState("");
  const [state, setState] = useState<CloneState | null>(null);
  const active = useRef(true);
  const working = useRef(false);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const run = async (name: string, action: () => Promise<void>) => {
    if (working.current) return;
    working.current = true;
    setBusy(true);
    setDoing(name);
    setError("");
    try {
      await action();
    } catch (e) {
      if (active.current) setError(message(e));
    } finally {
      working.current = false;
      if (active.current) setBusy(false);
    }
  };
  useEffect(() => {
    let alive = true;
    void call<CloneState>("status", { repository }).then(
      (s) => {
        if (alive) setState(s);
      },
      () => undefined,
    );
    return () => {
      alive = false;
    };
  }, [call, repository]);
  useEffect(() => {
    if (state?.state !== "running") return;
    let alive = true;
    const timer = setInterval(() => {
      void call<CloneState>("status", { repository }).then(
        async (next) => {
          if (!alive) return;
          setState(next);
          if (next.state === "done") await changed();
        },
        (e) => {
          if (alive) setError(message(e));
        },
      );
    }, 1000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [state?.state, call, repository, changed]);
  const attach = async (folder: string) => {
    await call("attach", { repository, path: folder });
    await changed();
    if (active.current) {
      setMode("");
      setPath("");
    }
  };
  const choose = () =>
    void run("choose", async () => {
      if (hasFolderPicker()) {
        const selected = await invoke<string | null>("choose_repository");
        if (selected) await attach(selected);
      } else setMode("folder");
    });
  const clone = () =>
    void run("clone", async () => {
      const target = await call<{ dir: string }>("target", { repository });
      if (active.current) {
        // Shown with ~ for the home directory; submitting expands it.
        setPath(displayPath(target.dir, home));
        setMode("clone");
      }
    });
  const running = state?.state === "running";
  const progress =
    state?.state === "running" ? (
      <LoadingStatus>Cloning to {displayPath(state.dir, home)}…</LoadingStatus>
    ) : null;
  const form = mode ? (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const full = repositoryPath(path.trim(), home ?? "");
        if (!full) {
          setError("Enter an absolute folder path.");
          return;
        }
        void run("submit", async () => {
          if (mode === "folder") await attach(full);
          else {
            const next = await call<CloneState>("clone", { repository, dir: full });
            if (active.current) {
              setState(next);
              setMode("");
            }
            await changed();
          }
        });
      }}
    >
      <div className="repository-path">
        <label htmlFor={fieldId}>{mode === "clone" ? "Clone into" : "Local clone path"}</label>
        <span className="repository-path-field">
          <input
            id={fieldId}
            value={path}
            onChange={(e) => setPath(e.target.value)}
            disabled={busy}
            required
          />
          {mode === "clone" && hasFolderPicker() ? (
            <button
              type="button"
              disabled={busy}
              aria-label="Choose a folder to clone into"
              title="Choose a folder"
              onClick={() =>
                void run("parent", async () => {
                  const parent = await invoke<string | null>("choose_repository", {
                    cloneTarget: true,
                  });
                  if (parent && active.current)
                    setPath(displayPath(cloneDirectory(parent, repository), home));
                })
              }
            >
              <svg viewBox="0 0 24 24" aria-hidden="true">
                <path d="M3.5 7.5a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2v7.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z" />
              </svg>
            </button>
          ) : null}
        </span>
      </div>
      <div className="repository-buttons">
        <button type="submit" disabled={busy}>
          {busy ? "Working…" : mode === "clone" ? "Clone repository" : "Use folder"}
        </button>
        <button type="button" disabled={busy} onClick={() => setMode("")}>
          Cancel
        </button>
      </div>
    </form>
  ) : null;
  const errors =
    state?.state === "failed" || error ? (
      <>
        {state?.state === "failed" ? (
          <p role="alert">{state.error ?? "The clone failed. Try again."}</p>
        ) : null}
        {error ? <p role="alert">{error}</p> : null}
      </>
    ) : null;
  return { busy, doing, mode, running, choose, clone, progress, form, errors };
}

/** One set of local actions, shared by setup, the repository picker and a PR.
 *  With a status they shrink to one line of text actions after it. While
 *  `looking` (baum is searching for a clone) they wait, unless a clone this
 *  set started is running: that one is reported instead. */
export function CloneActions({
  repository,
  call = repositoryCall,
  home,
  changed,
  status,
  looking = false,
}: {
  repository: string;
  call?: RepositoryCall;
  home?: string;
  changed: () => Promise<void>;
  status?: ReactNode;
  looking?: boolean;
}) {
  const actions = useCloneActions({ repository, call, home, changed });
  const { busy, doing } = actions;
  return (
    <div className="repository-actions">
      {actions.running ? (
        actions.progress
      ) : looking ? (
        <LoadingStatus>Looking for a local clone…</LoadingStatus>
      ) : status && actions.mode ? null : (
        // The one-line form gives way to an open path form, which has Cancel.
        <div className={status ? "repository-buttons repository-inline" : "repository-buttons"}>
          {status ? <span className="repository-inline-status">{status}</span> : null}
          <button type="button" disabled={busy} onClick={actions.choose}>
            {busy && doing === "choose" ? "Choosing…" : "Choose folder"}
          </button>
          <button type="button" disabled={busy} onClick={actions.clone}>
            {busy && doing === "clone" ? "Preparing…" : "Clone it"}
          </button>
        </div>
      )}
      {actions.form}
      {actions.errors}
    </div>
  );
}

export function RepositoryPicker({
  call = repositoryCall,
  home,
  onPicked,
  changed,
  connected = true,
  autoFocus = false,
}: {
  call?: RepositoryCall;
  home?: string;
  onPicked?: (count: number) => void;
  changed?: () => Promise<void>;
  connected?: boolean;
  /** Whether the search takes the focus when it shows. */
  autoFocus?: boolean;
}) {
  const [list, setList] = useState<RepositoryItem[]>([]);
  const [found, setFound] = useState<FoundClones | null>(null);
  const [query, setQuery] = useState("");
  const [error, setError] = useState("");
  // The connection the shown list was loaded for: until a load for the
  // current one finishes, the list is still loading, not empty.
  const [listed, setListed] = useState<boolean | null>(null);
  const loading = listed !== connected;
  const [busy, setBusy] = useState<string | null>(null);
  const [retrying, setRetrying] = useState(false);
  const active = useRef(true);
  const sequence = useRef(0);
  const working = useRef(false);
  const pickedCallback = useRef(onPicked);
  pickedCallback.current = onPicked;
  const changedCallback = useRef(changed);
  changedCallback.current = changed;
  const reload = useCallback(async () => {
    const seq = ++sequence.current;
    try {
      const [listing, registry, result] = await Promise.all([
        connected
          ? call<{ repositories: RepositoryItem[] }>("list")
          : Promise.resolve({ repositories: [] }),
        call<Repository[]>("picked"),
        call<FoundClones>("found").catch(() => null),
      ]);
      if (!active.current || seq !== sequence.current) return;
      if (!Array.isArray(registry) || !Array.isArray(listing.repositories))
        throw Error("Repository listing is unavailable. Connect GitHub in Settings and try again.");
      const picked = new Set(registry.map((r) => r.repository));
      setList(listing.repositories.map((r) => ({ ...r, picked: picked.has(r.repository) })));
      setFound(result && result.repositories ? result : null);
      pickedCallback.current?.(picked.size);
      setError("");
    } catch (e) {
      if (active.current && seq === sequence.current) setError(message(e));
    } finally {
      if (active.current && seq === sequence.current) setListed(connected);
    }
  }, [call, connected]);
  useEffect(() => {
    active.current = true;
    void reload();
    const timer = setInterval(() => {
      if (!working.current) void reload();
    }, 4000);
    return () => {
      active.current = false;
      sequence.current++;
      clearInterval(timer);
    };
  }, [reload]);
  // While the daemon looks for a picked repository's clone, check back sooner.
  const looking = Boolean(found?.running);
  useEffect(() => {
    if (!looking) return;
    const timer = setInterval(() => {
      if (!working.current) void reload();
    }, 1000);
    return () => clearInterval(timer);
  }, [looking, reload]);
  const update = useCallback(async () => {
    await reload();
    await changedCallback.current?.();
  }, [reload]);
  const action = async (name: string, fn: () => Promise<unknown>) => {
    if (working.current) return;
    sequence.current++;
    working.current = true;
    setBusy(name);
    setError("");
    try {
      await fn();
      await update();
    } catch (e) {
      if (active.current) setError(message(e));
    } finally {
      working.current = false;
      if (active.current) setBusy(null);
    }
  };
  const needle = query.trim().toLowerCase();
  // Exact names come first, then names that start with the search, so typing a
  // repository's name always reaches it however many others contain it.
  const rank = (repository: string) => {
    const full = repository.toLowerCase();
    const name = full.slice(full.indexOf("/") + 1);
    if (full === needle || name === needle) return 0;
    return full.startsWith(needle) || name.startsWith(needle) ? 1 : 2;
  };
  const matches = list
    .filter((r) => r.repository.toLowerCase().includes(needle))
    .sort((a, b) => (needle ? rank(a.repository) - rank(b.repository) : 0));
  // A short list keeps the step readable; search reaches the rest, and picked
  // repositories always stay visible.
  const rows = matches.filter((r, i) => i < shownRepositories || r.picked);
  const hidden = matches.length - rows.length;
  // Results take their own height without a search, and keep it while one
  // runs, so typing never moves the page.
  const results = useRef<HTMLDivElement>(null);
  const [restHeight, setRestHeight] = useState<number>();
  useLayoutEffect(() => {
    const height = results.current?.offsetHeight;
    if (!needle && !loading && height && height !== restHeight) setRestHeight(height);
  });
  const mac = /Mac/i.test(navigator.platform);
  return (
    <section className="repository-picker" aria-label="Choose GitHub repositories">
      {connected ? (
        <label className="repository-search">
          Search repositories
          <span className="repository-search-field">
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <circle cx="11" cy="11" r="6.5" />
              <path d="M16 16l4.5 4.5" />
            </svg>
            <input
              type="search"
              autoFocus={autoFocus}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                // Escape clears a search first, then leaves the field, so the
                // page's own keys work again.
                if (e.key === "Escape" && !query) e.currentTarget.blur();
              }}
              placeholder="Find a GitHub repository"
            />
          </span>
        </label>
      ) : (
        <p>Connect GitHub to list your repositories.</p>
      )}
      {error ? (
        <div role="alert">
          <p>{error}</p>
          <button
            type="button"
            disabled={retrying}
            onClick={() => {
              setRetrying(true);
              void reload().finally(() => {
                if (active.current) setRetrying(false);
              });
            }}
          >
            {retrying ? "Trying…" : "Try again"}
          </button>
        </div>
      ) : null}
      {connected || rows.length > 0 ? (
        // Searching keeps the box's height, so the page never moves; it
        // scrolls only when picked repositories run past the rows it shows.
        <div
          ref={results}
          className="repository-results"
          style={needle && restHeight ? { height: restHeight } : undefined}
        >
          {loading ? (
            <LoadingStatus
              block
              detail="baum is listing the repositories your GitHub account can access."
            >
              Loading your GitHub repositories…
            </LoadingStatus>
          ) : null}
          {!loading && connected && !error && rows.length === 0 ? (
            <p className="repository-empty">No repositories match.</p>
          ) : null}
          <ul className="repository-choices">
            {rows.map((row) => (
              <li key={row.repository} className={row.picked ? "picked" : undefined}>
                <label className="repository-choice">
                  <input
                    type="checkbox"
                    checked={row.picked}
                    disabled={busy !== null}
                    aria-label={`Pick ${row.repository}`}
                    onChange={(e) => {
                      const pick = e.target.checked;
                      setList((previous) =>
                        previous.map((r) =>
                          r.repository === row.repository ? { ...r, picked: pick } : r,
                        ),
                      );
                      void action(row.repository, () =>
                        call(pick ? "pick" : "unpick", { repository: row.repository }),
                      );
                    }}
                  />
                  <span className="repository-text">
                    <span className="repository-name">
                      <strong>{row.repository}</strong>
                      {row.private ? <small>Private</small> : null}
                    </span>
                    {row.description ? (
                      <span className="repository-description" title={row.description}>
                        {row.description}
                      </span>
                    ) : null}
                    {row.picked && row.clone ? (
                      <span className="repository-clone found" title={row.clone}>
                        Found at {displayPath(row.clone, home)}
                      </span>
                    ) : null}
                    {row.picked && row.clone && row.earlier_names?.length ? (
                      <span className="repository-warning">
                        {earlierNamesWarning(row.earlier_names, row.repository)}
                      </span>
                    ) : null}
                  </span>
                </label>
                {row.picked && !row.clone ? (
                  <div className="repository-missing">
                    <CloneActions
                      repository={row.repository}
                      call={call}
                      home={home}
                      changed={update}
                      status="No local clone found"
                      looking={Boolean(found?.running)}
                    />
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
          {hidden > 0 ? (
            <p className="repository-more">
              {needle
                ? `${hidden} more ${hidden === 1 ? "match" : "matches"}. Keep typing to narrow the list.`
                : `${hidden} more ${hidden === 1 ? "repository" : "repositories"}. Search to find one.`}
            </p>
          ) : null}
        </div>
      ) : null}
      {mac && list.some((r) => r.picked && !r.clone) ? (
        <button
          type="button"
          disabled={busy !== null || found?.running}
          onClick={() =>
            void action("documents", () => call("refresh", { include_documents: true }))
          }
        >
          {busy === "documents" ? "Looking…" : "Also look in Documents"}
        </button>
      ) : null}
    </section>
  );
}
