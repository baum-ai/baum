import { usePreferences } from "./preferences";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FocusEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import { Alert, Button, SegmentedControl, Toggle } from "@baum/viewer/components/ui";
import {
  automaticNotice,
  parseAgentRequests,
  request,
  saveAgentSettings,
  type Automatic,
  type AutomaticStatus,
  type Clone,
  type GitHubConnection,
  type Repository,
  type DiagnosticReport,
  type DecisionSettings,
} from "./api";
import { RepositoryPicker, CloneActions } from "./repositories";
import { GitHubConnectionSection } from "./github-connection";
import { messages as githubMessages } from "./github-auth";
import { DiagnosticsSection } from "./diagnostics";
import { invoke } from "./desktop/native";
import { Notice, PageBar, PanelHeader } from "./shell";
import { listKeys, onBody, useRowFocus, useShortcuts } from "./shortcuts";
import codexLogo from "./desktop/codex.svg";
import claudeLogo from "./desktop/claude-code.svg";
import {
  TerminalIcon,
  SessionsIcon,
  LightbulbIcon,
  CheckIcon,
  SearchIcon,
  CloseIcon,
  PlusIcon,
} from "@baum/viewer/components/icons";
import {
  DecisionModelPicker,
  DecisionsDisclosure,
  HarnessPicker,
  ModelPicker,
  harnesses,
  harnessName,
} from "./desktop/reviews";
import {
  describeCommand,
  describeAgent,
  displayPath,
  type SetupView,
  type Outcome,
  decisionModelDefaults,
  modelPresets,
  withModel,
  type PullRequests,
  type ReviewSettings,
} from "./desktop/setup-model";
import { LoadingStatus } from "./nine-dots";

/** The harness and model reviews use when a repository does not choose its
 *  own, and the ones fixes use: an empty `fix_backend` follows the review's. */
export type ReviewDefaults = {
  backend: string;
  model: string;
  fix_backend?: string;
  fix_model?: string;
};

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** "auto" has no tile: it shows as the harness it runs first. */
function shownBackend(backend: string) {
  return backend === "auto" || !backend ? "claude" : backend;
}

/** Only Claude Code and Codex confine an agent with write access, so only
 *  they fix pull requests. */
const fixHarnesses = ["claude", "codex"];

export const settingsSections = ["general", "reviews", "fixes", "connections", "computer"] as const;
export type SettingsSection = (typeof settingsSections)[number];
const sectionTitles: Record<SettingsSection, string> = {
  general: "General",
  reviews: "Reviews",
  fixes: "Fixes",
  connections: "Connections",
  computer: "This computer",
};

/** The controls the keyboard steps through in the shown section, in reading
 *  order. */
const SECTION_CONTROLS = [
  "button:not(:disabled)",
  "input:not(:disabled):not([type='hidden'])",
  "select:not(:disabled)",
  "textarea:not(:disabled)",
  "a[href]",
  "summary",
].join(", ");

/** Settings' keys (`SETTINGS_SHORTCUTS`). Tab moves nothing in the app, so
 *  the section list is the selection, as Up next's list is: j/↓ and k/↑ move
 *  it, 1 to 5 jump to a section, and Enter, l or → goes into the shown
 *  section. There j/↓ and k/↑ step through its controls, Enter or Space
 *  changes the focused one, and Esc, h or ← go back to the list. Esc in a text
 *  field or select gives it up first (`useEscapeBlurs`), leaving the place in
 *  the section, so j and k go on from it. */
function useSettingsKeys(
  sections: readonly SettingsSection[],
  section: SettingsSection,
  setSection: (section: SettingsSection) => void,
) {
  const nav = useRef<HTMLElement>(null);
  const content = useRef<HTMLDivElement>(null);
  // The control last focused in the section, kept while a field is given up.
  const at = useRef<HTMLElement | null>(null);
  const navItem = (id: SettingsSection) =>
    nav.current?.querySelector<HTMLElement>(`[data-section="${id}"]`) ?? null;
  const controls = () =>
    Array.from(content.current?.querySelectorAll<HTMLElement>(SECTION_CONTROLS) ?? []).filter(
      (control) => !control.closest("[hidden], [inert], details:not([open]) > :not(summary)"),
    );
  const focus = (target: HTMLElement | null | undefined) => {
    target?.focus({ preventScroll: true });
    target?.scrollIntoView?.({ block: "nearest" });
  };
  const show = (next: SettingsSection) => {
    at.current = null;
    setSection(next);
    focus(navItem(next));
  };
  // A section opened through navigation can leave focus on nothing;
  // it goes to the section's place in the list, so the keys go on from there.
  useEffect(() => {
    at.current = null;
    if (document.activeElement === document.body) navItem(section)?.focus({ preventScroll: true });
  }, [section]);
  useShortcuts((event) => {
    const key = event.key;
    const step = key === "j" || key === "ArrowDown" ? 1 : key === "k" || key === "ArrowUp" ? -1 : 0;
    const active = document.activeElement;
    const inSection =
      active instanceof HTMLElement &&
      content.current?.contains(active) &&
      active !== content.current;
    const current = inSection
      ? active
      : onBody(event) && at.current?.isConnected && content.current?.contains(at.current)
        ? at.current
        : null;
    if (current) {
      if (step) {
        event.preventDefault();
        const list = controls();
        const index = list.indexOf(current);
        focus(list[Math.min(list.length - 1, Math.max(0, index + step))] ?? list[0]);
      } else if (key === "Escape" || key === "h" || key === "ArrowLeft") {
        event.preventDefault();
        at.current = null;
        focus(navItem(section));
      } else if (
        key === "Enter" &&
        current instanceof HTMLInputElement &&
        (current.type === "checkbox" || current.type === "radio")
      ) {
        // Space already does this; Enter does too, as on a button.
        event.preventDefault();
        current.click();
      }
      return;
    }
    if (step) {
      event.preventDefault();
      const index = sections.indexOf(section);
      show(sections[Math.min(sections.length - 1, Math.max(0, index + step))]!);
    } else if (/^[1-9]$/.test(key) && sections[Number(key) - 1]) {
      event.preventDefault();
      show(sections[Number(key) - 1]!);
    } else if (key === "Enter" || key === "l" || key === "ArrowRight") {
      const first = controls()[0];
      if (!first) return;
      event.preventDefault();
      focus(first);
    }
  });
  return {
    nav,
    content,
    onFocus: (event: FocusEvent) => {
      if (event.target instanceof HTMLElement && event.target !== content.current)
        at.current = event.target;
    },
  };
}

/** The section a `section` query value names; anything else is General. */
export function settingsSection(value: string | null): SettingsSection {
  return settingsSections.includes(value as SettingsSection)
    ? (value as SettingsSection)
    : "general";
}

type Tone = "positive" | "caution" | "critical" | "neutral";

