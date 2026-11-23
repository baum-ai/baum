import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

/** Every font size is a step of the scale in `oss/viewer/src/viewer.css`,
 *  written as `text-<step>`. The viewer's own test scans only the viewer; this
 *  one keeps the app from naming a retired step, which sets no size at all. */
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
    const offenders = sources(join(process.cwd(), "src")).flatMap((path) => {
      const lines = readFileSync(path, "utf8").split("\n");
      return lines.flatMap((line, index) =>
        forbidden.test(line) ? [`${path}:${index + 1}: ${line.trim()}`] : [],
      );
    });
    expect(offenders).toEqual([]);
  });
});
