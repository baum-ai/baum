import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { invoke } from "./native";
import {
  describeAgent,
  describeCommand,
  displayPath,
  withModel,
  type Outcome,
  type ReviewSettings,
  type SetupView,
} from "./setup-model";
import { ReviewSettingsForm } from "./reviews";
import { GitHubStep } from "./github-step";
import { RepositoryPicker, setupRepositoryCall } from "../repositories";
import logo from "./logo.svg";
import codexLogo from "./codex.svg";
import claudeLogo from "./claude-code.svg";

const steps = [
  [
    "Connect GitHub",
    "baum finds your pull requests and keeps their reviews current with your GitHub account.",
    "Connect GitHub",
    "Pull requests and reviews",
  ],
  [
    "Install the baum CLI",
    "Agent hooks report to baum through this command. The app ships its own copy and keeps it updated.",
    "Install the CLI",
    "The baum command",
  ],
  [
    "Connect your agents",
    "Install the baum plugin in each agent you use. You can skip one and add it later.",
    "Connect agents",
    "Codex and Claude Code",
  ],
  [
    "Add repositories",
    "Pick your GitHub repositories. baum finds their local clones for you.",
    "Add repositories",
    "Your GitHub repositories",
  ],
  [
    "Set up reviews",
    "Choose what writes your reviews and which pull requests baum reviews on its own.",
    "Set up reviews",
    "Harness, model and automation",
  ],
] as const;
const last = steps.length - 1;
const agents = [
  {
    id: "codex",
    name: "Codex",
    logo: codexLogo,
    hint: (
      <>
        Open <code>/hooks</code> in Codex to trust the hooks.
      </>
    ),
  },
  {
    id: "claude-code",
    name: "Claude Code",
    logo: claudeLogo,
    hint: <>Approve the hooks in Claude Code.</>,
  },
];

/** The harness "auto" runs first: Claude Code when installed, else Codex. */
function autoBackend(view: SetupView | null) {
  const detected = (id: string) => view?.hooks.agents.some((a) => a.agent === id && a.detected);
  if (detected("claude-code")) return "claude";
  return detected("codex") ? "codex" : "claude";
}

