import { useId, useState } from "react";
import {
  decisionModelDefaults,
  modelPresets,
  type PullRequests,
  type ReviewSettings,
} from "./setup-model";
import codexLogo from "./codex.svg";
import claudeLogo from "./claude-code.svg";
import cursorLogo from "./cursor.svg";
import "./review-form.css";

export const harnesses = [
  { id: "claude", name: "Claude Code", detail: "Your Claude plan", logo: claudeLogo },
  { id: "codex", name: "Codex", detail: "Your ChatGPT plan", logo: codexLogo },
  { id: "cursor-agent", name: "Cursor Agent", detail: "Your Cursor plan", logo: cursorLogo },
  { id: "anthropic", name: "Anthropic API", detail: "Pay per use", logo: claudeLogo },
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
  legend = "Harness",
}: {
  value: string | null;
  onChange: (next: string | null) => void;
  disabled: boolean;
  inherit?: { label: string; detail: string };
  legend?: string;
}) {
  const name = useId();
  const options = [
    ...(inherit ? [{ id: INHERIT, name: inherit.label, detail: inherit.detail, logo: null }] : []),
    ...harnesses,
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
              disabled={disabled}
              onChange={() => onChange(harness.id === INHERIT ? null : harness.id)}
            />
            <span className="harness-tile">
              {harness.logo ? (
                <img src={harness.logo} alt="" width="20" height="20" />
              ) : (
                <span className="harness-inherit" aria-hidden="true" />
              )}
              <span>
                {harness.name}
                <small>{harness.detail}</small>
              </span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
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
}: {
  backend: string;
  value: string | null;
  onChange: (next: string | null) => void;
  disabled: boolean;
  inherit?: string;
  id: string;
  label?: string;
}) {
  const presets = modelPresets[backend] ?? [];
  const [custom, setCustom] = useState(value !== null && !presets.includes(value));
  const selected = value === null ? INHERIT : custom ? CUSTOM : value;
  return (
    <div className="review-section">
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
                {model}
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

/** The decision capture model, among the harness's models; Cursor Agent
 *  cannot capture decisions. */
export function DecisionModelPicker({
  backend,
  value,
  onChange,
  disabled,
  inherit,
  id,
}: {
  backend: string;
  value: string | null;
  onChange: (next: string | null) => void;
  disabled: boolean;
  inherit?: string;
  id: string;
}) {
  if (!decisionModelDefaults[backend])
    return (
      <div className="review-section">
        <span className="review-label">Decision log model</span>
        <p className="review-note">
          {harnessName(backend)} can't capture decisions. Choose another harness to capture them.
        </p>
      </div>
    );
  return (
    <ModelPicker
      key={backend}
      id={id}
      label="Decision log model"
      backend={backend}
      value={value}
      disabled={disabled}
      inherit={inherit}
      onChange={onChange}
    />
  );
}

export function ReviewSettingsForm({
  settings,
  disabled,
  onChange,
  className = "setup-card",
  decisions = false,
}: {
  settings: ReviewSettings;
  disabled: boolean;
  onChange: (next: ReviewSettings) => void;
  className?: string;
  /** Also offer the decision capture model, which follows the harness. */
  decisions?: boolean;
}) {
  const update = (patch: Partial<ReviewSettings>) => onChange({ ...settings, ...patch });
  return (
    <div id="review-panel" className={`${className} review-card`}>
      <HarnessPicker
        value={settings.backend}
        disabled={disabled}
        onChange={(backend) =>
          update({
            backend: backend ?? "",
            model: modelPresets[backend ?? ""]?.[0] ?? "",
            // A new harness has other models: start from its cheap default.
            ...(decisions ? { decisions_model: decisionModelDefaults[backend ?? ""] ?? "" } : {}),
          })
        }
      />
      <ModelPicker
        // A new harness lists other models, so the dropdown starts over.
        key={settings.backend}
        id="review-model"
        backend={settings.backend}
        value={settings.model}
        disabled={disabled}
        onChange={(model) => update({ model: model ?? "" })}
      />
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
      </div>
      {decisions ? (
        <>
          <DecisionModelPicker
            id="decisions-model"
            backend={settings.backend}
            value={settings.decisions_model ?? ""}
            disabled={disabled}
            onChange={(model) => update({ decisions_model: model ?? "" })}
          />
          <div className="review-section">
            <DecisionsDisclosure />
          </div>
        </>
      ) : null}
    </div>
  );
}
