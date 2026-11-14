import { describe, expect, it } from "vitest";
import type { Tile } from "../tile-document";
import { kindOf } from "./custom-tile";
import { isBuiltinType, setBuiltinDigests } from "./library";

const BUILTIN = `sha256:${"a".repeat(64)}`;
const OTHER = `sha256:${"b".repeat(64)}`;

function custom(digest: string, title = "Schema changes"): Tile {
  return {
    id: "schema_change",
    type: "custom",
    title,
    state: "ready",
    data: {},
    definition: { name: "schema_change", title, digest, kit: 1, sizes: ["half"] },
  } as unknown as Tile;
}

describe("built-in library tiles", () => {
  it("names a built-in type by its title and any other as custom", () => {
    setBuiltinDigests([BUILTIN]);
    expect(kindOf(custom(BUILTIN))).toBe("Schema changes");
    expect(isBuiltinType(custom(BUILTIN))).toBe(true);
    // A tile that calls itself by a built-in type's name is still custom.
    expect(kindOf(custom(OTHER))).toBe("Custom · Schema changes");
    expect(isBuiltinType({ ...custom(BUILTIN), type: "table" } as Tile)).toBe(false);
  });
});
