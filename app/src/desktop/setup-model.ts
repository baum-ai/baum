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
};

const claudeModels = ["claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"];

/** Models offered per harness, its default first; anything else is typed as
 *  a custom model. */
export const modelPresets: Record<string, string[]> = {
  claude: claudeModels,
  anthropic: claudeModels,
  codex: ["gpt-6-luna", "gpt-6-sol", "gpt-5.5"],
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
 *  capture model, when the settings carry one, is filled the same way, and
 *  so is one of another provider's family, which the harness would ignore. */
export function withModel(settings: ReviewSettings): ReviewSettings {
  const next = settings.model.trim()
    ? settings
    : { ...settings, model: modelPresets[settings.backend]?.[0] ?? "" };
  if (
    next.decisions_model === undefined ||
    (next.decisions_model.trim() && decisionModelFits(next.decisions_model, next.backend))
  )
    return next;
  return { ...next, decisions_model: decisionModelDefaults[next.backend] ?? "" };
}

/** Settings as they are saved: a decision capture model that is the
 *  harness's default is stored as none, so it follows the harness. */
export function storedReviews(settings: ReviewSettings): ReviewSettings {
  if (settings.decisions_model === undefined) return settings;
  return settings.decisions_model === decisionModelDefaults[settings.backend]
    ? { ...settings, decisions_model: "" }
    : settings;
}

/** Whether a decision capture model can run on the harness: a model of the
 *  other provider's family is ignored there. Unknown names count as its own. */
export function decisionModelFits(model: string, backend: string): boolean {
  const claude = /^claude/i.test(model);
  const gpt = /^(gpt|o\d|codex)/i.test(model);
  if (backend === "codex") return !claude;
  if (backend === "claude" || backend === "anthropic") return !gpt;
  return true;
}
