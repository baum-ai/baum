import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  asOf,
  planKey,
  planName,
  PlanUsageRailView,
  readablePlans,
  resetsAt,
  span,
  type PlanUsageEntry,
} from "./plan-usage";

const now = Date.parse("2026-10-26T12:00:00Z");
const plans: PlanUsageEntry[] = [
  {
    provider: "claude",
    available: true,
    windows: [
      { name: "5-hour", used_percent: 38, resets_at: "2026-10-26T15:00:00Z" },
      { name: "weekly", used_percent: 95, resets_at: "2026-10-30T12:00:00Z" },
    ],
    fetched_at: "2026-10-26T12:00:00Z",
  },
  {
    provider: "codex",
    available: true,
    plan: "pro",
    windows: [
      { name: "5-hour", used_percent: 75, resets_at: "2026-10-26T14:00:00Z" },
      { name: "weekly", used_percent: null, resets_at: "2026-10-26T10:00:00Z", reset: true },
    ],
    fetched_at: "2026-10-26T11:00:00Z",
  },
];

it("rounds spans to readable units", () => {
  expect(span(10_000)).toBe("1 min");
  expect(span(25 * 60_000)).toBe("25 min");
  expect(span(5 * 3_600_000)).toBe("5 h");
  expect(span(72 * 3_600_000)).toBe("3 d");
});

it("sums up each plan in the sidebar by its lowest window, with a bar per window", () => {
  const html = renderToStaticMarkup(<PlanUsageRailView plans={plans} now={now} />);
  // Each window by name, with what is left and when it resets.
  expect(html).toContain("Claude Code");
  expect(html).toMatch(/5-hour · <span[^>]*>62%<\/span>/);
  expect(html).toMatch(/Weekly · <span[^>]*>5%<\/span>/);
  expect(html).toContain("resets in 3 h");
  expect(html).toContain("resets in 4 d");
  expect(html).toMatch(/5-hour · <span[^>]*>25%<\/span>/);
  // Codex's weekly reset since its last session: no figure, no fill.
  expect(html).toContain(">reset<");
  expect(html.match(/plan-rail-track/g)).toHaveLength(4);
  // Codex says its plan; Claude here does not.
  expect(html).toMatch(/Codex<span class="plan-rail-tier">Pro<\/span>/);
  expect(html.match(/plan-rail-tier/g)).toHaveLength(1);
  // Hovering "resets in" tells exactly when.
  expect(html).toContain(`title="${resetsAt(Date.parse("2026-10-26T15:00:00Z"))}"`);
  expect(resetsAt(Date.parse("2026-10-26T15:00:00Z"))).toMatch(/^Resets \S.*26/);
  // Folded, the rail keeps the lowest share.
  expect(html).toMatch(/plan-rail-compact[^>]*>5%</);
  // The oldest figure, Codex's last session, sets the age.
  expect(html).toContain("1 h ago");
});

it("shows only the plans it can read, and nothing when it can read none", () => {
  const missing: unknown[] = [
    {
      provider: "claude",
      available: false,
      reason: "Claude Code is not signed in on this computer.",
      windows: [],
      fetched_at: "",
    },
    { provider: "codex", available: true, windows: [], fetched_at: "" },
    null,
    { provider: "codex" },
  ];
  expect(readablePlans(missing)).toEqual([]);
  expect(readablePlans(undefined)).toEqual([]);
  expect(readablePlans({ plans: [] })).toEqual([]);
  expect(readablePlans([...missing, plans[1]]).map((plan) => plan.provider)).toEqual(["codex"]);
  const html = renderToStaticMarkup(
    <PlanUsageRailView plans={readablePlans([missing[0], plans[1]])} now={now} />,
  );
  expect(html).not.toContain("Claude Code");
  expect(html).toContain("Codex");
  expect(asOf([], now)).toBe(null);
  expect(asOf(plans.slice(0, 1), now)).toBe("just now");
});

