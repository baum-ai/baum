import { useId, useState } from "react";
import { modelPresets, type PullRequests, type ReviewSettings } from "./setup-model";
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

export function ReviewSettingsForm({
  settings,
  disabled,
  onChange,
  className = "setup-card",
}: {
  settings: ReviewSettings;
  disabled: boolean;
  onChange: (next: ReviewSettings) => void;
  className?: string;
}) {
  const update = (patch: Partial<ReviewSettings>) => onChange({ ...settings, ...patch });
  return (
    <div id="review-panel" className={`${className} review-card`}>
      <HarnessPicker
        value={settings.backend}
        disabled={disabled}
        onChange={(backend) =>
          update({ backend: backend ?? "", model: modelPresets[backend ?? ""]?.[0] ?? "" })
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
          <fieldset className="scope">
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
            <p className="review-note">
              Drafts, forks and authors without write access are never reviewed automatically.
            </p>
          </fieldset>
        )}
      </div>
    </div>
  );
}
