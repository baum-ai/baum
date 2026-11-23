// The built app's JavaScript and CSS files, as the profile's Bundle table
// lists them: what index.html loads at once, and the rest, loaded later.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import type { Chunk } from "./summary.ts";

/** Sorts `files` (names under assets/) by how index.html loads them. */
export function classify(
  html: string,
  files: { file: string; bytes: number; gzip: number }[],
): Chunk[] {
  const referenced = (pattern: RegExp) =>
    new Set([...html.matchAll(pattern)].map((match) => match[1]!.replace(/^\.?\//, "")));
  const entries = referenced(/<script[^>]*\ssrc="([^"]+)"/g);
  const preloads = referenced(/<link[^>]*rel="modulepreload"[^>]*href="([^"]+)"/g);
  const styles = referenced(/<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"/g);
  return files.flatMap((f): Chunk[] => {
    const css = f.file.endsWith(".css");
    if (!css && !f.file.endsWith(".js")) return [];
    const kind = entries.has(f.file)
      ? "entry"
      : preloads.has(f.file)
        ? "preload"
        : styles.has(f.file)
          ? "css"
          : css
            ? "lazy-css"
            : "lazy";
    return [{ ...f, kind }];
  });
}

/** The chunks of the app build in `dist` (oss/internal/localapp/dist). */
export function readChunks(dist: string): Chunk[] {
  const assets = join(dist, "assets");
  const files = readdirSync(assets)
    .filter((file) => file.endsWith(".js") || file.endsWith(".css"))
    .map((file) => {
      const content = readFileSync(join(assets, file));
      return { file: `assets/${file}`, bytes: content.length, gzip: gzipSync(content).length };
    });
  return classify(readFileSync(join(dist, "index.html"), "utf8"), files);
}
