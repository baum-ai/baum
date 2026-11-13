import { useEffect, useRef, useState } from "react";
import { request } from "./api";
import { useVisibleInterval } from "./visible-interval";

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
  /** The reason asks the person to open the agent to refresh its sign-in. */
  needs_sign_in?: boolean;
  plan?: string;
  windows: PlanWindow[];
  fetched_at: string;
};

/** "weekly" reads "Weekly", "pro" reads "Pro". */
export const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
export const providerName = { claude: "Claude Code", codex: "Codex" } as const;
/** The daemon caches for five minutes, so asking more often only repeats its answer. */
export const PLAN_USAGE_REFRESH_MS = 5 * 60 * 1000;
/** How often "resets in" is worked out again between readings. */
const CLOCK_MS = 30 * 1000;

/** "3 h", "25 min", "2 d": a span rounded to a readable unit. */
export function span(ms: number): string {
  const minutes = Math.max(0, Math.round(ms / 60000));
  if (minutes < 60) return `${Math.max(minutes, 1)} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1440)} d`;
}

export function tone(left: number): string {
  if (left <= 10) return "var(--status-critical)";
  if (left <= 30) return "var(--status-caution)";
  return "var(--status-positive)";
}

/** The share of a window left, as a whole percent. */
export const leftOf = (used: number) => Math.min(100, Math.max(0, Math.round(100 - used)));

/** Sent on `window` when the plans to show were changed in Settings, so the
 *  sidebar reads again at once. */
export const PLAN_USAGE_CHANGED = "baum:plan-usage-changed";

/** Plan usage from the daemon, asking for a fresh reading on mount and every
 *  five minutes while the page is visible (the daemon still reads at most once
 *  a minute), with a clock
 *  that keeps "resets in" current between readings. A failed request leaves
 *  the last answer, or `plans` null. */