/** Where something stands, drawn as a coloured dot beside its words. */
function Dot({ tone }: { tone: Tone }) {
  return <span className="settings-dot" data-tone={tone} aria-hidden="true" />;
}

/** How the GitHub connection reads in the section list. */
function githubHealth(github: GitHubConnection | null): { tone: Tone; detail: string } {
  if (!github) return { tone: "neutral", detail: "Checking the connection…" };
  if (github.pending) return { tone: "caution", detail: "Waiting for you to finish signing in." };
  if (github.error)
    return {
      tone: "critical",
      detail: githubMessages[github.error] ?? "The connection needs attention.",
    };
  if (github.override)
    return { tone: "positive", detail: `Using a credential from ${github.source ?? "elsewhere"}.` };
  if (github.saved)
    return { tone: "positive", detail: `Signed in as ${github.login ?? "you"} on ${github.host}.` };
  return { tone: "critical", detail: "Not connected. baum needs GitHub to find pull requests." };
}

/** The agent that fixes, in words, and whether it can. */
function fixSummary(defaults: ReviewDefaults): { label: string; fixable: boolean } {
  if (defaults.fix_backend)
    return {
      label: [harnessName(defaults.fix_backend), defaults.fix_model].filter(Boolean).join(" · "),
      fixable: true,
    };
  const review = shownBackend(defaults.backend);
  return {
    label: `Same as reviews (${harnessName(review)})`,
    fixable: fixHarnesses.includes(review),
  };
}

/** A setting on one line: its name and a sentence about it on the left, its
 *  control on the right; narrow, the control drops below. */
export function SettingRow({
  label,
  htmlFor,
  detail,
  children,
}: {
  label: ReactNode;
  htmlFor?: string;
  detail?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="settings-row">
      <div className="settings-row-text">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="settings-row-label">
            {label}
          </label>
        ) : (
          <span className="settings-row-label">{label}</span>
        )}
        {detail ? <div className="settings-row-detail">{detail}</div> : null}
      </div>
      {children ? <div className="settings-row-control">{children}</div> : null}
    </div>
  );
}

/** A section's title and what it holds, with the outcome of its last save. */
function SectionHead({
  id,
  title,
  detail,
  status,
}: {
  id: string;
  title: string;
  detail?: ReactNode;
  status?: ReactNode;
}) {
  return (
    <header className="settings-section-head">
      <div className="min-w-0">
        <h2 id={id}>{title}</h2>
        {detail ? <p>{detail}</p> : null}
      </div>
      {status}
    </header>
  );
}

/** Saves review and fix choices as they change. Each section sends all of its
 *  choices, so one PUT runs at a time and a change made meanwhile replaces any
 *  other waiting one: an older choice never lands after a newer one. The app
 *  reads its state again once nothing waits, and `Saved` shows for a moment.
 *  `failures` counts failed saves, so a section puts its controls back. */
function useAutoSave(changed: () => Promise<void>) {
  const [state, setState] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState("");
  const [failures, setFailures] = useState(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const waiting = useRef<Record<string, unknown> | null>(null);
  const busy = useRef(false);
  useEffect(() => () => clearTimeout(timer.current), []);
  const drain = async () => {
    busy.current = true;
    clearTimeout(timer.current);
    setState("saving");
    setError("");
    let failure = "";
    while (waiting.current) {
      const body = waiting.current;
      waiting.current = null;
      try {
        await request("api/automatic", body, "PUT");
        failure = "";
      } catch (e) {
        failure = errorText(e);
      }
      // Read the state even after a failure: an earlier save may have landed.
      if (!waiting.current) {
        await changed().catch((e: unknown) => {
          failure ||= errorText(e);
        });
      }
    }
    busy.current = false;
    setError(failure);
    if (failure) {
      setState("idle");
      setFailures((n) => n + 1);
    } else {
      setState("saved");
      timer.current = setTimeout(() => setState("idle"), 2000);
    }
  };
  const save = (body: Record<string, unknown>) => {
    waiting.current = body;
    if (!busy.current) void drain();
  };
  const status = (
    <p role="status" className="settings-saved">
      {state === "saving" ? "Saving…" : state === "saved" ? "Saved" : ""}
    </p>
  );
  return { save, error, status, failures };
}

/** The harness and model a desktop app or daemon page shows, as one select
 *  per row. `same` adds a first choice that follows another setting. */
function AgentSelect({
  id,
  value,
  options,
  same,
  disabled,
  onChange,
}: {
  id: string;
  value: string;
  options: string[];
  same?: string;
  disabled?: boolean;
  onChange: (next: string) => void;
}) {
  return (
    <select
      id={id}
      className="local-field settings-select"
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
    >
      {same !== undefined ? <option value="">{same}</option> : null}
      {options.map((option) => (
        <option key={option} value={option}>
          {harnessName(option)}
        </option>
      ))}
    </select>
  );
}

const CUSTOM = "\u0000custom";

/** The harness's models, its default first, and a name typed in by hand,
 *  which is saved when the field is left or Enter is pressed. */
function ModelField({
  id,
  backend,
  value,
  disabled,
  onChange,
}: {
  id: string;
  backend: string;
  value: string;
  disabled?: boolean;
  onChange: (next: string) => void;
}) {
  const presets = modelPresets[backend] ?? [];
  const [custom, setCustom] = useState(!presets.includes(value));
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  const commit = () => {
    const next = text.trim();
    if (next && next !== value) onChange(next);
  };
  return (
    <div className="settings-model">
      <select
        id={id}
        className="local-field settings-select"
        value={custom ? CUSTOM : value}
        disabled={disabled}
        onChange={(event) => {
          const next = event.target.value;
          setCustom(next === CUSTOM);
          if (next === CUSTOM) setText(presets.includes(value) ? "" : value);
          else onChange(next);
        }}
      >
        {presets.map((model) => (
          <option key={model} value={model}>
            {model}
          </option>
        ))}
        <option value={CUSTOM}>Custom…</option>
      </select>
      {custom ? (
        <input
          className="local-field settings-select"
          aria-label="Custom model"
          placeholder={`Model name, as ${harnessName(backend)} lists it`}
          value={text}
          disabled={disabled}
          spellCheck={false}
          onChange={(event) => setText(event.target.value)}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === "Enter") commit();
          }}
        />
      ) : null}
    </div>
  );
}

const scopes: { value: PullRequests; label: string }[] = [
  { value: "all", label: "All" },
  { value: "both", label: "Mine and requested" },
  { value: "authored", label: "Only mine" },
  { value: "requested", label: "Only requested" },
];

/** The global review settings: whether baum reviews on its own, which pull
 *  requests, and the agent and model that write reviews. */
