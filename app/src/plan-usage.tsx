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
export const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
export const providerName = { claude: "Claude Code", codex: "Codex" } as const;
/** The daemon caches for five minutes, so asking more often only repeats its answer. */
export const PLAN_USAGE_REFRESH_MS = 5 * 60 * 1000;
/** A Codex figure older than this came from its session files, not live. */
const STALE_MS = 15 * 60 * 1000;
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
  const left = leftOf(win.used_percent);
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
          {plan.provider === "codex" &&
          plan.available &&
          now - Date.parse(plan.fetched_at) > STALE_MS ? (
            <p className="text-xs text-(--text-tertiary)">
              As of its last session, {span(now - Date.parse(plan.fetched_at))} ago.
            </p>
          ) : null}
        </section>
      ))}
    </div>
  );
}

/** Plan usage from the daemon, asking for a fresh reading on mount and every
 *  five minutes (the daemon still reads at most once a minute), with a clock
 *  that keeps "resets in" current between readings. A failed request leaves
 *  the last answer, or `plans` null. */
export function usePlanUsage(): { plans: PlanUsageEntry[] | null; now: number } {
  const [plans, setPlans] = useState<PlanUsageEntry[] | null>(null);
  const [now, setNow] = useState(() => Date.now());
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
    load();
    const timer = window.setInterval(load, PLAN_USAGE_REFRESH_MS);
    const clock = window.setInterval(() => setNow(Date.now()), CLOCK_MS);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.clearInterval(clock);
    };
  }, []);
  return { plans, now };
}

/** Plan usage from the daemon. A failed request shows nothing, heading and
 *  wrapper included: the figures are a convenience, not a setting. */
export function PlanUsage({
  only,
  heading,
  className,
}: {
  only?: string;
  heading?: ReactNode;
  className?: string;
}) {
  const { plans, now } = usePlanUsage();
  if (!plans?.some((plan) => !only || plan.provider === only)) return null;
  return (
    <div className={className}>
      {heading}
      <PlanUsageView plans={plans} now={now} only={only} />
    </div>
  );
}

/** The plans worth a meter: read, with at least one window. A provider
 *  that is not installed, signed out or has not run yet, and anything the
 *  daemon sent in another shape, is left out rather than explained. */
export function readablePlans(plans: unknown): PlanUsageEntry[] {
  if (!Array.isArray(plans)) return [];
  return plans
    .filter(
      (plan): plan is PlanUsageEntry =>
        typeof plan === "object" &&
        plan !== null &&
        plan.available === true &&
        typeof plan.provider === "string" &&
        Array.isArray(plan.windows) &&
        plan.windows.some((win: unknown) => typeof (win as PlanWindow | null)?.name === "string"),
    )
    .map((plan) => ({
      ...plan,
      windows: plan.windows.filter((win) => typeof win?.name === "string"),
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
    <section className="plan-rail" aria-label="Plan usage">
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
    </section>
  );
}

/** The sidebar's plan usage, from the daemon: only the plans it could read,
 *  and nothing at all until it answers, when it cannot, or when neither
 *  Claude Code nor Codex is there to read. */
export function PlanUsageRail() {
  const { plans, now } = usePlanUsage();
  const shown = readablePlans(plans);
  if (shown.length === 0) return null;
  return <PlanUsageRailView plans={shown} now={now} />;
}
