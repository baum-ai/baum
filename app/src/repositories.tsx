import { useCallback, useEffect, useRef, useState } from "react";
import { request, type Repository } from "./api";
import { invoke } from "./desktop/native";
import { displayPath, repositoryPath } from "./desktop/setup-model";
import "./repositories.css";

export type RepositoryItem = {
  repository: string;
  description: string;
  picked: boolean;
  clone: string;
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
const skippedFound = new Set<string>();
const skippedKey = "baum-unpicked-repositories";
export function skipFoundRepository(repository: string) {
  skippedFound.add(repository);
  try {
    localStorage.setItem(skippedKey, JSON.stringify([...skippedFound]));
  } catch {
    /* Private storage can be unavailable. */
  }
}
export function allowFoundRepository(repository: string) {
  skippedFound.delete(repository);
  try {
    localStorage.setItem(skippedKey, JSON.stringify([...skippedFound]));
  } catch {
    /* Private storage can be unavailable. */
  }
}
function restoreSkipped() {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(skippedKey) ?? "[]");
    if (Array.isArray(saved))
      for (const name of saved) if (typeof name === "string") skippedFound.add(name);
  } catch {
    /* Ignore an unreadable local preference. */
  }
}
const message = (error: unknown) => (error instanceof Error ? error.message : String(error));
export function hasFolderPicker() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}

