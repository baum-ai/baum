import type { CodexHookTrust } from "../codex-hooks";
import { supportedEffort } from "../model-effort";

export type Found = { path: string; resolved: string; version: string };
export type CommandStatus = {
  installed: boolean;
  unavailable?: string | null;
  target?: string | null;
  found?: Found | null;
  shadowed?: Found | null;
  record?: object | null;
  path_error?: string | null;
  plan?: {
    link: string;
    admin: boolean;
    blocked?: string | null;
    on_path: boolean;
    replaces?: Found | null;
    backup?: string | null;
  } | null;
};
export type Agent = {
  agent: string;
  name: string;
  detected: boolean;
  installed: boolean;
  enabled: boolean;
  plugin: string;
  version?: string | null;
  /** The plugin version the bundled baum shipped with. */
  latest?: string | null;
  /** The installed version is older than `latest`. */
  update_available?: boolean;
  trust?: CodexHookTrust | ClaudeHookState | null;
  error?: string | null;
};
/** What stops Claude Code running an enabled plugin's hooks. It has no
 *  per-hook approval, so only settings can: `ready` means none of the files
 *  baum can read does. */
export type ClaudeHookState = {
  state: "ready" | "hooks_disabled" | "managed_only" | "unknown";
};
export type SetupView = {
  first_run: boolean;
  home?: string | null;
  command: CommandStatus;
  hooks: { agents: Agent[]; error?: string | null };
};
export type Outcome = { ok: boolean; lines: string[] };
type CommandDescription = {
  state: string;
  note: string;
  action: string;
  replace?: string;
  ok?: boolean;
};
export function displayPath(value: string, homeDirectory?: string | null) {
  const home = homeDirectory?.replace(/[\\/]$/, "");
  if (!home) return value;
  const escaped = home.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const pattern = new RegExp("(^|[\\s(\"'=:`])" + escaped + "(?=$|[\\\\/\\s)\"'`,;:])", "g");
  return value.replace(pattern, (_, prefix) => `${prefix}~`);
}

export function repositoryPath(value: string, home?: string | null) {
  if (home && (value === "~" || /^~[\\/]/.test(value))) {
    return home + value.slice(1);
  }
  return value;
}

function version(found: Found) {
  return found.version ? `version ${found.version}` : "version unknown";
}

export function describeCommand(command: CommandStatus): CommandDescription {
  const plan = command.plan!;
  if (command.unavailable) return { state: command.unavailable, note: "", action: "" };
  if (command.installed) {
    const at = command.found?.path ?? command.target;
    const state = command.record
      ? `baum is installed at ${at}.`
      : `baum is installed at ${at}. It was installed outside the app, so the app won't update it.`;
    const shadowed = command.shadowed
      ? `A different baum comes first on your PATH: ${command.shadowed.path} (${version(command.shadowed)}).`
      : "";
    return { state, note: shadowed, action: "", ok: true };
  }
  // On Windows the plan names a directory to add to the PATH, not a link.
  const linking = /[\\/]baum$/.test(plan.link);
  const dir = linking ? plan.link.replace(/[\\/][^\\/]*$/, "") : plan.link;
  const notes = [];
  if (plan.admin && !plan.blocked) notes.push(`Changing ${dir} needs an administrator's password.`);
  if (!plan.on_path)
    notes.push(
      `${dir} is not on your PATH: add it in your shell's startup file so terminals and agents find baum.`,
    );
  if (command.path_error) notes.push(command.path_error);
  const found = command.found;
  if (plan.blocked) {
    const state = found
      ? `A different baum comes first on your PATH: ${found.path} (${version(found)}). ${plan.blocked}`
      : plan.blocked;
    return { state, note: notes.join(" "), action: "" };
  }
  if (plan.replaces) {
    const other = plan.replaces;
    return {
      state: `A different baum is installed at ${other.path} (${version(other)}).`,
      note: notes.join(" "),
      action: "Replace baum",
      replace: other.path,
    };
  }
  let state;
  if (!linking)
    state =
      `This will add ${dir} to the front of your PATH. New terminals will pick it up.` +
      (found ? ` A different baum is on your PATH now: ${found.path} (${version(found)}).` : "");
  else if (command.record)
    state = `The app's link at ${plan.link} no longer runs this app's baum. Updating points it at ${command.target}.`;
  else if (found) state = `${found.path} runs another copy of baum (${version(found)}).`;
  else state = `This will install baum at ${plan.link}.`;
  return { state, note: notes.join(" "), action: command.record ? "Update" : "Install" };
}

