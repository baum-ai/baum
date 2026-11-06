import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { UsageDetails } from "./usage";
import type { Accounting } from "./api";
const accounting: Accounting = {
  charged_tokens: 110,
  attempts: [],
  usage: {
    Input: 60,
    Output: 40,
    CacheRead: 1234567,
    CacheCreate: 10,
    CacheCreate1h: null,
    Speed: null,
    MinPrompt: null,
    MaxPrompt: null,
    Unreported: false,
  },
};
it("shows exact processed buckets separately from charged budget and unknown pricing", () => {
  const html = renderToStaticMarkup(
    <UsageDetails
      accounting={accounting}
      charged={110}
      amount={null}
      reason="speed_not_reported"
    />,
  );
  expect(html).toContain("1,234,567");
  expect(html).toContain("Processed tokens");
  expect(html).toContain("1,234,677");
  expect(html).toContain("Budget tokens");
  expect(html).toContain("provider did not report its pricing speed");
});
it("labels partial totals and keeps legacy usage unknown", () => {
  const partial = renderToStaticMarkup(
    <UsageDetails
      accounting={{ ...accounting, usage: { ...accounting.usage, Unreported: true } }}
      charged={110}
      amount={null}
    />,
  );
  expect(partial).toContain("Processed (partial)");
  expect(partial).not.toContain("Processed tokens");
  const legacy = renderToStaticMarkup(<UsageDetails charged={undefined} amount={null} />);
  expect(legacy).toContain("Provider token breakdown unavailable");
  expect(legacy).not.toContain("Cache reads 0");
});
it("leaves unreported speed, TTL and prompt range out", () => {
  const html = renderToStaticMarkup(
    <UsageDetails accounting={accounting} charged={110} amount={null} />,
  );
  expect(html).not.toContain("Unknown ·");
  expect(html).not.toContain("Speed");
  expect(html).not.toContain("Prompts");
});
it("shows a cost with unpriced calls as a lower bound", () => {
  const html = renderToStaticMarkup(
    <UsageDetails charged={undefined} amount={1.5} reason="unpriced_calls" />,
  );
  expect(html).toContain("≥ ");
  expect(html).toContain("at least the amount shown");
});
it("shows a cost estimated from incomplete usage as a lower bound", () => {
  const html = renderToStaticMarkup(
    <UsageDetails
      accounting={{ ...accounting, usage: { ...accounting.usage, Unreported: true } }}
      charged={110}
      amount={0.72}
      reason="usage_incomplete"
    />,
  );
  expect(html).toContain("≥ $0.72");
  expect(html).toContain("covers what was reported");
  expect(html).not.toContain("Unknown");
});
it("labels costs based on configured models as estimates", () => {
  const html = renderToStaticMarkup(
    <UsageDetails charged={110} amount={1.5} reason="estimated_from_configured_model" />,
  );
  expect(html).toContain("$1.50 (estimated)");
  expect(html).toContain("configured model&#x27;s API rates");
  expect(html).not.toContain("Unknown");
});