export function usePlanUsage(): { plans: PlanUsageEntry[] | null; now: number } {
  const [plans, setPlans] = useState<PlanUsageEntry[] | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const loadRef = useRef<() => void>(() => undefined);
  useEffect(() => {
    const controller = new AbortController();
    const load = () => {
      request<{ plans?: PlanUsageEntry[] }>(
        "api/plan-usage?refresh=1",
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
    };
    loadRef.current = load;
    load();
    window.addEventListener(PLAN_USAGE_CHANGED, load);
    return () => {
      controller.abort();
      window.removeEventListener(PLAN_USAGE_CHANGED, load);
    };
  }, []);
  useVisibleInterval(() => setNow(Date.now()), CLOCK_MS);
  useVisibleInterval(() => loadRef.current(), PLAN_USAGE_REFRESH_MS);
  return { plans, now };
}

/** A plan the daemon could not read because the agent's sign-in expired:
 *  shown with its reason, since only the person can fix it. */
export const needsSignIn = (plan: PlanUsageEntry) =>
  !plan.available && plan.needs_sign_in === true && typeof plan.reason === "string";

/** The plans worth showing: those read, with at least one window, and those
 *  whose sign-in needs refreshing, with no windows. A provider that is not
 *  installed or has not run yet, and anything the daemon sent in another
 *  shape, is left out rather than explained. */
export function readablePlans(plans: unknown): PlanUsageEntry[] {
  if (!Array.isArray(plans)) return [];
  return plans
    .filter(
      (plan): plan is PlanUsageEntry =>
        typeof plan === "object" &&
        plan !== null &&
        typeof plan.provider === "string" &&
        Array.isArray(plan.windows) &&
        ((plan.available === true &&
          plan.windows.some(
            (win: unknown) => typeof (win as PlanWindow | null)?.name === "string",
          )) ||
          needsSignIn(plan)),
    )
    .map((plan) => ({
      ...plan,
      windows: plan.available ? plan.windows.filter((win) => typeof win?.name === "string") : [],
    }));
}

/** When a window resets, exactly, in the viewer's locale and time zone:
 *  "Resets Mon 2 Nov, 10:42". */
export function resetsAt(at: number): string {
  return `Resets ${new Date(at).toLocaleString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    hour: "numeric",
    minute: "2-digit",
  })}`;
}

/** How old the oldest available figure is: "just now", "20 min ago". */
export function asOf(plans: PlanUsageEntry[], now: number): string | null {
  const times = plans
    .filter((plan) => plan.available)
    .map((plan) => Date.parse(plan.fetched_at))
    .filter((at) => !Number.isNaN(at));
  if (times.length === 0) return null;
  const age = now - Math.min(...times);
  return age < 60000 ? "just now" : `${span(age)} ago`;
}

/** The sidebar's summary of what is left: per readable plan each window's
 *  share left, when it resets and a thin bar, and how old the oldest figure
 *  is; hovering a reset shows exactly when. Folded with the rail it keeps the
 *  lowest share. Pure so it renders without the daemon. */
export function PlanUsageRailView({ plans, now }: { plans: PlanUsageEntry[]; now: number }) {
  const age = asOf(plans, now);
  const lines = plans.flatMap((plan) =>
    plan.windows.map((win) => {
      const left = typeof win.used_percent === "number" ? leftOf(win.used_percent) : null;
      const resets = win.resets_at ? Date.parse(win.resets_at) : Number.NaN;
      return {
        plan,
        win,
        left,
        at: left === null || Number.isNaN(resets) ? undefined : resetsAt(resets),
        reset:
          left === null ? "reset" : Number.isNaN(resets) ? "" : `resets in ${span(resets - now)}`,
      };
    }),
  );
  const figures = lines.flatMap((line) => (line.left === null ? [] : [line.left]));
  const lowest = figures.length ? Math.min(...figures) : null;
  const name = (plan: PlanUsageEntry) => providerName[plan.provider] ?? plan.provider;
  return (
    // Not a named region: its own heading says what it is, and the Analytics
    // page's "Plan usage" card is the region of that name.
    <div className="plan-rail">
      <div className="plan-rail-head">
        <span>Plan usage</span>
        {age ? <span className="plan-rail-age">{age}</span> : null}
      </div>
      {plans.map((plan) => (
        <div key={plan.provider} className="plan-rail-plan" data-provider={plan.provider}>
          <span className="plan-rail-name">
            {name(plan)}
            {plan.plan ? <span className="plan-rail-tier">{capital(plan.plan)}</span> : null}
          </span>
          {needsSignIn(plan) ? <span className="plan-rail-note">{plan.reason}</span> : null}
          {lines
            .filter((line) => line.plan === plan)
            .map(({ win, left, reset, at }) => (
              <div key={win.name} className="plan-rail-window" data-window={win.name}>
                <span className="plan-rail-line">
                  <span>
                    {capital(win.name)}
                    {left === null ? null : (
                      <>
                        {" · "}
                        <span className="plan-rail-left" style={{ color: tone(left) }}>
                          {left}%
                        </span>
                      </>
                    )}
                  </span>
                  <span className="plan-rail-reset" title={at}>
                    {reset}
                  </span>
                </span>
                <span
                  className="plan-rail-track"
                  role="meter"
                  aria-label={`${name(plan)} ${win.name} left`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={left ?? undefined}
                >
                  {left === null ? null : (
                    <span style={{ width: `${left}%`, background: tone(left) }} />
                  )}
                </span>
              </div>
            ))}
        </div>
      ))}
      <span
        className="plan-rail-compact"
        aria-hidden="true"
        style={lowest === null ? undefined : { color: tone(lowest) }}
      >
        {lowest === null ? "–" : `${lowest}%`}
      </span>
    </div>
  );
}

/** The sidebar's plan usage, from the daemon: the plans it could read and
 *  those whose sign-in needs refreshing, and nothing at all until it answers,
 *  when it cannot, or when neither Claude Code nor Codex is there to read. */
export function PlanUsageRail() {
  const { plans, now } = usePlanUsage();
  const shown = readablePlans(plans);
  if (shown.length === 0) return null;
  return <PlanUsageRailView plans={shown} now={now} />;
}
