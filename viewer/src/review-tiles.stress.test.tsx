// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import reviewSchema from "../../schema/review-1.json" with { type: "json" };
import { reviewDocumentFor } from "../test/review-document";
import legacyTileSchema from "../../schema/legacy-review-tiles.json" with { type: "json" };
import { legacyTileKinds, tileKinds } from "../test/review-tile-inputs";
import { ReviewTiles } from "./components/review-tiles/review-tiles";
import type { ReviewDocument } from "./review-document";
import { validReviewTile } from "./review-tiles";

/** Stress test: every tile kind is rendered with many random payloads that
 *  honour the schema — empty optional lists, repeated names, references to
 *  ids the tile does not list, long and unusual text — and must render as
 *  itself every time, never as a fallback, a crash, or "NaN" / "undefined".
 *
 *  Each payload is also rendered the way the browser does, in jsdom (this file
 *  only; the rest of the suite stays on node), because React reports missing
 *  and duplicate list keys only there — and a duplicate key lets React drop or
 *  repeat rows when a review is re-rendered. */

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Schema = {
  $ref?: string;
  const?: unknown;
  enum?: unknown[];
  oneOf?: Schema[];
  anyOf?: Schema[];
  type?: string | string[];
  properties?: Record<string, Schema>;
  required?: string[];
  items?: Schema;
  minItems?: number;
  maxItems?: number;
  minimum?: number;
  pattern?: string;
};

const defs = { ...reviewSchema.$defs, ...legacyTileSchema.$defs } as unknown as Record<
  string,
  Schema
>;
const LONG = "Very long review text that keeps going ".repeat(12).trim();
const TEXT = [
  "x",
  "Ordinary label",
  LONG,
  "名前 ünïcode ✓",
  "<b>not html</b>",
  "users",
  "users.id",
  "public.orders.user_id",
  "orders UNIQUE (a, b)",
  "403 for readers; 404 for others",
  "GET /api/x",
  "line one\nline two",
  "timestamptz nullable",
];
const IDS = ["a", "b", "c", "writer", "lock"];

function random(seed: number) {
  let state = seed;
  return () => {
    state = (state * 1103515245 + 12345) % 2147483648;
    return state / 2147483648;
  };
}

function generate(schema: Schema, next: () => number, depth = 0): unknown {
  const pick = <T,>(items: readonly T[]): T => items[Math.floor(next() * items.length)]!;
  if (schema.$ref) return generate(defs[schema.$ref.split("/").at(-1)!]!, next, depth);
  if ("const" in schema) return schema.const;
  if (schema.enum) return pick(schema.enum);
  if (schema.oneOf || schema.anyOf)
    return generate(pick((schema.oneOf ?? schema.anyOf)!), next, depth);
  if (Array.isArray(schema.type))
    return generate({ ...schema, type: pick(schema.type) }, next, depth);
  if (schema.type === "object") {
    return Object.fromEntries(
      Object.entries(schema.properties ?? {})
        .filter(([key]) => (schema.required ?? []).includes(key) || next() < 0.5)
        .map(([key, value]) => [key, generate(value, next, depth + 1)]),
    );
  }
  if (schema.type === "array") {
    const min = schema.minItems ?? 0;
    const max = schema.maxItems ?? min + (depth > 3 ? 2 : 5);
    const length = min + Math.floor(next() * (max - min + 1));
    return Array.from({ length }, () => generate(schema.items!, next, depth + 1));
  }
  if (schema.type === "integer") return (schema.minimum ?? 0) + Math.floor(next() * 5);
  if (schema.type === "boolean") return next() < 0.5;
  if (schema.type === "null") return null;
  if (schema.pattern === "^[a-z][a-z0-9_]*$") return pick(IDS);
  if (schema.pattern) return "x";
  return pick(TEXT);
}

// Keep each kind's seeded sequence, while timing every payload independently.
const samples = [...tileKinds, ...legacyTileKinds].flatMap((kind) => {
  const next = random(kind.length * 7919);
  return Array.from({ length: 100 }, (_, sample) => ({
    kind,
    sample,
    data: generate(defs[`tileData_${kind}`]!, next),
  }));
});

const document = () =>
  structuredClone(reviewDocumentFor("acme/widgets", 42)) as unknown as ReviewDocument;

const KEY_WARNING = /same key|unique "key"/i;

describe("review tiles under schema-valid variation", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it.each(samples)("renders $kind payload $sample", ({ kind, sample, data }) => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    const tile = {
      id: "tile_stress",
      kind,
      anchors: [{ type: "file", path: "a.ts" }],
      code: [],
      data,
    };
    const doc = document();
    expect(validReviewTile(tile, doc)).toBe(true);
    const html = renderToStaticMarkup(<ReviewTiles document={doc} tiles={[tile]} />);
    const context = `${kind} #${sample}: ${JSON.stringify(tile.data).slice(0, 300)}`;
    expect(html, context).not.toContain("Tile unavailable");
    expect(html, context).not.toMatch(/NaN|Infinity/);
    expect(html, context).not.toMatch(/>undefined<|"undefined"| undefined /);

    errors.mockClear();
    const host = window.document.createElement("div");
    const root = createRoot(host);
    act(() => root.render(<ReviewTiles document={doc} tiles={[tile]} />));
    expect(host.innerHTML, context).not.toContain("Tile unavailable");
    act(() => root.unmount());
    const keyWarnings = errors.mock.calls
      .map((call) => call.map(String).join(" "))
      .filter((message) => KEY_WARNING.test(message));
    expect(keyWarnings, context).toEqual([]);
  });
});
