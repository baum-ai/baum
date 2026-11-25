import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { clearCommands, dispatch } from "./commands.ts";
import {
  MAX_FILE_BYTES,
  registerTileFileCommands,
  type SaveDialog,
  validName,
} from "./tile-files.ts";

const context = { window: null, role: "daemon-pages", origin: "http://127.0.0.1:4100" } as const;

describe("tile type export", () => {
  beforeEach(clearCommands);

  it("names are tile type names", () => {
    expect(validName("migration_steps")).toBe(true);
    expect(validName("a1")).toBe(true);
    for (const bad of ["", "1a", "Mig", "../x", "a/b", "a.b", "a".repeat(65)]) {
      expect(validName(bad), bad).toBe(false);
    }
    expect(validName("a".repeat(64))).toBe(true);
  });

  it("asks where to save and writes there", async () => {
    const dir = mkdtempSync(join(tmpdir(), "baum-tile-"));
    const target = join(dir, "x.baumtile.json");
    const dialogs: SaveDialog[] = [];
    registerTileFileCommands({
      chooseSavePath: async (d) => {
        dialogs.push(d);
        return target;
      },
    });
    const path = await dispatch("save_tile_type", { name: "steps", contents: '{"a":1}' }, context);
    expect(path).toBe(target);
    expect(readFileSync(target, "utf8")).toBe('{"a":1}');
    expect(dialogs[0]?.defaultName).toBe("steps.baumtile.json");
  });

  it("returns null without writing when the person cancels", async () => {
    const written: string[] = [];
    registerTileFileCommands({
      chooseSavePath: async () => null,
      writeFile: async (p) => {
        written.push(p);
      },
    });
    expect(await dispatch("save_tile_type", { name: "steps", contents: "x" }, context)).toBeNull();
    expect(written).toEqual([]);
  });

  it("refuses a bad name or a file over 3 MiB before asking", async () => {
    let asked = false;
    registerTileFileCommands({
      chooseSavePath: async () => {
        asked = true;
        return null;
      },
    });
    await expect(
      dispatch("save_tile_type", { name: "../x", contents: "x" }, context),
    ).rejects.toThrow("That is not a tile type's name.");
    await expect(dispatch("save_tile_type", { name: 7, contents: "x" }, context)).rejects.toThrow();
    await expect(
      dispatch("save_tile_type", { name: "ok", contents: "x".repeat(MAX_FILE_BYTES + 1) }, context),
    ).rejects.toThrow("The file is larger than 3 MiB.");
    expect(asked).toBe(false);
    await dispatch("save_tile_type", { name: "ok", contents: "x".repeat(MAX_FILE_BYTES) }, context);
    expect(asked).toBe(true);
  });
});
