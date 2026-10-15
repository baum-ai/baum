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
  expect(html).toContain("Total processed tokens");
  expect(html).toContain("1,234,677");
  expect(html).toContain("Charged-token budget counter");
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
  expect(partial).toContain("Reported processed subtotal");
  expect(partial).not.toContain("Total processed tokens");
  const legacy = renderToStaticMarkup(<UsageDetails charged={undefined} amount={null} />);
  expect(legacy).toContain("Provider token breakdown unavailable");
  expect(legacy).not.toContain("Cache reads 0");
});