export function Setup() {
  const [view, setView] = useState<SetupView | null>(null);
  const [step, setStep] = useState(0);
  const [pending, setPending] = useState<string | null>("initial");
  const [error, setError] = useState("");
  const [pickedCount, setPickedCount] = useState(0);
  const [reviews, setReviews] = useState<ReviewSettings | null>(null);
  const [savedReviews, setSavedReviews] = useState("");
  const [github, setGitHub] = useState(false);
  const working = useRef(false);
  const title = useRef<HTMLHeadingElement>(null);
  const busy = pending !== null;
  const display = (value: string) => displayPath(value, view?.home);
  const refresh = useCallback(async () => {
    try {
      setView(await invoke<SetupView>("setup_state"));
    } catch (cause) {
      setError(String(cause));
    }
  }, []);
  useEffect(() => {
    let active = true;
    invoke<SetupView>("setup_state")
      .then((state) => {
        if (active) setView(state);
      })
      .catch((cause) => {
        if (active) setError(String(cause));
      })
      .finally(() => {
        if (active) setPending(null);
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    if (step !== last || reviews) return;
    let active = true;
    invoke<ReviewSettings>("review_settings")
      .then((settings) => {
        if (!active) return;
        // "auto" and an unset model have no choice on screen: show the harness
        // "auto" picks and that harness's default model. They differ from what
        // is saved, so opening baum saves the explicit choice.
        const backend = settings.backend === "auto" ? autoBackend(view) : settings.backend;
        setReviews(withModel({ ...settings, backend }));
        setSavedReviews(JSON.stringify(settings));
      })
      .catch((cause) => {
        if (active) setError(String(cause));
      });
    return () => {
      active = false;
    };
  }, [step, reviews, view]);
  useEffect(() => {
    if (view) title.current?.focus();
  }, [step, Boolean(view)]);

  async function run(id: string, action: () => Promise<void>) {
    if (working.current) return;
    working.current = true;
    setPending(id);
    setError("");
    try {
      await action();
    } catch (cause) {
      setError(String(cause));
    } finally {
      working.current = false;
      setPending(null);
    }
  }
  async function change(command: string, args?: Record<string, unknown>) {
    const outcome = await invoke<Outcome>(command, args);
    if (!outcome.ok) setError(outcome.lines.join("\n"));
    await refresh();
  }
  function button(
    id: string,
    children: ReactNode,
    onClick: () => void,
    className?: string,
    label?: string,
    blocked = false,
  ) {
    return (
      <button
        id={id}
        type="button"
        disabled={busy || blocked}
        className={`${className ?? ""}${pending === id ? " loading" : ""}`}
        aria-busy={pending === id || undefined}
        aria-label={label}
        onClick={onClick}
      >
        {children}
      </button>
    );
  }
  const command = view ? describeCommand(view.command) : null;
  const done = view
    ? [
        github,
        Boolean(command?.ok),
        view.hooks.agents.length > 0 &&
          view.hooks.agents.every((a) => a.installed && a.enabled && !a.error),
        pickedCount > 0,
        true,
      ][step]
    : false;
  const reviewsChanged = reviews !== null && JSON.stringify(reviews) !== savedReviews;
  const modelMissing = step === last && reviews !== null && !reviews.model.trim();
  const started = view
    ? [
        github,
        Boolean(command?.ok),
        view.hooks.agents.some((a) => a.installed),
        pickedCount > 0,
        true,
      ][step]
    : false;
  async function finish() {
    if (reviews && reviewsChanged) {
      const outcome = await invoke<Outcome>("save_review_settings", { settings: reviews });
      if (!outcome.ok) {
        setError(outcome.lines.join("\n"));
        return;
      }
      setSavedReviews(JSON.stringify(reviews));
    }
    await invoke<void>("finish_setup");
  }
  function navigate(next: number) {
    setError("");
    setStep(next);
  }

  return (
    <main className="onboarding">
      <aside className="setup-rail">
        <div className="wordmark">
          <img src={logo} alt="" width="40" height="40" />
          baum
        </div>
        <ol className="step-list" aria-label="Setup progress">
          {steps.map((item, index) => (
            <li
              key={item[0]}
              data-step
              aria-current={index === step ? "step" : undefined}
              className={index < step ? "complete" : undefined}
            >
              <button
                type="button"
                className="step-link"
                disabled={busy || !view}
                onClick={() => navigate(index)}
              >
                <span className="step-number">{index < step ? "✓" : index + 1}</span>
                <div>
                  {item[2]}
                  <small>{item[3]}</small>
                </div>
              </button>
            </li>
          ))}
        </ol>
      </aside>
      <section className="setup-content">
        <header>
          <p id="step-label" className="eyebrow">
            STEP {step + 1} OF {steps.length}
          </p>
          <h1 id="title" ref={title} tabIndex={-1}>
            {steps[step]![0]}
          </h1>
          <p id="intro">{steps[step]![1]}</p>
        </header>
        {!view && !error && (
          <p id="checking" role="status">
            Checking this computer…
          </p>
        )}
        {view && command && (
          <div id="panels">
            {step === 0 && <GitHubStep onConnected={setGitHub} />}
            {step === 1 && (
              <div id="command-panel">
                <p id="command-state" className={command.ok ? "ok" : undefined}>
                  {display(command.state)}
                </p>
                {command.note && (
                  <p id="command-note" className="callout">
                    {display(command.note)}
                  </p>
                )}
                <div className="actions">
                  {command.action &&
                    button(
                      "command-install",
                      command.replace ? command.action : `${command.action} baum CLI`,
                      () =>
                        void run("command-install", () =>
                          change(
                            "install_command",
                            command.replace ? { replace: command.replace } : {},
                          ),
                        ),
                    )}
                  {view.command.record &&
                    !view.first_run &&
                    button(
                      "command-remove",
                      "Remove CLI",
                      () => void run("command-remove", () => change("uninstall_command")),
                      "secondary",
                    )}
                </div>
              </div>
            )}
            {step === 2 && (
              <div id="agent-panel">
                <ul className="setup-card agent-list">
                  {agents.map((definition) => {
                    const agent = view.hooks.agents.find((a) => a.agent === definition.id);
                    const connected = agent?.installed && agent.enabled && !agent.error;
                    const state = agent
                      ? describeAgent(agent)
                      : `${definition.name} status unavailable. Check again to retry.`;
                    const verb = agent?.installed ? "Enable" : "Install";
                    return (
                      <li className="agent-row" key={definition.id}>
                        <img
                          className="agent-logo"
                          src={definition.logo}
                          alt=""
                          width="24"
                          height="24"
                        />
                        <div className="agent-info">
                          <h2>{definition.name}</h2>
                          {state && (
                            <p
                              id={`${definition.id}-state`}
                              className={connected ? "ok" : undefined}
                            >
                              {display(state)}
                            </p>
                          )}
                          {agent?.installed && !agent.error && (
                            <p id={`${definition.id}-hint`} className="agent-hint">
                              {definition.hint}
                            </p>
                          )}
                        </div>
                        <div className="actions">
                          {agent?.installed &&
                            !view.first_run &&
                            button(
                              `${definition.id}-remove`,
                              "Remove",
                              () =>
                                void run(`${definition.id}-remove`, () =>
                                  change("uninstall_hooks", { agent: definition.id }),
                                ),
                              "quiet",
                              `Remove ${definition.name} hooks`,
                            )}
                          {agent?.detected &&
                            !agent.error &&
                            !connected &&
                            button(
                              `${definition.id}-install`,
                              verb,
                              () =>
                                void run(`${definition.id}-install`, () =>
                                  change("install_hooks", { agent: definition.id }),
                                ),
                              undefined,
                              `${verb} ${definition.name} hooks`,
                            )}
                        </div>
                      </li>
                    );
                  })}
                </ul>
                {view.hooks.error && (
                  <p id="hooks-error" role="status">
                    {display(view.hooks.error)}
                  </p>
                )}
              </div>
            )}
            {step === 3 && (
              <div id="repo-panel" className="setup-card">
                <RepositoryPicker
                  call={setupRepositoryCall}
                  home={view.home ?? undefined}
                  connected={github}
                  onPicked={setPickedCount}
                />
              </div>
            )}
            {step === last && reviews && (
              <ReviewSettingsForm settings={reviews} disabled={busy} onChange={setReviews} />
            )}
            {step === last && !reviews && !error && (
              <p id="checking" role="status">
                Reading your review settings…
              </p>
            )}
          </div>
        )}
        {error && (
          <pre id="log" className="failed" role="alert">
            {display(error)}
          </pre>
        )}
        <footer className="setup-footer">
          <div className="actions">
            {!done && button("refresh", "Check again", () => void run("refresh", refresh), "quiet")}
            {step > 0 && button("back", "← Back", () => navigate(step - 1), "quiet")}
            {button(
              "continue",
              step === last
                ? reviewsChanged
                  ? "Save and open baum →"
                  : "Open baum →"
                : started
                  ? "Continue →"
                  : "Skip →",
              () => {
                if (!view) return;
                if (step < last) navigate(step + 1);
                else void run("continue", finish);
              },
              "secondary",
              undefined,
              modelMissing,
            )}
          </div>
          {!done && (
            <p id="skip-note">
              {step === 0
                ? "You can connect GitHub later in Settings."
                : step === 3
                  ? "You can add repositories later in Settings."
                  : "You can finish setup later from the app menu."}
            </p>
          )}
        </footer>
      </section>
    </main>
  );
}
