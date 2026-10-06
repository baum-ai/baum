/** Synthetic test inputs derived from the contract, never imported by application code. */
import reviewTileSchema from "../../schema/review-1.json" with { type: "json" };
import legacyTileSchema from "../src/legacy-review-tiles.json" with { type: "json" };
import type { ReviewTile } from "../generated/review-tile-types.js";

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
  minimum?: number;
  pattern?: string;
};
const defs = { ...reviewTileSchema.$defs, ...legacyTileSchema.$defs } as unknown as Record<
  string,
  Schema
>;
function valueFor(schema: Schema): unknown {
  if (schema.$ref) return valueFor(defs[schema.$ref.split("/").at(-1)!]!);
  if ("const" in schema) return schema.const;
  if (schema.enum) return schema.enum[0];
  if (schema.oneOf || schema.anyOf) return valueFor((schema.oneOf ?? schema.anyOf)![0]!);
  if (Array.isArray(schema.type)) return valueFor({ ...schema, type: schema.type[0] });
  if (schema.type === "object")
    return Object.fromEntries(
      (schema.required ?? []).map((key) => [key, valueFor(schema.properties![key]!)]),
    );
  if (schema.type === "array")
    return Array.from({ length: schema.minItems ?? 0 }, () => valueFor(schema.items!));
  if (schema.type === "integer") return schema.minimum ?? 1;
  if (schema.type === "boolean") return false;
  if (schema.type === "null") return null;
  if (schema.pattern?.startsWith("^h_")) return "h_000000000000";
  return "supplied";
}
/** Kinds generation may produce. */
export const tileKinds = reviewTileSchema.$defs.tile.oneOf.map(
  (entry) => entry.properties.kind.const,
) as ReviewTile["kind"][];
/** Kinds only older stored documents carry. */
export const legacyTileKinds = legacyTileSchema.$defs.legacyTile.oneOf.map(
  (entry) => entry.properties.kind.const,
) as ReviewTile["kind"][];
export function tileInput<K extends ReviewTile["kind"]>(
  kind: K,
  id = "tile_test",
): Extract<ReviewTile, { kind: K }> {
  const data = valueFor(defs[`tileData_${kind}`]!);
  return {
    id,
    kind,
    anchors: [{ type: "file", path: "test.ts" }],
    code: [],
    data,
  } as unknown as Extract<ReviewTile, { kind: K }>;
}
