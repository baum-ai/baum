import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import {
  automaticNotice,
  request,
  type Automatic,
  type AutomaticStatus,
  type Clone,
  type DiagnosticReport,
} from "./api";
import { DiagnosticsSection } from "./diagnostics";
import { invoke } from "./desktop/native";
import { Notice, PageBar } from "./shell";
import { HarnessPicker, ModelPicker, ReviewSettingsForm, harnessName } from "./desktop/reviews";
import {
  modelPresets,
  withModel,
  type PullRequests,
  type ReviewSettings,
} from "./desktop/setup-model";

/** The harness and model reviews use when a repository does not choose its own. */
export type ReviewDefaults = { backend: string; model: string };

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** "auto" has no tile: it shows as the harness it runs first. */
function shownBackend(backend: string) {
  return backend === "auto" || !backend ? "claude" : backend;
}

export function SettingsPage({
  automatic,
  defaults,
  changed,
  problems,
  diagnosticsOpen,
  setDiagnosticsOpen,
  onDiagnostics,
}: {
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  changed: () => Promise<void>;
  problems: number;
  diagnosticsOpen: boolean;
  setDiagnosticsOpen: (open: boolean) => void;
  onDiagnostics: (report: DiagnosticReport | null) => void;
}) {
  return (
    <section>
      <PageBar
        title="Settings"
        detail="How baum reviews pull requests on this computer"
        actions={
          <Button
            variant="secondary"
            size="compact"
            className="inline-flex items-center gap-2"
            onClick={() => setDiagnosticsOpen(true)}
          >
            Check this computer
            {problems > 0 ? (
              <span className="local-count-badge">
                {problems}
                <span className="sr-only"> {problems === 1 ? "problem" : "problems"}</span>
              </span>
            ) : null}
          </Button>
        }
      />
      {diagnosticsOpen ? (
        <DiagnosticsDialog close={() => setDiagnosticsOpen(false)} onReport={onDiagnostics} />
      ) : null}
      <div className="local-page-body">
        {automatic && defaults ? (
          <ReviewSettingsSection
            key={JSON.stringify([automatic.enabled, automatic.pull_requests, defaults])}
            automatic={automatic}
            defaults={defaults}
            changed={changed}
          />
        ) : (
          <p role="status">Reading your review settings…</p>
        )}
        <p className="text-xs text-(--text-tertiary)">
          The daemon keeps generating after you close this window. Quit the desktop app with “Quit
          and stop background reviews”, or run <code>baum daemon stop</code>, to stop it.
        </p>
      </div>
    </section>
  );
}

/** The global review settings, drawn as the desktop setup's last step. */
function ReviewSettingsSection({
  automatic,
  defaults,
  changed,
}: {
  automatic: Automatic;
  defaults: ReviewDefaults;
  changed: () => Promise<void>;
}) {
  const saved: ReviewSettings = withModel({
    backend: shownBackend(defaults.backend),
    model: defaults.backend === "auto" ? "" : defaults.model,
    automatic: automatic.enabled,
    pull_requests: (automatic.pull_requests ?? "both") as PullRequests,
  });
  const [draft, setDraft] = useState(saved);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const save = async () => {
    setPending(true);
    setError("");
    try {
      await request(
        "api/automatic",
        {
          enabled: draft.automatic,
          backend: draft.backend,
          model: draft.model,
          pull_requests: draft.pull_requests,
        },
        "PUT",
      );
      await changed();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <section aria-label="Reviews" className="local-stack">
      {error ? <Alert>{error}</Alert> : null}
      <ReviewSettingsForm
        settings={draft}
        disabled={pending}
        onChange={setDraft}
        className="local-panel"
      />
      <div className="local-inline-form">
        <Button
          size="compact"
          disabled={!dirty || pending || !draft.model.trim()}
          onClick={() => void save()}
        >
          {pending ? "Saving…" : "Save review settings"}
        </Button>
        {dirty ? (
          <Button variant="ghost" size="compact" onClick={() => setDraft(saved)}>
            Discard changes
          </Button>
        ) : null}
      </div>
    </section>
  );
}

/** What baum needs on this computer, in a dialog over the settings. */
function DiagnosticsDialog({
  close,
  onReport,
}: {
  close: () => void;
  onReport: (report: DiagnosticReport | null) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="diagnostics-dialog-title"
      className="local-dialog-wide"
    >
      <div className="local-dialog-head">
        <div>
          <h2 id="diagnostics-dialog-title">Check this computer</h2>
          <p>What baum needs here, what it found, and how to fix each problem.</p>
        </div>
        <Button variant="ghost" size="compact" onClick={close}>
          Close
        </Button>
      </div>
      <DiagnosticsSection onReport={onReport} />
    </dialog>
  );
}

export function RepositoriesPage({
  clones,
  automatic,
  defaults,
  changed,
}: {
  clones: Clone[];
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  changed: () => Promise<void>;
}) {
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
    <section>
      <PageBar
        title="Repositories"
        detail="Local clones baum reviews; settings here apply to every clone of a repository"
      />
      <div className="local-page-body">
        {error ? <Alert>{error}</Alert> : null}
        <AddRepository add={(path) => action(() => request("api/repos", { path }))} />
        {clones.length === 0 ? (
          <div className="local-empty">
            <h2>No repositories yet</h2>
            <p>Register a local clone of a GitHub repository to review its pull requests.</p>
          </div>
        ) : null}
        {clones.length > 0 ? (
          <ul className="local-panel local-repo-list">
            {clones.map((clone) => (
              <RepositoryRow
                key={clone.id}
                clone={clone}
                automatic={automatic}
                defaults={defaults}
                changed={changed}
                remove={() => action(() => request(`api/repos/${clone.id}`, {}, "DELETE"))}
              />
            ))}
          </ul>
        ) : null}
      </div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="local-field-label">
      {label}
      {children}
    </label>
  );
}

