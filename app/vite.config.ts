import { copyFileSync, mkdirSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { desktopDevelopment } from "./vite.development.ts";

// A recursive cpSync fails with EACCES on its first subdirectory when the
// destination is a Docker Desktop (macOS) bind mount; copying file by file
// does not.
function copyDirectory(source: string, destination: string) {
  mkdirSync(destination, { recursive: true });
  for (const entry of readdirSync(source, { withFileTypes: true })) {
    const from = join(source, entry.name);
    const to = join(destination, entry.name);
    if (entry.isDirectory()) copyDirectory(from, to);
    else copyFileSync(from, to);
  }
}

// The same build supplies the daemon app and the desktop app's own screens
// (../desktop/dist): setup, the tray panel, and the connecting screen the
// window opens on, which becomes index.html there. The connecting screen and
// the tray panel are taken out of the daemon's copy.
function desktopBundle(): Plugin {
  let output = "";
  return {
    name: "desktop-bundle",
    apply: "build",
    configResolved(config) {
      output = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      const destination = resolve(import.meta.dirname, "../desktop/dist");
      rmSync(destination, { recursive: true, force: true });
      copyDirectory(output, destination);
      renameSync(join(destination, "connecting.html"), join(destination, "index.html"));
      rmSync(join(output, "connecting.html"));
      rmSync(join(output, "tray.html"));
    },
  };
}

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

// The profiling build's collector (src/perf.ts) must not reach any other build.
function withoutProfiling(): Plugin {
  return {
    name: "without-profiling",
    apply: "build",
    generateBundle(_, bundle) {
      for (const [name, chunk] of Object.entries(bundle)) {
        if (chunk.type === "chunk" && chunk.code.includes("__baumPerf"))
          this.error(`${name} contains the profiling collector outside a profiling build`);
      }
    },
  };
}

export default defineConfig(({ mode }) => ({
  // The server serves the app under a secret /s/<namespace>/ path, so built
  // asset URLs resolve against the page rather than the origin root.
  base: "./",
  plugins: [
    react(),
    tailwindcss(),
    keepPlaceholder(),
    desktopBundle(),
    ...(mode === "profile" ? [] : [withoutProfiling()]),
    ...(process.env.BAUM_DESKTOP_DEV_DIR
      ? [desktopDevelopment(process.env.BAUM_DESKTOP_DEV_DIR, process.env.BAUM_DESKTOP_DAEMON_HOST)]
      : []),
  ],
  build: {
    assetsInlineLimit: 0,
    outDir: "../internal/localapp/dist",
    emptyOutDir: true,
    // A profile names functions by their source files and lines.
    sourcemap: mode === "profile",
    rolldownOptions: {
      input: {
        app: resolve(import.meta.dirname, "index.html"),
        setup: resolve(import.meta.dirname, "setup.html"),
        connecting: resolve(import.meta.dirname, "connecting.html"),
        tray: resolve(import.meta.dirname, "tray.html"),
      },
    },
  },
  resolve: {
    dedupe: ["react", "react-dom"],
    // React's profiling build reports renders to <Profiler> in production.
    alias:
      mode === "profile" ? [{ find: "react-dom/client", replacement: "react-dom/profiling" }] : [],
  },
}));
