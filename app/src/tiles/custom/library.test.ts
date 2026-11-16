import { describe, expect, it } from "vitest";
import type { Tile } from "../types";
import { isBuiltinType } from "./library";

function custom(builtin: unknown, title = "Schema changes"): Tile {
  return {
    id: "schema_change",
    type: "custom",
    title,
    state: "ready",
    data: {},
    definition: {
      name: "schema_change",
      title,
      digest: `sha256:${"a".repeat(64)}`,
      kit: 1,
      sizes: ["half"],
      builtin,
    },
  } as unknown as Tile;
}

describe("built-in library tiles", () => {
  it("are baum's own only when the daemon marks them", () => {
    expect(isBuiltinType(custom(true))).toBe(true);
    // Anything but the daemon's mark is custom, whatever it is called.
    for (const mark of [undefined, "true", 1, false])
      expect(isBuiltinType(custom(mark))).toBe(false);
    expect(isBuiltinType({ ...custom(true), type: "table" } as Tile)).toBe(false);
  });
});