function ReviewsSection({
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
  const key = JSON.stringify(saved);
  const [draft, setDraft] = useState(saved);
  const { save, error, status, failures } = useAutoSave(changed);
  // A save, another window or the tray changes what is saved; a failed save
  // puts the controls back.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `saved`
  useEffect(() => setDraft(saved), [key, failures]);
  const change = (patch: Partial<ReviewSettings>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    save({
      enabled: next.automatic,
      backend: next.backend,
      model: next.model,
      pull_requests: next.pull_requests,
    });
  };
  const harness = harnesses.find((h) => h.id === draft.backend);
  return (
    <section aria-labelledby="settings-reviews-heading" className="settings-section">
      <SectionHead
        id="settings-reviews-heading"
        title="Reviews"
        detail="Which pull requests baum reviews on its own, and the agent that writes the reviews."
        status={status}
      />
      {error ? <Alert>{error}</Alert> : null}
      <div className="local-panel settings-list">
        <SettingRow
          label="Review pull requests automatically"
          htmlFor="review-automatic"
          detail="New pushes and review requests in your repositories. Drafts, forks and authors without write access are skipped."
        >
          <input
            id="review-automatic"
            type="checkbox"
            role="switch"
            className="local-switch"
            checked={draft.automatic}
            onChange={(event) => change({ automatic: event.target.checked })}
          />
        </SettingRow>
        {draft.automatic ? (
          <SettingRow
            label="Which pull requests"
            detail={
              draft.pull_requests === "all" ? (
                <span id="review-all-note">
                  Every pull request in your repositories gets reviewed, including your team's, so
                  reviews, notifications and cost grow with their activity.
                </span>
              ) : null
            }
          >
            <SegmentedControl
              label="Which pull requests"
              value={draft.pull_requests}
              items={scopes}
              onChange={(pull_requests) => change({ pull_requests })}
            />
          </SettingRow>
        ) : null}
        <SettingRow
          label="Agent that writes reviews"
          htmlFor="review-agent"
          detail={harness ? `Uses ${harness.detail.replace(/^Your/, "your")}.` : null}
        >
          <AgentSelect
            id="review-agent"
            value={draft.backend}
            options={harnesses.map((h) => h.id)}
            onChange={(backend) => change({ backend, model: modelPresets[backend]?.[0] ?? "" })}
          />
        </SettingRow>
        <SettingRow label="Model" htmlFor="review-model">
          <ModelField
            // A new harness lists other models, so the field starts over.
            key={draft.backend}
            id="review-model"
            backend={draft.backend}
            value={draft.model}
            onChange={(model) => change({ model })}
          />
        </SettingRow>
      </div>
      <p className="settings-footnote">
        Changes save as you make them and apply to the next review. A repository can choose its own
        agent and model in Repositories.
      </p>
    </section>
  );
}

/** The agent and model that fix pull requests: the review's, or one of the
 *  harnesses that can fix. */
function FixesSection({
  defaults,
  changed,
}: {
  defaults: ReviewDefaults;
  changed: () => Promise<void>;
}) {
  const review = shownBackend(defaults.backend);
  const savedBackend = defaults.fix_backend ?? "";
  const saved = {
    backend: savedBackend,
    model: savedBackend ? defaults.fix_model || (modelPresets[savedBackend]?.[0] ?? "") : "",
  };
  const key = JSON.stringify(saved);
  const [draft, setDraft] = useState(saved);
  const { save, error, status, failures } = useAutoSave(changed);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `saved`
  useEffect(() => setDraft(saved), [key, failures]);
  const change = (next: typeof saved) => {
    setDraft(next);
    save({ fix_backend: next.backend, fix_model: next.model });
  };
  const reviewModel = defaults.backend === "auto" ? "" : defaults.model;
  const reviewAgent = [harnessName(review), reviewModel].filter(Boolean).join(" · ");
  return (
    <section aria-labelledby="settings-fixes-heading" className="settings-section">
      <SectionHead
        id="settings-fixes-heading"
        title="Fixes"
        detail="The agent that fixes conflicts, failing checks and review comments when you ask it to."
        status={status}
      />
      {error ? <Alert>{error}</Alert> : null}
      <div className="local-panel settings-list">
        <SettingRow
          label="Agent that fixes"
          htmlFor="fix-agent"
          detail={
            !draft.backend && !fixHarnesses.includes(review) ? (
              <span className="settings-caution">
                Reviews use {harnessName(review)}, which can't fix pull requests. Choose Claude Code
                or Codex to fix them.
              </span>
            ) : (
              <>
                {draft.backend ? "" : `Now ${reviewAgent}. `}It works with write access to a
                temporary checkout. Only Claude Code and Codex can fix.
              </>
            )
          }
        >
          <AgentSelect
            id="fix-agent"
            value={draft.backend}
            options={fixHarnesses}
            same="Same as reviews"
            onChange={(backend) =>
              change({ backend, model: backend ? (modelPresets[backend]?.[0] ?? "") : "" })
            }
          />
        </SettingRow>
        {draft.backend ? (
          <SettingRow label="Model" htmlFor="fix-model">
            <ModelField
              key={draft.backend}
              id="fix-model"
              backend={draft.backend}
              value={draft.model}
              onChange={(model) => change({ ...draft, model })}
            />
          </SettingRow>
        ) : null}
      </div>
      <p className="settings-footnote">
        {draft.backend
          ? "Fixes use this agent in every repository, whatever agent the repository reviews with."
          : "Fixes follow the review agent, including a repository's own choice."}
      </p>
    </section>
  );
}

/** The desktop app's view of the CLI and agent hooks, and the actions that
 *  change them. Outside the desktop app it stays empty. */
function useDesktopSetup() {
  const desktop = hasFolderPicker();
  const [view, setView] = useState<SetupView | null>(null);
  const [busy, setBusy] = useState(false);
  // What the running action is, so only its own button changes its label.
  const [doing, setDoing] = useState("");
  const [error, setError] = useState("");
  const [output, setOutput] = useState("");
  const working = useRef(false);
  async function refresh() {
    setView(await invoke<SetupView>("setup_state"));
  }
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once on mount
  useEffect(() => {
    if (desktop) void refresh().catch((cause) => setError(String(cause)));
  }, []);
  async function run(command?: string, args?: Record<string, unknown>) {
    if (working.current) return;
    working.current = true;
    setBusy(true);
    setDoing(command ? `${command}:${args?.agent ?? ""}` : "refresh");
    setError("");
    setOutput("");
    try {
      if (command) {
        const outcome = await invoke<Outcome>(command, args);
        if (outcome.ok) setOutput(outcome.lines.join("\n"));
        else setError(outcome.lines.join("\n"));
      }
      await refresh();
    } catch (cause) {
      setError(String(cause));
    } finally {
      working.current = false;
      setBusy(false);
    }
  }
  const display = (text: string) => displayPath(text, view?.home);
  const feedback = (
    <>
      {error ? <Alert>{display(error)}</Alert> : null}
      {output ? (
        <p role="status" className="whitespace-pre-wrap text-xs text-(--text-secondary)">
          {display(output)}
        </p>
      ) : null}
    </>
  );
  return { desktop, view, busy, doing, run, display, feedback };
}
type DesktopSetup = ReturnType<typeof useDesktopSetup>;

function connectedState(label: string, connected: boolean) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs ${connected ? "text-(--status-positive)" : "text-(--text-tertiary)"}`}
    >
      {connected ? <CheckIcon size={14} /> : null}
      {label}
    </span>
  );
}

/** Whether each coding agent's hooks are in, so baum can follow its work. */
function AgentHooksPanel({ setup }: { setup: DesktopSetup }) {
  const { view, busy, doing, run, display } = setup;
  return (
    <>
      <section className="local-panel p-0!" aria-labelledby="hooks-heading">
        <PanelHeader
          id="hooks-heading"
          icon={<SessionsIcon size={20} />}
          title="Agent hooks"
          detail="Connect your coding agents so baum can follow their work."
        />
        {view?.hooks.agents.map((agent) => (
          <div
            key={agent.agent}
            className="flex flex-wrap items-center justify-between gap-4 border-t border-(--border-subtle) px-5 py-4"
          >
            <div className="flex min-w-0 flex-[1_1_16rem] items-start gap-3">
              <img
                src={agent.agent === "codex" ? codexLogo : claudeLogo}
                alt=""
                width="24"
                height="24"
                className="mt-0.5 shrink-0"
              />
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-3">
                  <h3 className="text-base font-medium">{agent.name}</h3>
                  {connectedState(
                    agent.installed && agent.enabled
                      ? "Connected"
                      : agent.installed
                        ? "Disabled"
                        : "Not connected",
                    agent.installed && agent.enabled && !agent.error,
                  )}
                </div>
                {agent.error || !agent.installed ? (
                  <p className="text-xs text-(--text-secondary)">{display(describeAgent(agent))}</p>
                ) : null}
                {agent.installed ? (
                  <p className="text-xs text-(--text-tertiary)">
                    {agent.agent === "codex"
                      ? "Review hook trust with /hooks in Codex."
                      : "Review plugin trust in Claude Code."}
                  </p>
                ) : null}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {agent.detected && !agent.error && (!agent.installed || !agent.enabled) ? (
                <Button
                  disabled={busy}
                  size="compact"
                  variant="secondary"
                  onClick={() => void run("install_hooks", { agent: agent.agent })}
                >
                  {doing === `install_hooks:${agent.agent}`
                    ? agent.installed
                      ? "Enabling…"
                      : "Connecting…"
                    : `${agent.installed ? "Enable" : "Connect"} ${agent.name}`}
                </Button>
              ) : null}
              {agent.installed ? (
                <Button
                  disabled={busy}
                  size="compact"
                  variant="secondary"
                  aria-label={`Remove ${agent.name} hooks`}
                  onClick={() => void run("uninstall_hooks", { agent: agent.agent })}
                >
                  {doing === `uninstall_hooks:${agent.agent}` ? "Removing…" : "Remove"}
                </Button>
              ) : null}
            </div>
          </div>
        ))}
        <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-(--border-subtle) px-5 py-3">
          <p className="text-xs text-(--text-tertiary)">
            Installed something new? Refresh to check its status.
          </p>
          <Button disabled={busy} size="compact" variant="secondary" onClick={() => void run()}>
            {doing === "refresh" && busy ? "Checking…" : "Check again"}
          </Button>
        </footer>
      </section>
      {view?.hooks.error ? <Alert>{display(view.hooks.error)}</Alert> : null}
    </>
  );
}

/** The baum CLI the desktop app installs, with install, update and removal. */
function CommandLinePanel({ setup }: { setup: DesktopSetup }) {
  const { view, busy, doing, run, display } = setup;
  const command = view ? describeCommand(view.command) : null;
  return (
    <section className="local-panel p-0!" aria-labelledby="cli-heading">
      <PanelHeader
        id="cli-heading"
        icon={<TerminalIcon size={20} />}
        title="Command line"
        detail="Use baum from your terminal. Updates come with the desktop app."
      />
      <div className="flex flex-wrap items-center justify-between gap-4 border-t border-(--border-subtle) px-5 py-4">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-3">
            <h3 className="text-base font-medium">baum CLI</h3>
            {command
              ? connectedState(command.ok ? "Installed" : "Not installed", !!command.ok)
              : null}
          </div>
          {command ? (
            <p className="break-all text-xs text-(--text-secondary)">{display(command.state)}</p>
          ) : (
            <p role="status" className="text-xs text-(--text-secondary)">
              Checking installation…
            </p>
          )}
          {command?.note ? (
            <p className="text-xs text-(--text-secondary)">{display(command.note)}</p>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {command?.action ? (
            <Button
              disabled={busy}
              size="compact"
              variant="secondary"
              onClick={() =>
                void run("install_command", command.replace ? { replace: command.replace } : {})
              }
            >
              {doing === "install_command:"
                ? command.replace
                  ? "Replacing…"
                  : "Installing…"
                : command.replace
                  ? `Replace ${display(command.replace)}`
                  : `${command.action} baum CLI`}
            </Button>
          ) : null}
          {view?.command.record ? (
            <Button
              disabled={busy}
              size="compact"
              variant="secondary"
              onClick={() => void run("uninstall_command")}
            >
              {doing === "uninstall_command:" ? "Removing…" : "Remove CLI"}
            </Button>
          ) : null}
        </div>
      </div>
    </section>
  );
}

/** How the agent hooks stand, for the section list: none connected is a caution
 *  only where an agent was found to connect. */
function hooksHealth(view: SetupView | null): { tone: Tone; detail: string } | null {
  if (!view) return null;
  const agents = view.hooks.agents;
  const connected = agents.filter((a) => a.installed && a.enabled && !a.error);
  const waiting = agents.filter((a) => a.error || (a.detected && (!a.installed || !a.enabled)));
  if (view.hooks.error || waiting.length > 0)
    return {
      tone: "caution",
      detail: `${waiting.map((a) => a.name).join(" and ") || "An agent"} ${waiting.length === 1 ? "isn't" : "aren't"} connected, so baum can't follow ${waiting.length === 1 ? "its" : "their"} sessions.`,
    };
  if (connected.length === 0)
    return { tone: "neutral", detail: "No coding agent found to connect." };
  return {
    tone: "positive",
    detail: `baum follows ${connected.map((a) => a.name).join(" and ")} sessions on this computer.`,
  };
}

