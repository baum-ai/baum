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
  error?: string | null;
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

export function describeAgent(agent: Agent) {
  if (agent.error) return agent.error;
  if (agent.installed) return agent.enabled ? "Connected" : "Installed but disabled";
  if (!agent.detected)
    return `${agent.name} isn't on your PATH. Install or update it, then check again.`;
  return "";
}

export type PullRequests = "all" | "both" | "authored" | "requested";
export type ReviewSettings = {
  backend: string;
  model: string;
  automatic: boolean;
  pull_requests: PullRequests;
  /** The decision capture model; empty is the harness's default. Only the
   *  desktop setup edits it here. */
  decisions_model?: string;
  /** What the daemon reports, never saved: the harness and model capture
   *  uses (empty when unknown), and whether it ignores `decisions_model`. */
  decisions_backend?: string;
  decisions_effective_model?: string;
  decisions_model_ignored?: boolean;
};

const claudeModels = ["claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"];

/** Models offered per harness, its default first; anything else is typed as
 *  a custom model. */
export const modelPresets: Record<string, string[]> = {
  claude: claudeModels,
  anthropic: claudeModels,
  codex: ["gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra"],
  "cursor-agent": ["composer-2.5", "composer-2.5-fast", "grok-4.7-medium", "grok-4.7-high"],
};

/** The cheap model decision capture uses per harness when none is chosen;
 *  Cursor Agent cannot capture decisions. */
export const decisionModelDefaults: Record<string, string> = {
  claude: "claude-haiku-5-5",
  anthropic: "claude-haiku-5-5",
  codex: "gpt-6-luna",
};

/** Settings with an explicit model: an unset one becomes the harness's
 *  default, so the user always sees and saves a real choice. The decision
 *  capture model, when the settings carry one, is filled the same way; a
 *  chosen one is kept, even one the harness ignores, which is shown so. */
export function withModel(settings: ReviewSettings): ReviewSettings {
  const next = settings.model.trim()
    ? settings
    : { ...settings, model: modelPresets[settings.backend]?.[0] ?? "" };
  if (next.decisions_model === undefined || next.decisions_model.trim()) return next;
  return { ...next, decisions_model: decisionModelDefaults[next.backend] ?? "" };
}

/** Settings as they are saved: what the daemon reports is left out, and a
 *  decision capture model that is the harness's default is stored as none,
 *  so it follows the harness. */
export function storedReviews(settings: ReviewSettings): ReviewSettings {
  const {
    decisions_backend: _,
    decisions_effective_model: __,
    decisions_model_ignored: ___,
    ...saved
  } = settings;
  if (saved.decisions_model === undefined) return saved;
  return saved.decisions_model === decisionModelDefaults[saved.backend]
    ? { ...saved, decisions_model: "" }
    : saved;
}

/** The harness the setup shows for saved settings: "auto" has no tile, so it
 *  is the harness the daemon resolved it to, else the screen's guess. */
export function shownHarness(settings: ReviewSettings, guess: string): string {
  if (settings.backend !== "auto") return settings.backend;
  return settings.decisions_backend || guess;
}
