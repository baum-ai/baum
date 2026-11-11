import { Avatar } from "@baum/viewer/components/avatar";
import { BrandMark } from "@baum/viewer/components/brand-logo";
import { usePreferences, type PageModifier } from "./preferences";
import { PlanUsageSettings } from "./plan-usage-settings";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ComponentType,
  type FocusEvent,
  type ReactNode,
} from "react";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import {
  Alert,
  Badge,
  Button,
  CloseButton,
  LiveDot,
  SegmentedControl,
} from "@baum/viewer/components/ui";
import {
  formatCount,
  formatDateTime,
  formatRelative,
  githubAvatarURL,
} from "@baum/viewer/components/format";
import {
  appURL,
  automaticNotice,
  isActiveJob,
  navigate,
  sameRepository,
  parseAgentRequests,
  request,
  saveAgentSettings,
  type Automatic,
  type AutomaticStatus,
  type Clone,
  type Entry,
  type Job,
  type GitHubConnection,
  type Repository,
  type DiagnosticReport,
  type DecisionSettings,
  type PromptEntry,
  type RepositoryPromptEntry,
} from "./api";
import {
  RepositoryPicker,
  CloneActions,
  earlierNamesWarning,
  useCloneActions,
} from "./repositories";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { ListSection } from "./list";
import { GitHubConnectionSection } from "./github-connection";
import { OrganizationAccessNotice } from "./organization-access";
import { messages as githubMessages } from "./github-auth";
import { CodexHookWarning, type useCodexHookTrust } from "./codex-hooks";
import { DiagnosticsSection } from "./diagnostics";
import { pendingUpdate, UpdatesSection, type AppUpdates } from "./desktop/updates";
import { invoke } from "./desktop/native";
import { withCode } from "./desktop/code-terms";
import { Notice, PageBar, PanelHeader, RefreshButton } from "./shell";
import { listKeys, MAC, onBody, pageModifier, useRowFocus, useShortcuts } from "./shortcuts";
import {
  TerminalIcon,
  SessionsIcon,
  CheckIcon,
  SearchIcon,
  CloseIcon,
  PlusIcon,
  ChevronLeftIcon,
} from "@baum/viewer/components/icons";
import { DecisionsDisclosure, harnesses, harnessName, signIn } from "./desktop/reviews";
import { effortLabels } from "./model-effort";
import {
  describeCommand,
  commandMissing,
  commandMissingWarning,
  hooksMissing,
  hooksMissingWarning,
  describeAgent,
  agentConnected,
  agentUpdatable,
  agentHookNote,
  agentNeedsAction,
  agentUnsupported,
  terminalSetup,
  displayPath,
  type Agent,
  type SetupView,
  type Outcome,
  decisionModelDefaults,
  detectedHarnesses,
  signedOutHarnesses,
  modelName,
  modelPresets,
  withModel,
  type PullRequests,
  type ReviewSettings,
} from "./desktop/setup-model";
import { CommandText, useCopy } from "./desktop/terminal-setup";
import { useModal } from "./modal";
import { LoadingStatus } from "./nine-dots";
import { isCustom, PromptList, promptsNeedAttention } from "./prompt-editor";

/** The harness and model reviews use, and the ones fixes use: an empty
 *  `fix_backend` follows the review's. */
