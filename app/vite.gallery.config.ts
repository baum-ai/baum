import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

import { scanSources } from "./gallery/sources.ts";

/** The native diagram gallery's dev server (`pnpm gallery`,
 *  `just local_app::diagram-gallery`). It is not a build: the app's own
 *  config never sees `gallery/`.
 *
 *  Directories, all optional:
 *  - DIAGRAM_GALLERY_RUNS: replay's output directory
 *  - DIAGRAM_GALLERY_STORES: searched for the desktop app's review stores
 *  - DIAGRAM_GALLERY_FILES: loose JSON files (default ./gallery-data)
 *  - DIAGRAM_GALLERY_PORT: the port (default 5181)
 *  - DIAGRAM_GALLERY_HOST: the address to listen on (default 127.0.0.1, as
 *    the reviews it shows are private; the container recipe sets 0.0.0.0
 *    and publishes the port on the host's loopback only) */
const here = (path: string) => fileURLToPath(new URL(path, import.meta.url));

const directories = {
  runs: process.env.DIAGRAM_GALLERY_RUNS || undefined,
  stores: process.env.DIAGRAM_GALLERY_STORES || undefined,
  files: process.env.DIAGRAM_GALLERY_FILES || here("./gallery-data"),
};

/** Lists the diagrams, read fresh on every request so a finished replay case,
 *  a new review version or an edited file appears without a restart. With
 *  `?version=` equal to the current one it answers `{ version, unchanged }`. */
function diagrams(): Plugin {
  return {
    name: "diagram-gallery-sources",
    configureServer(server) {
      server.middlewares.use("/api/diagrams", (request, response) => {
        const url = new URL(request.url ?? "/", "http://localhost");
        try {
          const found = scanSources(directories);
          const unchanged = url.searchParams.get("version") === found.version;
          response.setHeader("content-type", "application/json");
          response.setHeader("cache-control", "no-store");
          response.end(
            JSON.stringify(unchanged ? { version: found.version, unchanged: true } : found),
          );
        } catch (error) {
          response.statusCode = 500;
          response.end(error instanceof Error ? error.name : "scan failed");
        }
      });
    },
  };
}

export default defineConfig({
  root: here("./gallery"),
  plugins: [react(), tailwindcss(), diagrams()],
  resolve: { dedupe: ["react", "react-dom"] },
  // The review view's components come from src/ and the linked @baum/viewer.
  server: {
    host: process.env.DIAGRAM_GALLERY_HOST ?? "127.0.0.1",
    port: Number.parseInt(process.env.DIAGRAM_GALLERY_PORT ?? "5181", 10),
    strictPort: true,
    // The fixture imports the schema's example review from oss/schema.
    fs: { allow: [here("."), here("../viewer"), here("../schema")] },
    watch: { usePolling: process.env.DIAGRAM_GALLERY_POLLING === "1" },
  },
});