export function SettingsPage({
  automatic,
  defaults,
  github,
  section,
  setSection,
  changed,
  problems,
  diagnosticsOpen,
  setDiagnosticsOpen,
  onDiagnostics,
  authRevision,
  authChanged,
}: {
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  github: GitHubConnection | null;
  section: SettingsSection;
  setSection: (section: SettingsSection) => void;
  changed: () => Promise<void>;
  problems: number;
  diagnosticsOpen: boolean;
  setDiagnosticsOpen: (open: boolean) => void;
  onDiagnostics: (report: DiagnosticReport | null) => void;
  authRevision: number;
  authChanged: () => void;
}) {
  const setup = useDesktopSetup();
  const gh = githubHealth(github);
  const hooks = setup.desktop ? hooksHealth(setup.view) : null;
  const fix = defaults ? fixSummary(defaults) : null;
  const connectionTone: Tone =
    gh.tone === "critical" ? "critical" : hooks?.tone === "caution" ? "caution" : gh.tone;
  const nav: { id: SettingsSection; sub: string; tone?: Tone }[] = [
    {
      id: "general",
      sub: "Appearance and keyboard",
    },
    {
      id: "reviews",
      sub: defaults
        ? `${automatic?.enabled ? "Automatic" : "When you ask"} · ${harnessName(shownBackend(defaults.backend))}`
        : "Reading…",
    },
    {
      id: "fixes",
      sub: defaults?.fix_backend ? harnessName(defaults.fix_backend) : "Same as reviews",
      tone: fix && !fix.fixable ? "caution" : undefined,
    },
    {
      id: "connections",
      sub: setup.desktop ? "GitHub and agent hooks" : "GitHub",
      tone:
        connectionTone === "critical" || connectionTone === "caution" ? connectionTone : undefined,
    },
    {
      id: "computer",
      sub:
        problems > 0
          ? `${problems} ${problems === 1 ? "problem" : "problems"} found`
          : setup.desktop
            ? "Diagnostics, CLI, environment"
            : "Diagnostics, environment",
      tone: problems > 0 ? "critical" : undefined,
    },
  ];
  const keys = useSettingsKeys(
    nav.map((item) => item.id),
    section,
    setSection,
  );
  const loading = <LoadingStatus block>Reading your review settings…</LoadingStatus>;
  return (
    <section className="settings-page">
      <PageBar
        title="Settings"
        detail="How baum reviews and fixes pull requests on this computer"
      />
      {diagnosticsOpen ? (
        <DiagnosticsDialog
          close={() => setDiagnosticsOpen(false)}
          onReport={onDiagnostics}
          authRevision={authRevision}
          authChanged={authChanged}
        />
      ) : null}
      <div className="local-page-body settings-layout">
        <nav ref={keys.nav} aria-label="Settings sections" className="settings-nav">
          {nav.map((item, index) => (
            <button
              key={item.id}
              type="button"
              className="settings-nav-item"
              data-section={item.id}
              aria-current={item.id === section ? "page" : undefined}
              aria-keyshortcuts={String(index + 1)}
              onClick={() => setSection(item.id)}
            >
              <span className="settings-nav-text">
                <span className="settings-nav-label">
                  {sectionTitles[item.id]}{" "}
                  {item.tone ? (
                    <span
                      role="img"
                      aria-label="Needs attention"
                      title="Needs attention"
                      className="ml-2 inline-flex"
                    >
                      <Dot tone={item.tone} />
                    </span>
                  ) : null}
                </span>
                <span className="settings-nav-sub">{item.sub}</span>
              </span>
            </button>
          ))}
        </nav>
        <div ref={keys.content} className="settings-content" onFocus={keys.onFocus}>
          {section === "general" ? (
            <GeneralSection />
          ) : section === "reviews" ? (
            automatic && defaults ? (
              <>
                <ReviewsSection automatic={automatic} defaults={defaults} changed={changed} />
                {/* A new review harness changes what capture resolves to. */}
                <DecisionSettingsSection key={defaults.backend} defaults={defaults} />
              </>
            ) : (
              loading
            )
          ) : section === "fixes" ? (
            defaults ? (
              <FixesSection defaults={defaults} changed={changed} />
            ) : (
              loading
            )
          ) : section === "connections" ? (
            <section aria-labelledby="settings-connections-heading" className="settings-section">
              <SectionHead
                id="settings-connections-heading"
                title="Connections"
                detail={
                  setup.desktop
                    ? "GitHub, where baum finds pull requests, and the coding agents it follows."
                    : "GitHub, where baum finds pull requests."
                }
              />
              {diagnosticsOpen ? null : <GitHubConnectionSection onChanged={authChanged} />}
              {setup.desktop ? <AgentHooksPanel setup={setup} /> : null}
              {setup.feedback}
            </section>
          ) : (
            <section aria-labelledby="settings-computer-heading" className="settings-section">
              <SectionHead
                id="settings-computer-heading"
                title="This computer"
                detail="What baum needs here, and how it runs."
              />
              <div className="local-panel settings-list">
                <SettingRow
                  label="Diagnostics"
                  detail={
                    problems > 0
                      ? `${problems} ${problems === 1 ? "problem" : "problems"} found. Check this computer to see how to fix them.`
                      : "No problems found on this computer."
                  }
                >
                  <Button
                    variant="secondary"
                    size="compact"
                    onClick={() => setDiagnosticsOpen(true)}
                  >
                    Check this computer
                  </Button>
                </SettingRow>
                <SettingRow
                  label="Background reviews"
                  detail={
                    <>
                      The daemon keeps reviewing after you close this window. Quit the desktop app
                      with “Quit and stop background reviews”, or run <code>baum daemon stop</code>,
                      to stop it.
                    </>
                  }
                />
              </div>
              {setup.desktop ? (
                <>
                  <CommandLinePanel setup={setup} />
                  <ZoomSection />
                  {setup.feedback}
                </>
              ) : null}
              <AgentEnvSection />
              <AgentApprovalsSection />
            </section>
          )}
        </div>
      </div>
    </section>
  );
}