/** One set of local actions, shared by setup, the repository picker and a PR. */
export function CloneActions({
  repository,
  call = repositoryCall,
  home,
  changed,
  skip,
}: {
  repository: string;
  call?: RepositoryCall;
  home?: string;
  changed: () => Promise<void>;
  skip?: () => void;
}) {
  const [mode, setMode] = useState<"" | "folder" | "clone">("");
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
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
  const run = async (action: () => Promise<void>) => {
    if (working.current) return;
    working.current = true;
    setBusy(true);
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
  return (
    <div className="repository-actions">
      {state?.state === "running" ? (
        <p role="status">Cloning to {displayPath(state.dir, home)}…</p>
      ) : (
        <div className="repository-buttons">
          <button
            type="button"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                if (hasFolderPicker()) {
                  const selected = await invoke<string | null>("choose_repository");
                  if (selected) await attach(selected);
                } else setMode("folder");
              })
            }
          >
            Choose folder
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                const target = await call<{ dir: string }>("target", { repository });
                if (active.current) {
                  setPath(target.dir);
                  setMode("clone");
                }
              })
            }
          >
            Clone it
          </button>
          {skip ? (
            <button type="button" disabled={busy} onClick={skip}>
              Skip
            </button>
          ) : null}
        </div>
      )}
      {mode ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const full = repositoryPath(path.trim(), home ?? "");
            if (!full) {
              setError("Enter an absolute folder path.");
              return;
            }
            void run(async () => {
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
          <label>
            {mode === "clone" ? "Clone into" : "Local clone path"}
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              disabled={busy}
              required
            />
          </label>
          <div className="repository-buttons">
            <button type="submit" disabled={busy}>
              {busy ? "Working…" : mode === "clone" ? "Clone repository" : "Use folder"}
            </button>
            <button type="button" disabled={busy} onClick={() => setMode("")}>
              Cancel
            </button>
          </div>
        </form>
      ) : null}
      {state?.state === "failed" ? (
        <p role="alert">{state.error ?? "The clone failed. Try again."}</p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </div>
  );
}

export function RepositoryPicker({
  call = repositoryCall,
  home,
  onPicked,
  changed,
  connected = true,
}: {
  call?: RepositoryCall;
  home?: string;
  onPicked?: (count: number) => void;
  changed?: () => Promise<void>;
  connected?: boolean;
}) {
  const [list, setList] = useState<RepositoryItem[]>([]);
  const [found, setFound] = useState<FoundClones | null>(null);
  const [query, setQuery] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [fallback, setFallback] = useState("");
  const skipped = useRef(skippedFound);
  restoreSkipped();
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
      // Found clones are preselected unless the person deliberately unpicked them.
      for (const row of listing.repositories) {
        if (!active.current || seq !== sequence.current || working.current) break;
        if (row.clone && !picked.has(row.repository) && !skipped.current.has(row.repository)) {
          working.current = true;
          setBusy(row.repository);
          try {
            await call("pick", { repository: row.repository });
            picked.add(row.repository);
          } finally {
            working.current = false;
            if (active.current) setBusy(null);
          }
        }
      }
      if (active.current && seq === sequence.current) {
        setList(listing.repositories.map((r) => ({ ...r, picked: picked.has(r.repository) })));
        pickedCallback.current?.(picked.size);
      }
    } catch (e) {
      if (active.current && seq === sequence.current) setError(message(e));
    } finally {
      if (active.current && seq === sequence.current) setLoading(false);
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
  const rows = list.filter((r) => r.repository.toLowerCase().includes(query.trim().toLowerCase()));
  const mac = /Mac/i.test(navigator.platform);
  return (
    <section className="repository-picker" aria-label="Choose GitHub repositories">
      <p className="repository-help">
        baum looks for clones in folders your agents have worked in and in common code folders.
        Nothing leaves this computer.
      </p>
      {connected ? (
        <label className="repository-search">
          Search repositories
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Find a GitHub repository"
          />
        </label>
      ) : (
        <p>Connect GitHub to list your repositories, or choose a local folder below.</p>
      )}
      {loading ? <p role="status">Finding your repositories…</p> : null}
      {error ? (
        <div role="alert">
          <p>{error}</p>
          <button type="button" onClick={() => void reload()}>
            Try again
          </button>
        </div>
      ) : null}
      {!loading && connected && !error && rows.length === 0 ? <p>No repositories match.</p> : null}
      <ul className="repository-choices">
        {rows.map((row) => (
          <li key={row.repository}>
            <label className="repository-choice">
              <input
                type="checkbox"
                checked={row.picked}
                disabled={busy !== null}
                aria-label={`Pick ${row.repository}`}
                onChange={(e) => {
                  const pick = e.target.checked;
                  if (pick) allowFoundRepository(row.repository);
                  else skipFoundRepository(row.repository);
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
              <span>
                <strong>{row.repository}</strong>
                {row.private ? <small>Private</small> : null}
                {row.description ? (
                  <span className="repository-description">{row.description}</span>
                ) : null}
              </span>
            </label>
            <p className="repository-clone">
              {row.clone
                ? `✓ ${displayPath(row.clone, home)}`
                : found?.running
                  ? "Finding a clone…"
                  : "No clone found"}
            </p>
            {row.picked && !row.clone ? (
              <CloneActions
                repository={row.repository}
                call={call}
                home={home}
                changed={update}
                skip={() => {
                  skipFoundRepository(row.repository);
                  void action(row.repository, () => call("unpick", { repository: row.repository }));
                }}
              />
            ) : null}
          </li>
        ))}
      </ul>
      {mac && list.some((r) => r.picked && !r.clone) ? (
        <button
          type="button"
          disabled={busy !== null || found?.running}
          onClick={() =>
            void action("documents", () => call("refresh", { include_documents: true }))
          }
        >
          Also look in Documents
        </button>
      ) : null}
      <details className="repository-fallback" open={!connected}>
        <summary>Choose a local folder instead</summary>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const path = repositoryPath(fallback.trim(), home ?? "");
            if (path)
              void action("folder", async () => {
                await call("attach", { path });
                setFallback("");
              });
          }}
        >
          <label>
            Repository folder
            <input
              placeholder="/path/to/a/local/clone"
              value={fallback}
              onChange={(e) => setFallback(e.target.value)}
            />
          </label>
          <div className="repository-buttons">
            {hasFolderPicker() ? (
              <button
                type="button"
                disabled={busy !== null}
                onClick={() =>
                  void action("folder", async () => {
                    const path = await invoke<string | null>("choose_repository");
                    if (path) await call("attach", { path });
                  })
                }
              >
                Choose folder
              </button>
            ) : null}
            <button type="submit" disabled={busy !== null || !fallback.trim()}>
              Add folder
            </button>
          </div>
        </form>
      </details>
    </section>
  );
}
