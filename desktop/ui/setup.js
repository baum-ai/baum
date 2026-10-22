// The bundled setup screen: first-run setup, and "Command Line and Hooks…"
// later. It shows what the app reports and asks it to change things only when
// the user clicks; the app makes every change. withGlobalTauri is off, so it
// uses the core IPC bridge Tauri injects into its own pages.
const invoke = (command, args) => window.__TAURI_INTERNALS__.invoke(command, args);

const $ = (id) => document.getElementById(id);
const buttons = () => [...document.querySelectorAll("button")];

let view = null;
let busy = false;
// After the first change on first run, the screen manages like it does later.
let acted = false;

function text(element, value) {
  element.textContent = value;
  element.hidden = !value;
}

function show(lines, failed) {
  const log = $("log");
  log.textContent = lines.join("\n");
  log.hidden = lines.length === 0;
  log.classList.toggle("failed", Boolean(failed));
}

function version(found) {
  return found.version ? `version ${found.version}` : "version unknown";
}

function describeCommand(command) {
  const plan = command.plan;
  if (command.unavailable) return { state: command.unavailable, note: "", action: "" };
  if (command.installed) {
    const by = command.record
      ? "The app installed it and keeps it up to date with the app."
      : "It was installed outside the app (for example by a package), so the app leaves it alone.";
    const found = command.found;
    const runs = found
      ? `the hooks run ${found.path}${found.path === found.resolved ? "" : ` (${found.resolved})`}`
      : `new terminals run ${command.target}`;
    const shadowed = command.shadowed
      ? `A different baum still comes first on your PATH: ${command.shadowed.path} (${version(command.shadowed)}). Remove it, or move its directory after the app's in your PATH.`
      : "";
    return { state: `Installed: ${runs}. ${by}`, note: shadowed, action: "" };
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
      state:
        `A different baum comes first on your PATH: ${other.path} (${version(other)}). ` +
        `The hooks run it, not the app's baum. Replacing it puts a link to the app's baum ` +
        `there and keeps yours as ${plan.backup}; removing the command later puts yours back.`,
      note: notes.join(" "),
      action: `Replace ${other.path}`,
      replace: other.path,
    };
  }
  let state;
  if (!linking)
    state =
      `Not installed. Installing adds ${dir}, which holds only the app's baum, to the front of ` +
      `your PATH; new terminals pick it up.` +
      (found ? ` A different baum is on your PATH now: ${found.path} (${version(found)}).` : "");
  else if (command.record)
    state = `The app's link at ${plan.link} no longer runs this app's baum. Updating points it at ${command.target}.`;
  else if (found) state = `${found.path} runs another copy of baum (${version(found)}).`;
  else
    state = `Not installed. Installing puts a link to the app's baum (${command.target}) at ${plan.link}.`;
  return { state, note: notes.join(" "), action: command.record ? "Update" : "Install" };
}

function describeAgent(agent) {
  if (agent.pluginManaged) return `${agent.name}: managed by the baum plugin; inspect installation and trust in your coding agent`;
  if (agent.error) return `${agent.name}: ${agent.error}`;
  if (agent.installed) return `${agent.name}: installed in ${agent.settings}`;
  if (!agent.detected) return `${agent.name}: not found on this computer`;
  return `${agent.name}: not installed (${agent.settings})`;
}

function render() {
  const firstRun = view.first_run && !acted;
  $("title").textContent = view.first_run ? "Set up baum" : "Command line and hooks";
  $("intro").hidden = !view.first_run;
  $("checking").hidden = true;
  $("panels").hidden = false;

  const command = describeCommand(view.command);
  text($("command-state"), command.state);
  text($("command-note"), command.note);
  const install = $("command-install");
  install.textContent = command.action;
  install.hidden = !command.action;
  install.dataset.replace = command.replace ?? "";
  $("command-remove").hidden = !view.command.record;

  const hooks = view.hooks;
  const list = $("hooks-list");
  list.replaceChildren(
    ...hooks.agents.map((agent) => {
      const item = document.createElement("li");
      item.textContent = describeAgent(agent);
      return item;
    }),
  );
  text($("hooks-error"), hooks.error ?? "");
  const pending = hooks.agents.some((a) => a.detected && !a.installed && !a.pluginManaged && !a.error);
  const installed = hooks.agents.some((a) => a.installed);
  const noAgent = !hooks.error && !hooks.agents.some((a) => a.detected);
  if (noAgent)
    text($("hooks-error"), "Neither Claude Code nor Codex was found. Install one, then come back here.");
  $("hooks-install").hidden = !pending;
  $("hooks-install").textContent = installed ? "Update hooks" : "Install hooks";
  $("hooks-remove").hidden = !installed;

  // First run: one action installs both; panels show what it will do.
  for (const actions of document.querySelectorAll(".manage")) actions.hidden = firstRun;
  const commandPending = Boolean(command.action);
  const all = $("setup-all");
  all.hidden = !firstRun || (!commandPending && !pending);
  all.textContent = command.replace ? "Replace it and install the hooks" : "Install";
  $("hooks-only").hidden = !firstRun || !command.replace || !pending;
  const done = !commandPending && !pending;
  const next = $("continue");
  next.textContent = view.first_run ? (done || acted ? "Continue" : "Skip for now") : "Done";
  next.classList.toggle("secondary", !done && firstRun);
  setBusy(false);
}

function setBusy(value) {
  busy = value;
  for (const button of buttons()) button.disabled = value;
}

async function refresh() {
  try {
    view = await invoke("setup_state");
    render();
  } catch (error) {
    $("checking").textContent = String(error);
    setBusy(false);
  }
}

// Runs changes one after another, shows what they printed, then rereads the
// state. A change that fails stops the rest.
async function change(steps) {
  if (busy) return;
  setBusy(true);
  show(["Working… (your system may ask for your password)"]);
  const lines = [];
  let failed = false;
  for (const [command, args] of steps) {
    try {
      const outcome = await invoke(command, args);
      lines.push(...outcome.lines);
      if (!outcome.ok) failed = true;
    } catch (error) {
      lines.push(String(error));
      failed = true;
    }
    if (failed) break;
  }
  acted = true;
  show(lines, failed);
  await refresh();
}

function installCommandStep() {
  const replace = $("command-install").dataset.replace;
  return ["install_command", replace ? { replace } : {}];
}

$("command-install").addEventListener("click", () => change([installCommandStep()]));
$("command-remove").addEventListener("click", () => change([["uninstall_command", {}]]));
$("hooks-install").addEventListener("click", () => change([["install_hooks", {}]]));
$("hooks-remove").addEventListener("click", () => change([["uninstall_hooks", {}]]));
$("setup-all").addEventListener("click", () => {
  const steps = [];
  if (!$("command-install").hidden) steps.push(installCommandStep());
  if (!$("hooks-install").hidden) steps.push(["install_hooks", {}]);
  change(steps);
});
$("hooks-only").addEventListener("click", () => change([["install_hooks", {}]]));
$("continue").addEventListener("click", () => {
  setBusy(true);
  invoke("finish_setup").catch((error) => {
    show([String(error)], true);
    setBusy(false);
  });
});

setBusy(true);
refresh();
