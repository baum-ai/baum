import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

/** Every font size is a step of the scale in `viewer.css`, written as
 *  `text-<step>`. An arbitrary `text-[13.5px]` beside a `text-[13px]` is how
 *  the same kind of text ended up in a dozen sizes across the panes; this test
 *  keeps the scale the one place a size is decided. */
function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
  });
}

const forbidden =
  /(?<![\w-])text-(?:\[\d+(?:\.\d+)?(?:px|rem|em)\]|(?:2xs|sm|md|xl|4xl|5xl)(?![\w-]))/;

describe("typography", () => {
  it("uses the type scale's steps rather than arbitrary or retired sizes", () => {
    // 2xs, sm, md and xl were steps once; the scale no longer defines them,
    // so a class naming one would silently set no size at all. 4xl and 5xl
    // belong to hosts, not to the viewer.
    const offenders = sources(fileURLToPath(new URL(".", import.meta.url))).flatMap((path) => {
      const lines = readFileSync(path, "utf8").split("\n");
      return lines.flatMap((line, index) =>
        forbidden.test(line) ? [`${path}:${index + 1}: ${line.trim()}`] : [],
      );
    });
    expect(offenders).toEqual([]);
  });

  it("matches arbitrary and retired sizes in real markup, and only those", () => {
    for (const line of ['"text-[13px] x"', "'\"text-[13px]\"'", '"sm:text-sm"', '"text-xl/5"'])
      expect(forbidden.test(line), line).toBe(true);
    for (const line of ['"text-2xl"', '"text-3xs"', '"text-(--text-secondary)"'])
      expect(forbidden.test(line), line).toBe(false);
  });
});
