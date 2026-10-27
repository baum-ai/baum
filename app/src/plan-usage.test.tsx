import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import {
  asOf,
  PlanUsageRailView,
  PlanUsageView,
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

it("shows what is left, when it resets and colours by remaining", () => {
  const html = renderToStaticMarkup(<PlanUsageView plans={plans} now={now} />);
  expect(html).toContain("62% left");
  expect(html).toContain("resets in 3 h");
  expect(html).toContain("5% left");
  expect(html).toContain("resets in 4 d");
  expect(html).toContain("var(--status-positive)");
  expect(html).toContain("var(--status-caution)");
  expect(html).toContain("var(--status-critical)");
  expect(html).toContain("As of its last session, 1 h ago.");
  // A live Codex reading says nothing about its age.
  const live = renderToStaticMarkup(
    <PlanUsageView plans={[{ ...plans[1]!, fetched_at: "2026-10-26T11:59:00Z" }]} now={now} />,
  );
  expect(live).not.toContain("As of its last session");
  expect(html).toContain("Reset since baum last saw it");
  expect(html).toContain("pro");
});

it("says why a plan is unavailable and can show one provider", () => {
  const html = renderToStaticMarkup(
    <PlanUsageView
      only="claude"
      now={now}
      plans={[
        {
          provider: "claude",
          available: false,
          reason: "Open Claude Code to refresh its sign-in.",
          windows: [],
          fetched_at: "",
        },
        ...plans.slice(1),
      ]}
    />,
  );
  expect(html).toContain("Open Claude Code to refresh its sign-in.");
  expect(html).not.toContain("Codex");
});

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