type ZoomState = { level: number; levels: number[] };

/** The desktop window's zoom, which the app saves; the shortcuts change it too. */
function ZoomSection() {
  const [zoom, setZoom] = useState<ZoomState | null>(null);
  const [hidden, setHidden] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    // A daemon newer or older than the app may serve this page without the
    // app offering zoom; the card then stays hidden.
    void invoke<ZoomState>("zoom_state").then(setZoom, () => setHidden(true));
    // The shortcuts announce each new level.
    const changed = (event: Event) => {
      const level = (event as CustomEvent<number>).detail;
      setZoom((current) => (current ? { ...current, level } : current));
    };
    window.addEventListener("baum:zoom", changed);
    return () => window.removeEventListener("baum:zoom", changed);
  }, []);
  async function choose(level: number) {
    setError("");
    try {
      const applied = await invoke<number>("set_zoom", { level });
      setZoom((current) => (current ? { ...current, level: applied } : current));
    } catch (cause) {
      setError(String(cause));
    }
  }
  if (hidden) return null;
  const mod = navigator.platform.startsWith("Mac") ? "⌘" : "Ctrl";
  return (
    <section className="local-panel p-0!" aria-labelledby="zoom-heading">
      <PanelHeader
        id="zoom-heading"
        icon={<SearchIcon size={20} />}
        title="Zoom"
        detail="Make everything in this window larger or smaller."
      />
      <div className="flex flex-wrap items-center justify-between gap-4 border-t border-(--border-subtle) px-5 py-4">
        <p className="min-w-0 flex-1 text-xs text-(--text-secondary)">
          {mod} + and {mod} − also change it, and {mod} 0 resets it to 100%.
        </p>
        <select
          className="local-field"
          aria-label="Zoom"
          disabled={!zoom}
          value={zoom?.level ?? 1}
          onChange={(e) => void choose(Number(e.target.value))}
        >
          {(zoom?.levels ?? [1]).map((level) => (
            <option key={level} value={level}>
              {Math.round(level * 100)}%
            </option>
          ))}
        </select>
      </div>
      {error ? (
        <div className="px-5 pb-4">
          <Alert>{error}</Alert>
        </div>
      ) : null}
    </section>
  );
}

