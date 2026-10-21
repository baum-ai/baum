import { writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import tailwindcss from "@tailwindcss/vite";

// Vite empties the output directory first; this puts back the tracked, empty
// .gitkeep that keeps the directory, which go:embed needs, in a checkout
// without a build.
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

export default defineConfig({
  // The server serves the app under a secret /s/<namespace>/ path, so built
  // asset URLs resolve against the page rather than the origin root.
  base: "./",
  plugins: [tailwindcss(), keepPlaceholder()],
  build: { outDir: "../internal/localapp/dist", emptyOutDir: true, sourcemap: false },
  resolve: { dedupe: ["react", "react-dom"] },
});
