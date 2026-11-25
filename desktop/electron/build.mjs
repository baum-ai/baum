// Bundles the Electron main process and the preload script into build/.
// The preload runs sandboxed, so it must be one CommonJS file that requires
// nothing but "electron".
import { build } from "esbuild";

const shared = {
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node22",
  external: ["electron"],
  sourcemap: true,
  logLevel: "warning",
};

await Promise.all([
  build({ ...shared, entryPoints: ["electron/main/index.ts"], outfile: "build/main.cjs" }),
  build({ ...shared, entryPoints: ["electron/preload/index.ts"], outfile: "build/preload.cjs" }),
]);
