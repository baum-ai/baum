import { readFileSync } from "node:fs";

import { type Page, expect, test as base } from "@playwright/test";

import type { Block, ReviewDocument } from "../src/review-document.ts";

/** The synthetic `baum.review/1` example `baum view --sample` serves. */
const exampleText = readFileSync(
  new URL("../../schema/review-example.json", import.meta.url),
  "utf8",
);

export function sampleDocument(): ReviewDocument {
  return JSON.parse(exampleText) as ReviewDocument;
}

export function section(document: ReviewDocument, id: string) {
  const found = document.sections.find((candidate) => candidate.id === id);
  if (!found) throw new Error(`the sample has no section ${id}`);
  return found;
}

/** A block of a kind this page does not know, as a newer generator may write. */
export function unknownBlock(kind: string): Block {
  return { kind, anchors: [] } as unknown as Block;
}

/** Answers `/api/document` with `document` for this page. */
export async function serveDocument(page: Page, document: unknown, status = 200) {
  await page.route("**/api/document", (route) => route.fulfill({ status, json: document }));
}

/** Answers `/api/sessions` for this page. */
export async function serveSessions(page: Page, body: unknown, status = 200) {
  await page.route("**/api/sessions", (route) => route.fulfill({ status, json: body }));
}

/** A promise settled from outside, to hold a response until a test lets it go. */
export function deferred() {
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { released, release };
}

export type Collected = {
  /** Uncaught exceptions and console errors, which a clean page has none of. */
  browserErrors: string[];
  /** Requests to any origin but the page's own. */
  foreignRequests: string[];
};

export const test = base.extend<Collected>({
  browserErrors: async ({ page }, provide) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push(message.text());
    });
    await provide(errors);
  },
  foreignRequests: async ({ page, baseURL }, provide) => {
    const foreign: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.protocol === "data:" || url.protocol === "blob:") return;
      if (url.origin !== new URL(baseURL ?? "http://localhost").origin) foreign.push(url.href);
    });
    await provide(foreign);
  },
});

export { expect };