export function agentConnected(agent: Agent) {
  return (
    agent.installed &&
    agent.enabled &&
    !agent.error &&
    (agent.agent === "codex"
      ? agent.trust?.state === "trusted"
      : !agent.trust || agent.trust.state === "ready")
  );
}

/** Whether Update would change anything: a newer plugin, or Codex hooks
 *  missing from an installed plugin. */
export function agentUpdatable(agent: Agent) {
  return (
    agent.detected &&
    !agent.error &&
    agent.installed &&
    agent.enabled &&
    (!!agent.update_available || (agent.agent === "codex" && agent.trust?.state === "missing"))
  );
}

/** The line under an installed agent: how its hooks come to run, or how to
 *  unblock them. */
export function agentHookNote(agent: Agent) {
  if (agent.agent === "codex")
    return agent.trust?.state === "trusted"
      ? "Codex hooks are approved."
      : "Review hook trust with /hooks in Codex.";
  switch (agent.trust?.state) {
    case "hooks_disabled":
      return "disableAllHooks is on in your Claude Code settings. Turn it off, then check again.";
    case "managed_only":
      return "Managed settings allow only managed hooks. Ask your administrator to enable baum@baum in them.";
    case "unknown":
      return "A Claude Code settings file isn't valid JSON or can't be read. Fix it, then check again.";
    default:
      return "Hooks run in folders you trust in Claude Code.";
  }
}

export function describeAgent(agent: Agent) {
  if (agent.error) return agent.error;
  if (agent.installed) {
    if (!agent.enabled) return "Installed but disabled";
    if (agent.agent === "codex" && !agentConnected(agent)) {
      switch (agent.trust?.state) {
        case "untrusted":
          return "Needs approval in Codex";
        case "hooks_disabled":
          return "Lifecycle hooks disabled in Codex";
        case "disabled":
          return "Hooks disabled in Codex";
        case "missing":
          return "Update Codex hooks";
        default:
          return "Hook approval could not be verified";
      }
    }
    if (agent.agent === "claude-code" && !agentConnected(agent)) {
      switch (agent.trust?.state) {
        case "hooks_disabled":
          return "Hooks turned off in Claude Code";
        case "managed_only":
          return "Blocked by your organization";
        default:
          return "Claude Code settings could not be read";
      }
    }
    return "Connected";
  }
  if (!agent.detected)
    return `${agent.name} isn't on your PATH. Install or update it, then check again.`;
  return "";
}

export type PullRequests = "all" | "both" | "authored" | "requested";
export type ReviewSettings = {
  backend: string;
  model: string;
  effort?: string;
  automatic: boolean;
  pull_requests: PullRequests;
  /** The fixes' own agent, model and effort; an empty agent follows the
   *  reviews. */
  fix_backend?: string;
  fix_model?: string;
  fix_effort?: string;
  /** Extraction preferences. Setup shows the reviews' choice for them and
   *  saves them only when it changes that choice (`setupSave`). */
  decisions_model?: string;
  decisions_effort?: string;
  /** What the daemon reports, never saved: the data extraction harness
   *  Settings chose (empty while it follows the reviews'), the harness and
   *  model capture uses (empty when unknown), and whether it ignores
   *  `decisions_model`. */
  decisions_chosen_backend?: string;
  decisions_backend?: string;
  decisions_effective_model?: string;
  decisions_model_ignored?: boolean;
};

const claudeModels = ["claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"];

/** Models offered per harness, its default first; anything else is typed as
 *  a custom model. */
export const modelPresets: Record<string, string[]> = {
  claude: claudeModels,
  codex: ["gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra"],
};

/** The cheap model decision capture uses per harness when none is chosen. */
export const decisionModelDefaults: Record<string, string> = {
  claude: "claude-haiku-5-5",
  codex: "gpt-6-luna",
};

/** Settings with an explicit model: an unset one becomes the harness's
 *  default, so the user always sees and saves a real choice. The decision
 *  capture model, when the settings carry one, is filled the same way; a
 *  chosen one is kept, even one the harness ignores, which is shown so. */