/** Decision capture: the switch and the model. */
function DecisionSettingsSection({ defaults }: { defaults: ReviewDefaults }) {
  const [saved, setSaved] = useState<DecisionSettings | null>(null);
  // The model is null while it is inherited and a string, "" included, once
  // one is picked or being typed: "" is not the harness's default here.
  const [draft, setDraft] = useState<{ enabled: boolean; model: string | null } | null>(null);
  const [pending, setPending] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  const load = (settings: DecisionSettings) => {
    setSaved(settings);
    setDraft({ enabled: settings.enabled, model: settings.model || null });
  };
  useEffect(() => {
    request<{ decisions?: DecisionSettings }>("api/settings/decisions").then(
      (settings) => {
        if (settings.decisions) load(settings.decisions);
      },
      (e) => setError(errorText(e)),
    );
  }, []);
  if (!saved || !draft) return error ? <Alert>{error}</Alert> : null;
  // The daemon resolves the harness capture uses, auto included.
  const backend = saved.backend || shownBackend(defaults.backend);
  // A blank custom name is saved as none, as a repository's model is.
  const next = { enabled: draft.enabled, model: draft.model?.trim() ?? "" };
  const dirty = next.enabled !== saved.enabled || next.model !== saved.model;
  const save = async () => {
    setPending(true);
    setError("");
    setDone(false);
    try {
      const answer = await request<{ decisions: DecisionSettings }>(
        "api/settings/decisions",
        next,
        "PUT",
      );
      load(answer.decisions);
      setDone(true);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  const fallback = decisionModelDefaults[backend];
  // A saved model the daemon reports capture ignores is shown as such.
  const ignored = saved.model_ignored && saved.model ? saved.model : undefined;
  return (
    <section aria-labelledby="decisions-heading" className="local-panel settings-card p-0!">
      <PanelHeader
        id="decisions-heading"
        icon={<LightbulbIcon size={20} />}
        title="Decision capture"
        detail="baum records the decisions made in your agent sessions, with a small model through the review harness, and shows them beside each pull request."
        actions={
          <Toggle
            checked={draft.enabled}
            disabled={pending}
            onChange={(enabled) => {
              setDone(false);
              setDraft({ ...draft, enabled });
            }}
          >
            Capture decisions
          </Toggle>
        }
      />
      {error ? <Alert className="mx-5 mb-4">{error}</Alert> : null}
      <div className="review-card settings-form">
        <DecisionModelPicker
          id="settings-decisions-model"
          backend={backend}
          value={draft.model}
          unused={ignored}
          inherit={`Default${fallback ? ` (${fallback})` : ""}`}
          disabled={pending || !draft.enabled}
          onChange={(model) => {
            setDone(false);
            setDraft({ ...draft, model });
          }}
        />
        {saved.backend === "" ? (
          <div className="review-section">
            <p className="review-note">
              {saved.backend_undetermined
                ? "The harness capture uses could not be determined: its check did not answer in time."
                : "No harness that can capture decisions is available on this computer, so capture does not run."}
            </p>
          </div>
        ) : null}
        <div className="review-section">
          <DecisionsDisclosure />
        </div>
      </div>
      <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-(--border-subtle) px-5 py-3">
        <p role="status" className="text-xs text-(--text-tertiary)">
          {done ? "Saved." : dirty ? "You have unsaved changes." : ""}
        </p>
        <Button size="compact" disabled={!dirty || pending} onClick={() => void save()}>
          {pending ? "Saving…" : "Save decision capture"}
        </Button>
      </footer>
    </section>
  );
}

type EnvRow = { id: number; name: string; value: string };
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const ENV_MAX = 32;

const envRows = (env: Record<string, string>): EnvRow[] =>
  Object.entries(env).map(([name, value], id) => ({ id, name, value }));

/** Why a list of variables cannot be saved, per row; empty when it can. */
function envProblems(rows: EnvRow[]): Record<number, string> {
  const problems: Record<number, string> = {};
  const seen = new Set<string>();
  for (const row of rows) {
    if (!ENV_NAME.test(row.name))
      problems[row.id] = row.name
        ? "Use letters, digits and underscores, not starting with a digit."
        : "Enter a name.";
    else if (seen.has(row.name)) problems[row.id] = "Another row has this name.";
    seen.add(row.name);
  }
  return problems;
}

/** Whether Codex approvals are answered from the Sessions page. Hidden
 *  while the daemon does not serve the setting (an older one). */
function AgentApprovalsSection() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    request<unknown>("api/agent-requests").then(
      (body) => setEnabled(parseAgentRequests(body).settings?.codex_approvals ?? null),
      () => setEnabled(null),
    );
  }, []);
  if (enabled === null) return null;
  const change = async (next: boolean) => {
    setEnabled(next);
    setPending(true);
    setError("");
    try {
      await saveAgentSettings({ codex_approvals: next });
    } catch (e) {
      setEnabled(!next);
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <section aria-labelledby="agent-approvals-heading" className="local-panel settings-card p-0!">
      <PanelHeader
        id="agent-approvals-heading"
        icon={<SessionsIcon size={20} />}
        title="Answering agents"
        detail="Approve commands and answer questions from the Sessions page."
      />
      <div className="review-section px-5 pb-4">
        <label className="switch-row">
          <span>
            Answer Codex approvals from the app
            <small>
              While on, Codex hides its own approval prompt for up to 25 seconds while it waits for
              an answer here.
            </small>
          </span>
          <input
            type="checkbox"
            role="switch"
            checked={enabled}
            disabled={pending}
            onChange={(event) => void change(event.target.checked)}
          />
        </label>
        {error ? <Alert className="mt-3">{error}</Alert> : null}
      </div>
    </section>
  );
}

