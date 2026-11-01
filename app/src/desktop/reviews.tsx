import { BrandMark } from "@baum/viewer/components/brand-logo";
import { useId, useState } from "react";
import { modelPresets, type PullRequests, type ReviewSettings } from "./setup-model";
import { effortLabels, modelEfforts, supportedEffort } from "../model-effort";
import "./review-form.css";

export const harnesses = [
  { id: "claude", name: "Claude Code", detail: "Your Claude plan", brand: "claude" as const },
  { id: "codex", name: "Codex", detail: "Your ChatGPT plan", brand: "openai" as const },
  {
    id: "cursor-agent",
    name: "Cursor Agent",
    detail: "Your Cursor plan",
    brand: "cursor" as const,
  },
  { id: "anthropic", name: "Anthropic API", detail: "Pay per use", brand: "claude" as const },
];
const scopes: { id: PullRequests; label: string }[] = [
  { id: "all", label: "All" },
  { id: "both", label: "Mine and requested" },
  { id: "authored", label: "Only mine" },
  { id: "requested", label: "Only requested" },
];
const CUSTOM = "\u0000custom";
const INHERIT = "\u0000inherit";
export const harnessName = (id: string) => harnesses.find((h) => h.id === id)?.name ?? id;

/** The harness tiles. With `inherit`, a first tile leaves the choice to the
 *  level above, and `value` is null while it is picked. */