export function withModel(settings: ReviewSettings): ReviewSettings {
  const next = {
    ...settings,
    model: settings.model.trim() ? settings.model : (modelPresets[settings.backend]?.[0] ?? ""),
  };
  if (next.fix_backend && !next.fix_model?.trim())
    next.fix_model = modelPresets[next.fix_backend]?.[0] ?? "";
  const extraction = extractionHarness(next);
  const defaultDecisionModel = decisionModelDefaults[extraction] ?? "";
  if (next.decisions_model !== undefined && !next.decisions_model.trim())
    next.decisions_model = defaultDecisionModel;
  return withEfforts(
    next,
    settings.decisions_model_ignored ? settings.decisions_effective_model : undefined,
  );
}

/** Keep an effort only when the chosen model supports it. An ignored saved
 *  decision model uses the daemon's effective model instead. */
export function withEfforts(
  settings: ReviewSettings,
  effectiveDecisionModel?: string,
): ReviewSettings {
  const next = { ...settings };
  if (next.effort !== undefined)
    next.effort = supportedEffort(next.backend, next.model, next.effort);
  if (next.fix_effort !== undefined)
    next.fix_effort = next.fix_backend
      ? supportedEffort(next.fix_backend, next.fix_model ?? "", next.fix_effort)
      : "";
  if (next.decisions_effort !== undefined) {
    const extraction = extractionHarness(next);
    const decisionModel =
      effectiveDecisionModel || next.decisions_model || decisionModelDefaults[extraction] || "";
    next.decisions_effort = supportedEffort(extraction, decisionModel, next.decisions_effort);
  }
  return next;
}

/** Setup's review and fix controls as it saves them. */
export function storedReviews(settings: ReviewSettings): ReviewSettings {
  const fix = settings.fix_backend ?? "";
  return {
    backend: settings.backend,
    model: settings.model,
    ...(settings.effort !== undefined ? { effort: settings.effort } : {}),
    automatic: settings.automatic,
    pull_requests: settings.pull_requests,
    fix_backend: fix,
    fix_model: fix ? (settings.fix_model ?? "") : "",
    fix_effort: fix ? (settings.fix_effort ?? "") : "",
  };
}

/** What finishing setup saves, null when nothing changed. Extraction takes
 *  the reviews' agent, model and effort, but only when setup changes that
 *  choice, so an untouched setup keeps what Settings chose. The harness's
 *  default decision model is saved as none, so it follows the harness. */
export function setupSave(settings: ReviewSettings, saved: ReviewSettings): ReviewSettings | null {
  const next = storedReviews(settings);
  const before = storedReviews(saved);
  if (JSON.stringify(next) === JSON.stringify(before)) return null;
  if (
    next.backend === before.backend &&
    next.model === before.model &&
    (next.effort ?? "") === (before.effort ?? "")
  )
    return next;
  return {
    ...next,
    decisions_model: next.model === decisionModelDefaults[next.backend] ? "" : next.model,
    decisions_effort: next.effort ?? "",
  };
}

/** Fixes follow the reviews again when their saved agent is not installed. */
export function shownFix(settings: ReviewSettings, available: readonly string[]): ReviewSettings {
  if (!settings.fix_backend || available.includes(settings.fix_backend)) return settings;
  return { ...settings, fix_backend: "", fix_model: "", fix_effort: "" };
}

/** Agent CLIs found on the login shell's PATH, in default preference order. */
export function detectedHarnesses(agents: readonly Agent[]): string[] {
  return ["claude", "codex"].filter((backend) =>
    agents.some(
      (agent) => agent.agent === (backend === "claude" ? "claude-code" : backend) && agent.detected,
    ),
  );
}

/** Keep a saved choice only while its CLI is installed. Never select a missing agent. */
export function shownHarness(settings: ReviewSettings, available: readonly string[]): string {
  const backend =
    settings.backend === "auto"
      ? (!settings.decisions_chosen_backend && settings.decisions_backend) || ""
      : settings.backend;
  return available.includes(backend) ? backend : (available[0] ?? "");
}

/** The harness data extraction runs with: the one Settings chose, else the
 *  reviews'. */
export function extractionHarness(settings: ReviewSettings): string {
  return settings.decisions_chosen_backend || settings.backend;
}