it("asks to open the agent when its sign-in expired, without setting the age", () => {
  const expired: unknown = {
    provider: "claude",
    available: false,
    needs_sign_in: true,
    reason: "Open Claude Code to refresh its sign-in.",
    windows: [{ name: "weekly", used_percent: 10, resets_at: null }],
    fetched_at: "2026-10-17T12:00:00Z",
  };
  const shown = readablePlans([expired, plans[1]]);
  expect(shown.map((plan) => [plan.provider, plan.windows.length])).toEqual([
    ["claude", 0],
    ["codex", 2],
  ]);
  const html = renderToStaticMarkup(<PlanUsageRailView plans={shown} now={now} />);
  expect(html).toContain("Claude Code");
  expect(html).toContain(
    '<span class="plan-rail-note">Open Claude Code to refresh its sign-in.</span>',
  );
  expect(html.match(/plan-rail-track/g)).toHaveLength(2);
  // The age is still the oldest figure read, not the failed reading.
  expect(html).toContain("1 h ago");
  // Alone, it still draws the box, with no age and no share when folded.
  const alone = renderToStaticMarkup(
    <PlanUsageRailView plans={readablePlans([expired])} now={now} />,
  );
  expect(alone).toContain("plan-rail-note");
  expect(alone).not.toContain("plan-rail-age");
  expect(alone).toMatch(/plan-rail-compact[^>]*>–</);
  // One of several sign-ins of the same agent says it under its directory.
  const grouped = renderToStaticMarkup(
    <PlanUsageRailView
      plans={readablePlans([
        plans[0],
        { ...(expired as object), config_dir: "~/.claude.personal" },
      ])}
      now={now}
    />,
  );
  expect(grouped).toMatch(
    /data-config-dir="~\/\.claude\.personal">.*plan-rail-account-dir[^>]*>~\/\.claude\.personal<.*plan-rail-note">Open Claude Code/,
  );
});

it("tells a second Claude plan from the first by its configuration directory", () => {
  const second: PlanUsageEntry = {
    ...plans[0]!,
    plan: "Ultra",
    config_dir: "~/.claude.personal",
    windows: [{ name: "5-hour", used_percent: 10, resets_at: "2026-10-26T15:00:00Z" }],
  };
  expect(planName(plans[0]!)).toBe("Claude Code");
  expect(planName(second)).toBe("Claude Code (~/.claude.personal)");
  expect(planKey(second)).not.toBe(planKey(plans[0]!));
  // Wherever the daemon lists it, it sits with the agent's other plan, under
  // one heading, each named by its directory with its plan beside it.
  const html = renderToStaticMarkup(
    <PlanUsageRailView plans={[plans[0]!, plans[1]!, second]} now={now} />,
  );
  expect(html.match(/class="plan-rail-plan"/g)).toHaveLength(2);
  expect(html.match(/>Claude Code</g)).toHaveLength(1);
  expect(html.match(/class="plan-rail-account"/g)).toHaveLength(2);
  expect(html.indexOf("~/.claude.personal")).toBeLessThan(html.indexOf(">Codex<"));
  expect(html).toMatch(/plan-rail-account-dir">Default<\/span>/);
  expect(html).toMatch(
    /plan-rail-account-dir" title="[^"]*">~\/\.claude\.personal<\/span><span class="plan-rail-reset">Ultra<\/span>/,
  );
  // Alone, a plan keeps its agent's heading, with its directory under it.
  const alone = renderToStaticMarkup(<PlanUsageRailView plans={[second]} now={now} />);
  expect(alone).not.toContain("plan-rail-account");
  expect(alone).toMatch(/<span class="plan-rail-dir"[^>]*>~\/\.claude\.personal<\/span>/);
  expect(html).toContain('aria-label="Claude Code (~/.claude.personal) 5-hour left"');
  expect(html).toContain('aria-label="Claude Code 5-hour left"');
});