export function HarnessPicker({
  value,
  onChange,
  disabled,
  inherit,
  legend = "Agent",
  available,
  missingLabel = "Not installed",
}: {
  value: string | null;
  onChange: (next: string | null) => void;
  disabled: boolean;
  inherit?: { label: string; detail: string };
  legend?: string;
  available?: readonly string[];
  missingLabel?: string;
}) {
  const name = useId();
  const options = [
    ...(inherit ? [{ id: INHERIT, name: inherit.label, detail: inherit.detail, brand: null }] : []),
    ...harnesses.filter((harness) => harness.id === "claude" || harness.id === "codex"),
  ];
  return (
    <fieldset className="review-section">
      <legend>{legend}</legend>
      <div className={inherit ? "harness-grid with-inherit" : "harness-grid"}>
        {options.map((harness) => (
          <label key={harness.id} className="harness-option">
            <input
              type="radio"
              name={name}
              value={harness.id === INHERIT ? "" : harness.id}
              checked={(value ?? INHERIT) === harness.id}
              disabled={
                disabled ||
                (available !== undefined &&
                  harness.id !== INHERIT &&
                  !available.includes(harness.id))
              }
              onChange={() => onChange(harness.id === INHERIT ? null : harness.id)}
            />
            <span className="harness-tile">
              {harness.brand ? (
                <BrandMark brand={harness.brand} size={20} />
              ) : (
                <span className="harness-inherit" aria-hidden="true" />
              )}
              <span>
                {harness.name}
                <small>
                  {available !== undefined &&
                  harness.id !== INHERIT &&
                  !available.includes(harness.id)
                    ? missingLabel
                    : harness.detail}
                </small>
              </span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

const effortNames: Record<string, string> = {
  "review-effort": "Review reasoning effort",
  "fix-effort": "Fix reasoning effort",
};

type EffortSelection = {
  value: string;
  onChange: (next: string) => void;
};

function EffortPicker({
  backend,
  model,
  value,
  onChange,
  disabled,
  id,
}: EffortSelection & { backend: string; model: string; disabled: boolean; id: string }) {
  const levels = modelEfforts(backend, model);
  return (
    <div className="effort-field">
      <label htmlFor={id} className="review-label">
        Effort
      </label>
      <span className="select">
        <select
          id={id}
          value={supportedEffort(backend, model, value)}
          disabled={disabled || levels.length === 0}
          onChange={(event) => onChange(event.target.value)}
          aria-label={effortNames[id] ?? "Decision extraction reasoning effort"}
        >
          <option value="">Default</option>
          {levels.map((level) => (
            <option
              key={level}
              value={level}
              title={
                level === "ultra" ? "Allows Codex to delegate tasks automatically." : undefined
              }
            >
              {effortLabels[level]}
            </option>
          ))}
        </select>
      </span>
    </div>
  );
}

/** The model dropdown: the harness's models, its default first, and a custom
 *  name. With `inherit`, a first option leaves it to the level above (null). */
export function ModelPicker({
  backend,
  value,
  onChange,
  disabled,
  inherit,
  id,
  label = "Model",
  unused,
  effort,
  note,
}: {
  backend: string;
  value: string | null;
  onChange: (next: string | null) => void;
  disabled: boolean;
  inherit?: string;
  id: string;
  label?: string;
  /** A saved model the harness does not use, listed as its own option so
   *  it shows as chosen and choosing another is a change. */
  unused?: string;
  effort?: EffortSelection & { model?: string };
  note?: string;
}) {
  const presets = [...(modelPresets[backend] ?? []), ...(unused ? [unused] : [])];
  const [custom, setCustom] = useState(value !== null && !presets.includes(value));
  const selected = value === null ? INHERIT : custom ? CUSTOM : value;
  return (
    <div className={`review-section model-settings${effort ? " with-effort" : ""}`}>
      <div className="model-field">
        <label htmlFor={id} className="review-label">
          {label}
        </label>
        <div className="model-row">
          <span className="select">
            <select
              id={id}
              value={selected}
              disabled={disabled}
              onChange={(event) => {
                const next = event.target.value;
                setCustom(next === CUSTOM);
                onChange(next === INHERIT ? null : next === CUSTOM ? "" : next);
              }}
            >
              {inherit ? <option value={INHERIT}>{inherit}</option> : null}
              {presets.map((model) => (
                <option key={model} value={model}>
                  {model === unused ? `${model} (not used)` : model}
                </option>
              ))}
              <option value={CUSTOM}>Custom…</option>
            </select>
          </span>
          {custom && value !== null ? (
            <input
              id={`${id}-custom`}
              aria-label="Custom model"
              placeholder="Model name"
              value={value}
              disabled={disabled}
              onChange={(event) => onChange(event.target.value)}
            />
          ) : null}
        </div>
        {custom && value !== null && !value.trim() ? (
          <p className="review-note">Enter the model's name as {harnessName(backend)} lists it.</p>
        ) : null}
      </div>
      {effort ? (
        <EffortPicker
          id={id.replace(/-model$/, "-effort")}
          backend={backend}
          model={effort.model ?? value ?? ""}
          value={effort.value}
          onChange={effort.onChange}
          disabled={disabled}
        />
      ) : null}
      {note ? <p className="review-note">{note}</p> : null}
    </div>
  );
}

/** What decision capture sends, said once wherever it is set up. */
export function DecisionsDisclosure() {
  return (
    <p className="review-note">
      baum captures the decisions of your agent sessions in the repositories you pick. It sends each
      session's prompts, the agent's visible text, its plans, question-and-answer dialogue and the
      names of the files it edited, with secrets masked, to the harness's provider; never tool
      output or file contents. Capture follows the sessions the agent hooks report, so it needs the
      hooks installed. Turn it off in Settings or per repository.
    </p>
  );
}

export function ReviewSettingsForm({
  settings,
  disabled,
  onChange,
  className = "setup-card",
  available,
  detectionError,
}: {
  settings: ReviewSettings;
  disabled: boolean;
  onChange: (next: ReviewSettings) => void;
  className?: string;
  available: readonly string[];
  detectionError?: string | null;
}) {
  const update = (patch: Partial<ReviewSettings>) => {
    const next = { ...settings, ...patch };
    onChange({
      ...next,
      effort: supportedEffort(next.backend, next.model, next.effort),
      fix_effort: next.fix_backend
        ? supportedEffort(next.fix_backend, next.fix_model ?? "", next.fix_effort)
        : "",
    });
  };
  const fix = settings.fix_backend || null;
  return (
    <div id="review-panel" className={`${className} review-card`}>
      <HarnessPicker
        legend="Reviews and extraction"
        value={available.includes(settings.backend) ? settings.backend : null}
        disabled={disabled}
        available={available}
        missingLabel={detectionError ? "Not detected" : "Not installed"}
        onChange={(backend) =>
          update({
            backend: backend ?? "",
            model: modelPresets[backend ?? ""]?.[0] ?? "",
          })
        }
      />
      {available.length === 0 ? (
        <div id="agent-installation" className="review-section agent-installation" role="status">
          <h2>{detectionError ? "Check your installed agents" : "Install an agent to continue"}</h2>
          <p className="review-note">
            {detectionError
              ? "baum couldn't check which agents are installed. Choose Check again to retry."
              : "baum needs Claude Code or Codex to run reviews, fixes and decision extraction. Install one, open it in your terminal to sign in, then choose Check again."}
          </p>
          {!detectionError && (
            <div className="agent-install-links">
              <a
                href="https://code.claude.com/docs/en/setup#install-claude-code"
                target="_blank"
                rel="noreferrer"
              >
                Install Claude Code ↗
              </a>
              <a href="https://learn.chatgpt.com/docs/cli" target="_blank" rel="noreferrer">
                Install Codex ↗
              </a>
            </div>
          )}
        </div>
      ) : (
        <>
          <ModelPicker
            // A new harness lists other models, so the dropdown starts over.
            key={settings.backend}
            id="review-model"
            backend={settings.backend}
            value={settings.model}
            disabled={disabled}
            onChange={(model) => update({ model: model ?? "" })}
            effort={{ value: settings.effort ?? "", onChange: (effort) => update({ effort }) }}
            note="You can customise the agent and model per task in the app settings."
          />
          <HarnessPicker
            legend="Fixes"
            inherit={{ label: "Same as reviews", detail: "Agent and model" }}
            value={fix}
            disabled={disabled}
            available={available}
            missingLabel={detectionError ? "Not detected" : "Not installed"}
            onChange={(backend) =>
              update({
                fix_backend: backend ?? "",
                fix_model: backend ? (modelPresets[backend]?.[0] ?? "") : "",
                fix_effort: backend ? settings.fix_effort : "",
              })
            }
          />
          {fix ? (
            <ModelPicker
              key={fix}
              id="fix-model"
              backend={fix}
              value={settings.fix_model ?? ""}
              disabled={disabled}
              onChange={(model) => update({ fix_model: model ?? "" })}
              effort={{
                value: settings.fix_effort ?? "",
                onChange: (fix_effort) => update({ fix_effort }),
              }}
            />
          ) : null}
          <div className="review-section">
            <label className="switch-row">
              <span>
                Review pull requests automatically
                <small>baum reviews new pushes and review requests in your repositories.</small>
              </span>
              <input
                id="review-automatic"
                type="checkbox"
                role="switch"
                checked={settings.automatic}
                disabled={disabled}
                onChange={(event) => update({ automatic: event.target.checked })}
              />
            </label>
            {settings.automatic && (
              <fieldset
                className="scope"
                aria-describedby={settings.pull_requests === "all" ? "review-all-note" : undefined}
              >
                <legend className="review-label">Which pull requests</legend>
                <div className="segmented">
                  {scopes.map((scope) => (
                    <label key={scope.id}>
                      <input
                        type="radio"
                        name="pull-requests"
                        value={scope.id}
                        checked={settings.pull_requests === scope.id}
                        disabled={disabled}
                        onChange={() => update({ pull_requests: scope.id })}
                      />
                      <span>{scope.label}</span>
                    </label>
                  ))}
                </div>
                {settings.pull_requests === "all" && (
                  <p id="review-all-note" className="review-note">
                    Every pull request in your repositories gets reviewed, including your team's, so
                    reviews, notifications and cost grow with their activity.
                  </p>
                )}
                <p className="review-note">
                  Drafts, forks and authors without write access are never reviewed automatically.
                </p>
              </fieldset>
            )}
            <p className="review-note">
              You can customise this per repository in the repository settings.
            </p>
          </div>
        </>
      )}
    </div>
  );
}
