import type { Page } from "@playwright/test";

import type { ReviewDocument } from "../src/review-document.ts";
import { expect, sampleDocument, section, serveDocument, test, unknownBlock } from "./fixtures.ts";

const retry = "/?pane=section&section=sec_retry";

test("a section shows its summary, intent, entities, behavior, decisions and sources", async ({
  page,
}) => {
  await page.goto(retry);
  const pane = page.locator("#review-section");

  await expect(
    pane.getByRole("heading", { level: 2, name: "Retry policy and backoff" }),
  ).toBeVisible();
  await expect(pane).toContainText("Adds RetryPolicy with exponential backoff");
  await expect(pane.getByRole("region", { name: "Intent" })).toContainText(
    "stop after a bounded number of attempts",
  );
  const entities = pane.getByRole("region", { name: "Changed entities" });
  await expect(entities.getByRole("row", { name: /RetryPolicy\.Delay/ })).toContainText("Added");
  await expect(entities.getByRole("link", { name: "queue/retry.go" }).first()).toHaveAttribute(
    "href",
    /^https:\/\/github\.com\/example-org\/taskline\/pull\/212\/files#diff-/,
  );

  const behavior = pane.getByRole("region", { name: "Behavior", exact: true });
  await expect(behavior).toContainText("A job is exhausted once its attempt count reaches Budget");

  const decisions = pane.getByRole("region", { name: "Key decisions" });
  await expect(decisions).toContainText("Use plain doubling with a cap");
  await expect(decisions).toContainText("Instead of:");
  await expect(decisions.getByText("Human").first()).toBeVisible();
  await expect(decisions.getByText("Agent").first()).toBeVisible();
});

test("a section's table, code and diagram blocks draw as written", async ({ page }) => {
  await page.goto(retry);
  const pane = page.locator("#review-section");

  const table = pane.getByRole("region", { name: "Table" });
  await expect(table.getByRole("columnheader")).toHaveText(["Attempt", "Delay before jitter"]);
  await expect(table.getByRole("row")).toHaveCount(6);
  await expect(pane.getByRole("region", { name: "Code", exact: true })).toContainText(
    "queue.RetryPolicy{Base: 2 * time.Second",
  );
  await expect(pane.getByRole("region", { name: "Diagram (mermaid)" })).toContainText(
    "sequenceDiagram",
  );
});

test("a behavior step's code evidence opens to the stored excerpt", async ({ page }) => {
  await page.goto(retry);
  const behavior = page
    .locator("#review-section")
    .getByRole("region", { name: "Behavior", exact: true });
  const evidence = behavior.locator("details").first();

  await expect(evidence.locator("summary")).toContainText("1 excerpt in 1 file");
  await evidence.locator("summary").click();

  await expect(evidence.getByRole("region", { name: "Code changes" })).toBeVisible();
  await expect(evidence).toContainText("queue/retry.go");
});

const hunkId = "h_141757b19901";

/** The unified diff of the retry policy's hunk, in the words the clone on this
 *  machine would hold it: every line is prefixed so the test can tell it from
 *  the stored excerpt. */
function filePatch(document: ReviewDocument): string {
  const diff = document.index.hunks.find((hunk) => hunk.id === hunkId)?.diff;
  if (!diff) throw new Error("the sample's hunk has no excerpt");
  const prefix = { add: "+", remove: "-", context: " " } as const;
  return `${diff.header}\n${diff.lines
    .map((line) => `${prefix[line.kind]}patched ${line.text}`)
    .join("\n")}\n`;
}

/** Stores the retry policy's hunk without its excerpt, as a document whose
 *  generation input was capped does, and opens the section. */
async function withoutExcerpt(page: Page) {
  const document = sampleDocument();
  for (const hunk of document.index.hunks) {
    if (hunk.id === hunkId) delete (hunk as { diff?: unknown }).diff;
  }
  await serveDocument(page, document);
  await page.goto(retry);
  return page
    .locator("#review-section")
    .getByRole("region", { name: "Behavior", exact: true })
    .locator("details")
    .first();
}

test("an excerpt the document left out is read from the local clone when opened", async ({
  page,
}) => {
  const asked: string[] = [];
  const patch = filePatch(sampleDocument());
  await page.route("**/api/patch?*", (route) => {
    const path = new URL(route.request().url()).searchParams.get("path") ?? "";
    asked.push(path);
    return route.fulfill({ json: { patch } });
  });
  const evidence = await withoutExcerpt(page);
  await expect(evidence.locator("summary")).toBeVisible();
  // Nothing is asked for evidence nobody has opened.
  expect(asked).toEqual([]);

  await evidence.locator("summary").click();

  await expect(evidence).toContainText("patched func (p RetryPolicy) Delay(attempt int)");
  await expect(evidence).not.toContainText("Excerpt unavailable");
  expect(asked).toEqual(["queue/retry.go"]);
});