/** Variables set for every agent baum runs and for the git commands that
 *  commit and push a fix, kept by the daemon on this computer. */
function AgentEnvSection() {
  const [saved, setSaved] = useState<Record<string, string> | null>(null);
  const [source, setSource] = useState("");
  const [rows, setRows] = useState<EnvRow[]>([]);
  const [next, setNext] = useState(0);
  const [pending, setPending] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const load = (env: Record<string, string>) => {
    setSaved(env);
    setRows(envRows(env));
    setNext(Object.keys(env).length);
  };
  useEffect(() => {
    request<{ agent_env?: Record<string, string>; agent_env_source?: string }>("api/settings").then(
      (settings) => {
        load(settings.agent_env ?? {});
        setSource(settings.agent_env_source ?? "");
      },
      (e) => setLoadError(errorText(e)),
    );
  }, []);
  const problems = envProblems(rows);
  const current = Object.fromEntries(rows.map((row) => [row.name, row.value]));
  const dirty =
    saved !== null &&
    (rows.length !== Object.keys(saved).length ||
      rows.some((row, i) => row.name !== Object.keys(saved)[i] || row.value !== saved[row.name]));
  const tooMany = rows.length > ENV_MAX;
  const edit = (id: number, change: Partial<EnvRow>) => {
    setDone(false);
    setRows((list) => list.map((row) => (row.id === id ? { ...row, ...change } : row)));
  };
  const save = async () => {
    setPending(true);
    setError("");
    setDone(false);
    try {
      const answer = await request<{ agent_env?: Record<string, string> }>(
        "api/settings/agent-env",
        { agent_env: current },
        "PUT",
      );
      load(answer.agent_env ?? current);
      setSource(
        Object.keys(current).length
          ? "app"
          : Object.keys(answer.agent_env ?? {}).length
            ? "file"
            : "",
      );
      setDone(true);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };
  return (
    <section aria-labelledby="agent-env-heading" className="local-panel p-0!">
      <PanelHeader
        id="agent-env-heading"
        icon={<TerminalIcon size={20} />}
        title="Environment for agents"
        detail="Extra environment variables for the agents baum runs, and for the commit and push after a fix. Values can reference other environment variables. Stored on this computer, so don't put secrets here."
      />
      {loadError ? <Alert className="mx-5 mb-4">{loadError}</Alert> : null}
      {source === "file" ? (
        <p className="px-5 pb-3 text-xs text-(--text-tertiary)">
          These come from the <code>[agent_env]</code> section of the config file. Saving here
          replaces them.
        </p>
      ) : null}
      {saved === null ? (
        loadError ? null : (
          <LoadingStatus block>Reading the environment…</LoadingStatus>
        )
      ) : (
        <div className="settings-env">
          {rows.map((row) => (
            <div key={row.id} className="settings-env-row">
              <input
                aria-label="Variable name"
                placeholder="NAME"
                value={row.name}
                disabled={pending}
                aria-invalid={problems[row.id] ? true : undefined}
                spellCheck={false}
                onChange={(event) => edit(row.id, { name: event.target.value })}
              />
              <input
                aria-label={`Value of ${row.name || "the variable"}`}
                placeholder="value"
                value={row.value}
                disabled={pending}
                spellCheck={false}
                onChange={(event) => edit(row.id, { value: event.target.value })}
              />
              <Button
                variant="secondary"
                size="compact"
                aria-label={`Remove ${row.name || "variable"}`}
                disabled={pending}
                onClick={() => {
                  setDone(false);
                  setRows((list) => list.filter((r) => r.id !== row.id));
                }}
              >
                <CloseIcon size={14} />
              </Button>
              {problems[row.id] && row.name !== "" ? (
                <p className="settings-env-problem">{problems[row.id]}</p>
              ) : null}
            </div>
          ))}
          {error ? <Alert>{error}</Alert> : null}
          {tooMany ? <Alert>At most {ENV_MAX} variables.</Alert> : null}
        </div>
      )}
      <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-(--border-subtle) px-5 py-3">
        <Button
          variant="secondary"
          size="compact"
          className="inline-flex items-center gap-2"
          disabled={saved === null || pending || rows.length >= ENV_MAX}
          onClick={() => {
            setDone(false);
            setRows((list) => [...list, { id: next, name: "", value: "" }]);
            setNext(next + 1);
          }}
        >
          <PlusIcon size={14} />
          Add variable
        </Button>
        <div className="flex flex-wrap items-center gap-2">
          <p role="status" className="text-xs text-(--text-tertiary)">
            {done
              ? "Saved. Applies to the next agent run."
              : dirty
                ? "You have unsaved changes."
                : ""}
          </p>
          <Button
            size="compact"
            disabled={!dirty || pending || tooMany || Object.keys(problems).length > 0}
            onClick={() => void save()}
          >
            {pending ? "Saving…" : "Save environment"}
          </Button>
        </div>
      </footer>
    </section>
  );
}

/** What baum needs on this computer, in a dialog over the settings. */
function DiagnosticsDialog({
  close,
  onReport,
  authRevision,
  authChanged,
}: {
  close: () => void;
  onReport: (report: DiagnosticReport | null) => void;
  authRevision: number;
  authChanged: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
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
      <GitHubConnectionSection onChanged={authChanged} />
      <DiagnosticsSection key={authRevision} onReport={onReport} />
    </dialog>
  );
}

export function RepositoriesPage({
  repositories,
  automatic,
  defaults,
  changed,
}: {
  repositories: Repository[];
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  changed: () => Promise<void>;
}) {
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  // The keyboard moves through the repositories as through any list, onto
  // each one's Settings; `a` opens or closes the picker and `/` searches it.
  const rows = useRowFocus<HTMLUListElement>(
    "settings-repositories",
    repositories.map((r) => r.repository),
  );
  const page = useRef<HTMLElement>(null);
  useShortcuts((event) => {
    if (listKeys(event, rows)) return;
    if (event.key === "a") {
      event.preventDefault();
      setAdding(!adding);
    } else if (event.key === "/") {
      event.preventDefault();
      if (adding)
        page.current?.querySelector<HTMLInputElement>(".repository-search input")?.focus();
      else setAdding(true);
    }
  });
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
    <section ref={page}>
      <PageBar
        title="Repositories"
        detail="GitHub repositories baum follows on this computer"
        actions={
          <Button size="compact" onClick={() => setAdding(!adding)}>
            {adding ? "Done" : "Add repositories"}
          </Button>
        }
      />
      <div className="local-page-body">
        {error ? <Alert>{error}</Alert> : null}
        {adding ? <RepositoryPicker changed={changed} autoFocus /> : null}
        {repositories.length === 0 ? (
          <div className="local-empty">
            <h2>No repositories yet</h2>
            <p>
              Pick GitHub repositories to see their pull requests. baum finds their local clones for
              you.
            </p>
          </div>
        ) : null}
        {repositories.length > 0 ? (
          <ul ref={rows.list} className="local-panel local-repo-list">
            {repositories.map((repository) => (
              <RepositoryRow
                key={repository.repository}
                focused={rows.focused === repository.repository}
                repository={repository}
                automatic={automatic}
                defaults={defaults}
                changed={changed}
                remove={() =>
                  action(() => request(`api/repositories/${repository.repository}`, {}, "DELETE"))
                }
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
  remove?: () => Promise<void>;
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
  const [decisions, setDecisions] = useState(clone.settings.decisions !== false);
  const [pending, setPending] = useState(false);
  const [removing, setRemoving] = useState(false);
  const globalBackend = shownBackend(defaults?.backend ?? "auto");
  const submit = (event: FormEvent) => {
    event.preventDefault();

    setPending(true);
    void save({
      automatic: mode === "default" ? null : mode === "on",
      backend,
      model: model?.trim() ? model.trim() : null,
      decisions: decisions ? null : false,
    }).finally(() => setPending(false));
  };
  const unregister = () => {
    if (!remove) return;
    setRemoving(true);
    void remove().finally(() => setRemoving(false));
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
          disabled={pending || removing}
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
          disabled={pending || removing}
          inherit={`Global setting${defaults?.model ? ` (${defaults.model})` : ""}`}
          onChange={setModel}
        />
      </div>
      <Toggle checked={decisions} disabled={pending || removing} onChange={setDecisions}>
        Capture the decisions of agent sessions in this repository
      </Toggle>
      <div className="local-dialog-actions">
        {remove ? (
          <Button
            variant="danger-outline"
            size="compact"
            disabled={pending || removing}
            onClick={unregister}
            className="mr-auto"
          >
            {removing ? "Removing…" : "Remove registration"}
          </Button>
        ) : null}
        {cancel ? (
          <Button variant="ghost" size="compact" disabled={pending || removing} onClick={cancel}>
            Cancel
          </Button>
        ) : null}
        <Button type="submit" size="compact" disabled={pending || removing}>
          {pending ? "Saving…" : "Save repository settings"}
        </Button>
      </div>
    </form>
  );
}

/** One registered repository: what it does at a glance, and its settings in a
 *  dialog. */
function RepositoryRow({
  repository,
  focused,
  automatic,
  defaults,
  changed,
  remove,
}: {
  repository: Repository;
  focused: boolean;
  automatic: Automatic | null;
  defaults: ReviewDefaults | null;
  changed: () => Promise<void>;
  remove: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  // The panel that lists the registered clones and adds another.
  const [changing, setChanging] = useState(false);
  const [cloneError, setCloneError] = useState("");
  const [removingClone, setRemovingClone] = useState("");
  const clone: Clone = repository.clones.find((c) => c.available) ??
    repository.clones[0] ?? {
      id: repository.repository,
      path: "Needs a local clone",
      repository: repository.repository,
      available: false,
      settings: repository.settings,
    };
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
    <li
      data-row-id={repository.repository}
      data-focused={focused || undefined}
      className="local-repo-row"
    >
      <div className="local-repo-main">
        <h3>{clone.repository}</h3>
        {clone.available ? (
          <p className="truncate" title={clone.path}>
            {clone.path}
          </p>
        ) : (
          <p className="local-repo-missing">Needs a local clone</p>
        )}
        <p className="local-repo-summary">{summary.join(" · ")}</p>
      </div>
      <div className="local-repo-actions">
        {clone.available ? (
          <Button
            variant="secondary"
            size="compact"
            aria-expanded={changing}
            onClick={() => setChanging(!changing)}
          >
            {changing ? "Done" : "Change clone"}
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="compact"
          data-row-link=""
          aria-label={`Settings for ${clone.repository}`}
          onClick={() => setOpen(true)}
        >
          Settings
        </Button>
      </div>
      {cloneError ? (
        <p role="alert" className="local-repo-panel">
          {cloneError}
        </p>
      ) : null}
      {!clone.available ? (
        <div className="local-repo-panel">
          <CloneActions repository={repository.repository} changed={changed} />
        </div>
      ) : null}
      {clone.available && changing ? (
        <div className="local-repo-panel local-repo-clones">
          <p>
            Keep the clone you want baum to use registered. Removing a registration leaves its files
            and saved reviews on disk.
          </p>
          <ul>
            {repository.clones.map((c) => (
              <li key={c.id}>
                <code className="truncate" title={c.path}>
                  {c.path}
                </code>
                {c.available ? null : <span className="local-pill">Unavailable</span>}
                <Button
                  size="compact"
                  variant="secondary"
                  className="text-base"
                  disabled={removingClone !== ""}
                  onClick={() => {
                    setRemovingClone(c.id);
                    setCloneError("");
                    void request(`api/repos/${c.id}`, {}, "DELETE")
                      .then(changed)
                      .catch((e) => setCloneError(errorText(e)))
                      .finally(() => setRemovingClone(""));
                  }}
                >
                  {removingClone === c.id ? "Removing…" : "Remove registration"}
                </Button>
              </li>
            ))}
          </ul>
          <CloneActions repository={repository.repository} changed={changed} />
        </div>
      ) : null}
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
  useLayoutEffect(() => {
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
        remove={() => remove().then(close)}
        save={async (settings) => {
          try {
            await request(`api/repositories/${clone.repository}/settings`, settings, "PUT");

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

/** Whether the daemon page runs in the desktop app. */
function hasFolderPicker() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}

function GeneralSection() {
  const [preferences, update] = usePreferences();
  return (
    <section aria-labelledby="settings-general-heading" className="settings-section">
      <SectionHead
        id="settings-general-heading"
        title="General"
        detail="Appearance and keyboard preferences on this device."
      />
      <div className="local-panel settings-list">
        <SettingRow label="Preferred theme" htmlFor="preferred-theme">
          <select
            id="preferred-theme"
            className="local-field settings-select"
            value={preferences.theme}
            onChange={(event) => update({ theme: event.target.value as typeof preferences.theme })}
          >
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
        </SettingRow>
        <SettingRow
          label="Show keyboard shortcuts with Alt/Option"
          htmlFor="keyboard-hints"
          detail="Hold Alt (Option on Mac) to show shortcut hints."
        >
          <input
            id="keyboard-hints"
            type="checkbox"
            role="switch"
            className="local-switch"
            checked={preferences.keyHints}
            onChange={(event) => update({ keyHints: event.target.checked })}
          />
        </SettingRow>
      </div>
    </section>
  );
}