function RepositorySettings({
  clone,
  automatic,
  defaults,
  status,
  save,
  remove,
  cancel,
}: {
  clone: Clone;
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  status?: AutomaticStatus;
  save: (settings: Clone["settings"]) => Promise<void>;
  remove?: () => void;
  cancel?: () => void;
}) {
  const [backend, setBackend] = useState<string | null>(clone.settings.backend ?? null);
  const [model, setModel] = useState<string | null>(clone.settings.model ?? null);
  const [mode, setMode] = useState(
    clone.settings.automatic === null || clone.settings.automatic === undefined
      ? "default"
      : clone.settings.automatic
        ? "on"
        : "off",
  );
  const [pending, setPending] = useState(false);
  const globalBackend = shownBackend(defaults?.backend ?? "auto");
  const submit = (event: FormEvent) => {
    event.preventDefault();

    setPending(true);
    void save({
      automatic: mode === "default" ? null : mode === "on",
      // Spending caps are gone from the app; saving clears an old one.
      daily_cap: null,
      backend,
      model: model?.trim() ? model.trim() : null,
    }).finally(() => setPending(false));
  };
  const notice = status ? automaticNotice(status) : null;
  return (
    <form onSubmit={submit} className="local-stack" noValidate>
      {automatic ? (
        <div className="local-fields">
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
        </div>
      ) : null}

      {status ? (
        <p className="text-xs text-(--text-tertiary)" role="status">
          {status.enabled
            ? "Automatic reviews are on for this repository."
            : clone.settings.automatic === false
              ? "Automatic reviews are off for this repository."
              : `Automatic reviews are paused by the global switch${
                  clone.settings.automatic ? ", although this repository is set to On" : ""
                }. Turn them on in Settings to resume them.`}

          {status.pending ? ` ${status.pending} waiting to start.` : ""}
        </p>
      ) : null}
      {notice ? <Notice>{notice}</Notice> : null}
      <div className="review-card repo-review">
        <HarnessPicker
          legend="Harness for this repository"
          value={backend}
          disabled={pending}
          inherit={{ label: "Global setting", detail: harnessName(globalBackend) }}
          onChange={(next) => {
            setBackend(next);
            setModel(next === null ? null : (modelPresets[next]?.[0] ?? null));
          }}
        />
        <ModelPicker
          key={backend ?? ""}
          id={`model-${clone.id}`}
          label="Model for this repository"
          backend={backend ?? globalBackend}
          value={model}
          disabled={pending}
          inherit={`Global setting${defaults?.model ? ` (${defaults.model})` : ""}`}
          onChange={setModel}
        />
      </div>
      <div className="local-dialog-actions">
        {remove ? (
          <Button variant="danger-outline" size="compact" onClick={remove} className="mr-auto">
            Remove registration
          </Button>
        ) : null}
        {cancel ? (
          <Button variant="ghost" size="compact" onClick={cancel}>
            Cancel
          </Button>
        ) : null}
        <Button type="submit" size="compact" disabled={pending}>
          {pending ? "Saving…" : "Save repository settings"}
        </Button>
      </div>
    </form>
  );
}

/** One registered repository: what it does at a glance, and its settings in a
 *  dialog. */
