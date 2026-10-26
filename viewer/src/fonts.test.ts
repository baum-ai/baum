import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

/** The viewer ships baum's type, Instrument Sans for text and Manrope for
 *  headings, as files beside `fonts/fonts.css`, so every `url()` there must
 *  name a file in the package, and each font's licence must ship with them. */
const read = (path: string) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
const viewer = read("./viewer.css");
const fonts = read("./fonts/fonts.css");

describe("bundled fonts", () => {
  it("uses the bundled text font when a host sets no --viewer-font-sans", () => {
    expect(viewer).toContain('@import "./fonts/fonts.css";');
    expect(viewer).toMatch(/--font-sans:\s*var\(\s*--viewer-font-sans,\s*var\(--font-text\)\s*\)/);
    expect(fonts).toMatch(/--font-text:\s*"Instrument Sans",/);
    expect(fonts).toMatch(/--font-heading:\s*"Manrope",/);
    expect(fonts.match(/font-family: "Instrument Sans";/g)).toHaveLength(4);
    expect(fonts.match(/font-family: "Manrope";/g)).toHaveLength(2);
    expect(viewer).not.toContain("Inter");
  });

  it("ships every font file fonts.css declares", () => {
    const urls = [...fonts.matchAll(/url\("([^"]+)"\)/g)].map((match) => match[1] ?? "");
    expect(urls).toEqual([
      "./instrument-sans-latin-ext-wght-normal.woff2",
      "./instrument-sans-latin-wght-normal.woff2",
      "./instrument-sans-latin-ext-wght-italic.woff2",
      "./instrument-sans-latin-wght-italic.woff2",
      "./manrope-latin-ext-wght-normal.woff2",
      "./manrope-latin-wght-normal.woff2",
    ]);
    for (const url of urls) {
      const path = fileURLToPath(new URL(`./fonts/${url}`, import.meta.url));
      expect(existsSync(path), url).toBe(true);
      // woff2 files start with the "wOF2" signature.
      expect(readFileSync(path).subarray(0, 4).toString("latin1"), url).toBe("wOF2");
    }
  });

  it("ships each font's licence", () => {
    for (const [file, authors] of [
      ["./fonts/instrument-sans-OFL.txt", "The Instrument Sans Project Authors"],
      ["./fonts/manrope-OFL.txt", "The Manrope Project Authors"],
    ] as const) {
      const licence = read(file);
      expect(licence, file).toContain(authors);
      expect(licence, file).toContain("SIL Open Font License, Version 1.1");
    }
  });
});
