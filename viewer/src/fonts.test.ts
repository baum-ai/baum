import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

/** The viewer ships its default sans-serif face, Inter, as files beside
 *  `viewer.css`, so every `url()` there must name a file in the package, and
 *  the font's licence must ship with them. */
const css = readFileSync(fileURLToPath(new URL("./viewer.css", import.meta.url)), "utf8");

describe("bundled font", () => {
  it("declares Inter as the default when a host sets no --viewer-font-sans", () => {
    expect(css).toMatch(/--font-sans:\s*var\(\s*--viewer-font-sans,\s*"Inter Variable",/);
    expect(css.match(/font-family: "Inter Variable";/g)).toHaveLength(2);
  });

  it("ships every font file viewer.css declares", () => {
    const urls = [...css.matchAll(/url\("([^"]+)"\)/g)].map((match) => match[1] ?? "");
    expect(urls).toEqual([
      "./fonts/inter-latin-ext-wght-normal.woff2",
      "./fonts/inter-latin-wght-normal.woff2",
    ]);
    for (const url of urls) {
      const path = fileURLToPath(new URL(url, import.meta.url));
      expect(existsSync(path), url).toBe(true);
      // woff2 files start with the "wOF2" signature.
      expect(readFileSync(path).subarray(0, 4).toString("latin1"), url).toBe("wOF2");
    }
  });

  it("ships the font's licence", () => {
    const licence = readFileSync(
      fileURLToPath(new URL("./fonts/OFL.txt", import.meta.url)),
      "utf8",
    );
    expect(licence).toContain("The Inter Project Authors");
    expect(licence).toContain("SIL Open Font License, Version 1.1");
  });
});
