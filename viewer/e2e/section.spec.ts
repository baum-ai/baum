import type { Page } from "@playwright/test";

import type { ReviewDocument } from "../src/review-document.ts";
import { expect, sampleDocument, serveDocument, test } from "./fixtures.ts";

const retry = "/?pane=section&section=sec_retry";

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
