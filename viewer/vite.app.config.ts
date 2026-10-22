import { writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import { defineConfig, type Plugin } from "vite";

/** Vite empties the output directory first; this puts back the tracked,
 *  empty `.gitkeep` that keeps the directory, which `go:embed` needs, in a
 *  checkout without a build. */
function keepPlaceholder(): Plugin {
  let outDir = "";
  return {
    name: "keep-placeholder",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      writeFileSync(join(outDir, ".gitkeep"), "");
    },
  };
}

/** Builds the standalone app in `app/` into the Go package that embeds it.
 *  A release commit carries the output, so it must be reproducible: no
 *  sourcemaps and no absolute paths. */
export default defineConfig({
  root: fileURLToPath(new URL("./app", import.meta.url)),
  base: "/",
  plugins: [tailwindcss(), keepPlaceholder()],
  build: {
    outDir: fileURLToPath(new URL("../internal/viewer/dist", import.meta.url)),
    emptyOutDir: true,
    sourcemap: false,
  },
});
