import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { PlanUsageView, span, type PlanUsageEntry } from "./plan-usage";

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
