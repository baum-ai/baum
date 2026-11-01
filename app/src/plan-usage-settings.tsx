import { useCallback, useEffect, useState } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import { request } from "./api";
import { PLAN_USAGE_CHANGED } from "./plan-usage";
import { SettingRow } from "./settings";

export type PlanChoice = {
  provider: string;
  name: string;
  /** Whether the plan appears in the sidebar and tray. */
  shown: boolean;
  /** What baum's backends would show without a choice. */
  default: boolean;
  /** Whether the person chose `shown`. */
  explicit: boolean;
};

const path = "api/settings/plan-usage";

/** Which plans plan usage shows: one switch per plan, on by default for the
 *  plans baum's backends use. A switch the person flips stays as chosen when
 *  the backends change; "Use baum's default" gives the choice back. */
export function PlanUsageSettings() {
  const [plans, setPlans] = useState<PlanChoice[] | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const body = await request<{ plans: PlanChoice[] }>(path, undefined, "GET", signal);
      if (!signal?.aborted) {
        setPlans(body.plans);
        setError("");
      }
    } catch (cause) {
      if (!signal?.aborted) setError(String(cause));
    }
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);
  const choose = async (provider: string, shown: boolean | null) => {
    if (saving) return;
    setSaving(true);
    setError("");
    try {
      const body = await request<{ plans: PlanChoice[] }>(path, { provider, shown }, "PUT");
      setPlans(body.plans);
      window.dispatchEvent(new Event(PLAN_USAGE_CHANGED));
    } catch (cause) {
      setError(String(cause));
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="grid gap-3" role="group" aria-label="Plan usage">
      {error ? (
        <Alert>
          {error}
          <Button variant="secondary" size="compact" onClick={() => void load()}>
            Try again
          </Button>
        </Alert>
      ) : null}
      <div className="local-panel settings-list">
        {(plans ?? []).map((plan) => (
          <SettingRow
            key={plan.provider}
            label={`Show ${plan.name} plan usage`}
            htmlFor={`plan-usage-${plan.provider}`}
            detail={
              plan.explicit ? (
                <>
                  Your choice.{" "}
                  <button
                    type="button"
                    className="underline"
                    disabled={saving}
                    onClick={() => void choose(plan.provider, null)}
                  >
                    Use baum's default ({plan.default ? "shown" : "hidden"})
                  </button>
                </>
              ) : plan.default ? (
                "Shown because baum uses it."
              ) : (
                "Hidden because baum does not use it."
              )
            }
          >
            <input
              id={`plan-usage-${plan.provider}`}
              type="checkbox"
              role="switch"
              className="local-switch"
              disabled={saving}
              checked={plan.shown}
              onChange={(event) => void choose(plan.provider, event.target.checked)}
            />
          </SettingRow>
        ))}
      </div>
    </div>
  );
}
