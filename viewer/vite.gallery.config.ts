import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import { defineConfig, type Plugin } from "vite";

import { scanSources } from "./gallery/sources.ts";

/** The tile gallery's dev server (`pnpm gallery`, `just viewer::gallery`).
 *  It is not a build: nothing here is part of the standalone app, whose own
 *  config (`vite.app.config.ts`) roots at `app/` and never sees `gallery/`.
 *
 *  Directories, all optional:
 *  - GALLERY_RUNS_DIR: prompt-eval's runs/ (default ../../tools/prompt-eval/runs)
 *  - GALLERY_DATA_DIR: holds stored/ and fixtures/ (default ./gallery-data)
 *  - GALLERY_HOST_RUNS_DIR: the runs directory's path on the host, for the
 *    `view` command shown beside each run's tiles
 *  - GALLERY_PORT: the port (default 5180) */
const here = (path: string) => fileURLToPath(new URL(path, import.meta.url));

const directories = {
  runs: process.env.GALLERY_RUNS_DIR ?? here("../../tools/prompt-eval/runs"),
  data: process.env.GALLERY_DATA_DIR ?? here("./gallery-data"),
  hostRuns: process.env.GALLERY_HOST_RUNS_DIR,
};

/** Lists what the gallery can show, read fresh on every request so an edited
 *  fixture or a finished eval run appears without restarting the server. With
 *  `?version=` equal to the current one it answers `{ unchanged: true }`. */
function sources(): Plugin {
  return {
    name: "gallery-sources",
    configureServer(server) {
      server.middlewares.use("/api/sources", (request, response) => {
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
          response.end(error instanceof Error ? error.message : String(error));
        }
      });
    },
  };
}

export default defineConfig({
  root: here("./gallery"),
  plugins: [tailwindcss(), sources()],
  // The tile components and the schema's example sit beside the gallery under
  // oss/, which the dev server may serve.
  server: {
    host: true,
    port: Number.parseInt(process.env.GALLERY_PORT ?? "5180", 10),
    strictPort: true,
    fs: { allow: [here("..")] },
    watch: { usePolling: process.env.GALLERY_POLLING === "1" },
  },
});