function RepositoryRow({
  clone,
  automatic,
  defaults,
  changed,
  remove,
}: {
  clone: Clone;
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  changed: () => Promise<void>;
  remove: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const status = automatic?.repositories.find(
    (item) => item.repository === clone.repository.toLowerCase(),
  );
  const harness = clone.settings.backend
    ? `${harnessName(clone.settings.backend)}${clone.settings.model ? ` · ${clone.settings.model}` : ""}`
    : clone.settings.model
      ? clone.settings.model
      : "Global harness and model";
  const summary = [
    status ? (status.enabled ? "Automatic reviews on" : "Automatic reviews off") : null,
    harness,
  ].filter(Boolean);
  return (
    <li className="local-repo-row">
      <div className="min-w-0 flex-1">
        <h3>{clone.repository}</h3>
        <p className="truncate" title={clone.path}>
          {clone.path}
        </p>
      </div>
      <p className="local-repo-summary">{summary.join(" · ")}</p>
      {clone.available ? null : <span className="local-pill">Unavailable</span>}
      <Button
        variant="secondary"
        size="compact"
        aria-label={`Settings for ${clone.repository}`}
        onClick={() => setOpen(true)}
      >
        Settings
      </Button>
      {open ? (
        <RepositoryDialog
          clone={clone}
          automatic={automatic}
          defaults={defaults}
          status={status}
          changed={changed}
          remove={remove}
          close={() => setOpen(false)}
        />
      ) : null}
    </li>
  );
}

function RepositoryDialog({
  clone,
  automatic,
  defaults,
  status,
  changed,
  remove,
  close,
}: {
  clone: Clone;
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  status?: AutomaticStatus;
  changed: () => Promise<void>;
  remove: () => Promise<void>;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-label={`Repository ${clone.repository}`}
      className="local-dialog-wide"
    >
      <div className="local-dialog-head">
        <div className="min-w-0">
          <h2>{clone.repository}</h2>
          <p className="[overflow-wrap:anywhere]">
            {clone.path}
            {clone.available ? "" : " · unavailable; history retained"}
          </p>
        </div>
      </div>
      {error ? <Alert>{error}</Alert> : null}
      <RepositorySettings
        clone={clone}
        automatic={automatic}
        defaults={defaults}
        status={status}
        cancel={close}
        remove={() => void remove().then(close)}
        save={async (settings) => {
          try {
            await request(`api/repos/${clone.id}/settings`, settings, "PUT");

            await changed();
            close();
          } catch (e) {
            setError(errorText(e));
          }
        }}
      />
    </dialog>
  );
}

type PathCheck = { valid: boolean; repository?: string; registered?: boolean; reason?: string };

/** A path and one button: Browse opens the desktop app's folder picker until
 *  the path is a clone the daemon would register, then the button adds it. */
function AddRepository({ add }: { add: (path: string) => Promise<void> }) {
  const [path, setPath] = useState("");
  const [check, setCheck] = useState<PathCheck | null>(null);
  const [pending, setPending] = useState(false);
  const picker = hasFolderPicker();
  useEffect(() => {
    const trimmed = path.trim();
    setCheck(null);
    if (!trimmed) return;
    let active = true;
    // Typing settles before the daemon looks at the folder.
    const timer = setTimeout(() => {
      request<PathCheck>(`api/repos/check?path=${encodeURIComponent(trimmed)}`).then(
        (answer) => {
          if (active) setCheck(answer);
        },
        () => {
          if (active) setCheck(null);
        },
      );
    }, 250);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [path]);
  const addable = check?.valid && !check.registered;
  const browse = async () => {
    const chosen = await invoke<string | null>("choose_repository").catch(() => null);
    if (chosen) setPath(chosen);
  };
  return (
    <form
      className="local-add-repo"
      onSubmit={(event) => {
        event.preventDefault();
        if (!addable) return;
        setPending(true);
        void add(path.trim())
          .then(() => setPath(""))
          .finally(() => setPending(false));
      }}
    >
      <div className="local-inline-form">
        <label className="local-grow">
          <span className="sr-only">Local clone path</span>
          <input
            className="local-field w-full"
            placeholder="/path/to/a/local/clone"
            value={path}
            onChange={(event) => setPath(event.target.value)}
          />
        </label>
        {addable ? (
          <Button type="submit" size="compact" disabled={pending}>
            {pending ? "Adding…" : "Add repo"}
          </Button>
        ) : picker ? (
          <Button type="button" variant="secondary" size="compact" onClick={() => void browse()}>
            Browse…
          </Button>
        ) : (
          <Button type="submit" size="compact" disabled>
            Add repo
          </Button>
        )}
      </div>
      {path.trim() && check ? (
        <p className={check.valid && !check.registered ? "local-add-hint ok" : "local-add-hint"}>
          {check.registered
            ? `${check.repository} is already added.`
            : check.valid
              ? `GitHub repository ${check.repository}`
              : check.reason
                ? `${check.reason.charAt(0).toUpperCase()}${check.reason.slice(1)}.`
                : null}
        </p>
      ) : null}
    </form>
  );
}

/** Whether the page runs in the desktop app, which lends it the folder picker. */
function hasFolderPicker() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}