test("an excerpt with no patch to read keeps its link to the source", async ({ page }) => {
  await page.route("**/api/patch?*", (route) => route.fulfill({ json: { patch: null } }));
  const evidence = await withoutExcerpt(page);

  await evidence.locator("summary").click();

  await expect(evidence).toContainText("Excerpt unavailable");
  await expect(evidence.getByRole("link", { name: /queue\/retry\.go/ }).first()).toHaveAttribute(
    "href",
    /github\.com\/example-org\/taskline/,
  );
});

test("a patch request that fails is asked again when the excerpt is reopened", async ({ page }) => {
  let failing = true;
  const requests: string[] = [];
  const patch = filePatch(sampleDocument());
  await page.route("**/api/patch?*", (route) => {
    const path = new URL(route.request().url()).searchParams.get("path") ?? "";
    requests.push(path);
    return failing
      ? route.fulfill({ status: 503, json: { error: "unavailable" } })
      : route.fulfill({ json: { patch } });
  });
  const evidence = await withoutExcerpt(page);
  const summary = evidence.locator("summary");

  await summary.click();
  await expect(evidence).toContainText("Excerpt unavailable");

  failing = false;
  await summary.click();
  await summary.click();
  await expect(evidence).toContainText("patched func (p RetryPolicy) Delay(attempt int)");
  expect(requests).toEqual(["queue/retry.go", "queue/retry.go"]);
});

test("a block of a kind this page does not know says so and leaves the rest readable", async ({
  page,
}) => {
  const document = sampleDocument();
  section(document, "sec_retry").blocks.push(unknownBlock("hologram_walkthrough"));
  await serveDocument(page, document);
  await page.goto(retry);

  const pane = page.locator("#review-section");
  const fallback = pane.getByRole("region", { name: "Unsupported content" });
  await expect(fallback).toContainText(
    "a “Hologram walkthrough” block this page cannot render yet",
  );
  // The blocks around it still draw.
  await expect(pane.getByRole("region", { name: "Table" })).toBeVisible();
  await expect(pane.getByRole("region", { name: "Behavior", exact: true })).toBeVisible();
});

test("tiles stay hidden until the URL asks, then draw their families", async ({ page }) => {
  await page.goto("/?pane=section&section=sec_api");
  const pane = page.locator("#review-section");
  await expect(pane.getByRole("region", { name: "Behavior", exact: true })).toBeVisible();
  await expect(page.getByLabel("Review tiles")).toHaveCount(0);

  await page.goto("/?pane=section&section=sec_api&tiles=1");
  await expect(pane.getByRole("region", { name: "API contract" }).first()).toBeVisible();
  await expect(page.getByLabel("Review tiles").first()).toBeVisible();
  await expect(pane.getByText("Tile unavailable")).toHaveCount(0);

  await page.goto("/?pane=section&section=sec_deadletter&tiles=1");
  await expect(
    page.locator("#review-section").getByRole("region", { name: "Database schema" }),
  ).toBeVisible();

  await page.goto("/?pane=section&section=sec_ackfix&tiles=1");
  await expect(
    page.locator("#review-section").getByRole("region", { name: "Bug fix" }),
  ).toBeVisible();
});

test("a tile of an unsupported kind is replaced by a notice and the review stays readable", async ({
  page,
}) => {
  const document = sampleDocument();
  const tiles = section(document, "sec_ackfix").tiles as unknown[];
  tiles.push({ id: "tile_future", kind: "hologram", anchors: [], code: [], data: {} });
  await serveDocument(page, document);
  await page.goto("/?pane=section&section=sec_ackfix&tiles=1");

  const pane = page.locator("#review-section");
  await expect(pane.getByText("Tile unavailable (hologram)")).toBeVisible();
  await expect(pane.getByRole("region", { name: "Bug fix" })).toBeVisible();
});

test("description images draw nothing, as the page has no source for attachments", async ({
  page,
  foreignRequests,
}) => {
  const attachmentRequests: string[] = [];
  page.on("request", (request) => {
    if (/attachment|\.png/i.test(request.url())) attachmentRequests.push(request.url());
  });
  await page.goto("/?pane=section&section=sec_docs");

  await expect(
    page.getByRole("heading", { level: 2, name: "Docs and dependency bump" }),
  ).toBeVisible();
  await expect(page.locator("#review-section").locator("img")).toHaveCount(0);
  expect(attachmentRequests).toEqual([]);
  expect(foreignRequests).toEqual([]);
});