export type ReviewDefaults = {
  backend: string;
  model: string;
  /** Reasoning efforts; empty is the agent's own. */
  effort?: string;
  fix_backend?: string;
  fix_model?: string;
  fix_effort?: string;
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

/** The harnesses that can make data extraction's structured calls, those
 *  with a default decision model. */
const extractionHarnesses = Object.keys(decisionModelDefaults);

export const settingsSections = [
  "general",
  "reviews",
  "fixes",
  "connections",
  "computer",
  "prompts",
] as const;
export type SettingsSection = (typeof settingsSections)[number];
const sectionTitles: Record<SettingsSection, string> = {
  general: "General",
  reviews: "Reviews",
  fixes: "Fixes",
  connections: "Connections",
  computer: "This computer",
  prompts: "Prompts",
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

/** Settings' keys (`SETTINGS_SHORTCUTS`), beside Tab (`useTabStops`):
 *  the section list is the selection, as Up next's list is: j/↓ and k/↑ move
 *  it, 1 to 6 jump to a section, and Enter, l or → goes into the shown
 *  section. There j/↓ and k/↑ step through its controls, Enter or Space
 *  changes the focused one, and Esc, h or ← go back to the list. Esc in a text
 *  field or select gives it up first (`useEscapeBlurs`), leaving the place in
 *  the section, so j and k go on from it. */
function useSettingsKeys<S extends string>(
  sections: readonly S[],
  section: S,
  setSection: (section: S) => void,
  /** Esc on the section list: where the page goes back to, if anywhere. */
  back?: () => void,
) {
  const nav = useRef<HTMLElement>(null);
  const content = useRef<HTMLDivElement>(null);
  // The control last focused in the section, kept while a field is given up.
  const at = useRef<HTMLElement | null>(null);
  const navItem = (id: S) =>
    nav.current?.querySelector<HTMLElement>(`[data-section="${id}"]`) ?? null;
  const controls = () =>
    Array.from(content.current?.querySelectorAll<HTMLElement>(SECTION_CONTROLS) ?? []).filter(
      (control) => !control.closest("[hidden], [inert], details:not([open]) > :not(summary)"),
    );
  const focus = (target: HTMLElement | null | undefined) => {
    target?.focus({ preventScroll: true });
    target?.scrollIntoView?.({ block: "nearest" });
  };
  const show = (next: S) => {
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
    } else if (key === "Escape" && back) {
      event.preventDefault();
      back();
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

/** Saves review, fix and data extraction choices as they change. Each section sends all of its
 *  choices, so one PUT runs at a time and a change made meanwhile replaces any
 *  other waiting one: an older choice never lands after a newer one. The app
 *  reads its state again once nothing waits, and `Saved` shows for a moment.
 *  `failures` counts failed saves, so a section puts its controls back. */
function useAutoSave(changed: () => Promise<void>, path = "api/automatic") {
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
        await request(path, body, "PUT");
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

/** The prompts baum uses, read once and kept in step with each save so the
 *  section list and the section agree. */
export function usePrompts() {
  const [entries, setEntries] = useState<PromptEntry[] | null>(null);
  const [error, setError] = useState("");
  const load = () =>
    request<{ prompts: PromptEntry[] }>("api/settings/prompts")
      .then((body) => {
        setEntries(body.prompts ?? []);
        setError("");
      })
      .catch((e: unknown) => setError(errorText(e)));
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once; saves return their entry
  useEffect(() => {
    void load();
  }, []);
  const replace = (entry: PromptEntry) =>
    setEntries((all) => all?.map((e) => (e.id === entry.id ? entry : e)) ?? all);
  return { entries, error, load, replace };
}

/** Settings › Prompts: every prompt baum uses for reviews, fixes and data
 *  extraction, replaceable as a whole. */
export function PromptsSection({ prompts }: { prompts: ReturnType<typeof usePrompts> }) {
  return (
    <section aria-labelledby="settings-prompts-heading" className="settings-section">
      <SectionHead
        id="settings-prompts-heading"
        title="Prompts"
        detail="The instructions baum gives its agents. A custom prompt replaces the built-in one in every repository without a prompt of its own, set on its page under Repositories. Custom prompts stay on this computer."
      />
      {prompts.error ? (
        <Alert>
          {prompts.error}{" "}
          <Button variant="secondary" size="compact" onClick={() => void prompts.load()}>
            Try again
          </Button>
        </Alert>
      ) : null}
      {prompts.entries ? (
        <PromptList entries={prompts.entries} replace={prompts.replace} />
      ) : prompts.error ? null : (
        <LoadingStatus block>Reading the prompts…</LoadingStatus>
      )}
    </section>
  );
}

/** One repository's prompts, read once and kept in step with each save so
 *  the section list and the section agree. */
export function useRepositoryPrompts(repository: string) {
  const [entries, setEntries] = useState<RepositoryPromptEntry[] | null>(null);
  const [error, setError] = useState("");
  const load = () =>
    request<{ prompts: RepositoryPromptEntry[] }>(`api/repositories/${repository}/prompts`)
      .then((body) => {
        setEntries(body.prompts ?? []);
        setError("");
      })
      .catch((e: unknown) => setError(errorText(e)));
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once; saves return their entry
  useEffect(() => {
    void load();
  }, [repository]);
  const replace = (entry: PromptEntry) =>
    setEntries(
      (all) => all?.map((e) => (e.id === entry.id ? (entry as RepositoryPromptEntry) : e)) ?? all,
    );
  return { entries, error, load, replace };
}

/** A repository's Prompts section, each prompt saved straight to the daemon
 *  by its own editor. */
export function RepositoryPrompts({
  repository,
  prompts,
}: {
  repository: string;
  prompts: ReturnType<typeof useRepositoryPrompts>;
}) {
  return (
    <section aria-labelledby="repository-prompts-heading" className="settings-section">
      <SectionHead
        id="repository-prompts-heading"
        title="Prompts"
        detail="A prompt customised here replaces the global one for this repository only. Each saves on its own; unsaved text is lost when you leave this page."
      />
      {prompts.error ? (
        <Alert>
          {prompts.error}{" "}
          <Button variant="secondary" size="compact" onClick={() => void prompts.load()}>
            Try again
          </Button>
        </Alert>
      ) : null}
      {prompts.entries ? (
        <PromptList entries={prompts.entries} repository={repository} replace={prompts.replace} />
      ) : prompts.error ? null : (
        <LoadingStatus block>Reading the prompts…</LoadingStatus>
      )}
    </section>
  );
}

/** The harness and model a desktop app or daemon page shows, as one select
 *  per row. `same` adds a first choice that follows another setting. */
function AgentSelect({
  id,
  value,
  options,
  same,
  disabled,
  signedOut = [],
  onChange,
}: {
  id: string;
  value: string;
  options: string[];
  same?: string;
  disabled?: boolean;
  /** Harnesses the desktop app found signed out, labelled so. */
  signedOut?: readonly string[];
  onChange: (next: string) => void;
}) {
  // A saved harness the app no longer offers, such as a retired backend from
  // the config file, shows as chosen so choosing any offered one is a change.
  const unavailable = value !== "" && !options.includes(value) ? value : null;
  return (
    <select
      id={id}
      className="local-field settings-select"
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
    >
      {same !== undefined ? <option value="">{same}</option> : null}
      {unavailable !== null ? (
        <option value={unavailable} disabled>
          {harnessName(unavailable)} (no longer offered)
        </option>
      ) : null}
      {options.map((option) => (
        <option key={option} value={option}>
          {harnessName(option)}
          {signedOut.includes(option) ? " (not signed in)" : ""}
        </option>
      ))}
    </select>
  );
}

/** Why `work` will fail while `backend` is signed out, and how to sign in. */
function SignedOutCaution({ backend, work }: { backend: string; work: string }) {
  return (
    <span className="settings-caution">
      {harnessName(backend)} isn't signed in, so {work} will fail. {signIn[backend]}.
    </span>
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
            {modelName(model)}
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

const efforts: { value: string; label: string }[] = [
  { value: "", label: "Default" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
];

/** A use case's reasoning effort: "" leaves it to the agent. */
function EffortRow({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (next: string) => void;
}) {
  const items = efforts.some((item) => item.value === value)
    ? efforts
    : [...efforts, { value, label: effortLabels[value] ?? value }];
  return (
    <SettingRow
      label="Reasoning effort"
      detail="Default leaves it to the agent. Higher thinks longer and costs more."
    >
      <SegmentedControl label={label} value={value} items={items} onChange={onChange} />
    </SettingRow>
  );
}

/** The global review settings: whether baum reviews on its own, which pull
 *  requests, and the agent, model and effort that write reviews. */
function ReviewsSection({
  automatic,
  defaults,
  changed,
  available,
  signedOut = [],
}: {
  automatic: Automatic;
  defaults: ReviewDefaults;
  changed: () => Promise<void>;
  /** The agents found on this computer; undefined when the app cannot tell. */
  available?: readonly string[];
  /** Harnesses the desktop app found signed out. */
  signedOut?: readonly string[];
}) {
  const saved: ReviewSettings & { effort: string } = {
    ...withModel({
      backend: shownBackend(defaults.backend),
      model: defaults.backend === "auto" ? "" : defaults.model,
      automatic: automatic.enabled,
      pull_requests: (automatic.pull_requests ?? "both") as PullRequests,
    }),
    effort: defaults.effort ?? "",
  };
  const key = JSON.stringify(saved);
  const [draft, setDraft] = useState(saved);
  const { save, error, status, failures } = useAutoSave(changed);
  // A save, another window or the tray changes what is saved; a failed save
  // puts the controls back.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `saved`
  useEffect(() => setDraft(saved), [key, failures]);
  const change = (patch: Partial<typeof saved>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    save({
      enabled: next.automatic,
      backend: next.backend,
      model: next.model,
      effort: next.effort,
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
      {available?.length === 0 ? (
        <p role="status" className="settings-caution">
          Neither Claude Code nor Codex is installed on this computer, so baum can't write reviews.
          Install one and open it in your terminal to sign in; baum doesn't switch to another agent.
        </p>
      ) : null}
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
          detail={
            signedOut.includes(draft.backend) ? (
              <SignedOutCaution backend={draft.backend} work="reviews" />
            ) : harness ? (
              `Uses ${harness.detail.replace(/^Your/, "your")}.`
            ) : null
          }
        >
          <AgentSelect
            id="review-agent"
            value={draft.backend}
            options={harnesses.map((h) => h.id)}
            signedOut={signedOut}
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
        <EffortRow
          label="Review reasoning effort"
          value={draft.effort}
          onChange={(effort) => change({ effort })}
        />
      </div>
      <p className="settings-footnote">
        Changes save as you make them and apply to the next review in every repository.
      </p>
    </section>
  );
}

/** The agent and model that fix pull requests: the review's, or one of the
 *  harnesses that can fix. */
function FixesSection({
  defaults,
  changed,
  signedOut = [],
}: {
  defaults: ReviewDefaults;
  changed: () => Promise<void>;
  /** Harnesses the desktop app found signed out. */
  signedOut?: readonly string[];
}) {
  const review = shownBackend(defaults.backend);
  const savedBackend = defaults.fix_backend ?? "";
  const saved = {
    backend: savedBackend,
    model: savedBackend ? defaults.fix_model || (modelPresets[savedBackend]?.[0] ?? "") : "",
    effort: savedBackend ? (defaults.fix_effort ?? "") : "",
  };
  const key = JSON.stringify(saved);
  const [draft, setDraft] = useState(saved);
  const { save, error, status, failures } = useAutoSave(changed);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `saved`
  useEffect(() => setDraft(saved), [key, failures]);
  const change = (next: typeof saved) => {
    setDraft(next);
    save({ fix_backend: next.backend, fix_model: next.model, fix_effort: next.effort });
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
            ) : signedOut.includes(draft.backend || review) ? (
              <SignedOutCaution backend={draft.backend || review} work="fixes" />
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
            signedOut={signedOut}
            onChange={(backend) =>
              change({
                backend,
                model: backend ? (modelPresets[backend]?.[0] ?? "") : "",
                effort: backend ? draft.effort : "",
              })
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
        {draft.backend ? (
          <EffortRow
            label="Fix reasoning effort"
            value={draft.effort}
            onChange={(effort) => change({ ...draft, effort })}
          />
        ) : null}
      </div>
      <p className="settings-footnote">
        {draft.backend
          ? "Fixes use this agent in every repository."
          : "Fixes follow the review agent."}
      </p>
    </section>
  );
}

/** Whether the baum CLI or every agent's hooks are missing, for the sidebar's
 *  Settings badge and the Sessions page. Only the desktop app can tell; it
 *  rechecks on returning to the window (at most every 5 minutes, as the native
 *  check runs the agent CLIs) and at once when Settings changes the setup. */
export const COMMAND_LINE_FOCUS_INTERVAL_MS = 5 * 60_000;

export function useCommandLineCheck() {
  const desktop = hasFolderPicker();
  const [missing, setMissing] = useState(false);
  const [noHooks, setNoHooks] = useState(false);
  const checked = useRef(0);
  const last = useRef("");
  const recheck = useCallback(async () => {
    if (!desktop) return;
    checked.current = Date.now();
    try {
      const view = await invoke<SetupView>("setup_state");
      const key = JSON.stringify(view);
      if (key === last.current) return;
      last.current = key;
      setMissing(commandMissing(view));
      setNoHooks(hooksMissing(view));
    } catch {
      // Keep the last answer until a check succeeds.
    }
  }, [desktop]);
  useEffect(() => {
    if (!desktop) return;
    void recheck();
    const focus = () => {
      if (Date.now() - checked.current >= COMMAND_LINE_FOCUS_INTERVAL_MS) void recheck();
    };
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [desktop, recheck]);
  return {
    missing,
    hooksMissing: noHooks,
    warning:
      [missing ? commandMissingWarning : null, noHooks ? hooksMissingWarning : null]
        .filter(Boolean)
        .join(" ") || null,
    recheck,
  };
}

/** The desktop app's view of the CLI and agent hooks, and the actions that
 *  change them. Outside the desktop app it stays empty. */
function useDesktopSetup(onChanged?: () => Promise<void>) {
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
    void onChanged?.();
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

function connectedState(label: string, connected: boolean, attention = false) {
  const tone = connected
    ? "text-(--status-positive)"
    : attention
      ? "text-(--status-caution)"
      : "text-(--text-tertiary)";
  return (
    <span className={`inline-flex items-center gap-1.5 text-xs ${tone}`}>
      {connected ? <CheckIcon size={14} /> : attention ? <Dot tone="caution" /> : null}
      {label}
    </span>
  );
}

/** An agent's status badge: short, since the line below it explains a missing,
 *  too old or unreadable agent in full. */
function agentBadge(agent: Agent) {
  if (agentUnsupported(agent)) return "Needs a newer version";
  if (agent.error) return "Couldn't check";
  if (!agent.detected) return "Not found";
  return describeAgent(agent) || "Not connected";
}

/** The `baum desktop-setup` command for an agent baum found too old, with a
 *  button that copies it (setup's TerminalSetup). */
function TerminalSetupRow({ text, command }: { text: string; command: string }) {
  const { copied, copy } = useCopy();
  return (
    <div id="terminal-setup" className="border-t border-(--border-subtle) px-5 py-4">
      <p className="text-xs text-(--text-secondary)">{text}</p>
      <div className="terminal-command">
        <CommandText command={command} />
        <Button
          size="compact"
          variant="secondary"
          aria-label="Copy the command"
          onClick={() => void copy(command)}
        >
          {copied || "Copy"}
        </Button>
      </div>
    </div>
  );
}

/** Whether each coding agent's hooks are in, so baum can follow its work. */
/** The agents baum connects, shown while the first check runs. */
const checkingAgents = [
  { agent: "claude-code", name: "Claude Code" },
  { agent: "codex", name: "Codex" },
];

function AgentHooksPanel({ setup }: { setup: DesktopSetup }) {
  const { view, busy, doing, run, display } = setup;
  const terminal = view ? terminalSetup(view) : null;
  const missing = hooksMissing(view);
  // Checking shows beside each agent; the panel keeps its rows meanwhile.
  const checking = !view || (doing === "refresh" && busy);
  return (
    <>
      <section
        className="local-panel p-0!"
        aria-labelledby="hooks-heading"
        data-attention={missing ? "true" : undefined}
      >
        <PanelHeader
          id="hooks-heading"
          icon={<SessionsIcon size={20} />}
          title="Agent hooks"
          detail="Connect your coding agents so baum can follow their work."
          actions={
            <Button disabled={busy} size="tiny" variant="ghost" onClick={() => void run()}>
              {doing === "refresh" && busy ? "Checking…" : "Check again"}
            </Button>
          }
        />
        {view
          ? null
          : checkingAgents.map((agent) => (
              <div
                key={agent.agent}
                className="flex flex-wrap items-center justify-between gap-4 border-t border-(--border-subtle) px-5 py-4"
              >
                <div className="flex min-w-0 flex-[1_1_16rem] items-center gap-3">
                  <BrandMark
                    brand={agent.agent === "codex" ? "openai" : "claude"}
                    size={24}
                    className="shrink-0"
                  />
                  <h3 className="text-base font-medium">{agent.name}</h3>
                  <div className="text-xs text-(--text-tertiary)">
                    <LoadingStatus>Checking…</LoadingStatus>
                  </div>
                </div>
              </div>
            ))}
        {view?.hooks.agents.map((agent) => (
          <div
            key={agent.agent}
            className="flex flex-wrap items-center justify-between gap-4 border-t border-(--border-subtle) px-5 py-4"
          >
            <div className="flex min-w-0 flex-[1_1_16rem] items-start gap-3">
              <BrandMark
                brand={agent.agent === "codex" ? "openai" : "claude"}
                size={24}
                className="mt-0.5 shrink-0"
              />
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-3">
                  <h3 className="text-base font-medium">{agent.name}</h3>
                  {checking ? (
                    <div className="text-xs text-(--text-tertiary)">
                      <LoadingStatus>Checking…</LoadingStatus>
                    </div>
                  ) : (
                    connectedState(
                      agentBadge(agent),
                      agentConnected(agent),
                      agentNeedsAction(agent) || agentUnsupported(agent),
                    )
                  )}
                  {agent.installed && agent.version ? (
                    <span className="text-xs text-(--text-tertiary)">Plugin {agent.version}</span>
                  ) : null}
                </div>
                {(agent.error || !agent.installed || agentUnsupported(agent)) &&
                describeAgent(agent) ? (
                  <p className="text-xs text-(--text-secondary)">{display(describeAgent(agent))}</p>
                ) : null}
                {agent.installed ? (
                  <p className="text-xs text-(--text-tertiary)">
                    {withCode(agentHookNote(agent), ["/hooks"])}
                  </p>
                ) : null}
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {agent.detected &&
              !agent.error &&
              !agentUnsupported(agent) &&
              (!agent.installed || !agent.enabled) ? (
                <Button
                  disabled={busy}
                  size="compact"
                  variant={missing ? "primary" : "secondary"}
                  onClick={() => void run("install_hooks", { agent: agent.agent })}
                >
                  {doing === `install_hooks:${agent.agent}`
                    ? agent.installed
                      ? "Enabling…"
                      : "Connecting…"
                    : `${agent.installed ? "Enable" : "Connect"} ${agent.name}`}
                </Button>
              ) : null}
              {agentUpdatable(agent) ? (
                <Button
                  disabled={busy}
                  size="compact"
                  variant="secondary"
                  aria-label={`Update ${agent.name} hooks`}
                  onClick={() => void run("update_hooks", { agent: agent.agent })}
                >
                  {doing === `update_hooks:${agent.agent}` && busy ? "Updating…" : "Update"}
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
        {terminal ? <TerminalSetupRow {...terminal} /> : null}
      </section>
      {view?.hooks.error ? <Alert>{display(view.hooks.error)}</Alert> : null}
    </>
  );
}

/** The baum CLI the desktop app installs, with install, update and removal. */
function CommandLinePanel({ setup }: { setup: DesktopSetup }) {
  const { view, busy, doing, run, display } = setup;
  const command = view ? describeCommand(view.command) : null;
  const missing = commandMissing(view);
  return (
    <section
      className="local-panel p-0!"
      aria-labelledby="cli-heading"
      data-attention={missing ? "true" : undefined}
    >
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
              ? connectedState(command.ok ? "Installed" : "Not installed", !!command.ok, missing)
              : null}
          </div>
          {command ? (
            <p className="break-all text-xs text-(--text-secondary)">
              {withCode(display(command.state), [command.path && display(command.path)])}
            </p>
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
              variant={missing ? "primary" : "secondary"}
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
  const connected = agents.filter(agentConnected);
  const waiting = agents.filter((a) => a.error || (a.detected && !agentConnected(a)));
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
  authChanged,
  codexTrust,
  commandLine,
  updates,
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
  authChanged: () => void;
  codexTrust?: ReturnType<typeof useCodexHookTrust>;
  /** The app shell's CLI check, rechecked when Settings changes the setup. */
  commandLine?: ReturnType<typeof useCommandLineCheck>;
  /** The desktop app's updater, from the app shell. */
  updates?: AppUpdates;
}) {
  const setup = useDesktopSetup(async () => {
    await Promise.all([codexTrust?.recheck(), commandLine?.recheck()]);
  });
  const cliMissing = setup.desktop && commandMissing(setup.view);
  const noHooks = setup.desktop && hooksMissing(setup.view);
  const update = pendingUpdate(updates?.view ?? null);
  const prompts = usePrompts();
  // Only the desktop app can ask the agent CLIs whether they are signed in.
  const signedOut = setup.desktop && setup.view ? signedOutHarnesses(setup.view.hooks.agents) : [];
  const gh = githubHealth(github);
  const hooks = setup.desktop ? hooksHealth(setup.view) : null;
  const fix = defaults ? fixSummary(defaults) : null;
  const connectionTone: Tone =
    gh.tone === "critical"
      ? "critical"
      : hooks?.tone === "caution" || codexTrust?.warning || noHooks
        ? "caution"
        : gh.tone;
  // `count` repeats the sidebar's Settings badge where it comes from.
  const nav: { id: SettingsSection; sub: string; tone?: Tone; count?: number }[] = [
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
      sub: noHooks
        ? "No coding agent connected"
        : setup.desktop
          ? "GitHub and agent hooks"
          : "GitHub",
      tone:
        connectionTone === "critical" || connectionTone === "caution" ? connectionTone : undefined,
    },
    {
      id: "computer",
      sub:
        problems > 0
          ? `${problems} ${problems === 1 ? "problem" : "problems"} found`
          : cliMissing
            ? "baum CLI not installed"
            : update
              ? "Update available"
              : setup.desktop
                ? "Diagnostics, CLI, environment"
                : "Diagnostics, environment",
      tone: problems > 0 ? "critical" : cliMissing ? "caution" : undefined,
      count: update ? 1 : undefined,
    },
    {
      id: "prompts",
      sub: prompts.entries
        ? `${prompts.entries.filter(isCustom).length} custom`
        : prompts.error
          ? "Unavailable"
          : "Reading…",
      tone: prompts.entries && promptsNeedAttention(prompts.entries) ? "caution" : undefined,
    },
  ];
  const keys = useSettingsKeys(
    nav.map((item) => item.id),
    section,
    setSection,
  );
  // Set by the Codex warning's button; the panel scrolls into view once shown.
  const [showHooks, setShowHooks] = useState(false);
  useEffect(() => {
    if (!showHooks || section !== "connections") return;
    document.getElementById("hooks-heading")?.scrollIntoView?.({ block: "start" });
    setShowHooks(false);
  }, [showHooks, section]);
  // Set by the CLI banner's button, the same way.
  const [showCommandLine, setShowCommandLine] = useState(false);
  useEffect(() => {
    if (!showCommandLine || section !== "computer") return;
    document.getElementById("cli-heading")?.scrollIntoView?.({ block: "center" });
    setShowCommandLine(false);
  }, [showCommandLine, section]);
  const loading = <LoadingStatus block>Reading your review settings…</LoadingStatus>;
  return (
    <section className="settings-page">
      <PageBar
        title="Settings"
        detail="How baum reviews and fixes pull requests on this computer"
      />
      {diagnosticsOpen ? (
        <DiagnosticsDialog close={() => setDiagnosticsOpen(false)} onReport={onDiagnostics} />
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
                  {item.count ? (
                    <span
                      className="settings-nav-count"
                      aria-hidden="true"
                      title="Update available"
                    >
                      {item.count}
                    </span>
                  ) : null}
                </span>
                <span className="settings-nav-sub">{item.sub}</span>
              </span>
            </button>
          ))}
        </nav>
        <div ref={keys.content} className="settings-content" onFocus={keys.onFocus}>
          <CodexHookWarning
            check={codexTrust}
            open={
              setup.desktop
                ? () => {
                    setSection("connections");
                    setShowHooks(true);
                  }
                : undefined
            }
          />
          {noHooks ? (
            <Notice
              action={
                <button
                  type="button"
                  onClick={() => {
                    setSection("connections");
                    setShowHooks(true);
                  }}
                >
                  Connect an agent
                </button>
              }
            >
              {hooksMissingWarning}
            </Notice>
          ) : null}
          {cliMissing ? (
            <Notice
              action={
                <button
                  type="button"
                  onClick={() => {
                    setSection("computer");
                    setShowCommandLine(true);
                  }}
                >
                  Install the CLI
                </button>
              }
            >
              {commandMissingWarning}
            </Notice>
          ) : null}
          {section === "general" ? (
            <GeneralSection />
          ) : section === "reviews" ? (
            automatic && defaults ? (
              <>
                <ReviewsSection
                  automatic={automatic}
                  defaults={defaults}
                  changed={changed}
                  available={
                    setup.desktop && setup.view && !setup.view.hooks.error
                      ? detectedHarnesses(setup.view.hooks.agents)
                      : undefined
                  }
                  signedOut={signedOut}
                />
                {/* A new review harness changes what capture resolves to. */}
                <DecisionSettingsSection
                  key={defaults.backend}
                  defaults={defaults}
                  signedOut={signedOut}
                />
              </>
            ) : (
              loading
            )
          ) : section === "fixes" ? (
            defaults ? (
              <FixesSection defaults={defaults} changed={changed} signedOut={signedOut} />
            ) : (
              loading
            )
          ) : section === "prompts" ? (
            <PromptsSection prompts={prompts} />
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
              <GitHubConnectionSection onChanged={authChanged} />
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
              {updates ? <UpdatesSection updates={updates} /> : null}
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
                <JobConcurrencyRow />
                <TimeLimitRows />
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

/** The most jobs the daemon runs at once (repos.MaxJobConcurrency). */
const MAX_JOB_CONCURRENCY = 4;

/** How many reviews, fixes and feature builds run at once; questions to a
 *  review's agent run beside them. The daemon applies a change at once. */
function JobConcurrencyRow() {
  const [value, setValue] = useState<number | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    request<{ job_concurrency?: number }>("api/settings").then(
      (settings) => setValue(settings.job_concurrency ?? MAX_JOB_CONCURRENCY),
      (e) => setError(errorText(e)),
    );
  }, []);
  async function choose(next: number) {
    if (saving) return;
    const before = value;
    setValue(next);
    setSaving(true);
    setError("");
    try {
      const answer = await request<{ job_concurrency: number }>(
        "api/settings/job-concurrency",
        { job_concurrency: next },
        "PUT",
      );
      setValue(answer.job_concurrency);
    } catch (e) {
      setValue(before);
      setError(errorText(e));
    } finally {
      setSaving(false);
    }
  }
  return (
    <SettingRow
      label="Jobs at once"
      htmlFor="job-concurrency"
      detail={
        error ? (
          <Alert>{error}</Alert>
        ) : (
          "How many reviews, fixes and feature builds run in parallel. Lower it if they slow this computer down; running jobs are not stopped."
        )
      }
    >
      <select
        id="job-concurrency"
        className="local-field settings-select"
        disabled={value === null}
        value={value ?? MAX_JOB_CONCURRENCY}
        onChange={(e) => void choose(Number(e.target.value))}
      >
        {Array.from({ length: MAX_JOB_CONCURRENCY }, (_, i) => i + 1).map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </SettingRow>
  );
}

type TimeLimits = { review_time_limit_minutes: number; run_time_limit_minutes: number };

/** The daemon's defaults (config.DefaultReviewTimeout, DefaultRunTimeout). */
const DEFAULT_TIME_LIMITS: TimeLimits = {
  review_time_limit_minutes: 10,
  run_time_limit_minutes: 30,
};

/** The minutes offered, up to repos.MaxTimeLimit. */
const TIME_LIMIT_CHOICES = [5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 240];

/** How long a review, and any other agent run, may take before it stops.
 *  Runs have no call or token limit; a change applies to runs started after
 *  it. */
function TimeLimitRows() {
  const [value, setValue] = useState<TimeLimits | null>(null);
  const [saving, setSaving] = useState(false);
  // The row whose change failed shows why; a failed load shows on the first.
  const [error, setError] = useState<{ key: keyof TimeLimits; text: string } | null>(null);
  useEffect(() => {
    request<Partial<TimeLimits>>("api/settings").then(
      (settings) =>
        setValue({
          review_time_limit_minutes:
            settings.review_time_limit_minutes ?? DEFAULT_TIME_LIMITS.review_time_limit_minutes,
          run_time_limit_minutes:
            settings.run_time_limit_minutes ?? DEFAULT_TIME_LIMITS.run_time_limit_minutes,
        }),
      (e) => setError({ key: "review_time_limit_minutes", text: errorText(e) }),
    );
  }, []);
  async function choose(key: keyof TimeLimits, minutes: number) {
    if (!value || saving) return;
    const before = value;
    setValue({ ...value, [key]: minutes });
    setSaving(true);
    setError(null);
    try {
      setValue(
        await request<TimeLimits>("api/settings/time-limits", { ...value, [key]: minutes }, "PUT"),
      );
    } catch (e) {
      setValue(before);
      setError({ key, text: errorText(e) });
    } finally {
      setSaving(false);
    }
  }
  const row = (key: keyof TimeLimits, id: string, label: string, detail: string) => {
    const current = value?.[key] ?? DEFAULT_TIME_LIMITS[key];
    const choices = TIME_LIMIT_CHOICES.includes(current)
      ? TIME_LIMIT_CHOICES
      : [...TIME_LIMIT_CHOICES, current].sort((a, b) => a - b);
    return (
      <SettingRow
        label={label}
        htmlFor={id}
        detail={error?.key === key ? <Alert>{error.text}</Alert> : detail}
      >
        <select
          id={id}
          className="local-field settings-select"
          disabled={value === null}
          value={current}
          onChange={(e) => void choose(key, Number(e.target.value))}
        >
          {choices.map((minutes) => (
            <option key={minutes} value={minutes}>
              {minutes} min
            </option>
          ))}
        </select>
      </SettingRow>
    );
  };
  return (
    <>
      {row(
        "review_time_limit_minutes",
        "review-time-limit",
        "Review time limit",
        "How long a review may run before it stops. Reviews have no call or token limit.",
      )}
      {row(
        "run_time_limit_minutes",
        "run-time-limit",
        "Fix time limit",
        "How long a fix, a question to a review's agent or a feature build may run before it stops.",
      )}
    </>
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

/** Data extraction, decision capture and the feature log: the switch, the
 *  agent and the model, saved as they change like the review settings. */
function DecisionSettingsSection({
  defaults,
  signedOut = [],
}: {
  defaults: ReviewDefaults;
  /** Harnesses the desktop app found signed out. */
  signedOut?: readonly string[];
}) {
  const [saved, setSaved] = useState<DecisionSettings | null>(null);
  const [loadError, setLoadError] = useState("");
  const load = () =>
    request<{ decisions?: DecisionSettings }>("api/settings/decisions").then((settings) => {
      if (settings.decisions) setSaved(settings.decisions);
    });
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once; saves read again
  useEffect(() => {
    load().catch((e: unknown) => setLoadError(errorText(e)));
  }, []);
  const { save, error, status, failures } = useAutoSave(load, "api/settings/decisions");
  // The agent is "" while it follows the reviews', the model "" while it is
  // the agent's default.
  const choice = saved && {
    enabled: saved.enabled,
    backend: saved.backend,
    model: saved.model,
    effort: saved.effort ?? "",
  };
  const key = JSON.stringify(choice);
  const [draft, setDraft] = useState(choice);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `key` stands for `choice`
  useEffect(() => setDraft(choice), [key, failures]);
  if (!saved || !draft) return loadError ? <Alert>{loadError}</Alert> : null;
  const review = shownBackend(defaults.backend);
  // The daemon resolves the harness extraction uses, auto included.
  const resolve = (chosen: string) =>
    chosen || (!saved.backend && saved.effective_backend) || review;
  const backend = resolve(draft.backend);
  const fallback = decisionModelDefaults[backend] ?? "";
  const change = (patch: Partial<typeof draft>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    // The default is saved as none, so it follows the agent.
    const model = next.model === decisionModelDefaults[resolve(next.backend)] ? "" : next.model;
    save({ enabled: next.enabled, backend: next.backend, model, effort: next.effort });
  };
  // A saved model the daemon reports extraction ignores is shown as such.
  const ignored =
    saved.model_ignored && saved.model && draft.backend === saved.backend ? saved.model : "";
  const off = !draft.enabled;
  return (
    <section aria-labelledby="decisions-heading" className="settings-section">
      <SectionHead
        id="decisions-heading"
        title="Data extraction"
        detail="The agent that records the decisions of your agent sessions and keeps the feature log, in the background. A small model keeps this cheap."
        status={status}
      />
      {error ? <Alert>{error}</Alert> : null}
      <div className="local-panel settings-list">
        <SettingRow
          label="Extract decisions and features"
          htmlFor="decisions-enabled"
          detail="Shows the decisions beside each pull request and the features in Timeline."
        >
          <input
            id="decisions-enabled"
            type="checkbox"
            role="switch"
            className="local-switch"
            checked={draft.enabled}
            onChange={(event) => change({ enabled: event.target.checked })}
          />
        </SettingRow>
        <SettingRow
          label="Agent that extracts"
          htmlFor="decisions-agent"
          detail={
            !fallback ? (
              <span className="settings-caution">
                Reviews use {harnessName(backend)}, which can't extract decisions and features.
                Choose another agent.
              </span>
            ) : !saved.effective_backend && !draft.backend ? (
              saved.backend_undetermined ? (
                "The agent extraction uses could not be determined: its check did not answer in time."
              ) : (
                <span className="settings-caution">
                  No agent that can extract decisions and features is available on this computer, so
                  extraction does not run.
                </span>
              )
            ) : signedOut.includes(backend) && !off ? (
              <SignedOutCaution backend={backend} work="extraction" />
            ) : draft.backend ? null : (
              `Now ${harnessName(backend)}.`
            )
          }
        >
          <AgentSelect
            id="decisions-agent"
            value={draft.backend}
            options={extractionHarnesses}
            same="Same as reviews"
            signedOut={signedOut}
            disabled={off}
            // A new harness lists other models, so the model starts over.
            onChange={(next) => change({ backend: next, model: "" })}
          />
        </SettingRow>
        {fallback ? (
          <SettingRow
            label="Model"
            htmlFor="decisions-model"
            detail={
              ignored && draft.model === ignored ? (
                <span className="settings-caution">
                  {harnessName(backend)} doesn't use {ignored}, so extraction uses {fallback}.
                  Choose a model to replace it.
                </span>
              ) : null
            }
          >
            <ModelField
              key={backend}
              id="decisions-model"
              backend={backend}
              value={draft.model || fallback}
              disabled={off}
              onChange={(model) => change({ model })}
            />
          </SettingRow>
        ) : null}
        {fallback ? (
          <EffortRow
            label="Extraction reasoning effort"
            value={draft.effort}
            onChange={(effort) => change({ effort })}
          />
        ) : null}
      </div>
      <div className="settings-footnote">
        <DecisionsDisclosure />
      </div>
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
          {rows.length === 0 ? (
            <p className="text-xs text-(--text-tertiary)">No variables set.</p>
          ) : null}
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
}: {
  close: () => void;
  onReport: (report: DiagnosticReport | null) => void;
}) {
  const dialog = useModal();
  const title = useRef<HTMLHeadingElement>(null);
  // Open on the title rather than on the close control, the first button.
  useEffect(() => title.current?.focus({ preventScroll: true }), []);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="diagnostics-dialog-title"
      className="local-dialog-wide"
    >
      <div className="local-dialog-head">
        <div>
          <h2 ref={title} id="diagnostics-dialog-title" tabIndex={-1} className="outline-none">
            Check this computer
          </h2>
          <p>What baum needs here, what it found, and how to fix each problem.</p>
        </div>
        <CloseButton onClick={close} />
      </div>
      <DiagnosticsSection onReport={onReport} />
    </dialog>
  );
}

export function RepositoriesPage({
  repositories,
  loading = false,
  automatic,
  github,
  entries = [],
  jobs = [],
  changed,
  Link,
}: {
  repositories: Repository[];
  /** Whether the first read of the repositories is still running. */
  loading?: boolean;
  automatic: Automatic | null;
  github?: GitHubConnection | null;
  /** The pull requests the app knows, for each repository's open count and
   *  latest review. */
  entries?: Entry[];
  /** The jobs, for the reviews running in each repository. */
  jobs?: Job[];
  /** Reads everything again; a refresh pressed again restarts the read. */
  changed: (how?: { restart?: boolean }) => Promise<void>;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const [adding, setAdding] = useState(false);
  // The keyboard moves through the repositories as through any list, onto
  // each one's Settings; `a` opens or closes the picker and `/` searches it.
  // One section per owner, in the order the owners first appear.
  const owners = new Map<string, Repository[]>();
  for (const repository of repositories) {
    const owner = repository.repository.split("/")[0] ?? "";
    owners.set(owner, [...(owners.get(owner) ?? []), repository]);
  }
  const rows = useRowFocus<HTMLDivElement>(
    "settings-repositories",
    [...owners.values()].flat().map((r) => r.repository),
  );
  const page = useRef<HTMLElement>(null);
  const menu = useContextMenu();
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
  return (
    <section ref={page}>
      <PageBar
        title="Repositories"
        detail="GitHub repositories baum follows on this computer"
        actions={
          <>
            <Button size="compact" onClick={() => setAdding(!adding)}>
              {adding ? "Done" : "Add repositories"}
              <kbd aria-hidden="true" className="key-hint">
                a
              </kbd>
            </Button>
            <RefreshButton refresh={(run) => changed({ restart: run.restart })} />
          </>
        }
      />
      <div className="local-page-body local-list-page">
        {github?.organization_access ? (
          <div className="local-list-note">
            <OrganizationAccessNotice
              access={github.organization_access}
              recheck={async () => {
                await request("api/github-auth/retry", {});
                await changed();
              }}
            />
          </div>
        ) : null}
        {adding ? (
          <div className="local-list-note local-repo-adding">
            <RepositoryPicker changed={changed} autoFocus showOrganizations={false} />
          </div>
        ) : null}
        {repositories.length === 0 && loading ? (
          <LoadingStatus block>Loading repositories…</LoadingStatus>
        ) : repositories.length === 0 ? (
          <div className="local-empty">
            <h2>No repositories yet</h2>
            <p>
              Pick GitHub repositories to see their pull requests. baum finds their local clones for
              you.
            </p>
          </div>
        ) : null}
        {menu.menu}
        {repositories.length > 0 ? (
          <div ref={rows.list}>
            {[...owners].map(([owner, owned]) => (
              <ListSection
                key={owner}
                title={owner}
                count={owned.length}
                icon={
                  <Avatar src={githubAvatarURL(owner, 32)} name={owner} size={16} shape="square" />
                }
              >
                {owned.map((repository) => (
                  <RepositoryRow
                    key={repository.repository}
                    menu={menu}
                    focused={rows.focused === repository.repository}
                    repository={repository}
                    activity={repositoryActivity(repository.repository, entries, jobs)}
                    automatic={automatic}
                    changed={changed}
                    Link={Link}
                  />
                ))}
              </ListSection>
            ))}
          </div>
        ) : null}
      </div>
    </section>
  );
}

/** How a repository's automatic reviews are set: by the global switch, or
 *  on or off for it alone. */
type AutomaticMode = "default" | "on" | "off";
const automaticMode = (clone: Clone): AutomaticMode =>
  clone.settings.automatic === null || clone.settings.automatic === undefined
    ? "default"
    : clone.settings.automatic
      ? "on"
      : "off";

/** A repository's Reviews section, saved as it changes, as Settings' are. */
function RepositoryReviews({
  clone,
  automatic,
  status,
  changed,
}: {
  clone: Clone;
  automatic: Automatic | null;
  status?: AutomaticStatus;
  changed: () => Promise<void>;
}) {
  const saver = useAutoSave(changed, `api/repositories/${clone.repository}/settings`);
  const stored = () => ({
    mode: automaticMode(clone),
    decisions: clone.settings.decisions !== false,
  });
  const [draft, setDraft] = useState(stored);
  // A failed save puts the controls back to what is stored.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only after a failure
  useEffect(() => {
    if (saver.failures) setDraft(stored());
  }, [saver.failures]);
  const change = (patch: Partial<ReturnType<typeof stored>>) => {
    const next = { ...draft, ...patch };
    setDraft(next);
    saver.save({
      automatic: next.mode === "default" ? null : next.mode === "on",
      decisions: next.decisions ? null : false,
    });
  };
  const notice = status ? automaticNotice(status) : null;
  return (
    <section aria-labelledby="repository-reviews-heading" className="settings-section">
      <SectionHead
        id="repository-reviews-heading"
        title="Reviews"
        detail="How baum reviews this repository's pull requests and reads its agent sessions."
        status={saver.status}
      />
      {saver.error ? <Alert>{saver.error}</Alert> : null}
      {notice ? <Notice>{notice}</Notice> : null}
      <div className="local-panel settings-list">
        {automatic ? (
          <SettingRow
            label="Automatic reviews"
            htmlFor="repository-automatic"
            detail={
              status ? (
                <span role="status">
                  {status.enabled
                    ? "On for this repository."
                    : clone.settings.automatic === false
                      ? "Off for this repository."
                      : `Paused by the global switch${
                          clone.settings.automatic ? ", although this repository is set to On" : ""
                        }. Turn them on in Settings to resume them.`}
                  {status.pending ? ` ${status.pending} waiting to start.` : ""}
                </span>
              ) : (
                "New pushes and review requests in this repository."
              )
            }
          >
            <select
              id="repository-automatic"
              className="local-field settings-select"
              value={draft.mode}
              onChange={(e) => change({ mode: e.target.value as AutomaticMode })}
            >
              <option value="default">Global setting ({automatic.enabled ? "on" : "off"})</option>
              <option value="on">
                {automatic.enabled ? "On" : "On (paused by the global switch)"}
              </option>
              <option value="off">Off</option>
            </select>
          </SettingRow>
        ) : null}
        <SettingRow
          label="Capture decisions"
          htmlFor="repository-decisions"
          detail="Read the decisions of agent sessions in this repository."
        >
          <input
            id="repository-decisions"
            type="checkbox"
            role="switch"
            className="local-switch"
            checked={draft.decisions}
            onChange={(event) => change({ decisions: event.target.checked })}
          />
        </SettingRow>
      </div>
    </section>
  );
}

/** What a repository has going on: its open pull requests, when baum last
 *  reviewed one of them, and how many reviews are running now. */
type RepositoryActivity = { open: number; reviewed: string | null; running: number };

function repositoryActivity(repository: string, entries: Entry[], jobs: Job[]): RepositoryActivity {
  const own = entries.filter((e) => sameRepository(e.pull_request.repository, repository));
  const reviewed = own
    .map((e) => e.latest?.generated_at)
    .filter((at): at is string => !!at)
    .sort()
    .at(-1);
  return {
    // The index lists a pull request once per clone store, so count each
    // pull request once however many registered clones see it.
    open: new Set(own.filter((e) => !e.historical).map((e) => e.pull_request.number)).size,
    reviewed: reviewed ?? null,
    running: jobs.filter(
      (job) => !job.kind && isActiveJob(job) && sameRepository(job.repository, repository),
    ).length,
  };
}

/** A clone's path with the home folder written as `~`. */
const homePath = (path: string) => path.replace(/^\/(?:Users|home)\/[^/]+(?=\/|$)/, "~");

/** One registered repository: what it does at a glance; its name opens its
 *  page. */
function RepositoryRow({
  repository,
  activity,
  menu,
  focused,
  automatic,
  changed,
  Link,
}: {
  repository: Repository;
  activity: RepositoryActivity;
  /** The list's menu, which the row's ⋯ and a right click open. */
  menu: ReturnType<typeof useContextMenu>;
  focused: boolean;
  automatic: Automatic | null;
  changed: () => Promise<void>;
  Link: ComponentType<ViewerLinkProps>;
}) {
  // The panel that lists the registered clones and adds another.
  const [changing, setChanging] = useState(false);
  const [cloneError, setCloneError] = useState("");
  const [removingClone, setRemovingClone] = useState("");
  const clone = primaryClone(repository);
  const href = repositoryHref(repository.repository);
  const name = clone.repository.split("/").slice(1).join("/");
  const status = automatic?.repositories.find(
    (item) => item.repository === clone.repository.toLowerCase(),
  );
  // A repository without a clone is offered one from its menu; what that
  // starts (a path form, a running clone) shows under the row.
  const cloneActions = useCloneActions({ repository: repository.repository, changed });
  // The row opens its settings; the rest is in its menu.
  const actions = (): MenuItem[] => {
    const items: MenuItem[] = [{ label: "Settings", hint: "↵", onSelect: () => navigate(href) }];
    if (clone.available)
      items.push({
        label: changing ? "Hide local clones" : "Change local clone…",
        onSelect: () => setChanging(!changing),
      });
    else if (!cloneActions.busy && !cloneActions.running)
      items.push(
        { label: "Choose folder…", onSelect: cloneActions.choose },
        { label: "Clone it…", onSelect: cloneActions.clone },
      );
    return items;
  };
  const cloneState =
    !clone.available && (cloneActions.progress || cloneActions.form || cloneActions.errors);
  return (
    <li
      data-row-id={repository.repository}
      data-focused={focused || undefined}
      className="list-row local-repo-row"
      onContextMenu={(event) => menu.open(event, actions(), clone.repository)}
    >
      <div className="local-repo-main">
        <h3>
          <Link
            href={href}
            className="local-repo-name"
            data-row-link=""
            aria-label={`Settings for ${clone.repository}`}
          >
            {name}
          </Link>
        </h3>
        {clone.available ? (
          <p className="truncate" title={clone.path}>
            {homePath(clone.path)}
          </p>
        ) : (
          <p className="local-repo-missing">
            {cloneActions.busy && cloneActions.doing === "choose"
              ? "Choosing a folder…"
              : cloneActions.busy && cloneActions.doing === "clone"
                ? "Preparing the clone…"
                : "Needs a local clone"}
          </p>
        )}
        {clone.available && repository.earlier_names?.length ? (
          <p className="repository-warning">{earlierNamesWarning(repository.earlier_names)}</p>
        ) : null}
      </div>
      <span className="local-repo-state">
        {activity.running ? (
          <>
            <LiveDot className="text-(--status-positive)" />
            {activity.running === 1 ? "Reviewing" : `Reviewing ${formatCount(activity.running)}`}
          </>
        ) : status?.pending ? (
          `${formatCount(status.pending)} waiting`
        ) : status && !status.enabled ? (
          // Only the exception is marked: automatic reviews are on by default.
          <Badge size="compact">
            {clone.settings.automatic === false
              ? "Automatic reviews off"
              : "Automatic reviews paused"}
          </Badge>
        ) : null}
      </span>
      <span className="local-repo-figure">
        {activity.open === 0
          ? "No open PRs"
          : `${formatCount(activity.open)} open ${activity.open === 1 ? "PR" : "PRs"}`}
      </span>
      <span
        className="local-repo-figure"
        title={activity.reviewed ? formatDateTime(activity.reviewed) : undefined}
      >
        {activity.reviewed ? `Reviewed ${formatRelative(activity.reviewed)}` : "Not reviewed yet"}
      </span>
      <div className="local-repo-actions">
        <MenuButton label={clone.repository} items={actions} menu={menu} />
      </div>
      {cloneError ? (
        <p role="alert" className="local-repo-panel">
          {cloneError}
        </p>
      ) : null}
      {cloneState ? (
        <div className="local-repo-panel repository-actions">
          {cloneActions.progress}
          {cloneActions.form}
          {cloneActions.errors}
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
          <Button
            variant="ghost"
            size="compact"
            className="justify-self-start"
            onClick={() => setChanging(false)}
          >
            Done
          </Button>
        </div>
      ) : null}
    </li>
  );
}

/** The clone baum uses for a repository: an available one, else the first,
 *  else a stand-in for one that has none. */
function primaryClone(repository: Repository): Clone {
  return (
    repository.clones.find((c) => c.available) ??
    repository.clones[0] ?? {
      id: repository.repository,
      path: "Needs a local clone",
      repository: repository.repository,
      available: false,
      settings: repository.settings,
    }
  );
}

/** The address of a repository's page. */
export const repositoryHref = (repository: string) =>
  appURL(`?page=repositories&repository=${encodeURIComponent(repository)}`);

const repositorySections = ["reviews", "prompts", "registration"] as const;
export type RepositorySection = (typeof repositorySections)[number];
const repositorySectionTitles: Record<RepositorySection, string> = {
  reviews: "Reviews",
  prompts: "Prompts",
  registration: "Registration",
};
/** A section named in the address, else the first. */
export const repositorySection = (value: string | null): RepositorySection =>
  repositorySections.find((s) => s === value) ?? "reviews";

/** `?page=repositories&repository=<owner/name>&section=`: one repository's
 *  settings, drawn as Settings is, its sections in a list down the left. */
export function RepositoryPage({
  name,
  repositories,
  loading,
  automatic,
  changed,
  section,
  setSection,
  Link,
}: {
  /** The repository in the address. */
  name: string;
  repositories: Repository[];
  loading: boolean;
  automatic: Automatic | null;
  changed: () => Promise<void>;
  section: RepositorySection;
  setSection: (section: RepositorySection) => void;
  Link: ComponentType<ViewerLinkProps>;
}) {
  const repository = repositories.find((r) => sameRepository(r.repository, name));
  const prompts = useRepositoryPrompts(repository?.repository ?? name);
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState(false);
  const toList = () => navigate(appURL("?page=repositories"));
  const keys = useSettingsKeys(repositorySections, section, setSection, toList);
  const back = (
    <Link
      href={appURL("?page=repositories")}
      className="local-page-back"
      aria-label="Back to repositories"
      title="Back to repositories (Esc)"
    >
      <ChevronLeftIcon size={18} />
      <kbd aria-hidden="true" className="key-hint">
        Esc
      </kbd>
    </Link>
  );
  if (!repository)
    return (
      <section>
        <PageBar title={name} back={back} />
        <div className="local-page-body">
          {loading ? (
            <LoadingStatus block>Reading the repositories…</LoadingStatus>
          ) : (
            <div className="local-empty">
              <h2>This repository is not registered</h2>
              <p>
                <Link href={appURL("?page=repositories")}>Go to Repositories</Link>
              </p>
            </div>
          )}
        </div>
      </section>
    );
  const clone = primaryClone(repository);
  const status = automatic?.repositories.find(
    (item) => item.repository === clone.repository.toLowerCase(),
  );
  const remove = () => {
    setRemoving(true);
    setError("");
    void request(`api/repositories/${repository.repository}`, {}, "DELETE")
      .then(async () => {
        await changed();
        toList();
      })
      .catch((e: unknown) => {
        setError(errorText(e));
        setRemoving(false);
      });
  };
  const mode = automaticMode(clone);
  const nav: { id: RepositorySection; sub: string; tone?: Tone }[] = [
    {
      id: "reviews",
      sub: [
        status ? (status.enabled ? "Automatic" : "When you ask") : null,
        mode === "default" ? null : "set here",
        clone.settings.decisions === false ? "no decisions" : null,
      ]
        .filter(Boolean)
        .join(" · "),
    },
    {
      id: "prompts",
      sub: prompts.entries
        ? `${prompts.entries.filter(isCustom).length} custom`
        : prompts.error
          ? "Unavailable"
          : "Reading…",
      tone: prompts.entries && promptsNeedAttention(prompts.entries) ? "caution" : undefined,
    },
    {
      id: "registration",
      sub: clone.available ? homePath(clone.path) : "Needs a local clone",
      tone: clone.available ? undefined : "caution",
    },
  ];
  return (
    <section className="settings-page" aria-label={`Repository ${repository.repository}`}>
      <PageBar
        title={repository.repository}
        detail={
          clone.available ? homePath(clone.path) : `${clone.path} · unavailable; history retained`
        }
        back={back}
      />
      <div className="local-page-body settings-layout">
        <nav ref={keys.nav} aria-label="Repository sections" className="settings-nav">
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
                  {repositorySectionTitles[item.id]}{" "}
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
                {item.sub ? <span className="settings-nav-sub">{item.sub}</span> : null}
              </span>
            </button>
          ))}
        </nav>
        <div ref={keys.content} className="settings-content" onFocus={keys.onFocus}>
          {error ? <Alert>{error}</Alert> : null}
          {section === "reviews" ? (
            <RepositoryReviews
              key={repository.repository}
              clone={clone}
              automatic={automatic}
              status={status}
              changed={changed}
            />
          ) : section === "prompts" ? (
            <RepositoryPrompts repository={repository.repository} prompts={prompts} />
          ) : (
            <section aria-labelledby="repository-registration-heading" className="settings-section">
              <SectionHead
                id="repository-registration-heading"
                title="Registration"
                detail="The local clone baum reads this repository from."
              />
              <div className="local-panel settings-list">
                <SettingRow
                  label={clone.available ? "Local clone" : "No local clone"}
                  detail={
                    clone.available
                      ? clone.path
                      : "Choose a folder or clone it from the repository's menu in Repositories."
                  }
                />
                <SettingRow
                  label="Remove registration"
                  detail="baum stops reviewing this repository. Its files and saved reviews stay on disk."
                >
                  <Button
                    variant="danger-outline"
                    size="compact"
                    disabled={removing}
                    onClick={remove}
                  >
                    {removing ? "Removing…" : "Remove registration"}
                  </Button>
                </SettingRow>
              </div>
            </section>
          )}
        </div>
      </div>
    </section>
  );
}

/** Whether the daemon page runs in the desktop app. */
function hasFolderPicker() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}

function GeneralSection() {
  const { preferences, update, ready, error, refresh } = usePreferences();
  return (
    <section aria-labelledby="settings-general-heading" className="settings-section">
      <SectionHead
        id="settings-general-heading"
        title="General"
        detail="Appearance, keyboard and plan usage preferences on this device."
      />
      {error ? (
        <Alert>
          {error}
          <Button variant="secondary" size="compact" onClick={refresh}>
            Try again
          </Button>
        </Alert>
      ) : null}
      <div className="local-panel settings-list">
        <SettingRow label="Preferred theme" htmlFor="preferred-theme">
          <select
            id="preferred-theme"
            disabled={!ready}
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
          label="Page shortcut modifier"
          htmlFor="page-modifier"
          detail="The key held with 1 to 8 to open the sidebar's pages."
        >
          <select
            id="page-modifier"
            disabled={!ready}
            className="local-field settings-select"
            value={pageModifier(preferences.page_modifier)}
            onChange={(event) => update({ page_modifier: event.target.value as PageModifier })}
          >
            {MAC ? <option value="meta">⌘ Command</option> : null}
            <option value="ctrl">{MAC ? "⌃ Control" : "Ctrl"}</option>
            <option value="alt">{MAC ? "⌥ Option" : "Alt"}</option>
          </select>
        </SettingRow>
        <SettingRow
          label={`Show keyboard shortcuts with ${MAC ? "Option" : "Alt"}`}
          htmlFor="keyboard-hints"
          detail={`Hold ${MAC ? "Option" : "Alt"} to show shortcut hints.`}
        >
          <input
            id="keyboard-hints"
            disabled={!ready}
            type="checkbox"
            role="switch"
            className="local-switch"
            checked={preferences.key_hints}
            onChange={(event) => update({ key_hints: event.target.checked })}
          />
        </SettingRow>
      </div>
      <h3 className="settings-subhead">Plan usage</h3>
      <PlanUsageSettings />
    </section>
  );
}
