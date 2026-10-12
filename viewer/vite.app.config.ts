import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

/** Builds the standalone app in `app/` into the Go package that embeds it.
 *  The output is committed, so it must be reproducible: no sourcemaps and no
 *  absolute paths. */
export default defineConfig({
  root: fileURLToPath(new URL("./app", import.meta.url)),
  base: "/",
  plugins: [tailwindcss()],
  build: {
    outDir: fileURLToPath(new URL("../internal/viewer/dist", import.meta.url)),
    emptyOutDir: true,
    sourcemap: false,
  },
});
