import { useEffect, useState, type ReactNode } from "react";
import { request } from "./api";

export type PlanWindow = {
  name: string;
  used_percent: number | null;
  resets_at: string | null;
  reset?: boolean;
};
export type PlanUsageEntry = {
  provider: "claude" | "codex";
  available: boolean;
  reason?: string;
  plan?: string;
  windows: PlanWindow[];
  fetched_at: string;
};

/** "weekly" reads "Weekly", "pro" reads "Pro". */
const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
const providerName = { claude: "Claude Code", codex: "Codex" } as const;
/** The daemon caches for five minutes, so asking more often only repeats its answer. */
export const PLAN_USAGE_REFRESH_MS = 5 * 60 * 1000;

/** "3 h", "25 min", "2 d": a span rounded to a readable unit. */
export function span(ms: number): string {
  const minutes = Math.max(0, Math.round(ms / 60000));
  if (minutes < 60) return `${Math.max(minutes, 1)} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1440)} d`;
}

function tone(left: number): string {
  if (left <= 10) return "var(--status-critical)";
  if (left <= 30) return "var(--status-caution)";
  return "var(--status-positive)";
}

function Meter({ win, now }: { win: PlanWindow; now: number }) {
  const resets = win.resets_at && !Number.isNaN(Date.parse(win.resets_at));
  if (win.used_percent === null) {
    return (
      <li className="grid gap-1" data-window={win.name}>
        <span className="text-xs text-(--text-secondary)">{capital(win.name)}</span>
        <span className="text-xs text-(--text-tertiary)">
          Reset since baum last saw it. Use it once to see the new figure.
        </span>
      </li>
    );
  }
  const left = Math.min(100, Math.max(0, Math.round(100 - win.used_percent)));
  return (
    <li className="grid gap-1" data-window={win.name}>
      <div className="flex items-baseline justify-between gap-3 text-xs">
        <span className="text-(--text-secondary)">{capital(win.name)}</span>
        <span className="text-(--text-tertiary)">
          {`${left}% left`}
          {resets && win.resets_at ? ` · resets in ${span(Date.parse(win.resets_at) - now)}` : ""}
        </span>
      </div>
      <div
        role="meter"
        aria-label={`${capital(win.name)} usage left`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={left}
        className="h-1.5 overflow-hidden rounded-full bg-(--status-neutral-bg)"
      >
        <div
          className="h-full rounded-full"
          style={{ width: `${left}%`, background: tone(left) }}
        />
      </div>
    </li>
  );
}

/** What remains of each plan. Pure so it renders without the daemon. */
export function PlanUsageView({
  plans,
  now = Date.now(),
  only,
}: {
  plans: PlanUsageEntry[];
  now?: number;
  /** Shows a single provider, such as the one chosen as the harness. */
  only?: string;
}) {
  const shown = plans.filter((plan) => !only || plan.provider === only);
  if (shown.length === 0) return null;
  return (
    <div className="grid gap-3" aria-label="Plan usage">
      {shown.map((plan) => (
        <section key={plan.provider} className="grid gap-2" data-provider={plan.provider}>
          <h3 className="text-xs font-semibold text-(--text-primary)">
            {providerName[plan.provider] ?? plan.provider}
            {plan.plan ? (
              <span className="font-normal text-(--text-tertiary)"> · {capital(plan.plan)}</span>
            ) : null}
          </h3>
          {plan.available ? (
            <ul className="grid gap-2">
              {plan.windows.map((win) => (
                <Meter key={win.name} win={win} now={now} />
              ))}
            </ul>
          ) : (
            <p className="text-xs text-(--text-tertiary)">Unavailable: {plan.reason}</p>
          )}
          {plan.provider === "codex" && plan.available ? (
            <p className="text-xs text-(--text-tertiary)">
              As of its last session, {span(now - Date.parse(plan.fetched_at))} ago.
            </p>
          ) : null}
        </section>
      ))}
    </div>
  );
}

/** Plan usage from the daemon, fetched on mount and every five minutes. A
 *  failed request shows nothing, heading and wrapper included: the figures
 *  are a convenience, not a setting. */
export function PlanUsage({
  only,
  heading,
  className,
}: {
  only?: string;
  heading?: ReactNode;
  className?: string;
}) {
  const [plans, setPlans] = useState<PlanUsageEntry[] | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const controller = new AbortController();
    let first = true;
    const load = () => {
      request<{ plans?: PlanUsageEntry[] }>(
        first ? "api/plan-usage?refresh=1" : "api/plan-usage",
        undefined,
        "GET",
        controller.signal,
      ).then(
        (body) => {
          setNow(Date.now());
          setPlans(Array.isArray(body.plans) ? body.plans : null);
        },
        () => undefined,
      );
      first = false;
    };
    load();
    const timer = window.setInterval(load, PLAN_USAGE_REFRESH_MS);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, []);
  if (!plans?.some((plan) => !only || plan.provider === only)) return null;
  return (
    <div className={className}>
      {heading}
      <PlanUsageView plans={plans} now={now} only={only} />
    </div>
  );
}
