import { describe, expect, it } from "vitest";
import { readableDocument } from "../api";
import { REVIEW_1303 } from "./fixture-1303";

describe("readableDocument", () => {
  it("reads a saved baum.review/2 review as its summary and sections", () => {
    // What the inbox preview and the Pull requests peeks read off /api/document.
    const doc = readableDocument(JSON.parse(JSON.stringify(REVIEW_1303)));
    expect(doc.summary.text).toBe(REVIEW_1303.summary.text);
    expect(doc.sections.map((section) => section.title)).toEqual(
      REVIEW_1303.sections.map((section) => section.title),
    );
  });

  it("refuses what is not a review", () => {
    expect(() => readableDocument({ schema: "baum.review/2" })).toThrow();
    expect(() => readableDocument({ schema: "other" })).toThrow();
  });
});
